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

package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestYanetConfigValidateCreate(t *testing.T) {
	tests := []struct {
		name        string
		config      *YanetConfig
		wantErr     bool
		errMsg      string
		wantWarning bool
	}{
		{
			name: "valid config with positive updatewindow",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 300,
					Stop:         false,
				},
			},
			wantErr:     false,
			wantWarning: false,
		},
		{
			name: "valid config with zero updatewindow",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 0,
					Stop:         false,
				},
			},
			wantErr:     false,
			wantWarning: false,
		},
		{
			name: "invalid config with negative updatewindow",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: -10,
					Stop:         false,
				},
			},
			wantErr: true,
			errMsg:  "spec.updatewindow must be >= 0, got -10",
		},
		{
			name: "config with stop enabled (warning)",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 0,
					Stop:         true,
				},
			},
			wantErr:     false,
			wantWarning: true,
		},
		{
			name: "config with autodiscovery but no typeuri (warning)",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 0,
					AutoDiscovery: AutoDiscovery{
						Enable:  true,
						TypeUri: "",
					},
				},
			},
			wantErr:     false,
			wantWarning: true,
		},
		{
			name: "config with autodiscovery but no namespace (warning)",
			config: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 0,
					AutoDiscovery: AutoDiscovery{
						Enable:    true,
						TypeUri:   "http://example.com/type",
						Namespace: "",
					},
				},
			},
			wantErr:     false,
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			warnings, err := tt.config.ValidateCreate(ctx, tt.config)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("ValidateCreate() error message = %v, want %v", err.Error(), tt.errMsg)
			}

			if tt.wantWarning && len(warnings) == 0 {
				t.Errorf("ValidateCreate() expected warnings but got none")
			}

			if !tt.wantWarning && len(warnings) > 0 {
				t.Errorf("ValidateCreate() unexpected warnings: %v", warnings)
			}
		})
	}
}

func TestYanetConfigValidateUpdate(t *testing.T) {
	tests := []struct {
		name    string
		old     *YanetConfig
		new     *YanetConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid update",
			old: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 300,
				},
			},
			new: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 600,
				},
			},
			wantErr: false,
		},
		{
			name: "invalid update - negative updatewindow",
			old: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: 300,
				},
			},
			new: &YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config",
				},
				Spec: YanetConfigSpec{
					UpdateWindow: -5,
				},
			},
			wantErr: true,
			errMsg:  "spec.updatewindow must be >= 0, got -5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := tt.new.ValidateUpdate(ctx, tt.old, tt.new)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("ValidateUpdate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestYanetConfigValidateDelete(t *testing.T) {
	config := &YanetConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
		Spec: YanetConfigSpec{
			UpdateWindow: 300,
		},
	}

	ctx := context.Background()
	_, err := config.ValidateDelete(ctx, config)
	if err != nil {
		t.Errorf("ValidateDelete() should not return error, got %v", err)
	}
}
