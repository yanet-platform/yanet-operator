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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func newClientWith(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(objs...).Build()
}

func clusterConfig(name, namespace string, boxTypes ...string) *YanetConfigV2 {
	cfg := &YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: YanetConfigSpec{
			Components: ComponentsSpec{
				Controlplane: ControlplaneSpec{Image: ImageRef{Name: "cp", Tag: "v1"}},
				Dataplane:    DataplaneSpec{Image: ImageRef{Name: "dp", Tag: "v1"}},
				Operators: []OperatorSpec{
					{Name: "antiddos", Containers: []OperatorContainer{
						{Name: "operator", Image: ImageRef{Name: "x"}},
						{Name: "agent", Image: ImageRef{Name: "y"}},
					}},
				},
			},
		},
	}
	for _, bt := range boxTypes {
		cfg.Spec.BoxTypes = append(cfg.Spec.BoxTypes, BoxType{
			Name: bt,
			Components: BoxComponents{
				Controlplane: &BoxComponent{},
				Dataplane:    &BoxComponent{},
			},
		})
	}
	return cfg
}

func makeYanet(name, ns, boxType string) *YanetV2 {
	return &YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       YanetSpec{BoxType: boxType},
	}
}

func TestYanetWebhook_BoxTypeRequired(t *testing.T) {
	v := &YanetCustomValidator{Client: newClientWith(t)}
	y := makeYanet("y", "yanet", "")
	if _, err := v.ValidateCreate(context.Background(), y); err == nil ||
		!strings.Contains(err.Error(), "boxType is required") {
		t.Errorf("expected boxType required error, got %v", err)
	}
}

func TestYanetWebhook_BoxTypeNotFoundInCluster(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "ghost")
	_, err := v.ValidateCreate(context.Background(), y)
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("expected boxType not defined error, got %v", err)
	}
}

func TestYanetWebhook_BoxTypeFoundInNamespace(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	if _, err := v.ValidateCreate(context.Background(), y); err != nil {
		t.Errorf("namespace-local boxType should pass: %v", err)
	}
}

func TestYanetWebhook_BoxTypeFoundClusterWide(t *testing.T) {
	// YanetV2 lives in ns="yanet" but the only YanetConfigV2 is in
	// ns="cluster-defaults". Validator should fall back.
	cfg := clusterConfig("c", "cluster-defaults", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	if _, err := v.ValidateCreate(context.Background(), y); err != nil {
		t.Errorf("cluster-wide fallback failed: %v", err)
	}
}

func TestYanetWebhook_NoConfigDegrades(t *testing.T) {
	v := &YanetCustomValidator{Client: newClientWith(t)}
	y := makeYanet("y", "yanet", "release")
	warns, err := v.ValidateCreate(context.Background(), y)
	if err != nil {
		t.Errorf("no config: should NOT error, got %v", err)
	}
	if len(warns) == 0 {
		t.Errorf("expected a warning when no config is reachable")
	}
}

func TestYanetWebhook_OperatorOverrideMustBeDeclared(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{
			"ghost": {},
		},
	}
	_, err := v.ValidateCreate(context.Background(), y)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected unknown operator override error, got %v", err)
	}
}

func TestYanetWebhook_OperatorOverrideDeclared_OK(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{
			"antiddos": {},
		},
	}
	if _, err := v.ValidateCreate(context.Background(), y); err != nil {
		t.Errorf("declared operator override should pass: %v", err)
	}
}

func TestYanetWebhook_HardcodedContainerOverride_OK(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Controlplane: &YanetComponentOverride{
			Containers: map[string]ImageRef{"controlplane": {Tag: "v2"}},
		},
	}
	if _, err := v.ValidateCreate(context.Background(), y); err != nil {
		t.Errorf("hardcoded controlplane override should pass: %v", err)
	}
}

func TestYanetWebhook_HardcodedContainerOverride_WrongKey(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Controlplane: &YanetComponentOverride{
			Containers: map[string]ImageRef{"main": {Tag: "v2"}}, // wrong: must be "controlplane"
		},
	}
	_, err := v.ValidateCreate(context.Background(), y)
	if err == nil || !strings.Contains(err.Error(), "controlplane") {
		t.Errorf("expected hardcoded key validation error, got %v", err)
	}
}

func TestYanetWebhook_OperatorContainerOverride_OK(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{
			"antiddos": {
				Containers: map[string]ImageRef{
					"operator": {Tag: "v0.5.1"},
					"agent":    {Tag: "v0.5.2"},
				},
			},
		},
	}
	if _, err := v.ValidateCreate(context.Background(), y); err != nil {
		t.Errorf("declared operator container overrides should pass: %v", err)
	}
}

func TestYanetWebhook_OperatorContainerOverride_UnknownContainer(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	y := makeYanet("y", "yanet", "release")
	y.Spec.Components = &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{
			"antiddos": {
				Containers: map[string]ImageRef{"ghost": {Tag: "v1"}},
			},
		},
	}
	_, err := v.ValidateCreate(context.Background(), y)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected unknown container override error, got %v", err)
	}
}

func TestYanetWebhook_BoxTypeImmutable(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release", "balancer")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	oldY := makeYanet("y", "yanet", "release")
	newY := makeYanet("y", "yanet", "balancer")
	_, err := v.ValidateUpdate(context.Background(), oldY, newY)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("expected immutable boxType error, got %v", err)
	}
}

func TestYanetWebhook_UpdateSameBoxType_OK(t *testing.T) {
	cfg := clusterConfig("c", "yanet", "release")
	v := &YanetCustomValidator{Client: newClientWith(t, cfg)}
	oldY := makeYanet("y", "yanet", "release")
	newY := makeYanet("y", "yanet", "release")
	if _, err := v.ValidateUpdate(context.Background(), oldY, newY); err != nil {
		t.Errorf("same boxType update should pass: %v", err)
	}
}

func TestYanetWebhook_DeleteAlwaysAllowed(t *testing.T) {
	v := &YanetCustomValidator{Client: newClientWith(t)}
	if _, err := v.ValidateDelete(context.Background(), makeYanet("y", "yanet", "")); err != nil {
		t.Errorf("delete must always pass: %v", err)
	}
}

func TestYanetWebhook_UpdateSkipsValidationWhenDeleting(t *testing.T) {
	// Simulate the finalizer-removal update that the controller issues when
	// DeletionTimestamp is set. At that point spec may be empty (no boxType),
	// so validation must be skipped to avoid blocking the deletion.
	v := &YanetCustomValidator{Client: newClientWith(t)}
	oldY := makeYanet("test-node", "yanet", "firewall")
	oldY.Finalizers = []string{"yanet.yanet-platform.io/finalizer"}

	newY := makeYanet("test-node", "yanet", "firewall")
	newY.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	newY.Finalizers = []string{}
	// spec is intentionally cleared (as the API server may return it)
	newY.Spec = YanetSpec{}

	if _, err := v.ValidateUpdate(context.Background(), oldY, newY); err != nil {
		t.Errorf("finalizer removal during deletion must not be blocked: %v", err)
	}
}
