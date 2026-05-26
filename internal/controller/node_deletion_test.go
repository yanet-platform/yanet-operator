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

package controller

import (
	"context"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestHandleNodeDeletion tests the handleNodeDeletion method
func TestHandleNodeDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = yanetv1alpha1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	tests := []struct {
		name           string
		nodeName       string
		existingYanets []client.Object
		expectDeletion bool
		expectError    bool
	}{
		{
			name:           "no yanet resources",
			nodeName:       "node1",
			existingYanets: []client.Object{},
			expectDeletion: false,
			expectError:    false,
		},
		{
			name:     "yanet found for deleted node",
			nodeName: "node1",
			existingYanets: []client.Object{
				&yanetv1alpha1.Yanet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "yanet-node1",
						Namespace: "default",
					},
					Spec: yanetv1alpha1.YanetSpec{
						NodeName: "node1",
					},
				},
			},
			expectDeletion: true,
			expectError:    false,
		},
		{
			name:     "yanet for different node",
			nodeName: "node1",
			existingYanets: []client.Object{
				&yanetv1alpha1.Yanet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "yanet-node2",
						Namespace: "default",
					},
					Spec: yanetv1alpha1.YanetSpec{
						NodeName: "node2",
					},
				},
			},
			expectDeletion: false,
			expectError:    false,
		},
		{
			name:     "multiple yanets, only one matches",
			nodeName: "node1",
			existingYanets: []client.Object{
				&yanetv1alpha1.Yanet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "yanet-node1",
						Namespace: "default",
					},
					Spec: yanetv1alpha1.YanetSpec{
						NodeName: "node1",
					},
				},
				&yanetv1alpha1.Yanet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "yanet-node2",
						Namespace: "default",
					},
					Spec: yanetv1alpha1.YanetSpec{
						NodeName: "node2",
					},
				},
			},
			expectDeletion: true,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with existing YanetV2 resources
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingYanets...).
				Build()

			// Create reconciler
			r := &YanetReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: events.NewFakeRecorder(10),
			}

			// Call handleNodeDeletion
			ctx := context.Background()
			_, err := r.handleNodeDeletion(ctx, tt.nodeName)

			// Check error
			if (err != nil) != tt.expectError {
				t.Errorf("handleNodeDeletion() error = %v, expectError %v", err, tt.expectError)
				return
			}

			// If deletion expected, verify YanetV2 was deleted
			if tt.expectDeletion {
				yanetList := &yanetv1alpha1.YanetList{}
				err = fakeClient.List(ctx, yanetList)
				if err != nil {
					t.Fatalf("Failed to list Yanets: %v", err)
				}

				// Check that YanetV2 for deleted node is gone
				for _, yanet := range yanetList.Items {
					if yanet.Spec.NodeName == tt.nodeName {
						t.Errorf("YanetV2 for node %s still exists after handleNodeDeletion", tt.nodeName)
					}
				}
			}
		})
	}
}
