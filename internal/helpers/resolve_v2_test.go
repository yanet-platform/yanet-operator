/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

func fixtureConfig() *yanetv2alpha1.YanetConfigSpec {
	return &yanetv2alpha1.YanetConfigSpec{
		Images: yanetv2alpha1.ImagesSpec{Registry: "registry.example/test", Prefix: "edge"},
		Components: yanetv2alpha1.ComponentsSpec{
			Controlplane: yanetv2alpha1.ControlplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "controlplane", Tag: "v2.1"},
				Numa:  Int32Ptr(2),
			},
			Dataplane: yanetv2alpha1.DataplaneSpec{
				Image:       yanetv2alpha1.ImageRef{Name: "dataplane", Tag: "v2.1"},
				Hugepages:   &yanetv2alpha1.Hugepages{Size: "1Gi", Count: 8},
				HostNetwork: PtrTrue(),
				Sidecars: &yanetv2alpha1.DataplaneSidecarsSpec{
					Bird: &yanetv2alpha1.DataplaneSidecarSpec{
						Image: yanetv2alpha1.ImageRef{Name: "bird", Tag: "2.15"},
					},
					NetlinkDataplaneSidecar: &yanetv2alpha1.DataplaneSidecarSpec{
						Image: yanetv2alpha1.ImageRef{Name: "netlink-dataplane-sidecar", Tag: "v2.1"},
					},
				},
			},
			BirdAdapter: &yanetv2alpha1.BirdAdapterComp{
				Image: yanetv2alpha1.ImageRef{Name: "bird-adapter", Tag: "v0.3"},
			},
			Announcer: &yanetv2alpha1.AnnouncerComp{
				Image: yanetv2alpha1.ImageRef{Name: "announcer", Tag: "v0.2"},
			},
			Operators: []yanetv2alpha1.OperatorSpec{
				{
					Name: "antiddos",
					Containers: []yanetv2alpha1.OperatorContainer{
						{Name: "operator", Image: yanetv2alpha1.ImageRef{Name: "antiddos-operator", Tag: "v0.5"}},
						{Name: "agent", Image: yanetv2alpha1.ImageRef{Name: "antiddos-agent", Tag: "v0.5"}, HostIPC: PtrTrue()},
					},
				},
				{
					Name: "route",
					Containers: []yanetv2alpha1.OperatorContainer{{
						Name: "route", Image: yanetv2alpha1.ImageRef{Name: "route-operator", Tag: "v0.4"},
					}},
				},
			},
		},
		Patches: []yanetv2alpha1.NamedPatch{
			{Name: "telegraf"},
			{Name: "checkpointer"},
			{Name: "cp-resources"},
		},
		BoxTypes: []yanetv2alpha1.BoxType{
			{
				Name: "release",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{Patches: []string{"telegraf", "cp-resources"}},
					Dataplane: &yanetv2alpha1.BoxDataplane{
						Patches: []string{"telegraf"},
						Sidecars: &yanetv2alpha1.BoxDataplaneSidecars{
							Bird:                    &yanetv2alpha1.BoxDataplaneSidecar{},
							NetlinkDataplaneSidecar: &yanetv2alpha1.BoxDataplaneSidecar{},
						},
					},
					Announcer: &yanetv2alpha1.BoxComponent{},
				},
			},
			{
				Name: "firewall",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{},
					Dataplane: &yanetv2alpha1.BoxDataplane{Sidecars: &yanetv2alpha1.BoxDataplaneSidecars{
						Bird:                    &yanetv2alpha1.BoxDataplaneSidecar{},
						NetlinkDataplaneSidecar: &yanetv2alpha1.BoxDataplaneSidecar{},
					}},
					BirdAdapter: &yanetv2alpha1.BoxComponent{},
				},
				Operators: map[string]yanetv2alpha1.BoxOperator{
					"antiddos": {Patches: []string{"telegraf"}},
				},
			},
			{
				Name: "minimal",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{},
					Dataplane:    &yanetv2alpha1.BoxDataplane{},
				},
			},
		},
	}
}

func TestFindBoxTypeAndOperator(t *testing.T) {
	config := fixtureConfig()
	if box, err := FindBoxType(config, "firewall"); err != nil || box.Name != "firewall" {
		t.Fatalf("FindBoxType = (%v, %v)", box, err)
	}
	if _, err := FindBoxType(config, "missing"); err == nil {
		t.Fatal("FindBoxType(missing) must fail")
	}
	if operator, err := FindOperator(config, "antiddos"); err != nil || operator.Name != "antiddos" {
		t.Fatalf("FindOperator = (%v, %v)", operator, err)
	}
	if _, err := FindOperator(config, "missing"); err == nil {
		t.Fatal("FindOperator(missing) must fail")
	}
}

func TestEnabledComponentsForBox(t *testing.T) {
	config := fixtureConfig()
	want := []ComponentRef{
		{Kind: KindControlplane},
		{Kind: KindDataplane},
		{Kind: KindBirdAdapter},
		{Kind: KindOperator, OperatorName: "antiddos"},
	}
	got, err := EnabledComponentsForBox(config, "firewall")
	if err != nil {
		t.Fatalf("EnabledComponentsForBox: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("component refs mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveBoxComponent_Hardcoded(t *testing.T) {
	config := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}
	controlplane, err := ResolveBoxComponent(config, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("resolve controlplane: %v", err)
	}
	if controlplane.Name != "controlplane" || controlplane.Numa != 2 || !controlplane.Enabled {
		t.Fatalf("controlplane = %+v", controlplane)
	}
	if controlplane.Image.FullPath() != "registry.example/test/edge/controlplane:v2.1" {
		t.Fatalf("controlplane image = %+v", controlplane.Image)
	}
	if diff := cmp.Diff([]string{"telegraf", "cp-resources"}, controlplane.Patches); diff != "" {
		t.Fatalf("patches mismatch (-want +got):\n%s", diff)
	}
	dataplane, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve dataplane: %v", err)
	}
	if dataplane.Hugepages == nil || dataplane.Hugepages.Count != 8 ||
		dataplane.HostNetwork == nil || !*dataplane.HostNetwork {
		t.Fatalf("dataplane = %+v", dataplane)
	}
	if len(dataplane.NativeSidecars) != 2 ||
		dataplane.NativeSidecars[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName ||
		dataplane.NativeSidecars[1].Name != yanetv2alpha1.BirdSidecarContainerName {
		t.Fatalf("dataplane native sidecars = %+v", dataplane.NativeSidecars)
	}
	if adapter, err := ResolveBoxComponent(config, yanet, KindBirdAdapter, ""); err != nil || adapter != nil {
		t.Fatalf("unwired adapter = (%v, %v)", adapter, err)
	}
}

func TestResolveBoxComponent_Operator(t *testing.T) {
	config := fixtureConfig()
	operator, err := ResolveBoxComponent(
		config,
		&yanetv2alpha1.YanetSpec{BoxType: "firewall"},
		KindOperator,
		"antiddos",
	)
	if err != nil {
		t.Fatalf("resolve operator: %v", err)
	}
	if operator.Name != "antiddos" || operator.Kind != KindOperator || len(operator.Containers) != 2 {
		t.Fatalf("operator = %+v", operator)
	}
	if operator.Containers[0].Name != "operator" || operator.Containers[1].Name != "agent" ||
		!operator.Containers[1].HostIPC {
		t.Fatalf("operator containers = %+v", operator.Containers)
	}
}

func TestResolveBoxComponent_Overrides(t *testing.T) {
	config := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "firewall",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
				YanetComponentOverride: yanetv2alpha1.YanetComponentOverride{
					Enabled: PtrFalse(),
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						"controlplane": {Tag: "hotfix"},
					},
				},
			},
			Dataplane: &yanetv2alpha1.YanetComponentOverride{
				Containers: map[string]yanetv2alpha1.YanetContainerOverride{
					yanetv2alpha1.BirdSidecarContainerName: {
						Enabled: PtrFalse(),
					},
					yanetv2alpha1.NetlinkDataplaneSidecarContainerName: {Tag: "hotfix"},
				},
			},
			BirdAdapter: &yanetv2alpha1.YanetComponentOverride{
				Containers: map[string]yanetv2alpha1.YanetContainerOverride{
					yanetv2alpha1.BirdAdapterContainerName: {Tag: "hotfix"},
				},
			},
			Operators: map[string]yanetv2alpha1.YanetComponentOverride{
				"antiddos": {Containers: map[string]yanetv2alpha1.YanetContainerOverride{
					"operator": {Tag: "v0.5.1"},
					"agent":    {Tag: "v0.5.2"},
				}},
			},
		},
	}
	controlplane, err := ResolveBoxComponent(config, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("resolve controlplane: %v", err)
	}
	if controlplane.Enabled || controlplane.Image.Tag != "hotfix" {
		t.Fatalf("controlplane override = %+v", controlplane)
	}
	dataplane, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve dataplane: %v", err)
	}
	if len(dataplane.NativeSidecars) != 1 ||
		dataplane.NativeSidecars[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName ||
		dataplane.NativeSidecars[0].Image.Tag != "hotfix" {
		t.Fatalf("dataplane sidecar overrides = %+v", dataplane.NativeSidecars)
	}
	adapter, err := ResolveBoxComponent(config, yanet, KindBirdAdapter, "")
	if err != nil {
		t.Fatalf("resolve bird adapter: %v", err)
	}
	if adapter.Image.Tag != "hotfix" {
		t.Fatalf("bird-adapter override = %+v", adapter.Image)
	}
	operator, err := ResolveBoxComponent(config, yanet, KindOperator, "antiddos")
	if err != nil {
		t.Fatalf("resolve operator: %v", err)
	}
	if operator.Containers[0].Image.Tag != "v0.5.1" || operator.Containers[1].Image.Tag != "v0.5.2" {
		t.Fatalf("operator overrides = %+v", operator.Containers)
	}
}

func TestResolveBoxComponent_DataplaneSidecarEnablement(t *testing.T) {
	config := fixtureConfig()
	box, err := FindBoxType(config, "release")
	if err != nil {
		t.Fatalf("find box: %v", err)
	}
	box.Components.Dataplane.Sidecars.Bird.Enabled = PtrFalse()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}

	dataplane, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve defaults: %v", err)
	}
	if len(dataplane.NativeSidecars) != 1 ||
		dataplane.NativeSidecars[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName {
		t.Fatalf("box-disabled BIRD sidecar = %+v", dataplane.NativeSidecars)
	}

	yanet.Components = &yanetv2alpha1.YanetComponentsOverride{
		Dataplane: &yanetv2alpha1.YanetComponentOverride{
			Containers: map[string]yanetv2alpha1.YanetContainerOverride{
				yanetv2alpha1.BirdSidecarContainerName: {Enabled: PtrTrue()},
			},
		},
	}
	dataplane, err = ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve override: %v", err)
	}
	if len(dataplane.NativeSidecars) != 2 ||
		dataplane.NativeSidecars[1].Name != yanetv2alpha1.BirdSidecarContainerName {
		t.Fatalf("installation-enabled BIRD sidecar = %+v", dataplane.NativeSidecars)
	}

	box.Components.Dataplane.Sidecars.Bird = nil
	dataplane, err = ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve stale unwired override: %v", err)
	}
	if len(dataplane.NativeSidecars) != 1 ||
		dataplane.NativeSidecars[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName {
		t.Fatalf("stale unwired BIRD override was not ignored: %+v", dataplane.NativeSidecars)
	}
}

func TestResolveControlplane_DisabledNuma(t *testing.T) {
	config := fixtureConfig()
	config.Components.Controlplane.DisabledNuma = []int32{1}
	tests := []struct {
		name     string
		override bool
		disabled []int32
		want     []int32
	}{
		{name: "inherit", want: []int32{1}},
		{name: "inherit with null list", override: true, want: []int32{1}},
		{name: "replace", disabled: []int32{0}, want: []int32{0}},
		{name: "clear", disabled: []int32{}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}
			if tt.override || tt.disabled != nil {
				yanet.Components = &yanetv2alpha1.YanetComponentsOverride{
					Controlplane: &yanetv2alpha1.YanetControlplaneOverride{DisabledNuma: tt.disabled},
				}
			}
			// Typed client updates must preserve the explicit empty-list override.
			raw, err := json.Marshal(yanet)
			if err != nil {
				t.Fatalf("marshal Yanet spec: %v", err)
			}
			yanet = &yanetv2alpha1.YanetSpec{}
			if err = json.Unmarshal(raw, yanet); err != nil {
				t.Fatalf("unmarshal Yanet spec: %v", err)
			}
			component, err := ResolveBoxComponent(config, yanet, KindControlplane, "")
			if err != nil {
				t.Fatalf("resolve controlplane: %v", err)
			}
			if diff := cmp.Diff(tt.want, component.DisabledNuma); diff != "" {
				t.Fatalf("disabled NUMA mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolveBoxComponent_ImageLocationOverrides(t *testing.T) {
	registry := "other.example/public"
	prefix := "stable"
	empty := ""
	tests := []struct {
		name     string
		registry *string
		prefix   *string
		want     string
	}{
		{name: "inherit", want: "registry.example/test/edge/controlplane:hotfix"},
		{name: "registry only", registry: &registry, want: "other.example/public/edge/controlplane:hotfix"},
		{name: "prefix only", prefix: &prefix, want: "registry.example/test/stable/controlplane:hotfix"},
		{name: "both", registry: &registry, prefix: &prefix, want: "other.example/public/stable/controlplane:hotfix"},
		{name: "clear prefix", registry: &registry, prefix: &empty, want: "other.example/public/controlplane:hotfix"},
		{name: "clear registry", registry: &empty, want: "edge/controlplane:hotfix"},
		{name: "clear both", registry: &empty, prefix: &empty, want: "controlplane:hotfix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := fixtureConfig()
			config.Components.Controlplane.Image.Registry = tt.registry
			config.Components.Controlplane.Image.Prefix = tt.prefix
			yanet := &yanetv2alpha1.YanetSpec{
				BoxType: "release",
				Components: &yanetv2alpha1.YanetComponentsOverride{
					Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
						YanetComponentOverride: yanetv2alpha1.YanetComponentOverride{
							Containers: map[string]yanetv2alpha1.YanetContainerOverride{
								"controlplane": {Tag: "hotfix"},
							},
						},
					},
				},
			}
			component, err := ResolveBoxComponent(config, yanet, KindControlplane, "")
			if err != nil {
				t.Fatalf("resolve controlplane: %v", err)
			}
			if got := component.Image.FullPath(); got != tt.want {
				t.Fatalf("image = %q, want %q", got, tt.want)
			}
			if config.Components.Controlplane.Image.Tag != "v2.1" {
				t.Fatal("resolving the override mutated the palette")
			}
		})
	}
}

func TestResolveBoxComponent_MixedRegistryPalette(t *testing.T) {
	config := fixtureConfig()
	publicRegistry, privateRegistry, empty := "docker.io/test", "private.example/test", ""
	config.Components.Dataplane.Image.Registry = &privateRegistry
	config.Components.Dataplane.Sidecars.Bird.Image.Registry = &publicRegistry
	config.Components.Dataplane.Sidecars.Bird.Image.Prefix = &empty
	config.Components.Operators[0].Containers[0].Image.Registry = &privateRegistry
	config.Components.Operators[0].Containers[1].Image.Registry = &publicRegistry
	config.Components.Operators[0].Containers[1].Image.Prefix = &empty
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "firewall"}
	dataplane, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
	if err != nil {
		t.Fatalf("resolve dataplane: %v", err)
	}
	operator, err := ResolveBoxComponent(config, yanet, KindOperator, "antiddos")
	if err != nil {
		t.Fatalf("resolve operator: %v", err)
	}
	if len(dataplane.NativeSidecars) != 2 || len(operator.Containers) != 2 {
		t.Fatalf("unexpected resolved container counts: dataplane=%+v operator=%+v", dataplane, operator)
	}
	want := []string{
		"private.example/test/edge/dataplane:v2.1",
		"registry.example/test/edge/netlink-dataplane-sidecar:v2.1",
		"docker.io/test/bird:2.15",
		"private.example/test/edge/antiddos-operator:v0.5",
		"docker.io/test/antiddos-agent:v0.5",
	}
	got := []string{
		dataplane.Image.FullPath(),
		dataplane.NativeSidecars[0].Image.FullPath(),
		dataplane.NativeSidecars[1].Image.FullPath(),
		operator.Containers[0].Image.FullPath(),
		operator.Containers[1].Image.FullPath(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("mixed-registry images mismatch (-want +got):\n%s", diff)
	}
}

func TestEnabledComponentsForBox_UndeclaredOperator(t *testing.T) {
	config := fixtureConfig()
	config.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"missing": {}}
	if refs, err := EnabledComponentsForBox(config, "release"); err == nil {
		t.Fatalf("undeclared wired operator must fail rather than disappear from the desired set: %+v", refs)
	}
}

func TestResolveBoxComponent_Errors(t *testing.T) {
	config := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}
	if _, err := ResolveBoxComponent(nil, yanet, KindControlplane, ""); err == nil {
		t.Error("nil config must fail")
	}
	if _, err := ResolveBoxComponent(config, nil, KindControlplane, ""); err == nil {
		t.Error("nil Yanet spec must fail")
	}
	if _, err := ResolveBoxComponent(config, &yanetv2alpha1.YanetSpec{BoxType: "ghost"}, KindControlplane, ""); err == nil {
		t.Error("unknown box type must fail")
	}
	if _, err := ResolveBoxComponent(config, yanet, ComponentKind("bogus"), ""); err == nil {
		t.Error("unknown component kind must fail")
	}
	config.Components.Dataplane.Sidecars.Bird = nil
	if _, err := ResolveBoxComponent(config, yanet, KindDataplane, ""); err == nil {
		t.Error("wired dataplane sidecar missing from palette must fail")
	}
}

func TestResolvedImage_FullPath(t *testing.T) {
	tests := []struct {
		image ResolvedImage
		want  string
	}{
		{image: ResolvedImage{Registry: "cr.io", Prefix: "edge", Name: "x", Tag: "v1"}, want: "cr.io/edge/x:v1"},
		{image: ResolvedImage{Registry: "cr.io", Name: "x", Tag: "v1"}, want: "cr.io/x:v1"},
		{image: ResolvedImage{Prefix: "edge", Name: "x", Tag: "v1"}, want: "edge/x:v1"},
		{image: ResolvedImage{Name: "x"}, want: "x"},
	}
	for _, tt := range tests {
		if got := tt.image.FullPath(); got != tt.want {
			t.Errorf("FullPath() = %q, want %q", got, tt.want)
		}
	}
}

func TestShortNodeKey_StableAndShort(t *testing.T) {
	first := ShortNodeKey("node-abc")
	if second := ShortNodeKey("node-abc"); first != second || len(first) != 8 {
		t.Fatalf("ShortNodeKey is not stable: %q and %q", first, second)
	}
	if other := ShortNodeKey("node-xyz"); other == first {
		t.Fatalf("unexpected short-key collision: %q", first)
	}
}
