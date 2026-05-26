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

func TestYanetValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		yanet   *Yanet
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid yanet with release type",
			yanet: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
				},
			},
			wantErr: false,
		},
		{
			name: "valid yanet with balancer type",
			yanet: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "balancer",
				},
			},
			wantErr: false,
		},
		{
			name: "valid yanet with empty type (default)",
			yanet: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid yanet with empty nodename",
			yanet: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "",
					Type:     "release",
				},
			},
			wantErr: true,
			errMsg:  "spec.nodename cannot be empty",
		},
		{
			name: "invalid yanet with wrong type",
			yanet: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "custom",
				},
			},
			wantErr: true,
			errMsg:  "spec.type must be either 'release' or 'balancer', got 'custom'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := tt.yanet.ValidateCreate(ctx, tt.yanet)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("ValidateCreate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestYanetValidateUpdate(t *testing.T) {
	tests := []struct {
		name    string
		old     *Yanet
		new     *Yanet
		wantErr bool
		errMsg  string
	}{
		{
			name: "allow finalizer removal when object is being deleted",
			old: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-node",
					Finalizers: []string{"yanet.yanet-platform.io/finalizer"},
				},
				Spec: YanetSpec{
					NodeName: "test-node",
					Type:     "release",
				},
			},
			new: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-node",
					DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
					Finalizers:        []string{},
				},
				Spec: YanetSpec{},
			},
			wantErr: false,
		},
		{
			name: "valid update - same nodename",
			old: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
				},
			},
			new: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
					AutoSync: true,
				},
			},
			wantErr: false,
		},
		{
			name: "valid update - change type",
			old: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
				},
			},
			new: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "balancer",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid update - change nodename",
			old: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
				},
			},
			new: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node2",
					Type:     "release",
				},
			},
			wantErr: true,
			errMsg:  "spec.nodename is immutable",
		},
		{
			name: "invalid update - empty nodename in new",
			old: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "node1",
					Type:     "release",
				},
			},
			new: &Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-yanet",
				},
				Spec: YanetSpec{
					NodeName: "",
					Type:     "release",
				},
			},
			wantErr: true,
			errMsg:  "spec.nodename is immutable",
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

func TestYanetValidateDelete(t *testing.T) {
	yanet := &Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-yanet",
		},
		Spec: YanetSpec{
			NodeName: "node1",
			Type:     "release",
		},
	}

	ctx := context.Background()
	_, err := yanet.ValidateDelete(ctx, yanet)
	if err != nil {
		t.Errorf("ValidateDelete() should not return error, got %v", err)
	}
}
