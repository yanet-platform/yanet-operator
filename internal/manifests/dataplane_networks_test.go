package manifests

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const testNetworks = `[{"name":"pf","interface":"eth2","resourceName":"example.net/pf"},{"name":"pf","interface":"eth4","resourceName":"example.net/pf"}]`

func TestDataplaneNetworksDefaultsAndOverrides(t *testing.T) {
	for _, tt := range []struct {
		name, override, annotation, resource string
		count                                int64
	}{
		{"inherit", `{}`, `[{"name":"pf","namespace":"yanet","interface":"eth2"},{"name":"pf","namespace":"yanet","interface":"eth4"}]`, "example.net/pf", 2},
		{"null inherits", `{"networks":null}`, `[{"name":"pf","namespace":"yanet","interface":"eth2"},{"name":"pf","namespace":"yanet","interface":"eth4"}]`, "example.net/pf", 2},
		{"replace", `{"networks":[{"name":"other-pf","namespace":"networks","interface":"eth6","resourceName":"example.net/other"}]}`, `[{"name":"other-pf","namespace":"networks","interface":"eth6"}]`, "example.net/other", 1},
		{"clear", `{"networks":[]}`, "", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			if err := json.Unmarshal([]byte(`{"networks":`+testNetworks+`}`), &config.Components.Dataplane); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(`{"dataplane":`+tt.override+`}`), spec.Components); err != nil {
				t.Fatal(err)
			}
			// Simulate API serialization: an explicit [] must not become inheritance.
			encoded, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip api.YanetSpec
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			config.Patches = []api.NamedPatch{{Name: "cpu", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","resources":{"requests":{"cpu":"2"},"limits":{"memory":"4Gi"}}}]}}}}`)}}}
			config.BoxTypes[0].Components.Dataplane.Patches = []string{"cpu"}
			before := config.DeepCopy()
			deployment, _ := renderRuntimeDataplane(t, config, &roundTrip)
			if got := deployment.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]; got != tt.annotation {
				t.Fatalf("networks annotation = %q, want %q", got, tt.annotation)
			}
			resources := deployment.Spec.Template.Spec.Containers[0].Resources
			if resources.Requests.Cpu().String() != "2" || resources.Limits.Memory().String() != "4Gi" {
				t.Fatalf("unrelated resources lost: %+v", resources)
			}
			for _, name := range []string{"example.net/pf", "example.net/other"} {
				requests, requestSet := resources.Requests[corev1.ResourceName(name)]
				limits, limitSet := resources.Limits[corev1.ResourceName(name)]
				if name == tt.resource {
					if !requestSet || !limitSet || requests.Value() != tt.count || limits.Value() != tt.count {
						t.Fatalf("%s allocation = %v/%v, want %d", name, requests, limits, tt.count)
					}
				} else if requestSet || limitSet {
					t.Fatalf("stale network resource %s retained", name)
				}
			}
			if !reflect.DeepEqual(before, config) {
				t.Fatal("render mutated the shared palette")
			}
		})
	}
}

func TestDataplaneNetworksRejectPatchConflicts(t *testing.T) {
	for _, tt := range []struct{ name, patch, override string }{
		{"annotation", `{"spec":{"template":{"metadata":{"annotations":{"k8s.v1.cni.cncf.io/networks":"other"}}}}}`, ""},
		{"request", `{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","resources":{"requests":{"example.net/pf":"3"}}}]}}}}`, ""},
		{"limit after clear", `{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","resources":{"limits":{"example.net/pf":"2"}}}]}}}}`, `{"networks":[]}`},
		{"stale replaced resource", `{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","resources":{"limits":{"example.net/pf":"2"}}}]}}}}`, `{"networks":[{"name":"other","interface":"eth6","resourceName":"example.net/other"}]}`},
		{"init container", `{"spec":{"template":{"spec":{"initContainers":[{"name":"setup","image":"setup","resources":{"limits":{"example.net/pf":"1"}}}]}}}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			if err := json.Unmarshal([]byte(`{"networks":`+testNetworks+`}`), &config.Components.Dataplane); err != nil {
				t.Fatal(err)
			}
			if tt.override != "" {
				if err := json.Unmarshal([]byte(`{"dataplane":`+tt.override+`}`), spec.Components); err != nil {
					t.Fatal(err)
				}
			}
			config.Patches = []api.NamedPatch{{Name: "conflict", Patch: runtime.RawExtension{Raw: []byte(tt.patch)}}}
			config.BoxTypes[0].Components.Dataplane.Patches = []string{"conflict"}
			component, err := helpers.ResolveBoxComponent(config, spec, helpers.KindDataplane, "")
			if err != nil {
				t.Fatal(err)
			}
			deployments, err := RenderDeployments(ctxV2(), component, NewPatchRegistry(config.Patches))
			if err == nil || !strings.Contains(err.Error(), "conflict") || len(deployments) != 0 {
				t.Fatalf("conflicting network patch rendered: %v / %v", deployments, err)
			}
		})
	}
}

func TestDataplaneNetworksUnmanagedPatchesRemainSupported(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	config.Patches = []api.NamedPatch{{Name: "network", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"metadata":{"annotations":{"k8s.v1.cni.cncf.io/networks":"legacy"}},"spec":{"containers":[{"name":"dataplane","resources":{"limits":{"example.net/legacy":"1"}}}]}}}}`)}}}
	config.BoxTypes[0].Components.Dataplane.Patches = []string{"network"}
	deployment, _ := renderRuntimeDataplane(t, config, spec)
	if deployment.Spec.Template.Annotations[multusNetworksAnnotation] != "legacy" {
		t.Fatal("unmanaged network patch lost")
	}
	quantity, present := deployment.Spec.Template.Spec.Containers[0].Resources.Limits["example.net/legacy"]
	if !present || quantity.Value() != 1 {
		t.Fatal("unmanaged device resource lost")
	}
}

func TestDataplaneNetworksRejectNamespaceAliasConflict(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	config.Components.Dataplane.Networks = []api.NetworkAttachment{
		{Name: "pf", Interface: "eth2", ResourceName: "example.net/pf"},
		{Name: "pf", Namespace: "yanet", Interface: "eth4", ResourceName: "example.net/other"},
	}
	component, err := helpers.ResolveBoxComponent(config, spec, helpers.KindDataplane, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderDeployments(ctxV2(), component, nil); err == nil {
		t.Fatal("same effective NAD advertised with two resource identities")
	}
}
