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
	"testing"

	"github.com/google/go-cmp/cmp"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

// fixtureConfig returns a fully-populated YanetConfigSpec covering the
// 5 hardcoded components plus two operators. Tests mutate copies if
// they need variations.
func fixtureConfig() *yanetv2alpha1.YanetConfigSpec {
	return &yanetv2alpha1.YanetConfigSpec{
		Images: yanetv2alpha1.ImagesSpec{
			Registry: "cr.yandex/yanet",
			Prefix:   "edge",
		},
		Components: yanetv2alpha1.ComponentsSpec{
			Controlplane: yanetv2alpha1.ControlplaneSpec{
				Image:     yanetv2alpha1.ImageRef{Name: "controlplane", Tag: "v2.1"},
				Port:      8080,
				PortRange: 4,
				Numa:      Int32Ptr(2),
			},
			Dataplane: yanetv2alpha1.DataplaneSpec{
				Image:       yanetv2alpha1.ImageRef{Name: "dataplane", Tag: "v2.1"},
				Port:        8081,
				Hugepages:   &yanetv2alpha1.Hugepages{Size: "1Gi", Count: 8},
				HostNetwork: PtrTrue(),
			},
			Bird: &yanetv2alpha1.BirdComponent{
				Image: yanetv2alpha1.ImageRef{Name: "bird", Tag: "2.15"},
				Port:  179,
			},
			BirdAdapter: &yanetv2alpha1.BirdAdapterComp{
				Image: yanetv2alpha1.ImageRef{Name: "bird-adapter", Tag: "v0.3"},
			},
			Announcer: &yanetv2alpha1.AnnouncerComp{
				Image: yanetv2alpha1.ImageRef{Name: "announcer", Tag: "v0.2"},
				Port:  9090,
			},
			Operators: []yanetv2alpha1.OperatorSpec{
				{
					Name: "antiddos",
					Port: 9001,
					Containers: []yanetv2alpha1.OperatorContainer{
						{Name: "operator", Image: yanetv2alpha1.ImageRef{Name: "antiddos-operator", Tag: "v0.5"}},
						{Name: "agent", Image: yanetv2alpha1.ImageRef{Name: "antiddos-agent", Tag: "v0.5"}, HostIPC: PtrTrue()},
					},
				},
				{
					Name: "route",
					Containers: []yanetv2alpha1.OperatorContainer{
						{Name: "route", Image: yanetv2alpha1.ImageRef{Name: "route-operator", Tag: "v0.4"}},
					},
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
					Dataplane:    &yanetv2alpha1.BoxComponent{Patches: []string{"telegraf"}},
					Bird:         &yanetv2alpha1.BoxComponent{},
					Announcer:    &yanetv2alpha1.BoxComponent{},
				},
			},
			{
				Name: "firewall",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{},
					Dataplane:    &yanetv2alpha1.BoxComponent{},
					BirdAdapter:  &yanetv2alpha1.BoxComponent{},
				},
				Operators: map[string]yanetv2alpha1.BoxOperator{
					"antiddos": {Patches: []string{"telegraf"}},
				},
			},
			{
				Name: "minimal",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{},
					Dataplane:    &yanetv2alpha1.BoxComponent{},
				},
			},
		},
	}
}

func TestFindBoxType(t *testing.T) {
	cfg := fixtureConfig()
	if box, err := FindBoxType(cfg, "firewall"); err != nil || box.Name != "firewall" {
		t.Fatalf("FindBoxType(firewall) = (%v, %v)", box, err)
	}
	if _, err := FindBoxType(cfg, "missing"); err == nil {
		t.Fatalf("FindBoxType(missing) expected error")
	}
	if _, err := FindBoxType(nil, "release"); err == nil {
		t.Fatalf("FindBoxType(nil) expected error")
	}
}

func TestFindOperator(t *testing.T) {
	cfg := fixtureConfig()
	if op, err := FindOperator(cfg, "antiddos"); err != nil || op.Name != "antiddos" {
		t.Fatalf("FindOperator(antiddos) = (%v, %v)", op, err)
	}
	if _, err := FindOperator(cfg, "missing"); err == nil {
		t.Fatalf("FindOperator(missing) expected error")
	}
}

func TestEnabledComponentsForBox(t *testing.T) {
	cfg := fixtureConfig()
	tests := []struct {
		name string
		box  string
		want []ComponentRef
	}{
		{
			name: "release wires 4 hardcoded",
			box:  "release",
			want: []ComponentRef{
				{Kind: KindControlplane},
				{Kind: KindDataplane},
				{Kind: KindBird},
				{Kind: KindAnnouncer},
			},
		},
		{
			name: "firewall wires cp+dp+adapter+antiddos op",
			box:  "firewall",
			want: []ComponentRef{
				{Kind: KindControlplane},
				{Kind: KindDataplane},
				{Kind: KindBirdAdapter},
				{Kind: KindOperator, OperatorName: "antiddos"},
			},
		},
		{
			name: "minimal only cp+dp",
			box:  "minimal",
			want: []ComponentRef{
				{Kind: KindControlplane},
				{Kind: KindDataplane},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EnabledComponentsForBox(cfg, tt.box)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v refs, want %v: %#v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ref[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestResolveBoxComponent_Hardcoded(t *testing.T) {
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}

	// controlplane: image, port, port-range, numa, patches.
	cp, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil || cp == nil {
		t.Fatalf("controlplane resolve: (%v, %v)", cp, err)
	}
	if cp.Name != "controlplane" || cp.Kind != KindControlplane {
		t.Errorf("cp identity wrong: %+v", cp)
	}
	if cp.Image.Name != "controlplane" || cp.Image.Tag != "v2.1" || cp.Image.Registry != "cr.yandex/yanet" {
		t.Errorf("cp image: %+v", cp.Image)
	}
	if cp.Port != 8080 || cp.PortRange != 4 || cp.Numa != 2 {
		t.Errorf("cp ports/numa: port=%d range=%d numa=%d", cp.Port, cp.PortRange, cp.Numa)
	}
	if !cp.Enabled {
		t.Errorf("cp default enabled should be true")
	}
	wantPatches := []string{"telegraf", "cp-resources"}
	if len(cp.Patches) != len(wantPatches) {
		t.Fatalf("cp patches len mismatch: %v", cp.Patches)
	}
	for i, p := range wantPatches {
		if cp.Patches[i] != p {
			t.Errorf("cp.Patches[%d] = %q, want %q", i, cp.Patches[i], p)
		}
	}

	// dataplane: hugepages, host-network.
	dp, err := ResolveBoxComponent(cfg, yanet, KindDataplane, "")
	if err != nil || dp == nil {
		t.Fatalf("dataplane resolve: (%v, %v)", dp, err)
	}
	if dp.Hugepages == nil || dp.Hugepages.Size != "1Gi" || dp.Hugepages.Count != 8 {
		t.Errorf("dp hugepages: %+v", dp.Hugepages)
	}
	if dp.HostNetwork == nil || !*dp.HostNetwork {
		t.Errorf("dp hostnetwork = %v", dp.HostNetwork)
	}

	// bird/announcer enabled in box.
	if got, _ := ResolveBoxComponent(cfg, yanet, KindBird, ""); got == nil {
		t.Errorf("bird should be enabled in release boxType")
	}
	if got, _ := ResolveBoxComponent(cfg, yanet, KindAnnouncer, ""); got == nil {
		t.Errorf("announcer should be enabled in release")
	}
	// birdAdapter not in release.
	if got, err := ResolveBoxComponent(cfg, yanet, KindBirdAdapter, ""); got != nil || err != nil {
		t.Errorf("birdAdapter not in release boxType, want (nil,nil), got (%v, %v)", got, err)
	}
}

func TestResolveBoxComponent_Operator(t *testing.T) {
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "firewall"}

	op, err := ResolveBoxComponent(cfg, yanet, KindOperator, "antiddos")
	if err != nil || op == nil {
		t.Fatalf("operator antiddos resolve: (%v, %v)", op, err)
	}
	if op.Name != "antiddos" || op.Kind != KindOperator || op.Port != 9001 {
		t.Errorf("op identity/port: %+v", op)
	}
	if len(op.Containers) != 2 {
		t.Fatalf("operator container count = %d", len(op.Containers))
	}
	if op.Containers[0].Name != "operator" || op.Containers[0].HostIPC {
		t.Errorf("c0 = %+v", op.Containers[0])
	}
	if op.Containers[1].Name != "agent" || !op.Containers[1].HostIPC {
		t.Errorf("c1 = %+v", op.Containers[1])
	}
	if op.Image.Name != "antiddos-operator" {
		t.Errorf("op primary image = %+v", op.Image)
	}

	// Operator not wired by minimal boxType ⇒ (nil, nil).
	yanet2 := &yanetv2alpha1.YanetSpec{BoxType: "minimal"}
	if got, err := ResolveBoxComponent(cfg, yanet2, KindOperator, "antiddos"); got != nil || err != nil {
		t.Errorf("operator unwired: want (nil,nil), got (%v, %v)", got, err)
	}

	// Operator wired but missing in palette ⇒ error.
	cfg2 := fixtureConfig()
	cfg2.BoxTypes[1].Operators["ghost"] = yanetv2alpha1.BoxOperator{}
	if _, err := ResolveBoxComponent(cfg2, yanet, KindOperator, "ghost"); err == nil {
		t.Errorf("operator ghost should error: not declared in palette")
	}
}

func TestResolveBoxComponent_Overrides(t *testing.T) {
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "release",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
				YanetComponentOverride: yanetv2alpha1.YanetComponentOverride{
					Enabled: PtrFalse(),
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						"controlplane": {Tag: "v2.1.5-hotfix"},
					},
				},
			},
			Dataplane: &yanetv2alpha1.YanetComponentOverride{
				Containers: map[string]yanetv2alpha1.YanetContainerOverride{
					"dataplane": {Name: "dataplane-fork"},
				},
			},
		},
	}

	cp, _ := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if cp == nil {
		t.Fatalf("cp nil")
	}
	if cp.Enabled {
		t.Errorf("cp enabled should be false")
	}
	if cp.Image.Tag != "v2.1.5-hotfix" {
		t.Errorf("cp tag override failed: %+v", cp.Image)
	}
	if cp.Image.Name != "controlplane" {
		t.Errorf("cp name should remain palette: %+v", cp.Image)
	}

	dp, _ := ResolveBoxComponent(cfg, yanet, KindDataplane, "")
	if dp.Image.Name != "dataplane-fork" || dp.Image.Tag != "v2.1" {
		t.Errorf("dp name override failed: %+v", dp.Image)
	}
}

func TestResolveControlplane_BindAndService(t *testing.T) {
	cfg := fixtureConfig()
	value := "[::]:8080"
	cp := &cfg.Components.Controlplane
	cp.GRPCPort = 8080
	cp.HTTPPort = 8081
	cp.Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
		Key: "YANET_GATEWAY_ENDPOINT", Value: &value,
	}}}
	cp.Service = &yanetv2alpha1.ServiceSpec{Enabled: true, ServiceName: "gateway"}

	resolved, err := ResolveBoxComponent(
		cfg, &yanetv2alpha1.YanetSpec{BoxType: "release"}, KindControlplane, "",
	)
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	if resolved.GRPCPort != 8080 || resolved.HTTPPort != 8081 {
		t.Fatalf("resolved ports = %d/%d", resolved.GRPCPort, resolved.HTTPPort)
	}
	if !resolved.ServiceEnabled || resolved.ServiceName != "gateway" {
		t.Fatalf("resolved Service = enabled:%t name:%q", resolved.ServiceEnabled, resolved.ServiceName)
	}
	if len(resolved.Bind.Env) != 1 || *resolved.Bind.Env[0].Value != value {
		t.Fatalf("resolved bind = %+v", resolved.Bind)
	}

	*resolved.Bind.Env[0].Value = "mutated"
	if *cfg.Components.Controlplane.Bind.Env[0].Value != value {
		t.Fatal("resolved bind aliases YanetConfigV2")
	}
}

func TestResolveControlplane_BindOverrideReplaces(t *testing.T) {
	cfg := fixtureConfig()
	baseA, baseB, replacement := "a", "b", "replacement"
	cfg.Components.Controlplane.Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{
		{Key: "A", Value: &baseA},
		{Key: "B", Value: &baseB},
	}}
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "release",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
				YanetComponentOverride: yanetv2alpha1.YanetComponentOverride{
					Bind: &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
						Key: "C", Value: &replacement,
					}}},
				},
			},
		},
	}

	resolved, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	if len(resolved.Bind.Env) != 1 || resolved.Bind.Env[0].Key != "C" {
		t.Fatalf("override must replace the inherited bind: %+v", resolved.Bind)
	}

	yanet.Components.Controlplane.Bind = &yanetv2alpha1.BindSpec{}
	resolved, err = ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent with empty override: %v", err)
	}
	if resolved.Bind == nil || len(resolved.Bind.Env) != 0 {
		t.Fatalf("empty override must clear the inherited bind: %+v", resolved.Bind)
	}
}

func TestResolveOperator_BindMergeByKey(t *testing.T) {
	cfg := fixtureConfig()
	op := &cfg.Components.Operators[0]
	opA, opB, containerB, containerC := "op-a", "op-b", "container-b", "container-c"
	op.Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{
		{Key: "A", Value: &opA},
		{Key: "B", Value: &opB},
	}}
	op.Containers[0].Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{
		{Key: "B", Value: &containerB},
		{Key: "C", Value: &containerC},
	}}

	resolved, err := ResolveBoxComponent(
		cfg, &yanetv2alpha1.YanetSpec{BoxType: "firewall"}, KindOperator, "antiddos",
	)
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	primary := resolved.Containers[0].Bind
	if len(primary.Env) != 3 {
		t.Fatalf("primary bind = %+v", primary)
	}
	if primary.Env[0].Key != "A" || *primary.Env[0].Value != opA ||
		primary.Env[1].Key != "B" || *primary.Env[1].Value != containerB ||
		primary.Env[2].Key != "C" || *primary.Env[2].Value != containerC {
		t.Fatalf("container entries must override by key while preserving order: %+v", primary.Env)
	}
	secondary := resolved.Containers[1].Bind
	if len(secondary.Env) != 2 || *secondary.Env[1].Value != opB {
		t.Fatalf("container without its own bind must inherit operator bind: %+v", secondary)
	}
}

func TestResolveOperator_PerInstallationBindReplacement(t *testing.T) {
	cfg := fixtureConfig()
	op := &cfg.Components.Operators[0]
	baseOperator, baseContainer := "operator", "container"
	overrideOperator, overrideContainer := "override-operator", "override-container"
	op.Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
		Key: "BASE_OPERATOR", Value: &baseOperator,
	}}}
	op.Containers[0].Bind = &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
		Key: "BASE_CONTAINER", Value: &baseContainer,
	}}}
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "firewall",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Operators: map[string]yanetv2alpha1.YanetComponentOverride{
				"antiddos": {
					Bind: &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
						Key: "OVERRIDE_OPERATOR", Value: &overrideOperator,
					}}},
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						"operator": {Bind: &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{{
							Key: "OVERRIDE_CONTAINER", Value: &overrideContainer,
						}}}},
					},
				},
			},
		},
	}

	resolved, err := ResolveBoxComponent(cfg, yanet, KindOperator, "antiddos")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	bind := resolved.Containers[0].Bind
	if len(bind.Env) != 2 || bind.Env[0].Key != "OVERRIDE_OPERATOR" || bind.Env[1].Key != "OVERRIDE_CONTAINER" {
		t.Fatalf("per-installation blocks must replace before keyed merge: %+v", bind)
	}
}

// --- disabled NUMA resolution -----------------------------------------------

// TestResolveControlplane_DisabledNuma_ClusterWideDefault verifies that the
// cluster-wide opt-out list from YanetConfigV2 is carried into the resolved
// component when the installation says nothing.
func TestResolveControlplane_DisabledNuma_ClusterWideDefault(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Components.Controlplane.DisabledNuma = []int32{1}
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}

	cp, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	if diff := cmp.Diff([]int32{1}, cp.DisabledNuma); diff != "" {
		t.Errorf("disabledNuma mismatch (-want +got):\n%s", diff)
	}
}

// TestResolveControlplane_DisabledNuma_InstallationReplaces verifies that the
// per-installation list REPLACES the cluster-wide default instead of merging
// with it.
func TestResolveControlplane_DisabledNuma_InstallationReplaces(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Components.Controlplane.DisabledNuma = []int32{1}
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "release",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
				DisabledNuma: []int32{0},
			},
		},
	}

	cp, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	if diff := cmp.Diff([]int32{0}, cp.DisabledNuma); diff != "" {
		t.Errorf("installation override must replace the default (-want +got):\n%s", diff)
	}
}

// TestResolveControlplane_DisabledNuma_EmptyListClearsDefault verifies that an
// empty but non-nil list is an explicit "enable every NUMA", distinct from an
// unset field that inherits the default.
func TestResolveControlplane_DisabledNuma_EmptyListClearsDefault(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Components.Controlplane.DisabledNuma = []int32{1}
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "release",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Controlplane: &yanetv2alpha1.YanetControlplaneOverride{
				DisabledNuma: []int32{},
			},
		},
	}

	cp, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	if len(cp.DisabledNuma) != 0 {
		t.Errorf("empty list must clear the cluster-wide default, got %v", cp.DisabledNuma)
	}
}

// TestResolveControlplane_DisabledNuma_NoAliasing guards against the resolved
// slice aliasing the CR: mutating the result must not corrupt the config.
func TestResolveControlplane_DisabledNuma_NoAliasing(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Components.Controlplane.DisabledNuma = []int32{1}
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}

	cp, err := ResolveBoxComponent(cfg, yanet, KindControlplane, "")
	if err != nil {
		t.Fatalf("ResolveBoxComponent: %v", err)
	}
	cp.DisabledNuma[0] = 42
	if cfg.Components.Controlplane.DisabledNuma[0] != 1 {
		t.Errorf("resolved slice aliases the config: %v", cfg.Components.Controlplane.DisabledNuma)
	}
}

func TestResolveBoxComponent_OperatorPerContainerOverride(t *testing.T) {
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "firewall",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Operators: map[string]yanetv2alpha1.YanetComponentOverride{
				"antiddos": {
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						"operator": {Tag: "v0.5.1"},
						"agent":    {Tag: "v0.5.2"},
					},
				},
			},
		},
	}
	op, _ := ResolveBoxComponent(cfg, yanet, KindOperator, "antiddos")
	if op == nil {
		t.Fatalf("op nil")
	}
	if op.Containers[0].Name != "operator" || op.Containers[0].Image.Tag != "v0.5.1" {
		t.Errorf("primary container override failed: %+v", op.Containers[0])
	}
	if op.Containers[1].Name != "agent" || op.Containers[1].Image.Tag != "v0.5.2" {
		t.Errorf("secondary container override failed: %+v", op.Containers[1])
	}
}

func TestResolveBoxComponent_OperatorPartialContainerOverride(t *testing.T) {
	// Only the primary container has an override; the agent must
	// keep its declared image untouched.
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{
		BoxType: "firewall",
		Components: &yanetv2alpha1.YanetComponentsOverride{
			Operators: map[string]yanetv2alpha1.YanetComponentOverride{
				"antiddos": {
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						"operator": {Tag: "v0.5.1"},
					},
				},
			},
		},
	}
	op, _ := ResolveBoxComponent(cfg, yanet, KindOperator, "antiddos")
	if op.Containers[0].Image.Tag != "v0.5.1" {
		t.Errorf("primary tag override expected: %+v", op.Containers[0].Image)
	}
	if op.Containers[1].Image.Tag != "v0.5" {
		t.Errorf("agent must keep its declared tag: %+v", op.Containers[1].Image)
	}
}

func TestResolveBoxComponent_Errors(t *testing.T) {
	cfg := fixtureConfig()
	yanet := &yanetv2alpha1.YanetSpec{BoxType: "release"}

	// nil config.
	if _, err := ResolveBoxComponent(nil, yanet, KindControlplane, ""); err == nil {
		t.Errorf("nil config: want error")
	}
	// nil yanet.
	if _, err := ResolveBoxComponent(cfg, nil, KindControlplane, ""); err == nil {
		t.Errorf("nil yanet: want error")
	}
	// boxType missing.
	bad := &yanetv2alpha1.YanetSpec{BoxType: "ghost"}
	if _, err := ResolveBoxComponent(cfg, bad, KindControlplane, ""); err == nil {
		t.Errorf("ghost boxType: want error")
	}
	// unknown kind.
	if _, err := ResolveBoxComponent(cfg, yanet, ComponentKind("bogus"), ""); err == nil {
		t.Errorf("bogus kind: want error")
	}

	// boxType wires bird but palette has no Bird.
	cfg2 := fixtureConfig()
	cfg2.Components.Bird = nil
	if _, err := ResolveBoxComponent(cfg2, yanet, KindBird, ""); err == nil {
		t.Errorf("bird wired without palette: want error")
	}
}

func TestResolvedImage_FullPath(t *testing.T) {
	tests := []struct {
		name string
		img  ResolvedImage
		want string
	}{
		{"all", ResolvedImage{Registry: "cr.io", Prefix: "edge", Name: "x", Tag: "v1"}, "cr.io/edge/x:v1"},
		{"no prefix", ResolvedImage{Registry: "cr.io", Name: "x", Tag: "v1"}, "cr.io/x:v1"},
		{"no registry", ResolvedImage{Prefix: "edge", Name: "x", Tag: "v1"}, "edge/x:v1"},
		{"name only", ResolvedImage{Name: "x"}, "x"},
		{"tag missing", ResolvedImage{Registry: "cr.io", Name: "x"}, "cr.io/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.img.FullPath(); got != tt.want {
				t.Errorf("FullPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortNodeKey_StableAndShort(t *testing.T) {
	a := ShortNodeKey("node-abc")
	b := ShortNodeKey("node-abc")
	if a != b || len(a) != 8 {
		t.Errorf("ShortNodeKey not stable/8 hex: %q vs %q", a, b)
	}
	c := ShortNodeKey("node-xyz")
	if c == a {
		t.Errorf("ShortNodeKey collision suspected: %q", c)
	}
}
