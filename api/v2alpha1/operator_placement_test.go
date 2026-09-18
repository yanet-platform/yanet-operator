package v2alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestOperatorPlacementAdmission(t *testing.T) {
	for _, tt := range []struct {
		listeners string
		wantErr   bool
	}{
		{listeners: `null`},
		{listeners: `["grpc","http"]`},
		{listeners: `[]`},
		{listeners: `["grpc","grpc"]`, wantErr: true},
		{listeners: `["tcp"]`, wantErr: true},
	} {
		t.Run(tt.listeners, func(t *testing.T) {
			cfg := validConfig()
			if err := json.Unmarshal([]byte(fmt.Sprintf(`[{"name":"monalive","image":{"name":"monalive"},"listeners":%s}]`, tt.listeners)), &cfg.Spec.Components.Dataplane.Sidecars); err != nil {
				t.Fatal(err)
			}
			cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]BoxDataplaneSidecar{"monalive": {}}
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validation error = %v, want error %t", err, tt.wantErr)
			}
		})
	}
}

func TestOperatorPlacementPatchScope(t *testing.T) {
	for _, tt := range []struct {
		patch   string
		wantErr bool
	}{
		{patch: `{"spec":{"template":{"spec":{"containers":[{"name":"worker","args":["run"]}],"volumes":[{"name":"extra","emptyDir":{}}]}}}}`},
		{patch: `{"spec":{"template":{"spec":{"initContainers":[{"name":"fetch","image":"fetch"}]}}}}`},
		{patch: `{"spec":{"replicas":1}}`, wantErr: true},
		{patch: `{"spec":{"template":{"metadata":{"labels":{"x":"y"}}}}}`, wantErr: true},
		{patch: `{"spec":{"template":{"spec":{"hostIPC":true}}}}`, wantErr: true},
		{patch: `{"spec":{"template":{"spec":{"containers":[{"name":"worker"},{"name":"worker"}]}}}}`, wantErr: true},
		{patch: `{"spec":{"template":{"spec":{"volumes":[{"name":"x"},{"name":"x"}]}}}}`, wantErr: true},
	} {
		t.Run(tt.patch, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.Components.Dataplane.Sidecars = []SidecarSpec{{Name: "worker", Image: ImageRef{Name: "worker"}}}
			cfg.Spec.Patches = append(cfg.Spec.Patches, NamedPatch{Name: "colocated", Patch: runtime.RawExtension{Raw: []byte(tt.patch)}})
			cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]BoxDataplaneSidecar{"worker": {Patches: []string{"colocated"}}}
			_, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validation error = %v, wantErr=%t", err, tt.wantErr)
			}
		})
	}
}

func TestOperatorListenersEmptyRoundTrip(t *testing.T) {
	var operator OperatorSpec
	if err := json.Unmarshal([]byte(`{"name":"client","containers":[{"name":"worker","image":{"name":"client"}}],"listeners":[]}`), &operator); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(operator.DeepCopy())
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	if string(encoded["listeners"]) != "[]" {
		t.Fatalf("explicit client-only contract must survive encoding/deepcopy: %s", raw)
	}
}

func TestOperatorPlacementEffectiveHostNetwork(t *testing.T) {
	for _, tt := range []struct {
		name    string
		patches []string
		wantErr bool
	}{
		{name: "private palette"},
		{name: "last patch selects private", patches: []string{"host", "private"}},
		{name: "private palette overridden", patches: []string{"host"}, wantErr: true},
		{name: "last patch selects host", patches: []string{"private", "host"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Spec.Components.Dataplane.Sidecars = []SidecarSpec{{Name: "netconfig", Image: ImageRef{Name: "netconfig"}}}
			cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]BoxDataplaneSidecar{"netconfig": {}}
			cfg.Spec.BoxTypes[0].Components.Dataplane.Patches = tt.patches
			cfg.Spec.Patches = append(cfg.Spec.Patches,
				makePatch("private", `{"spec":{"template":{"spec":{"hostNetwork":false}}}}`),
				makePatch("host", `{"spec":{"template":{"spec":{"hostNetwork":true}}}}`),
			)
			validator := &YanetConfigCustomValidator{}
			_, createErr := validator.ValidateCreate(context.Background(), cfg)
			_, updateErr := validator.ValidateUpdate(context.Background(), validConfig(), cfg)
			for _, err := range []error{createErr, updateErr} {
				if tt.wantErr {
					if err == nil || !strings.Contains(err.Error(), "private network namespace") {
						t.Fatalf("expected private network namespace error, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("effective private network rejected: %v", err)
				}
			}
		})
	}
}
