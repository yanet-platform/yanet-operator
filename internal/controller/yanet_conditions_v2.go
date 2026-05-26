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
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// computeConditionsV2 produces the standard kubebuilder conditions
// for a v2 YanetV2 resource based on the freshly aggregated SyncStatus
// and a set of operator names that were declared in the boxType but
// missing from the components.operators[] palette.
//
// Conditions:
//   - Available: True when there is no Error and no OutOfSync.
//   - Progressing: True when SyncWaiting is non-empty (the
//     UpdateWindow throttle is delaying a node).
//   - Degraded: True when Error is non-empty or operators are
//     declared but missing.
//   - Ready: aggregate (Available && !Degraded && !Progressing).
//
// Existing conditions that are already at the desired Status with the
// same Reason/Message are kept verbatim so LastTransitionTime stays
// stable.
func computeConditionsV2(yanet *yanetv2alpha1.YanetV2, missingOperators map[string]struct{}) []metav1.Condition {
	now := metav1.Now()
	gen := yanet.Generation
	sync := yanet.Status.Sync

	// Available --------------------------------------------------
	avail := metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gen,
		LastTransitionTime: now,
		Reason:             "AllSynced",
		Message:            "All deployments are in sync",
	}
	if len(sync.Error) > 0 {
		avail.Status = metav1.ConditionFalse
		avail.Reason = "SyncError"
		avail.Message = fmt.Sprintf("Errors syncing deployments: %v", sync.Error)
	} else if len(sync.OutOfSync) > 0 {
		avail.Status = metav1.ConditionFalse
		avail.Reason = "OutOfSync"
		avail.Message = fmt.Sprintf("Deployments out of sync (autoSync=false): %v", sync.OutOfSync)
	}

	// Progressing ------------------------------------------------
	prog := metav1.Condition{
		Type:               "Progressing",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: gen,
		LastTransitionTime: now,
		Reason:             "Idle",
		Message:            "No updates in progress",
	}
	if len(sync.SyncWaiting) > 0 {
		prog.Status = metav1.ConditionTrue
		prog.Reason = "WaitingForUpdateWindow"
		prog.Message = fmt.Sprintf("Waiting for UpdateWindow: %v", sync.SyncWaiting)
	}

	// Degraded ---------------------------------------------------
	deg := metav1.Condition{
		Type:               "Degraded",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: gen,
		LastTransitionTime: now,
		Reason:             "Healthy",
		Message:            "No errors detected",
	}
	switch {
	case len(missingOperators) > 0:
		names := make([]string, 0, len(missingOperators))
		for n := range missingOperators {
			names = append(names, n)
		}
		sort.Strings(names)
		deg.Status = metav1.ConditionTrue
		deg.Reason = "OperatorMissing"
		deg.Message = fmt.Sprintf("BoxType references operators missing from palette: %v", names)
	case len(sync.Error) > 0:
		deg.Status = metav1.ConditionTrue
		deg.Reason = "SyncError"
		deg.Message = fmt.Sprintf("Errors syncing deployments: %v", sync.Error)
	}

	// Ready: True iff Available=True && Progressing=False && Degraded=False
	ready := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gen,
		LastTransitionTime: now,
		Reason:             "AllChecksPassed",
		Message:            "YanetV2 is ready",
	}
	if avail.Status != metav1.ConditionTrue || prog.Status == metav1.ConditionTrue || deg.Status == metav1.ConditionTrue {
		ready.Status = metav1.ConditionFalse
		ready.Reason = "NotReady"
		ready.Message = "See Available/Progressing/Degraded conditions"
	}

	out := []metav1.Condition{avail, prog, deg, ready}
	return mergeConditions(yanet.Status.Conditions, out, now)
}

// setConditionsV2Degraded is a fast-path used by error-out branches
// that bail before the full reconcile completes. It sets only the
// Degraded condition to True with the given reason and leaves other
// conditions intact (or zero-valued when none existed).
func setConditionsV2Degraded(yanet *yanetv2alpha1.YanetV2, reason, message string) {
	now := metav1.Now()
	deg := metav1.Condition{
		Type:               "Degraded",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: yanet.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}
	yanet.Status.Conditions = mergeConditions(yanet.Status.Conditions, []metav1.Condition{deg}, now)
}

// mergeConditions overlays new conditions onto existing ones, keeping
// LastTransitionTime stable when (Status, Reason) did not change for
// a given Type.
func mergeConditions(existing, fresh []metav1.Condition, now metav1.Time) []metav1.Condition {
	byType := make(map[string]metav1.Condition, len(existing))
	for _, c := range existing {
		byType[c.Type] = c
	}
	out := make([]metav1.Condition, 0, len(fresh)+len(existing))
	seen := map[string]struct{}{}
	for _, nc := range fresh {
		if old, ok := byType[nc.Type]; ok {
			if old.Status == nc.Status && old.Reason == nc.Reason {
				nc.LastTransitionTime = old.LastTransitionTime
			}
		}
		out = append(out, nc)
		seen[nc.Type] = struct{}{}
	}
	// Preserve any pre-existing condition Types that were not
	// overwritten by the fresh slice (defensive: keeps custom
	// conditions written elsewhere alive).
	for _, oc := range existing {
		if _, ok := seen[oc.Type]; !ok {
			out = append(out, oc)
		}
	}
	_ = now
	return out
}

// collectPodsV2 lists Pods labelled as ours and groups their names
// by phase. An empty result is fine and merely means no Pod has been
// scheduled yet.
func collectPodsV2(
	ctx context.Context,
	cl client.Client,
	yanet *yanetv2alpha1.YanetV2,
	logger logr.Logger,
) map[corev1.PodPhase][]string {
	pods := &corev1.PodList{}
	if err := cl.List(ctx, pods,
		client.InNamespace(yanet.Namespace),
		client.MatchingLabels{manifests.LabelYanet: yanet.Name},
	); err != nil {
		logger.Info("pod list failed (continuing with empty pod set)", "error", err)
		return nil
	}
	out := map[corev1.PodPhase][]string{}
	for i := range pods.Items {
		p := &pods.Items[i]
		out[p.Status.Phase] = append(out[p.Status.Phase], p.Name)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
