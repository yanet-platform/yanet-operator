package manifests

import (
	"encoding/json"
	"reflect"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func overridePalette(t *testing.T) *api.YanetConfigSpec {
	t.Helper()
	var config api.YanetConfigSpec
	if err := json.Unmarshal([]byte(`{
		"images":{"registry":"registry.example","prefix":"runtime","tag":"stable"},
		"components":{
			"controlplane":{"image":{"name":"cp"}},
			"dataplane":{"image":{"name":"dp"},"hugepages":{"size":"2Mi"},
				"sidecars":[{"name":"bird","image":{"name":"bird"}}]},
			"birdAdapter":{"image":{"name":"adapter"}},
			"operators":[{"name":"announcer","containers":[{"name":"worker","image":{"name":"announcer"}},{"name":"helper","image":{"name":"helper"}}]}]},
		"patches":[
			{"name":"standby","patch":{"spec":{"replicas":0}}},
			{"name":"images","patch":{"spec":{"template":{"spec":{"containers":[
				{"name":"worker","image":"patched:old","env":[{"name":"EXTRA","value":"keep"}]},{"name":"helper","image":"patched-helper:keep"}]}}}}},
			{"name":"bird-image","patch":{"spec":{"template":{"spec":{"containers":[{"name":"bird","image":"patched:old"}]}}}}}
		],
		"boxTypes":[{"name":"test","components":{"controlplane":{"patches":["standby"]},
			"dataplane":{"sidecars":{"bird":{"patches":["bird-image"]}}},"birdAdapter":{"patches":["standby"]}},
			"operators":{"announcer":{"patches":["standby","images"]}}}]
	}`), &config); err != nil {
		t.Fatal(err)
	}
	return &config
}

func TestRenderDeployments_ExplicitEnablementWinsStandbyPatch(t *testing.T) {
	for _, kind := range []helpers.ComponentKind{helpers.KindControlplane, helpers.KindDataplane, helpers.KindBirdAdapter, helpers.KindOperator} {
		for _, enabled := range []*bool{nil, helpers.PtrTrue(), helpers.PtrFalse()} {
			t.Run(string(kind)+"/"+pointerLabel(enabled), func(t *testing.T) {
				config := overridePalette(t)
				config.BoxTypes[0].Components.Dataplane.Patches = []string{"standby"}
				field := string(kind)
				if kind == helpers.KindOperator {
					field = "operators"
				}
				value := map[string]any{"enabled": enabled}
				if kind == helpers.KindOperator {
					value = map[string]any{"announcer": value}
				}
				raw, err := json.Marshal(map[string]any{"boxType": "test", "components": map[string]any{field: value}})
				if err != nil {
					t.Fatal(err)
				}
				var installation api.YanetSpec
				if err = json.Unmarshal(raw, &installation); err != nil {
					t.Fatal(err)
				}
				component, err := helpers.ResolveBoxComponent(config, &installation, kind, "announcer")
				if err != nil {
					t.Fatal(err)
				}
				build := ctx()
				build.NodeAllocatable = corev1.ResourceList{"hugepages-2Mi": resource.MustParse("8Gi")}
				deployments, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
				if err != nil {
					t.Fatal(err)
				}
				want := int32(0)
				if enabled != nil && *enabled {
					want = 1
				}
				if got := *deployments[0].Spec.Replicas; got != want {
					t.Fatalf("replicas = %d, want %d", got, want)
				}
			})
		}
	}
}

func pointerLabel(value *bool) string {
	if value == nil {
		return "inherit"
	}
	if *value {
		return "enabled"
	}
	return "disabled"
}

func TestRenderDeployments_ExplicitImagesWinPalettePatches(t *testing.T) {
	config := overridePalette(t)
	installation := &api.YanetSpec{BoxType: "test", Components: &api.YanetComponentsOverride{
		Operators: map[string]api.YanetComponentOverride{"announcer": {
			Containers: map[string]api.YanetContainerOverride{"worker": {Tag: "candidate"}},
		}},
	}}
	component, err := helpers.ResolveBoxComponent(config, installation, helpers.KindOperator, "announcer")
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := RenderDeployments(ctx(), component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	containers := deployments[0].Spec.Template.Spec.Containers
	images := map[string]string{}
	for _, container := range containers {
		images[container.Name] = container.Image
	}
	if images["worker"] != "registry.example/runtime/announcer:candidate" || images["helper"] != "patched-helper:keep" {
		t.Fatalf("images = %+v; explicit image must win while unrelated patch survives", containers)
	}
	for _, container := range containers {
		if container.Name == "worker" && envValues(container.Env)["EXTRA"] != "keep" {
			t.Fatal("image override lost unrelated patched environment")
		}
	}
	component, err = helpers.ResolveBoxComponent(config, &api.YanetSpec{BoxType: "test", Components: &api.YanetComponentsOverride{
		Dataplane: &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{"bird": {Name: "custom-bird", Tag: "candidate"}}},
	}}, helpers.KindDataplane, "")
	if err != nil {
		t.Fatal(err)
	}
	build := ctx()
	build.NodeAllocatable = corev1.ResourceList{"hugepages-2Mi": resource.MustParse("8Gi")}
	deployments, err = RenderDeployments(build, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	if got := deployments[0].Spec.Template.Spec.InitContainers[0].Image; got != "registry.example/runtime/custom-bird:candidate" {
		t.Fatalf("sidecar image = %s", got)
	}
	// A later dataplane patch can address the already composed native sidecar.
	var final api.NamedPatch
	if err = json.Unmarshal([]byte(`{"name":"final","patch":{"spec":{"template":{"spec":{"initContainers":[{"name":"`+deployments[0].Spec.Template.Spec.InitContainers[0].Name+`","image":"patched-again:old"}]}}}}}`), &final); err != nil {
		t.Fatal(err)
	}
	config.Patches = append(config.Patches, final)
	component.Patches = []string{"final"}
	deployments, err = RenderDeployments(build, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	if got := deployments[0].Spec.Template.Spec.InitContainers[0].Image; got != "registry.example/runtime/custom-bird:candidate" {
		t.Fatalf("final dataplane patch beat explicit sidecar image: %s", got)
	}
}

func TestRenderDeployments_IgnoresRemovedOperatorContainerOverrides(t *testing.T) {
	config := overridePalette(t)
	config.Components.Operators[0].Containers = config.Components.Operators[0].Containers[:1]
	config.BoxTypes[0].Operators["announcer"] = api.BoxOperator{Patches: []string{"standby"}}
	installation := &api.YanetSpec{BoxType: "test", Components: &api.YanetComponentsOverride{
		Operators: map[string]api.YanetComponentOverride{"announcer": {
			Containers: map[string]api.YanetContainerOverride{
				"worker": {Tag: "candidate"},
				"helper": {Tag: "old-override"},
			},
		}},
	}}
	component, err := helpers.ResolveBoxComponent(config, installation, helpers.KindOperator, "announcer")
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := RenderDeployments(ctx(), component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	containers := deployments[0].Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "worker" || containers[0].Image != "registry.example/runtime/announcer:candidate" {
		t.Fatalf("palette removal must retain only the current overridden container: %+v", containers)
	}
	if *deployments[0].Spec.Replicas != 0 || installation.Components.Operators["announcer"].Containers["helper"].Tag != "old-override" {
		t.Fatal("rendering changed inherited replicas or the installation's stale override")
	}
}

func TestRenderDeployments_FixedComponentImageOverrides(t *testing.T) {
	for _, tc := range []struct {
		kind             helpers.ComponentKind
		container, image string
	}{
		{helpers.KindControlplane, "controlplane", "cp"},
		{helpers.KindDataplane, "dataplane", "dp"},
		{helpers.KindBirdAdapter, "bird-adapter", "adapter"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			config := overridePalette(t)
			var patch api.NamedPatch
			if err := json.Unmarshal([]byte(`{"name":"fixed-image","patch":{"spec":{"template":{"spec":{"containers":[{"name":"`+tc.container+`","image":"patched:old","env":[{"name":"EXTRA","value":"keep"}]}]}}}}}`), &patch); err != nil {
				t.Fatal(err)
			}
			config.Patches = append(config.Patches, patch)
			switch tc.kind {
			case helpers.KindControlplane:
				config.BoxTypes[0].Components.Controlplane.Patches = []string{"fixed-image"}
			case helpers.KindDataplane:
				config.BoxTypes[0].Components.Dataplane.Patches = []string{"fixed-image"}
			case helpers.KindBirdAdapter:
				config.BoxTypes[0].Components.BirdAdapter.Patches = []string{"fixed-image"}
			}
			var installation api.YanetSpec
			if err := json.Unmarshal([]byte(`{"boxType":"test","components":{"`+string(tc.kind)+`":{"containers":{"`+tc.container+`":{"tag":"candidate"}}}}}`), &installation); err != nil {
				t.Fatal(err)
			}
			component, err := helpers.ResolveBoxComponent(config, &installation, tc.kind, "")
			if err != nil {
				t.Fatal(err)
			}
			build := ctx()
			build.NodeAllocatable = corev1.ResourceList{"hugepages-2Mi": resource.MustParse("8Gi")}
			deployments, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
			if err != nil {
				t.Fatal(err)
			}
			container := deployments[0].Spec.Template.Spec.Containers[0]
			if container.Image != "registry.example/runtime/"+tc.image+":candidate" || envValues(container.Env)["EXTRA"] != "keep" {
				t.Fatalf("explicit image or unrelated env lost: %+v", container)
			}
		})
	}
}

func TestRenderDeployments_HugepagesWinsResourcePatches(t *testing.T) {
	config := overridePalette(t)
	var patch api.NamedPatch
	if err := json.Unmarshal([]byte(`{"name":"resources","patch":{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","resources":{"requests":{"cpu":"2","hugepages-2Mi":"2Gi","hugepages-1Gi":"1Gi"},"limits":{"memory":"4Gi","hugepages-2Mi":"2Gi","hugepages-1Gi":"1Gi"}}}]}}}}}`), &patch); err != nil {
		t.Fatal(err)
	}
	config.Patches = append(config.Patches, patch)
	config.BoxTypes[0].Components.Dataplane.Patches = []string{"resources"}
	component, err := helpers.ResolveBoxComponent(config, &api.YanetSpec{BoxType: "test"}, helpers.KindDataplane, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, pool := range []string{"8Gi", "16Gi"} {
		build := ctx()
		build.NodeAllocatable = corev1.ResourceList{"hugepages-2Mi": resource.MustParse(pool)}
		deployments, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
		if err != nil {
			t.Fatal(err)
		}
		resources := deployments[0].Spec.Template.Spec.Containers[0].Resources
		for name, values := range map[string]corev1.ResourceList{"requests": resources.Requests, "limits": resources.Limits} {
			got := values["hugepages-2Mi"]
			if got.Cmp(resource.MustParse(pool)) != 0 {
				t.Errorf("%s hugepages = %s, want node pool %s", name, got.String(), pool)
			}
			if _, stale := values["hugepages-1Gi"]; stale {
				t.Fatal("patch retained a stale hugepage size")
			}
		}
		if resources.Requests.Cpu().String() != "2" || resources.Limits.Memory().String() != "4Gi" {
			t.Fatal("unrelated resources lost")
		}
	}
	if component.Hugepages.Count != 0 || config.Components.Dataplane.Hugepages.Count != 0 {
		t.Fatal("node count leaked into reusable rendering input")
	}
}

func TestRenderDeployments_HugepagesInstallationPaletteNodePrecedence(t *testing.T) {
	for _, tc := range []struct{ name, override, wantKey, want string }{
		{"inherit palette", `null`, "hugepages-2Mi", "8Gi"},
		{"explicit count", `{"size":"2Mi","count":1024}`, "hugepages-2Mi", "2Gi"},
		{"explicit size automatic count", `{"size":"1Gi"}`, "hugepages-1Gi", "4Gi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := overridePalette(t)
			before := config.DeepCopy()
			var installation api.YanetSpec
			if err := json.Unmarshal([]byte(`{"boxType":"test","components":{"dataplane":{"hugepages":`+tc.override+`}}}`), &installation); err != nil {
				t.Fatal(err)
			}
			component, err := helpers.ResolveBoxComponent(config, &installation, helpers.KindDataplane, "")
			if err != nil {
				t.Fatal(err)
			}
			build := ctx()
			build.NodeAllocatable = corev1.ResourceList{"hugepages-2Mi": resource.MustParse("8Gi"), "hugepages-1Gi": resource.MustParse("4Gi")}
			deployments, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
			if err != nil {
				t.Fatal(err)
			}
			resources := deployments[0].Spec.Template.Spec.Containers[0].Resources
			for name, values := range map[string]corev1.ResourceList{"requests": resources.Requests, "limits": resources.Limits} {
				got := values[corev1.ResourceName(tc.wantKey)]
				if len(values) != 1 || got.Cmp(resource.MustParse(tc.want)) != 0 {
					t.Errorf("%s = %v, want only %s=%s", name, values, tc.wantKey, tc.want)
				}
			}
			if !reflect.DeepEqual(before, config) {
				t.Fatal("rendering mutated the shared palette")
			}
		})
	}
}
