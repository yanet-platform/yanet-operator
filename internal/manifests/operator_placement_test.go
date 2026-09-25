package manifests

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestOperatorListenersContracts(t *testing.T) {
	for _, tt := range []struct {
		name      string
		listeners []string
		want      []int32
	}{
		{name: "monalive", want: []int32{8080}},
		{name: "metrics", want: []int32{8080}},
		{name: "http-only", listeners: []string{"http"}, want: []int32{8081}},
		{name: "metrics", listeners: []string{"grpc", "http"}, want: []int32{8080, 8081}},
		{name: "client", listeners: []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, colocated := range []bool{false, true} {
				op := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: tt.name, Enabled: true,
					Image:         helpers.ResolvedImage{Name: "test"},
					ListenerNames: tt.listeners, Containers: []helpers.ResolvedContainer{{Name: "worker", Image: helpers.ResolvedImage{Name: "test"}}}}
				if colocated {
					op.Kind = helpers.KindSidecar
					op.PortIndex = 1
				}
				context := BuildContext{YanetName: "test", Namespace: "test", BoxType: "test"}
				deployments, err := BuildDeployments(context, op)
				if err != nil {
					t.Fatal(err)
				}
				var actual []int32
				for _, port := range deployments[0].Spec.Template.Spec.Containers[0].Ports {
					number := port.ContainerPort
					if colocated {
						number -= 2
					}
					actual = append(actual, number)
				}
				if !reflect.DeepEqual(actual, tt.want) {
					t.Fatalf("ports=%v want=%v", actual, tt.want)
				}
				services := BuildServices(context, op)
				if (len(services) == 0) != (len(tt.want) == 0) {
					t.Fatalf("unexpected services: %+v", services)
				}
			}
		})
	}
}

func manifestPlacementConfig(t *testing.T) (*api.YanetConfigSpec, *helpers.ResolvedComponent) {
	t.Helper()
	var config api.YanetConfigSpec
	err := json.Unmarshal([]byte(`{
		"components":{"dataplane":{"image":{"name":"dp"}, "sidecars":[
			{"name":"worker","image":{"name":"monalive"},"listeners":["grpc","http"]},
			{"name":"agent","image":{"name":"agent"},"listeners":[],"config":{"hostPath":"/etc/agent","args":["agent"]}}
		]}},
		"patches":[{"name":"configure","patch":{"spec":{"template":{"spec":{
			"volumes":[{"name":"downloaded-config","emptyDir":{}}],
			"initContainers":[{"name":"fetch","image":"fetch","volumeMounts":[{"name":"downloaded-config","mountPath":"/out"}]}],
			"containers":[{"name":"worker","args":["-c","/etc/yanet2/config"],"volumeMounts":[{"name":"downloaded-config","mountPath":"/etc/yanet2","readOnly":true}],"env":[{"name":"OWN_MEMORY","valueFrom":{"resourceFieldRef":{"containerName":"worker","resource":"limits.memory"}}}]}]
		}}}}}],
		"boxTypes":[{"name":"test","components":{"dataplane":{"sidecars":{"worker":{"patches":["configure"]},"agent":{}}}}}]
	}`), &config)
	if err != nil {
		t.Fatal(err)
	}
	component, err := helpers.ResolveBoxComponent(&config, &api.YanetSpec{BoxType: "test"}, helpers.KindDataplane, "")
	if err != nil {
		t.Fatal(err)
	}
	return &config, component
}

func TestOperatorPlacementConfigComposition(t *testing.T) {
	config, component := manifestPlacementConfig(t)
	context := BuildContext{YanetName: "test", Namespace: "test", BoxType: "test"}
	deployments, err := RenderDeployments(context, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	pod := deployments[0].Spec.Template.Spec
	if len(pod.InitContainers) != 3 || pod.InitContainers[0].RestartPolicy != nil ||
		pod.InitContainers[1].RestartPolicy == nil || pod.InitContainers[2].RestartPolicy == nil {
		t.Fatalf("config downloader and native sidecars have incorrect order/policy: %+v", pod.InitContainers)
	}
	worker, agent := pod.InitContainers[1], pod.InitContainers[2]
	if worker.Ports[0].ContainerPort != 8080 || worker.Ports[1].ContainerPort != 8081 {
		t.Fatalf("first declared sidecar must use 8080/8081: %+v", worker.Ports)
	}
	if !reflect.DeepEqual(worker.Args, []string{"-c", "/etc/yanet2/config"}) ||
		!reflect.DeepEqual(agent.Args, []string{"agent"}) || !pod.HostIPC {
		t.Fatal("composition changed config arguments/IPC")
	}
	if worker.VolumeMounts[0].Name != pod.InitContainers[0].VolumeMounts[0].Name {
		t.Fatal("config downloader and consumer no longer share the volume")
	}
	for _, variable := range worker.Env {
		if variable.Name == "OWN_MEMORY" && variable.ValueFrom.ResourceFieldRef.ContainerName != worker.Name {
			t.Fatal("resourceFieldRef must use the composed container name")
		}
	}

	// A dataplane patch cannot remove/reorder the role or change restartPolicy.
	patch := api.NamedPatch{Name: "mutate", Patch: runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"spec":{"template":{
		"metadata":{"labels":{%q:null}},"spec":{"initContainers":[
		{"name":%q,"$patch":"delete"},{"name":%q,"restartPolicy":null}],
		"$setElementOrder/initContainers":[{"name":%q},{"name":%q}]}}}}`,
		OperatorMembershipLabel("worker"), worker.Name, agent.Name, agent.Name, pod.InitContainers[0].Name))}}
	component.Patches = []string{"mutate"}
	config.Patches = append(config.Patches, patch)
	protected, err := RenderDeployments(context, component, NewPatchRegistry(config.Patches))
	if err == nil || protected != nil {
		t.Fatal("dataplane patch must not change declared sidecar composition/order/restartPolicy")
	}
}

func TestOperatorPlacementRejectsFinalReferences(t *testing.T) {
	for _, fragment := range []string{
		`{"containers":[{"name":"worker","volumeMounts":[{"name":"missing","mountPath":"/missing"}]}]}`,
		`{"containers":[{"name":"worker","volumeDevices":[{"name":"missing","devicePath":"/dev/x"}]}]}`,
		`{"containers":[{"name":"INVALID","image":"test"}]}`,
		`{"containers":[{"name":"agent","ports":[{"name":"grpc","containerPort":8082}]}]}`,
		`{"containers":[{"name":"agent","ports":[{"name":"invalid-port-name-is-too-long","containerPort":9090}]}]}`,
		`{"containers":[{"name":"agent","ports":[{"containerPort":65536}]}]}`,
		`{"containers":[{"name":"worker","env":[{"name":"MEMORY","valueFrom":{"resourceFieldRef":{"containerName":"missing","resource":"limits.memory"}}}]}]}`,
		`{"volumes":[{"name":"downward","downwardAPI":{"items":[{"path":"memory","resourceFieldRef":{"containerName":"missing","resource":"limits.memory"}}]}}]}`,
		`{"containers":[{"name":"agent","restartPolicy":"Never"}]}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			config, component := manifestPlacementConfig(t)
			config.Patches[0].Patch.Raw = []byte(`{"spec":{"template":{"spec":` + fragment + `}}}`)
			if _, err := RenderDeployments(BuildContext{YanetName: "test"}, component, NewPatchRegistry(config.Patches)); err == nil {
				t.Fatal("invalid composition must fail")
			}
		})
	}
}

func TestOperatorPlacementDisabledTargetCannotBeCaptured(t *testing.T) {
	config, component := manifestPlacementConfig(t)
	component.Sidecars[0].Enabled = false
	for _, useName := range []bool{false, true} {
		port := corev1.ContainerPort{ContainerPort: 8080}
		if useName {
			port.ContainerPort = 9000
			port.Name = BuildServices(BuildContext{BoxType: "test"}, component.Sidecars[0])[0].Ports[0].TargetPortName
		}
		raw, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []corev1.Container{{Name: "dataplane", Ports: []corev1.ContainerPort{port}}},
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		registry := NewPatchRegistry(config.Patches)
		registry["capture"] = api.NamedPatch{Patch: runtime.RawExtension{Raw: raw}}
		component.Patches = []string{"capture"}
		if _, err := RenderDeployments(BuildContext{YanetName: "test"}, component, registry); err == nil {
			t.Fatal("disabled role must retain both named and numeric port reservations")
		}
	}
}

func TestOperatorPlacementServicesDisambiguateRoleHashes(t *testing.T) {
	first := &helpers.ResolvedComponent{Kind: helpers.KindSidecar, Name: "role-47893"}
	second := &helpers.ResolvedComponent{Kind: helpers.KindSidecar, Name: "role-89356"}
	firstPlan := BuildServices(BuildContext{BoxType: "test"}, first)[0]
	secondPlan := BuildServices(BuildContext{BoxType: "test"}, second)[0]
	if reflect.DeepEqual(firstPlan.Selector, secondPlan.Selector) {
		t.Fatal("Services must distinguish different roles even when their name hashes collide across revisions")
	}
}
