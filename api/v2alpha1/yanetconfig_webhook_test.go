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
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "yanet"},
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

func TestYanetConfigWebhook_PortOverlap_CPRangeAndDataplane(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Controlplane.PortRange = 4 // 8080..8083
	cfg.Spec.Components.Dataplane.Port = 8082      // overlaps
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("expected port overlap error, got %v", err)
	}
}

func TestYanetConfigWebhook_PortOverlap_DuplicateSinglePorts(t *testing.T) {
	cfg := validConfig()
	cfg.Spec.Components.Controlplane.Port = 8080
	cfg.Spec.Components.Dataplane.Port = 8080 // exact duplicate
	v := &YanetConfigCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("expected port overlap error, got %v", err)
	}
}

func TestYanetConfigWebhook_PortOverlap_OperatorVsControlplaneRange(t *testing.T) {
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
	_, err := v.ValidateCreate(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("expected port overlap error, got %v", err)
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
