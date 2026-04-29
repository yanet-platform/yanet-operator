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
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestComputeConditions tests the computeConditions method
func TestComputeConditions(t *testing.T) {
	tests := []struct {
		name                 string
		sync                 yanetv1alpha1.Sync
		pods                 map[v1.PodPhase][]string
		expectedSynced       metav1.ConditionStatus
		expectedProgressing  metav1.ConditionStatus
		expectedReady        metav1.ConditionStatus
		expectedSyncedReason string
		expectedReadyReason  string
	}{
		{
			name: "all synced and running",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane", "controlplane"},
				OutOfSync:   []string{},
				SyncWaiting: []string{},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1", "pod2"},
			},
			expectedSynced:       metav1.ConditionTrue,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionTrue,
			expectedSyncedReason: "AllDeploymentsSynced",
			expectedReadyReason:  "AllPodsRunning",
		},
		{
			name: "out of sync deployments",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane"},
				OutOfSync:   []string{"controlplane"},
				SyncWaiting: []string{},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1"},
			},
			expectedSynced:       metav1.ConditionFalse,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionTrue,
			expectedSyncedReason: "DeploymentsOutOfSync",
			expectedReadyReason:  "AllPodsRunning",
		},
		{
			name: "sync errors",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{},
				OutOfSync:   []string{},
				SyncWaiting: []string{},
				Error:       []string{"dataplane: failed to create"},
				Disabled:    []string{},
			},
			pods:                 map[v1.PodPhase][]string{},
			expectedSynced:       metav1.ConditionFalse,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionTrue,
			expectedSyncedReason: "SyncError",
			expectedReadyReason:  "AllPodsRunning",
		},
		{
			name: "waiting for update window",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane"},
				OutOfSync:   []string{},
				SyncWaiting: []string{"controlplane"},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1"},
			},
			expectedSynced:       metav1.ConditionTrue,
			expectedProgressing:  metav1.ConditionTrue,
			expectedReady:        metav1.ConditionTrue,
			expectedSyncedReason: "AllDeploymentsSynced",
			expectedReadyReason:  "AllPodsRunning",
		},
		{
			name: "pods pending with running pods",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane"},
				OutOfSync:   []string{},
				SyncWaiting: []string{},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1"},
				v1.PodPending: {"pod2"},
			},
			expectedSynced:       metav1.ConditionTrue,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionFalse,
			expectedSyncedReason: "AllDeploymentsSynced",
			expectedReadyReason:  "PodsNotReady",
		},
		{
			name: "pods failed with running pods",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane"},
				OutOfSync:   []string{},
				SyncWaiting: []string{},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1"},
				v1.PodFailed:  {"pod2"},
			},
			expectedSynced:       metav1.ConditionTrue,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionFalse,
			expectedSyncedReason: "AllDeploymentsSynced",
			expectedReadyReason:  "PodsFailed",
		},
		{
			name: "no pods running but deployments enabled",
			sync: yanetv1alpha1.Sync{
				Synced:      []string{"dataplane"},
				OutOfSync:   []string{},
				SyncWaiting: []string{},
				Error:       []string{},
				Disabled:    []string{},
			},
			pods:                 map[v1.PodPhase][]string{},
			expectedSynced:       metav1.ConditionTrue,
			expectedProgressing:  metav1.ConditionFalse,
			expectedReady:        metav1.ConditionFalse,
			expectedSyncedReason: "AllDeploymentsSynced",
			expectedReadyReason:  "NoPodsRunning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &YanetReconciler{}
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
			}

			conditions := r.computeConditions(yanet, tt.sync, tt.pods)

			// Should have 3 conditions: Synced, Progressing, Ready
			if len(conditions) != 3 {
				t.Fatalf("Expected 3 conditions, got %d", len(conditions))
			}

			// Find each condition
			var syncedCond, progressingCond, readyCond *metav1.Condition
			for i := range conditions {
				switch conditions[i].Type {
				case ConditionTypeSynced:
					syncedCond = &conditions[i]
				case ConditionTypeProgressing:
					progressingCond = &conditions[i]
				case ConditionTypeReady:
					readyCond = &conditions[i]
				}
			}

			// Verify Synced condition
			if syncedCond == nil {
				t.Fatal("Synced condition not found")
			}
			if syncedCond.Status != tt.expectedSynced {
				t.Errorf("Synced condition status = %v, want %v", syncedCond.Status, tt.expectedSynced)
			}
			if syncedCond.Reason != tt.expectedSyncedReason {
				t.Errorf("Synced condition reason = %v, want %v", syncedCond.Reason, tt.expectedSyncedReason)
			}

			// Verify Progressing condition
			if progressingCond == nil {
				t.Fatal("Progressing condition not found")
			}
			if progressingCond.Status != tt.expectedProgressing {
				t.Errorf("Progressing condition status = %v, want %v", progressingCond.Status, tt.expectedProgressing)
			}

			// Verify Ready condition
			if readyCond == nil {
				t.Fatal("Ready condition not found")
			}
			if readyCond.Status != tt.expectedReady {
				t.Errorf("Ready condition status = %v, want %v", readyCond.Status, tt.expectedReady)
			}
			if readyCond.Reason != tt.expectedReadyReason {
				t.Errorf("Ready condition reason = %v, want %v", readyCond.Reason, tt.expectedReadyReason)
			}
		})
	}
}
