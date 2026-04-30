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
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

const (
	// ConditionTypeReady indicates whether all deployments are ready
	ConditionTypeReady = "Ready"
	// ConditionTypeSynced indicates whether deployments are in sync with spec
	ConditionTypeSynced = "Synced"
	// ConditionTypeProgressing indicates whether deployments are being updated
	ConditionTypeProgressing = "Progressing"
)

// computeConditions calculates status conditions based on sync state and pods
func (r *YanetReconciler) computeConditions(
	yanet *yanetv1alpha1.Yanet,
	sync yanetv1alpha1.Sync,
	pods map[v1.PodPhase][]string,
) []metav1.Condition {
	now := metav1.Now()
	conditions := []metav1.Condition{}

	// Condition: Synced
	syncedCondition := metav1.Condition{
		Type:               ConditionTypeSynced,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: yanet.Generation,
		LastTransitionTime: now,
		Reason:             "AllDeploymentsSynced",
		Message:            "All deployments are in sync with spec",
	}

	if len(sync.OutOfSync) > 0 {
		syncedCondition.Status = metav1.ConditionFalse
		syncedCondition.Reason = "DeploymentsOutOfSync"
		syncedCondition.Message = fmt.Sprintf("Deployments out of sync: %v (AutoSync disabled)", sync.OutOfSync)
	} else if len(sync.Error) > 0 {
		syncedCondition.Status = metav1.ConditionFalse
		syncedCondition.Reason = "SyncError"
		syncedCondition.Message = fmt.Sprintf("Errors syncing deployments: %v", sync.Error)
	}

	conditions = append(conditions, syncedCondition)

	// Condition: Progressing
	progressingCondition := metav1.Condition{
		Type:               ConditionTypeProgressing,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: yanet.Generation,
		LastTransitionTime: now,
		Reason:             "NoUpdatesInProgress",
		Message:            "No updates in progress",
	}

	if len(sync.SyncWaiting) > 0 {
		progressingCondition.Status = metav1.ConditionTrue
		progressingCondition.Reason = "WaitingForUpdateWindow"
		progressingCondition.Message = fmt.Sprintf("Waiting for UpdateWindow: %v", sync.SyncWaiting)
	}

	conditions = append(conditions, progressingCondition)

	// Condition: Ready
	readyCondition := metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: yanet.Generation,
		LastTransitionTime: now,
		Reason:             "AllPodsRunning",
		Message:            "All pods are running",
	}

	runningPods := len(pods[v1.PodRunning])
	totalEnabled := len(sync.Synced) + len(sync.Disabled)

	if runningPods == 0 && totalEnabled > 0 {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NoPodsRunning"
		readyCondition.Message = "No pods are running"
	} else if len(pods[v1.PodPending]) > 0 {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "PodsNotReady"
		readyCondition.Message = fmt.Sprintf("%d pods pending", len(pods[v1.PodPending]))
	} else if len(pods[v1.PodFailed]) > 0 {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "PodsFailed"
		readyCondition.Message = fmt.Sprintf("%d pods failed", len(pods[v1.PodFailed]))
	}

	conditions = append(conditions, readyCondition)

	return conditions
}
