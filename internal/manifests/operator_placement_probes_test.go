package manifests

import (
	"fmt"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestOperatorPlacementProbeReferences(t *testing.T) {
	for _, field := range []string{"startupProbe", "readinessProbe", "livenessProbe", "postStart", "preStop"} {
		for _, protocol := range []string{"tcpSocket", "httpGet", "grpc"} {
			if (field == "postStart" || field == "preStop") && protocol != "httpGet" {
				continue
			}
			t.Run(field+"/"+protocol, func(t *testing.T) {
				config, component := manifestPlacementConfig(t)
				port := `"grpc"`
				if protocol == "httpGet" {
					port = `"http"`
				} else if protocol == "grpc" {
					port = `8080`
				}
				handler := fmt.Sprintf(`{%q:{"port":%s}}`, protocol, port)
				entry := fmt.Sprintf(`%q:%s`, field, handler)
				if field == "postStart" || field == "preStop" {
					entry = `"lifecycle":{` + entry + `}`
				}
				config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"worker",` + entry + `}]}}}}`)
				deployments, err := RenderDeployments(BuildContextV2{YanetName: "test"}, component, NewPatchRegistry(config.Patches))
				if err != nil {
					t.Fatal(err)
				}
				worker := deployments[0].Spec.Template.Spec.InitContainers[0]
				var probe *corev1.Probe
				switch field {
				case "startupProbe":
					probe = worker.StartupProbe
				case "readinessProbe":
					probe = worker.ReadinessProbe
				case "livenessProbe":
					probe = worker.LivenessProbe
				case "postStart":
					probe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: worker.Lifecycle.PostStart.HTTPGet}}
				case "preStop":
					probe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: worker.Lifecycle.PreStop.HTTPGet}}
				}
				switch protocol {
				case "tcpSocket":
					if probe.TCPSocket.Port.StrVal != worker.Ports[0].Name {
						t.Fatalf("probe points to %q, but listener was renamed to %q", probe.TCPSocket.Port.StrVal, worker.Ports[0].Name)
					}
				case "httpGet":
					if probe.HTTPGet.Port.StrVal != worker.Ports[1].Name {
						t.Fatalf("HTTP action points to %q, not %q", probe.HTTPGet.Port.StrVal, worker.Ports[1].Name)
					}
				case "grpc":
					if probe.GRPC.Port != worker.Ports[0].ContainerPort {
						t.Fatalf("numeric gRPC probe hits %d, not its listener %d", probe.GRPC.Port, worker.Ports[0].ContainerPort)
					}
				}
				if worker.RestartPolicy == nil || *worker.RestartPolicy != corev1.ContainerRestartPolicyAlways {
					t.Fatal("probe rewriting changed native sidecar policy")
				}
			})
		}
	}
}

func TestOperatorPlacementRejectsUnresolvedProbes(t *testing.T) {
	for _, fragment := range []string{
		`"startupProbe":{"tcpSocket":{"port":"missing"}}`,
		`"readinessProbe":{"httpGet":{"port":"missing"}}`,
		`"livenessProbe":{"tcpSocket":{"port":"missing"}}`,
		`"lifecycle":{"preStop":{"httpGet":{"port":"missing"}}}`,
		`"startupProbe":{"grpc":{"port":8081}}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			config, component := manifestPlacementConfig(t)
			config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"worker",` + fragment + `}]}}}}`)
			if _, err := RenderDeployments(BuildContextV2{YanetName: "test"}, component, NewPatchRegistry(config.Patches)); err == nil {
				t.Fatal("unresolved probe must fail before workload writes")
			}
		})
	}
	// A sibling's named listener does not resolve in the probed container.
	config, component := manifestPlacementConfig(t)
	config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"agent","startupProbe":{"tcpSocket":{"port":"grpc"}}}]}}}}`)
	if _, err := RenderDeployments(BuildContextV2{YanetName: "test"}, component, NewPatchRegistry(config.Patches)); err == nil {
		t.Fatal("probe must not resolve a sibling's named listener")
	}
}

func TestOperatorPlacementProbeValidationAfterDataplanePatch(t *testing.T) {
	config, component := manifestPlacementConfig(t)
	build := BuildContextV2{YanetName: "test"}
	initial, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	worker := initial[0].Spec.Template.Spec.InitContainers[1]
	for _, probe := range []string{`{"tcpSocket":{"port":"missing"}}`, `{"grpc":{"port":8080}}`} {
		registry := NewPatchRegistry(config.Patches)
		registry["bad-probe"] = api.NamedPatch{Patch: runtime.RawExtension{Raw: []byte(fmt.Sprintf(
			`{"spec":{"template":{"spec":{"initContainers":[{"name":%q,"startupProbe":%s}]}}}}`, worker.Name, probe))}}
		component.Patches = []string{"bad-probe"}
		if _, err := RenderDeployments(build, component, registry); err == nil {
			t.Fatal("final dataplane patch bypassed probe validation")
		}
	}
}

func TestOperatorPlacementStandaloneProbeUnchanged(t *testing.T) {
	operator := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "test", Enabled: true,
		Containers: []helpers.ResolvedContainer{{Name: "worker", Image: helpers.ResolvedImage{Name: "test"}}}, Patches: []string{"probe"}}
	registry := PatchRegistry{"probe": {Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"worker","startupProbe":{"tcpSocket":{"port":"grpc"}}}]}}}}`)}}}
	deployments, err := RenderDeployments(BuildContextV2{YanetName: "test"}, operator, registry)
	if err != nil {
		t.Fatal(err)
	}
	if deployments[0].Spec.Template.Spec.Containers[0].StartupProbe.TCPSocket.Port.StrVal != "grpc" {
		t.Fatal("standalone probe contract changed")
	}
}

func TestOperatorPlacementCustomProbePorts(t *testing.T) {
	config, component := manifestPlacementConfig(t)
	config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[
		{"name":"worker","readinessProbe":{"httpGet":{"port":8081}},"startupProbe":{"grpc":{"port":8082}}},
		{"name":"agent","ports":[{"name":"health","containerPort":9090}],
		 "livenessProbe":{"tcpSocket":{"port":"health"}},"startupProbe":{"grpc":{"port":9090}}}
	]}}}}`)
	deployments, err := RenderDeployments(BuildContextV2{YanetName: "test"}, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	pod := deployments[0].Spec.Template.Spec
	worker, agent := pod.InitContainers[0], pod.InitContainers[1]
	if worker.ReadinessProbe.HTTPGet.Port.IntVal != 8083 || worker.StartupProbe.GRPC.Port != 8082 {
		t.Fatal("local numeric defaults must follow allocation; already-effective ports must remain unchanged")
	}
	if agent.LivenessProbe.TCPSocket.Port.StrVal != "health" || agent.StartupProbe.GRPC.Port != 9090 {
		t.Fatal("custom declared listener/probe ports must remain unchanged")
	}
}
