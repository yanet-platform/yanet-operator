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
				Controlplane: ControlplaneSpec{Image: ImageRef{Name: "cp", Tag: "v1"}, Port: 8080},
				Dataplane:    DataplaneSpec{Image: ImageRef{Name: "dp", Tag: "v1"}, Port: 8081},
			},
			Patches: []NamedPatch{
				makePatch("telegraf", `{"spec":{"template":{"metadata":{"annotations":{"k":"v"}}}}}`),
			},
			BoxTypes: []BoxType{{
				Name: "release",
				Components: BoxComponents{
					Controlplane: &BoxComponent{Patches: []string{"telegraf"}},
					Dataplane:    &BoxComponent{},
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

func TestYanetConfigWebhook_BindAndService(t *testing.T) {
	literal := func(value string) *string { return &value }
	validServiceBind := func() *BindSpec {
		return &BindSpec{Env: []BindEnv{
			{Key: "YANET_GATEWAY_ENDPOINT", Value: literal("[::]:8080")},
			{Key: "YANET_GATEWAY_ADVERTISE_ENDPOINT", Service: &ServiceRef{Port: 8080}},
		}}
	}

	tests := []struct {
		name    string
		mutate  func(*YanetConfigV2)
		wantErr string
	}{
		{
			name: "literal including empty value",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{{
					Key: "YANET_OPTIONAL_VALUE", Value: literal(""),
				}}}
			},
		},
		{
			name: "service reference",
			mutate: func(cfg *YanetConfigV2) {
				cp := &cfg.Spec.Components.Controlplane
				cp.GRPCPort = 8080
				cp.HTTPPort = 8081
				cp.Bind = validServiceBind()
				cp.Service = &ServiceSpec{Enabled: true}
			},
		},
		{
			name: "empty key",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{{
					Key: " ", Value: literal("x"),
				}}}
			},
			wantErr: "key is empty",
		},
		{
			name: "invalid environment variable key",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{{
					Key: "YANET=ENDPOINT", Value: literal("x"),
				}}}
			},
			wantErr: "key \"YANET=ENDPOINT\" is invalid",
		},
		{
			name: "value and service",
			mutate: func(cfg *YanetConfigV2) {
				cp := &cfg.Spec.Components.Controlplane
				cp.GRPCPort = 8080
				cp.HTTPPort = 8081
				cp.Service = &ServiceSpec{Enabled: true}
				cp.Bind = &BindSpec{Env: []BindEnv{{
					Key: "YANET_ENDPOINT", Value: literal("x"), Service: &ServiceRef{Port: 8080},
				}}}
			},
			wantErr: "exactly one",
		},
		{
			name: "neither value nor service",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{{Key: "YANET_ENDPOINT"}}}
			},
			wantErr: "exactly one",
		},
		{
			name: "duplicate key",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{
					{Key: "YANET_ENDPOINT", Value: literal("x")},
					{Key: "YANET_ENDPOINT", Value: literal("y")},
				}}
			},
			wantErr: "duplicated",
		},
		{
			name: "service reference without enabled service",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Controlplane.Bind = &BindSpec{Env: []BindEnv{{
					Key: "YANET_ENDPOINT", Service: &ServiceRef{Port: 8080},
				}}}
			},
			wantErr: "requires service.enabled",
		},
		{
			name: "enabled service without bind",
			mutate: func(cfg *YanetConfigV2) {
				cp := &cfg.Spec.Components.Controlplane
				cp.GRPCPort = 8080
				cp.HTTPPort = 8081
				cp.Service = &ServiceSpec{Enabled: true}
			},
			wantErr: "requires a non-empty bind override",
		},
		{
			name: "invalid custom service name",
			mutate: func(cfg *YanetConfigV2) {
				cp := &cfg.Spec.Components.Controlplane
				cp.GRPCPort = 8080
				cp.HTTPPort = 8081
				cp.Bind = validServiceBind()
				cp.Service = &ServiceSpec{Enabled: true, ServiceName: "Invalid_Name"}
			},
			wantErr: "serviceName \"Invalid_Name\" is invalid",
		},
		{
			name: "service reference to unexposed port",
			mutate: func(cfg *YanetConfigV2) {
				cp := &cfg.Spec.Components.Controlplane
				cp.GRPCPort = 8080
				cp.HTTPPort = 8081
				cp.Service = &ServiceSpec{Enabled: true}
				cp.Bind = &BindSpec{Env: []BindEnv{{
					Key: "YANET_ENDPOINT", Service: &ServiceRef{Port: 9000},
				}}}
			},
			wantErr: "is not exposed",
		},
		{
			name: "operator container bind satisfies service",
			mutate: func(cfg *YanetConfigV2) {
				cfg.Spec.Components.Operators = []OperatorSpec{{
					Name:    "route",
					Port:    9000,
					Service: &ServiceSpec{Enabled: true},
					Containers: []OperatorContainer{{
						Name: "route", Image: ImageRef{Name: "route"}, Bind: &BindSpec{Env: []BindEnv{{
							Key: "YANET_SERVER_ENDPOINT", Value: literal("[::]:9000"),
						}}},
					}},
				}}
				cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"route": {}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("valid bind/service config rejected: %v", err)
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

func TestYanetConfigWebhook_UnknownOperatorRef(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"ghost": {}}
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected unknown operator ref, got %v", err)
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

func TestYanetConfigWebhook_PortOverlap_CPRangeAndDataplane_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Controlplane.PortRange = 4 // 8080..8083
	cfg.Spec.Components.Dataplane.Port = 8082      // overlaps
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("ports in separate Pod network namespaces may overlap: %v", err)
	}
}

func TestYanetConfigWebhook_DuplicateSinglePorts_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Dataplane.Port = 8080 // exact duplicate
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("ports in separate Pod network namespaces may overlap: %v", err)
	}
}

func TestYanetConfigWebhook_OperatorVsControlplaneRange_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 9000
	cfg.Spec.Components.Controlplane.PortRange = 8 // 9000..9007
	cfg.Spec.Components.Operators = []OperatorSpec{{
		Name:       "x",
		Port:       9005, // inside CP range
		Containers: []OperatorContainer{{Name: "x", Image: ImageRef{Name: "x"}}},
	}}
	cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"x": {}}
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("operator and controlplane ports may overlap across Pods: %v", err)
	}
}

func TestYanetConfigWebhook_OperatorsMaySharePort(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Operators = []OperatorSpec{
		{Name: "route", Port: 9000, Containers: []OperatorContainer{{Name: "route", Image: ImageRef{Name: "route"}}}},
		{Name: "forward", Port: 9000, Containers: []OperatorContainer{{Name: "forward", Image: ImageRef{Name: "forward"}}}},
	}
	cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"route": {}, "forward": {}}
	if _, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("operators in separate Pods must be allowed to share port 9000: %v", err)
	}
}

func TestYanetConfigWebhook_ControlplaneGRPCAndHTTPPortsMustDiffer(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.GRPCPort = 8080
	cfg.Spec.Components.Controlplane.HTTPPort = 8080
	_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Errorf("expected distinct controlplane port error, got %v", err)
	}
}

func TestYanetConfigWebhook_PortRanges_Adjacent_OK(t *testing.T) {
	// CP range ends exactly one before the next port — no overlap.
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Controlplane.PortRange = 4 // 8080..8083
	cfg.Spec.Components.Dataplane.Port = 8084
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("adjacent port ranges must be accepted: %v", err)
	}
}

func TestYanetConfigWebhook_PortRange_Negative_Rejected(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.PortRange = -1
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "portRange") {
		t.Errorf("expected portRange error, got %v", err)
	}
}

func TestYanetConfigWebhook_PortRange_ExceedsMax_Rejected(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 65530
	cfg.Spec.Components.Controlplane.PortRange = 100 // would extend past 65535
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "exceeds 65535") {
		t.Errorf("expected port range overflow error, got %v", err)
	}
}

func TestYanetConfigWebhook_PortZero_Skipped(t *testing.T) {
	// Port=0 means "no listener"; should not be considered for
	// overlap checks even if multiple components are zero.
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 0
	cfg.Spec.Components.Dataplane.Port = 0
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("zero ports must be accepted: %v", err)
	}
}

func TestYanetConfigWebhook_AllComponents_DistinctPorts_OK(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Controlplane.PortRange = 2 // 8080..8081
	cfg.Spec.Components.Dataplane.Port = 8090
	cfg.Spec.Components.Bird = &BirdComponent{Image: ImageRef{Name: "bird"}, Port: 179}
	cfg.Spec.Components.BirdAdapter = &BirdAdapterComp{Image: ImageRef{Name: "ba"}, Port: 8100}
	cfg.Spec.Components.Announcer = &AnnouncerComp{Image: ImageRef{Name: "an"}, Port: 8110}
	cfg.Spec.BoxTypes[0].Components.Bird = &BoxComponent{}
	cfg.Spec.BoxTypes[0].Components.BirdAdapter = &BoxComponent{}
	cfg.Spec.BoxTypes[0].Components.Announcer = &BoxComponent{}
	v := &YanetConfigCustomValidator{}
	if _, err := v.ValidateCreate(context.Background(), cfg); err != nil {
		t.Errorf("distinct ports must be accepted: %v", err)
	}
}
