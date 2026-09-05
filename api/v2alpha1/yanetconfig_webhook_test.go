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

package v2alpha1

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// makePatch is a tiny helper that wraps a JSON string into a NamedPatch.
func makePatch(name, raw string) NamedPatch {
	return NamedPatch{Name: name, Patch: runtime.RawExtension{Raw: []byte(raw)}}
}

// validConfig returns a minimal but valid YanetConfigV2 for mutation tests.
func validConfig() *YanetConfigV2 {
	return &YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: YanetConfigName},
		Spec: YanetConfigSpec{
			Components: ComponentsSpec{
				Controlplane: ControlplaneSpec{Image: ImageRef{Name: "cp", Tag: "v1"}},
				Dataplane:    DataplaneSpec{Image: ImageRef{Name: "dp", Tag: "v1"}},
			},
			Patches: []NamedPatch{
				makePatch("telegraf", `{"spec":{"template":{"metadata":{"annotations":{"k":"v"}}}}}`),
			},
			BoxTypes: []BoxType{{
				Name: "release",
				Components: BoxComponents{
					Controlplane: &BoxComponent{Patches: []string{"telegraf"}},
					Dataplane:    &BoxDataplane{},
				},
			}},
		},
	}
}

func TestYanetConfigWebhook_Valid(t *testing.T) {
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), validConfig()); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestYanetConfigWebhook_RejectsNonCanonicalName(t *testing.T) {
	cfg := validConfig()
	cfg.Name = "other"
	_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), `metadata.name must be "config"`) {
		t.Fatalf("non-canonical singleton name must be rejected, got %v", err)
	}
}

func TestYanetConfigWebhook_Hugepages(t *testing.T) {
	tests := []struct {
		name    string
		size    string
		count   int32
		wantErr string
	}{
		{name: "two MiB pages", size: "2Mi", count: 28672},
		{name: "one GiB pages", size: "1Gi", count: 8},
		{name: "invalid size", size: "not-a-quantity", count: 8, wantErr: "not a valid Kubernetes quantity"},
		{name: "zero size", size: "0", count: 8, wantErr: "must be greater than zero"},
		{name: "zero count", size: "2Mi", count: 0, wantErr: "count must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.Components.Dataplane.Hugepages = &Hugepages{Size: tt.size, Count: tt.count}
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("valid hugepages rejected: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestYanetConfigWebhook_ConfigSource(t *testing.T) {
	tests := []struct {
		name    string
		source  *ConfigSource
		wantErr string
	}{
		{name: "host path", source: &ConfigSource{HostPath: "/etc/yanet2"}},
		{name: "typed args", source: &ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"-c", "/etc/yanet2/controlplane.yaml"},
		}},
		{name: "multiple variants", source: &ConfigSource{
			HostPath: "/etc/yanet2",
			Inline:   "logging: {}",
		}, wantErr: "exactly one"},
		{name: "no variant", source: &ConfigSource{Args: []string{"-c", "/etc/yanet2/config.yaml"}}, wantErr: "exactly one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.Components.Controlplane.Config = tt.source
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("valid config source rejected: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestYanetConfigWebhook_DataplaneSidecarConfigSource(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Dataplane.Sidecars = &DataplaneSidecarsSpec{
		Bird: &DataplaneSidecarSpec{
			Image:  ImageRef{Name: "bird"},
			Config: &ConfigSource{HostPath: "/etc/bird", Inline: "invalid"},
		},
	}
	cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = &BoxDataplaneSidecars{
		Bird: &BoxDataplaneSidecar{},
	}
	_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "spec.components.dataplane.sidecars.bird.config") {
		t.Fatalf("invalid BIRD sidecar config source must be rejected, got %v", err)
	}
}

func TestYanetConfigWebhook_HostNetworkPortRange(t *testing.T) {
	tests := []struct {
		name    string
		value   *HostNetworkPortRange
		wantErr string
	}{
		{name: "omitted"},
		{name: "single port", value: &HostNetworkPortRange{Start: 20000, End: 20000}},
		{name: "range", value: &HostNetworkPortRange{Start: 20000, End: 20100}},
		{name: "zero start", value: &HostNetworkPortRange{Start: 0, End: 20100}, wantErr: "start"},
		{name: "end too large", value: &HostNetworkPortRange{Start: 20000, End: 65536}, wantErr: "end"},
		{name: "reversed", value: &HostNetworkPortRange{Start: 20100, End: 20000}, wantErr: "must not exceed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.HostNetworkPortRange = tt.value
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("valid host-network range rejected: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestYanetConfigWebhook_DuplicatePatchName(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Patches = append(cfg.Spec.Patches, makePatch("telegraf", `{}`))
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("expected duplicated patch error, got %v", err)
	}
}

func TestYanetConfigWebhook_DuplicateOperatorName(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Operators = []OperatorSpec{
		{Name: "x", Containers: []OperatorContainer{{Name: "x", Image: ImageRef{Name: "x"}}}},
		{Name: "x", Containers: []OperatorContainer{{Name: "x", Image: ImageRef{Name: "x"}}}},
	}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("expected duplicated operator error, got %v", err)
	}
}

func TestYanetConfigWebhook_ReservedOperatorName(t *testing.T) {
	for _, name := range []string{
		"controlplane",
		"dataplane",
		"bird",
		"bird-adapter",
		"netlink-dataplane-sidecar",
		"announcer",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.Components.Operators = []OperatorSpec{{
				Name:       name,
				Containers: []OperatorContainer{{Name: "main", Image: ImageRef{Name: "operator"}}},
			}}
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("reserved operator name %q must be rejected, got %v", name, err)
			}
		})
	}
}

func TestYanetConfigWebhook_DuplicateContainerNameWithinOperator(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Operators = []OperatorSpec{{
		Name: "x",
		Containers: []OperatorContainer{
			{Name: "main", Image: ImageRef{Name: "x"}},
			{Name: "main", Image: ImageRef{Name: "y"}},
		},
	}}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("expected duplicated container name error, got %v", err)
	}
}

func TestYanetConfigWebhook_EmptyContainerName(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Operators = []OperatorSpec{{
		Name:       "x",
		Containers: []OperatorContainer{{Image: ImageRef{Name: "x"}}}, // missing Name
	}}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("expected empty container name error, got %v", err)
	}
}

func TestYanetConfigWebhook_DuplicateBoxTypeName(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes = append(cfg.Spec.BoxTypes, cfg.Spec.BoxTypes[0])
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("expected duplicated boxType error, got %v", err)
	}
}

func TestYanetConfigWebhook_BoxRequiresControlplane(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Components.Controlplane = nil
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "controlplane") {
		t.Errorf("expected missing controlplane error, got %v", err)
	}
}

func TestYanetConfigWebhook_BoxRequiresDataplane(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Components.Dataplane = nil
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "dataplane") {
		t.Errorf("expected missing dataplane error, got %v", err)
	}
}

func TestYanetConfigWebhook_UnknownPatchRef(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Components.Controlplane.Patches = []string{"ghost"}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected unknown patch ref, got %v", err)
	}
}

func TestYanetConfigWebhook_UndeclaredDataplaneSidecar(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = &BoxDataplaneSidecars{
		NetlinkDataplaneSidecar: &BoxDataplaneSidecar{},
	}
	_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "netlinkDataplaneSidecar") {
		t.Fatalf("undeclared dataplane sidecar must be rejected, got %v", err)
	}
}

func TestYanetConfigWebhook_UnknownOperatorRef(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"ghost": {}}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected unknown operator ref, got %v", err)
	}
}

func TestYanetConfigWebhook_OptionalComponentRequiresPaletteDeclaration(t *testing.T) {
	tests := []struct {
		name string
		wire func(*BoxComponents)
		want string
	}{
		{
			name: "bird adapter",
			wire: func(components *BoxComponents) { components.BirdAdapter = &BoxComponent{} },
			want: "birdAdapter",
		},
		{
			name: "announcer",
			wire: func(components *BoxComponents) { components.Announcer = &BoxComponent{} },
			want: "announcer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.wire(&cfg.Spec.BoxTypes[0].Components)
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("undeclared optional component must be rejected, got %v", err)
			}
		})
	}
}

func TestYanetConfigWebhook_ComponentImageNameRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Dataplane.Sidecars = &DataplaneSidecarsSpec{
		NetlinkDataplaneSidecar: &DataplaneSidecarSpec{},
	}
	cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = &BoxDataplaneSidecars{
		NetlinkDataplaneSidecar: &BoxDataplaneSidecar{},
	}
	_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "netlinkDataplaneSidecar.image.name is required") {
		t.Fatalf("empty sidecar image name must be rejected, got %v", err)
	}
}

func TestYanetConfigWebhook_DryRun_InvalidJSON(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Patches[0].Patch.Raw = []byte(`{not json`)
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("expected invalid JSON error, got %v", err)
	}
}

func TestYanetConfigWebhook_DryRun_EmptyPatch(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Patches[0].Patch.Raw = nil
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty patch error, got %v", err)
	}
}

func TestYanetConfigWebhook_DryRun_OK(t *testing.T) {
	// A patch that touches a real Deployment field should pass the
	// dry-run.
	cfg := validConfig()
	cfg.Spec.Patches[0].Patch.Raw = []byte(`{"spec":{"replicas":3}}`)
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("valid patch rejected: %v", err)
	}
}

func TestYanetConfigWebhook_Update_RunsValidation(t *testing.T) {
	cfg := validConfig()
	bad := validConfig()
	bad.Spec.BoxTypes[0].Components.Controlplane = nil
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateUpdate(context.Background(), cfg, bad); err == nil {
		t.Errorf("update validation must run full pipeline")
	}
}

func TestYanetConfigWebhook_Delete_AlwaysAllowed(t *testing.T) {
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateDelete(context.Background(), validConfig()); err != nil {
		t.Errorf("delete must always be allowed: %v", err)
	}
}

func TestYanetConfigWebhook_NegativeUpdateWindow(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.UpdateWindow = -1
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "updateWindow") {
		t.Errorf("expected updateWindow error, got %v", err)
	}
}

func TestYanetConfigWebhook_ZeroUpdateWindow_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.UpdateWindow = 0
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("zero updateWindow must be accepted: %v", err)
	}
}

func TestYanetConfigWebhook_PositiveUpdateWindow_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.UpdateWindow = 300
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("positive updateWindow must be accepted: %v", err)
	}
}

// --- disabledNuma -----------------------------------------------------------

func TestYanetConfigWebhook_DisabledNuma_Accepted(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Numa = ptrInt32(2)
	cfg.Spec.Components.Controlplane.DisabledNuma = []int32{1}
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("disabling a single NUMA must be accepted: %v", err)
	}
}

func TestYanetConfigWebhook_DisabledNuma_NegativeRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.DisabledNuma = []int32{-1}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected non-negative index error, got %v", err)
	}
}

// A list that disables every NUMA domain leaves the installation without any
// controlplane; the boxType is the right place to drop the component instead.
func TestYanetConfigWebhook_DisabledNuma_AllDisabledRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Numa = ptrInt32(2)
	cfg.Spec.Components.Controlplane.DisabledNuma = []int32{0, 1}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "every one of the 2 NUMA domains") {
		t.Errorf("expected all-disabled error, got %v", err)
	}
}

// With NFD auto-detection the fan-out count is a per-node runtime property, so
// the webhook cannot decide whether the list drains every domain.
func TestYanetConfigWebhook_DisabledNuma_AutoDetectionNotRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Numa = nil
	cfg.Spec.Components.Controlplane.DisabledNuma = []int32{0, 1}
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("without a pinned numa count the list must be accepted: %v", err)
	}
}

func ptrInt32(v int32) *int32 { return &v }
