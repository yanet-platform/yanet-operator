package manifests

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRenderDataplaneNetworkNamespaceIgnoresPatchIdentity(t *testing.T) {
	for _, namespace := range []string{"", "networks"} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			config.Components.Dataplane.Networks = []api.NetworkAttachment{{Name: "pf", Namespace: namespace, Interface: "eth2", ResourceName: "example.net/pf"}}
			config.Patches = []api.NamedPatch{{Name: "copied", Patch: runtime.RawExtension{Raw: []byte(`{"metadata":{"namespace":"copied-from"}}`)}}}
			config.BoxTypes[0].Components.Dataplane.Patches = []string{"copied"}
			validateRenderConfig(t, config)
			deployment, _ := renderRuntimeDataplane(t, config, spec)
			wantNamespace := namespace
			if wantNamespace == "" {
				wantNamespace = "yanet"
			}
			var networks []struct{ Name, Namespace, Interface string }
			if err := json.Unmarshal([]byte(deployment.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]), &networks); err != nil {
				t.Fatal(err)
			}
			if deployment.Namespace != "yanet" || len(networks) != 1 || networks[0].Namespace != wantNamespace {
				t.Fatalf("deployment namespace=%q, networks=%+v; want deployment yanet and NAD %q", deployment.Namespace, networks, wantNamespace)
			}
		})
	}
}

func TestRenderPreservesPatchedOneShotInitContainers(t *testing.T) {
	for _, sidecars := range []bool{false, true} {
		for _, name := range []string{"prepare", "op-prepare"} {
			t.Run(name+"/sidecars="+strconv.FormatBool(sidecars), func(t *testing.T) {
				config, spec := runtimeNetworkFixture()
				if !sidecars {
					config.Components.Dataplane.Sidecars = nil
					config.BoxTypes[0].Components.Dataplane.Sidecars = nil
				}
				initial, _ := renderRuntimeDataplane(t, config, spec)
				want := corev1.Container{Name: name, Image: "prepare:1", Command: []string{"/prepare"}, Args: []string{"--once"}}
				raw, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"initContainers": []corev1.Container{want}}}}})
				if err != nil {
					t.Fatal(err)
				}
				config.Patches = []api.NamedPatch{{Name: "prepare", Patch: runtime.RawExtension{Raw: raw}}}
				config.BoxTypes[0].Components.Dataplane.Patches = []string{"prepare"}
				validateRenderConfig(t, config)
				deployment, _ := renderRuntimeDataplane(t, config, spec)
				containers := deployment.Spec.Template.Spec.InitContainers
				if len(containers) != len(initial.Spec.Template.Spec.InitContainers)+1 || !reflect.DeepEqual(containers[0], want) || !slices.EqualFunc(containers[1:], initial.Spec.Template.Spec.InitContainers, func(a, b corev1.Container) bool { return reflect.DeepEqual(a, b) }) {
					t.Fatalf("one-shot initializer or existing sidecars changed: got %+v, want %+v followed by %+v", containers, want, initial.Spec.Template.Spec.InitContainers)
				}
			})
		}
	}
}

func validateRenderConfig(t *testing.T, spec *api.YanetConfigSpec) {
	t.Helper()
	validator := &api.YanetConfigCustomValidator{}
	if _, err := validator.ValidateCreate(context.Background(), &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: *spec}); err != nil {
		t.Fatalf("render fixture must pass admission validation: %v", err)
	}
}
