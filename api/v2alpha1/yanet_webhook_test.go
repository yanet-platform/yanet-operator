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
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func newClientWith(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(objects...).Build()
}

func clusterConfig(boxTypes ...string) *YanetConfigV2 {
	config := &YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: YanetConfigName},
		Spec: YanetConfigSpec{
			Components: ComponentsSpec{
				Controlplane: ControlplaneSpec{Image: ImageRef{Name: "cp", Tag: "v1"}},
				Dataplane: DataplaneSpec{
					Image: ImageRef{Name: "dp", Tag: "v1"},
					Sidecars: &DataplaneSidecarsSpec{
						Bird: &DataplaneSidecarSpec{Image: ImageRef{Name: "bird", Tag: "v1"}},
						NetlinkDataplaneSidecar: &DataplaneSidecarSpec{
							Image: ImageRef{Name: "netlink-dataplane-sidecar", Tag: "v1"},
						},
					},
				},
				Operators: []OperatorSpec{{
					Name: "antiddos",
					Containers: []OperatorContainer{
						{Name: "operator", Image: ImageRef{Name: "x"}},
						{Name: "agent", Image: ImageRef{Name: "y"}},
					},
				}},
			},
		},
	}
	for _, boxType := range boxTypes {
		config.Spec.BoxTypes = append(config.Spec.BoxTypes, BoxType{
			Name: boxType,
			Components: BoxComponents{
				Controlplane: &BoxComponent{},
				Dataplane: &BoxDataplane{Sidecars: &BoxDataplaneSidecars{
					Bird:                    &BoxDataplaneSidecar{},
					NetlinkDataplaneSidecar: &BoxDataplaneSidecar{},
				}},
			},
			Operators: map[string]BoxOperator{"antiddos": {}},
		})
	}
	return config
}

func makeYanet(name, namespace, boxType string) *YanetV2 {
	return &YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       YanetSpec{BoxType: boxType},
	}
}

func TestYanetWebhook_BoxTypeValidation(t *testing.T) {
	config := clusterConfig("release")
	tests := []struct {
		name    string
		client  client.Client
		boxType string
		wantErr string
		warning bool
	}{
		{name: "required", client: newClientWith(t, config), wantErr: "required"},
		{name: "not found", client: newClientWith(t, config), boxType: "ghost", wantErr: "not defined"},
		{name: "found", client: newClientWith(t, config), boxType: "release"},
		{name: "config unavailable", client: newClientWith(t), boxType: "release", warning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := &YanetCustomValidator{Client: tt.client}
			warnings, err := validator.ValidateCreate(
				context.Background(),
				makeYanet("edge", "yanet", tt.boxType),
			)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateCreate: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
			if tt.warning != (len(warnings) > 0) {
				t.Fatalf("warnings = %v, want warning %t", warnings, tt.warning)
			}
		})
	}
}

func TestYanetWebhook_InvalidOverrideRejectedWithoutConfig(t *testing.T) {
	yanet := makeYanet("edge", "yanet", "release")
	yanet.Spec.Components = &YanetComponentsOverride{
		Dataplane: &YanetComponentOverride{Containers: map[string]YanetContainerOverride{
			DataplaneContainerName: {Enabled: boolPointer(false)},
		}},
	}
	warnings, err := (&YanetCustomValidator{Client: newClientWith(t)}).ValidateCreate(
		context.Background(),
		yanet,
	)
	if err == nil || !strings.Contains(err.Error(), "spec.components.dataplane.enabled") {
		t.Fatalf("invalid local override shape must be rejected, got warnings=%v err=%v", warnings, err)
	}
}

func TestYanetWebhook_Overrides(t *testing.T) {
	config := clusterConfig("release")
	validator := &YanetCustomValidator{Client: newClientWith(t, config)}
	tests := []struct {
		name       string
		components *YanetComponentsOverride
		wantErr    string
	}{
		{
			name: "declared operator and containers",
			components: &YanetComponentsOverride{Operators: map[string]YanetComponentOverride{
				"antiddos": {Containers: map[string]YanetContainerOverride{
					"operator": {Tag: "v2"},
					"agent":    {Tag: "v2"},
				}},
			}},
		},
		{
			name: "unknown operator",
			components: &YanetComponentsOverride{Operators: map[string]YanetComponentOverride{
				"ghost": {},
			}},
			wantErr: "ghost",
		},
		{
			name: "unknown operator container",
			components: &YanetComponentsOverride{Operators: map[string]YanetComponentOverride{
				"antiddos": {Containers: map[string]YanetContainerOverride{"ghost": {Tag: "v2"}}},
			}},
			wantErr: "ghost",
		},
		{
			name: "hardcoded container",
			components: &YanetComponentsOverride{Controlplane: &YanetControlplaneOverride{
				YanetComponentOverride: YanetComponentOverride{Containers: map[string]YanetContainerOverride{
					"controlplane": {Tag: "v2"},
				}},
			}},
		},
		{
			name: "dataplane sidecar disable and image",
			components: &YanetComponentsOverride{Dataplane: &YanetComponentOverride{
				Containers: map[string]YanetContainerOverride{
					BirdSidecarContainerName: {
						Enabled: boolPointer(false),
					},
					NetlinkDataplaneSidecarContainerName: {Tag: "v2"},
				},
			}},
		},
		{
			name: "unknown dataplane container",
			components: &YanetComponentsOverride{Dataplane: &YanetComponentOverride{
				Containers: map[string]YanetContainerOverride{"ghost": {Tag: "v2"}},
			}},
			wantErr: "ghost",
		},
		{
			name: "primary dataplane container enable",
			components: &YanetComponentsOverride{Dataplane: &YanetComponentOverride{
				Containers: map[string]YanetContainerOverride{
					DataplaneContainerName: {Enabled: boolPointer(false)},
				},
			}},
			wantErr: "spec.components.dataplane.enabled",
		},
		{
			name: "wrong hardcoded container",
			components: &YanetComponentsOverride{Controlplane: &YanetControlplaneOverride{
				YanetComponentOverride: YanetComponentOverride{Containers: map[string]YanetContainerOverride{
					"main": {Tag: "v2"},
				}},
			}},
			wantErr: "controlplane",
		},
		{
			name: "negative disabled numa",
			components: &YanetComponentsOverride{Controlplane: &YanetControlplaneOverride{
				DisabledNuma: []int32{-1},
			}},
			wantErr: "non-negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yanet := makeYanet("edge", "yanet", "release")
			yanet.Spec.Components = tt.components
			_, err := validator.ValidateCreate(context.Background(), yanet)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateCreate: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestYanetWebhook_DataplaneSidecarOverrideRequiresBoxWiring(t *testing.T) {
	config := clusterConfig("release")
	config.Spec.BoxTypes[0].Components.Dataplane.Sidecars.Bird = nil
	validator := &YanetCustomValidator{Client: newClientWith(t, config)}
	yanet := makeYanet("edge", "yanet", "release")
	yanet.Spec.Components = &YanetComponentsOverride{
		Dataplane: &YanetComponentOverride{
			Containers: map[string]YanetContainerOverride{
				BirdSidecarContainerName: {Enabled: boolPointer(true)},
			},
		},
	}
	_, err := validator.ValidateCreate(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "is not wired") {
		t.Fatalf("unwired sidecar override must be rejected, got %v", err)
	}
}

func TestYanetWebhook_OptionalComponentOverrideRequiresBoxWiring(t *testing.T) {
	config := clusterConfig("release")
	config.Spec.Components.BirdAdapter = &BirdAdapterComp{Image: ImageRef{Name: "bird-adapter"}}
	validator := &YanetCustomValidator{Client: newClientWith(t, config)}
	yanet := makeYanet("edge", "yanet", "release")
	yanet.Spec.Components = &YanetComponentsOverride{
		BirdAdapter: &YanetComponentOverride{Containers: map[string]YanetContainerOverride{
			BirdAdapterContainerName: {Tag: "v2"},
		}},
	}
	_, err := validator.ValidateCreate(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "is not wired") {
		t.Fatalf("unwired optional component override must be rejected, got %v", err)
	}
}

func TestYanetWebhook_OperatorOverrideRequiresBoxWiring(t *testing.T) {
	config := clusterConfig("release")
	delete(config.Spec.BoxTypes[0].Operators, "antiddos")
	validator := &YanetCustomValidator{Client: newClientWith(t, config)}
	yanet := makeYanet("edge", "yanet", "release")
	yanet.Spec.Components = &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{"antiddos": {}},
	}
	_, err := validator.ValidateCreate(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "is not wired") {
		t.Fatalf("unwired operator override must be rejected, got %v", err)
	}
}

func TestValidateEffectiveYanetComponentOverridesIgnoresUnwiredOverrides(t *testing.T) {
	config := clusterConfig("release")
	delete(config.Spec.BoxTypes[0].Operators, "antiddos")
	overrides := &YanetComponentsOverride{
		Operators: map[string]YanetComponentOverride{"antiddos": {}},
	}

	if err := ValidateEffectiveYanetComponentOverrides(
		overrides,
		&config.Spec.Components,
		&config.Spec.BoxTypes[0],
	); err != nil {
		t.Fatalf("stale unwired override must not block reconciliation: %v", err)
	}
	if err := ValidateYanetComponentOverrides(
		overrides,
		&config.Spec.Components,
		&config.Spec.BoxTypes[0],
	); err == nil {
		t.Fatal("admission validation must still reject an unwired override")
	}
}

func TestYanetWebhook_BirdAdapterUsesRenderedContainerName(t *testing.T) {
	config := clusterConfig("release")
	config.Spec.Components.BirdAdapter = &BirdAdapterComp{Image: ImageRef{Name: "bird-adapter"}}
	config.Spec.BoxTypes[0].Components.BirdAdapter = &BoxComponent{}
	validator := &YanetCustomValidator{Client: newClientWith(t, config)}
	yanet := makeYanet("edge", "yanet", "release")
	yanet.Spec.Components = &YanetComponentsOverride{
		BirdAdapter: &YanetComponentOverride{Containers: map[string]YanetContainerOverride{
			BirdAdapterContainerName: {Tag: "v2"},
		}},
	}
	if _, err := validator.ValidateCreate(context.Background(), yanet); err != nil {
		t.Fatalf("rendered bird-adapter container name rejected: %v", err)
	}

	yanet.Spec.Components.BirdAdapter.Containers = map[string]YanetContainerOverride{
		"birdAdapter": {Tag: "v2"},
	}
	_, err := validator.ValidateCreate(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), BirdAdapterContainerName) {
		t.Fatalf("camelCase birdAdapter container name must be rejected, got %v", err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestYanetWebhook_BoxTypeImmutable(t *testing.T) {
	validator := &YanetCustomValidator{Client: newClientWith(t, clusterConfig("release", "balancer"))}
	oldYanet := makeYanet("edge", "yanet", "release")
	newYanet := oldYanet.DeepCopy()
	newYanet.Spec.BoxType = "balancer"
	if _, err := validator.ValidateUpdate(context.Background(), oldYanet, newYanet); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("boxType change must be rejected, got %v", err)
	}
}

func TestYanetWebhook_DeleteAlwaysAllowed(t *testing.T) {
	validator := &YanetCustomValidator{Client: newClientWith(t)}
	if _, err := validator.ValidateDelete(context.Background(), makeYanet("edge", "yanet", "release")); err != nil {
		t.Fatalf("ValidateDelete: %v", err)
	}
}
