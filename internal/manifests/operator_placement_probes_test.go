package manifests

import (
	"fmt"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
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
				port := `"health"`
				if protocol == "grpc" {
					port = `9000`
				}
				handler := fmt.Sprintf(`{%q:{"port":%s}}`, protocol, port)
				entry := fmt.Sprintf(`%q:%s`, field, handler)
				if field == "postStart" || field == "preStop" {
					entry = `"lifecycle":{` + entry + `}`
				}
				config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"worker","ports":[{"name":"health","containerPort":9000}],` + entry + `}]}}}}`)
				deployments, err := RenderDeployments(BuildContext{YanetName: "test"}, component, NewPatchRegistry(config.Patches))
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
					if probe.TCPSocket.Port.StrVal != "health" {
						t.Fatalf("explicit probe reference changed: %q", probe.TCPSocket.Port.StrVal)
					}
				case "httpGet":
					if probe.HTTPGet.Port.StrVal != "health" {
						t.Fatalf("explicit HTTP reference changed: %q", probe.HTTPGet.Port.StrVal)
					}
				case "grpc":
					if probe.GRPC.Port != 9000 {
						t.Fatalf("explicit numeric probe changed: %d", probe.GRPC.Port)
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
		`"startupProbe":{"grpc":{"port":65536}}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			config, component := manifestPlacementConfig(t)
			config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"worker",` + fragment + `}]}}}}`)
			if _, err := RenderDeployments(BuildContext{YanetName: "test"}, component, NewPatchRegistry(config.Patches)); err == nil {
				t.Fatal("unresolved probe must fail before workload writes")
			}
		})
	}
	// A sibling's named listener does not resolve in the probed container.
	config, component := manifestPlacementConfig(t)
	component.Sidecars[0].Patches = nil
	component.Sidecars[1].Patches = []string{"configure"}
	target := BuildServices(BuildContext{BoxType: "test"}, component.Sidecars[0])[0].Ports[0].TargetPortName
	config.Patches[0].Patch.Raw = []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"agent","startupProbe":{"tcpSocket":{"port":%q}}}]}}}}`, target))
	if _, err := RenderDeployments(BuildContext{YanetName: "test"}, component, NewPatchRegistry(config.Patches)); err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "does not name a TCP port in that container") {
		t.Fatalf("probe must not resolve a sibling's named listener: %v", err)
	}
}

func TestOperatorPlacementProbeValidationAfterDataplanePatch(t *testing.T) {
	config, component := manifestPlacementConfig(t)
	build := BuildContext{YanetName: "test"}
	initial, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	worker := initial[0].Spec.Template.Spec.InitContainers[1]
	for _, probe := range []string{`{"tcpSocket":{"port":"missing"}}`, `{"grpc":{"port":65536}}`} {
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
	deployments, err := RenderDeployments(BuildContext{YanetName: "test"}, operator, registry)
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
		{"name":"worker","ports":[{"name":"health","containerPort":9090}],
		 "readinessProbe":{"httpGet":{"port":9090}},"livenessProbe":{"tcpSocket":{"port":"health"}},"startupProbe":{"grpc":{"port":9090}}}
	]}}}}`)
	deployments, err := RenderDeployments(BuildContext{YanetName: "test"}, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	pod := deployments[0].Spec.Template.Spec
	worker := pod.InitContainers[0]
	if worker.ReadinessProbe.HTTPGet.Port.IntVal != 9090 || worker.StartupProbe.GRPC.Port != 9090 || worker.LivenessProbe.TCPSocket.Port.StrVal != "health" {
		t.Fatal("custom declared listener/probe ports must remain unchanged")
	}
}
