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
	"reflect"
	"strings"
	"testing"
)

func TestYanetConfigWebhook_BirdConsumerDependencies(t *testing.T) {
	for _, consumer := range []string{"", "birdAdapter", "announcer"} {
		for _, bird := range []string{"absent", "palette-only", "disabled", "default", "enabled"} {
			t.Run(consumer+"/"+bird, func(t *testing.T) {
				cfg := validConfig()
				box := &cfg.Spec.BoxTypes[0]
				if bird != "absent" {
					cfg.Spec.Components.Dataplane.Sidecars = &DataplaneSidecarsSpec{
						Bird: &DataplaneSidecarSpec{Image: ImageRef{Name: "bird"}},
					}
				}
				if bird != "absent" && bird != "palette-only" {
					slot := &BoxDataplaneSidecar{}
					if bird != "default" {
						slot.Enabled = boolPointer(bird == "enabled")
					}
					box.Components.Dataplane.Sidecars = &BoxDataplaneSidecars{Bird: slot}
				}
				switch consumer {
				case "birdAdapter":
					cfg.Spec.Components.BirdAdapter = &BirdAdapterComp{Image: ImageRef{Name: "bird-adapter"}}
					box.Components.BirdAdapter = &BoxComponent{}
				case "announcer":
					cfg.Spec.Components.Announcer = &AnnouncerComp{Image: ImageRef{Name: "announcer"}}
					box.Components.Announcer = &BoxComponent{}
				}
				wantErr := consumer != "" && bird != "default" && bird != "enabled"
				validator := &YanetConfigCustomValidator{}
				_, createErr := validator.ValidateCreate(context.Background(), cfg)
				_, updateErr := validator.ValidateUpdate(context.Background(), validConfig(), cfg)
				for _, err := range []error{createErr, updateErr} {
					if wantErr {
						if err == nil || !strings.Contains(err.Error(), consumer) || !strings.Contains(err.Error(), "managed BIRD") {
							t.Fatalf("expected actionable BIRD dependency error, got %v", err)
						}
					} else if err != nil {
						t.Fatalf("valid optional-BIRD topology rejected: %v", err)
					}
				}
			})
		}
	}
}

func TestYanetWebhook_BirdConsumerDependencies(t *testing.T) {
	for _, tt := range []struct {
		name         string
		bird         *bool
		dataplane    *bool
		adapter      *bool
		announcer    *bool
		installation *bool
		boxBird      *bool
		wantConsumer string
	}{
		{name: "defaults"},
		{name: "bird disabled", bird: boolPointer(false), wantConsumer: "birdAdapter"},
		{name: "remaining announcer", bird: boolPointer(false), adapter: boolPointer(false), wantConsumer: "announcer"},
		{name: "both consumers explicitly disabled", bird: boolPointer(false), adapter: boolPointer(false), announcer: boolPointer(false)},
		{name: "dataplane disabled", dataplane: boolPointer(false), wantConsumer: "birdAdapter"},
		{name: "dataplane and consumers disabled", dataplane: boolPointer(false), adapter: boolPointer(false), announcer: boolPointer(false)},
		{name: "installation scaled to zero", bird: boolPointer(false), adapter: boolPointer(true), announcer: boolPointer(true), installation: boolPointer(false)},
		{name: "box-disabled bird enabled by override", boxBird: boolPointer(false), bird: boolPointer(true)},
		{name: "box-disabled bird inherited", boxBird: boolPointer(false), wantConsumer: "birdAdapter"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clusterConfig("release")
			cfg.Spec.Components.BirdAdapter = &BirdAdapterComp{Image: ImageRef{Name: "bird-adapter"}}
			cfg.Spec.Components.Announcer = &AnnouncerComp{Image: ImageRef{Name: "announcer"}}
			cfg.Spec.BoxTypes[0].Components.BirdAdapter = &BoxComponent{}
			cfg.Spec.BoxTypes[0].Components.Announcer = &BoxComponent{}
			cfg.Spec.BoxTypes[0].Components.Dataplane.Sidecars.Bird.Enabled = tt.boxBird
			yanet := makeYanet("edge", "yanet", "release")
			yanet.Spec.Enabled = tt.installation
			if tt.bird != nil || tt.dataplane != nil || tt.adapter != nil || tt.announcer != nil {
				yanet.Spec.Components = &YanetComponentsOverride{
					Dataplane: &YanetComponentOverride{Enabled: tt.dataplane, Containers: map[string]YanetContainerOverride{
						BirdSidecarContainerName: {Enabled: tt.bird},
					}},
					BirdAdapter: &YanetComponentOverride{Enabled: tt.adapter},
					Announcer:   &YanetComponentOverride{Enabled: tt.announcer},
				}
			}
			before := yanet.DeepCopy()
			validator := &YanetCustomValidator{Client: newClientWith(t, cfg)}
			_, createErr := validator.ValidateCreate(context.Background(), yanet)
			_, updateErr := validator.ValidateUpdate(context.Background(), makeYanet("edge", "yanet", "release"), yanet)
			for _, err := range []error{createErr, updateErr} {
				if tt.wantConsumer != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantConsumer) || !strings.Contains(err.Error(), "managed BIRD") {
						t.Fatalf("expected %s dependency error, got %v", tt.wantConsumer, err)
					}
				} else if err != nil {
					t.Fatalf("valid effective topology rejected: %v", err)
				}
			}
			if !reflect.DeepEqual(before, yanet) {
				t.Fatal("validation must not silently cascade or mutate overrides")
			}
		})
	}
}
