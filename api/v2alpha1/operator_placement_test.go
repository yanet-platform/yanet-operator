package v2alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestOperatorPlacementAdmission(t *testing.T) {
	for _, tt := range []struct {
		placement string
		listeners string
		wantErr   bool
	}{
		{placement: "standalone", listeners: `null`},
		{placement: "dataplane", listeners: `["grpc","http"]`},
		{placement: "dataplane", listeners: `[]`},
		{placement: "unknown", listeners: `["grpc"]`, wantErr: true},
		{placement: "dataplane", listeners: `["grpc","grpc"]`, wantErr: true},
		{placement: "dataplane", listeners: `["tcp"]`, wantErr: true},
	} {
		t.Run(tt.placement+tt.listeners, func(t *testing.T) {
			cfg := validConfig()
			if err := json.Unmarshal([]byte(fmt.Sprintf(`[{"name":"monalive","containers":[{"name":"worker","image":{"name":"monalive"}}],"listeners":%s}]`, tt.listeners)), &cfg.Spec.Components.Operators); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"monalive":{"placement":%q}}`, tt.placement)), &cfg.Spec.BoxTypes[0].Operators); err != nil {
				t.Fatal(err)
			}
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
			cfg.Spec.Components.Operators = []OperatorSpec{{Name: "monalive", Containers: []OperatorContainer{{Name: "worker", Image: ImageRef{Name: "worker"}}}}}
			cfg.Spec.Patches = append(cfg.Spec.Patches, NamedPatch{Name: "colocated", Patch: runtime.RawExtension{Raw: []byte(tt.patch)}})
			cfg.Spec.BoxTypes[0].Operators = map[string]BoxOperator{"monalive": {Placement: OperatorPlacementDataplane, Patches: []string{"colocated"}}}
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
