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
	"time"

	"github.com/go-logr/logr"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// silentLogger returns a no-op logr.Logger suitable for unit tests
// that do not care about log output.
func silentLogger() logr.Logger {
	return logr.Discard()
}

// reconcileTwice performs the finalizer-install step and then the
// real reconcile, returning the second call's (Result, error). It
// also re-fetches the YanetV2 so callers always see the latest copy.
func reconcileTwice(t *testing.T, r *YanetV2Reconciler, yanet *yanetv2alpha1.YanetV2) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.reconcileYanetV2(ctx, yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: yanet.Name, Namespace: yanet.Namespace}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(ctx, yanet); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// ---------------------------------------------------------------------------
// H1: finalizer
// ---------------------------------------------------------------------------

func TestReconcileV2_FirstReconcile_AddsFinalizer(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	r, snap := makeReconcilerEnv(t, yanet)
	snap.Config = minimalConfigV2()

	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("err: %v", err)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	found := false
	for _, f := range got.ObjectMeta.Finalizers {
		if f == yanetFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("finalizer %q must be added on first reconcile, got %v", yanetFinalizer, got.ObjectMeta.Finalizers)
	}
}

func TestReconcileV2_DeletionTimestamp_RunsCleanupAndRemovesFinalizer(t *testing.T) {
	autoSync := true
	now := metav1.Now()
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "y",
			Namespace:         "yanet",
			Finalizers:        []string{yanetFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
			AutoSync:     &autoSync,
		},
	}
	// Pre-existing Deployment/Service/ConfigMap labelled as ours.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y-cp-old",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y-svc-old",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y-cm-old",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}
	r, _ := makeReconcilerEnv(t, yanet, dep, svc, cm)

	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	// All managed resources gone.
	gotDep := &appsv1.Deployment{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y-cp-old", Namespace: "yanet"}, gotDep); !apierrors.IsNotFound(err) {
		t.Errorf("expected Deployment deleted, got err=%v", err)
	}
	gotSvc := &corev1.Service{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y-svc-old", Namespace: "yanet"}, gotSvc); !apierrors.IsNotFound(err) {
		t.Errorf("expected Service deleted, got err=%v", err)
	}
	gotCM := &corev1.ConfigMap{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y-cm-old", Namespace: "yanet"}, gotCM); !apierrors.IsNotFound(err) {
		t.Errorf("expected ConfigMap deleted, got err=%v", err)
	}
	// Finalizer was removed (the fake client then GCs the CR).
	got := &yanetv2alpha1.YanetV2{}
	err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got)
	if err == nil {
		for _, f := range got.ObjectMeta.Finalizers {
			if f == yanetFinalizer {
				t.Errorf("finalizer must have been removed, got %v", got.ObjectMeta.Finalizers)
			}
		}
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("re-get: %v", err)
	}
}

// ---------------------------------------------------------------------------
// H2: prune orphans
// ---------------------------------------------------------------------------

func TestPruneOrphans_DeletesUnknownDeploymentsServicesConfigMaps(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	keep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "keep", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	drop := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "drop", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	other := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "another-yanet"},
		},
	}
	r, _ := makeReconcilerEnv(t, yanet, keep, drop, other)

	desired := newDesiredSet()
	desired.Deployments["keep"] = struct{}{}
	count, err := r.pruneOrphans(context.Background(), yanet, desired, true, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 orphan deleted, got %d", count)
	}
	// keep present
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "keep", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("kept Deployment must remain: %v", err)
	}
	// drop gone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "drop", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("orphan must be deleted, got err=%v", err)
	}
	// other untouched
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "other", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("foreign Deployment must not be touched: %v", err)
	}
}

// TestPruneOrphans_DeletesStaleInlineConfigMap simulates an inline
// patch whose content changed between reconciles: the old hash-named
// ConfigMap is no longer in the desired set but still carries the
// LabelYanet label, so prune must delete it while keeping the
// freshly-named one.
func TestPruneOrphans_DeletesStaleInlineConfigMap(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	stale := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp-cfg-deadbeef", Namespace: "yanet",
			Labels: map[string]string{
				manifests.LabelYanet:     "y",
				manifests.LabelComponent: "controlplane",
			},
		},
		Data: map[string]string{"config": "old content"},
	}
	fresh := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp-cfg-cafef00d", Namespace: "yanet",
			Labels: map[string]string{
				manifests.LabelYanet:     "y",
				manifests.LabelComponent: "controlplane",
			},
		},
		Data: map[string]string{"config": "new content"},
	}
	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-cfg", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "another-yanet"},
		},
	}
	r, _ := makeReconcilerEnv(t, yanet, stale, fresh, foreign)

	desired := newDesiredSet()
	desired.ConfigMaps[fresh.Name] = struct{}{}

	count, err := r.pruneOrphans(context.Background(), yanet, desired, true, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 orphan deleted, got %d", count)
	}

	// Stale CM (old hash) must be gone.
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: stale.Name, Namespace: "yanet"}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Errorf("stale inline ConfigMap must be deleted, got err=%v", err)
	}
	// Fresh CM (new hash) must remain.
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: fresh.Name, Namespace: "yanet"}, &corev1.ConfigMap{}); err != nil {
		t.Errorf("fresh inline ConfigMap must remain: %v", err)
	}
	// Foreign YanetV2's CM must not be touched.
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: foreign.Name, Namespace: "yanet"}, &corev1.ConfigMap{}); err != nil {
		t.Errorf("foreign ConfigMap must not be touched: %v", err)
	}
}

func TestPruneOrphans_AutoSyncFalse_DoesNotDelete(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	drop := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "drop", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	r, _ := makeReconcilerEnv(t, yanet, drop)

	count, err := r.pruneOrphans(context.Background(), yanet, newDesiredSet(), false, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 orphan reported, got %d", count)
	}
	// not actually deleted
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "drop", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("autoSync=false ⇒ orphan must remain, got err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// H3: UpdateWindow throttle for the v2 path
// ---------------------------------------------------------------------------

func TestApplyDeploymentV2_UpdateWindowThrottlesCrossHostUpdate(t *testing.T) {
	// Pre-create a Deployment on node-A so the second call sees a
	// drift to apply; record a recent update on node-A so node-B
	// gets throttled.
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp", Namespace: "yanet",
			Labels: map[string]string{"v": "new"},
		},
	}
	existing := desired.DeepCopy()
	existing.Labels = map[string]string{"v": "old"}
	r, _ := makeReconcilerEnv(t, existing)

	// Simulate "node-A just updated 10 ms ago".
	r.lock.Lock()
	r.lastUpdateTS = time.Now()
	r.lastUpdateHost = "node-A"
	r.lock.Unlock()

	state, requeue := r.applyDeploymentV2(
		context.Background(),
		desired.DeepCopy(),
		true,           // autoSync
		1*time.Hour,    // updateWindow far in the future
		"node-B",       // different host triggers throttle
		silentLogger(), // logger
	)
	if state != "sync-waiting" {
		t.Errorf("expected sync-waiting on cross-host throttle, got %q", state)
	}
	if requeue == 0 {
		t.Errorf("expected non-zero requeue on throttle, got %v", requeue)
	}
	// Existing Deployment must NOT have been updated.
	got := &appsv1.Deployment{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "cp", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Labels["v"] != "old" {
		t.Errorf("Deployment must not have been updated, got labels=%v", got.Labels)
	}
}

func TestApplyDeploymentV2_UpdateWindowZero_AlwaysApplies(t *testing.T) {
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "yanet"},
	}
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp", Namespace: "yanet",
			Labels: map[string]string{"v": "new"},
		},
	}
	r, _ := makeReconcilerEnv(t, existing)

	state, requeue := r.applyDeploymentV2(
		context.Background(),
		desired.DeepCopy(),
		true, // autoSync
		0,    // updateWindow=0 disables throttle
		"node-B",
		silentLogger(),
	)
	if state != "synced" {
		t.Errorf("expected synced when updateWindow=0, got %q (requeue=%v)", state, requeue)
	}
}

// ---------------------------------------------------------------------------
// H5: conditions
// ---------------------------------------------------------------------------

func TestComputeConditionsV2_Healthy(t *testing.T) {
	y := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet", Generation: 7},
		Status: yanetv2alpha1.YanetStatus{
			Sync: yanetv2alpha1.SyncStatus{Synced: []string{"d1", "d2"}},
		},
	}
	got := computeConditionsV2(y, nil)
	statusByType := map[string]metav1.ConditionStatus{}
	for _, c := range got {
		statusByType[c.Type] = c.Status
	}
	if statusByType["Available"] != metav1.ConditionTrue {
		t.Errorf("Available=True expected, got %v", statusByType["Available"])
	}
	if statusByType["Progressing"] != metav1.ConditionFalse {
		t.Errorf("Progressing=False expected, got %v", statusByType["Progressing"])
	}
	if statusByType["Degraded"] != metav1.ConditionFalse {
		t.Errorf("Degraded=False expected, got %v", statusByType["Degraded"])
	}
	if statusByType["Ready"] != metav1.ConditionTrue {
		t.Errorf("Ready=True expected, got %v", statusByType["Ready"])
	}
}

func TestComputeConditionsV2_OperatorMissing_Degraded(t *testing.T) {
	y := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	got := computeConditionsV2(y, map[string]struct{}{"telegraf": {}})
	for _, c := range got {
		if c.Type == "Degraded" {
			if c.Status != metav1.ConditionTrue {
				t.Errorf("Degraded must be True, got %v", c.Status)
			}
			if c.Reason != "OperatorMissing" {
				t.Errorf("Degraded reason: got %q want %q", c.Reason, "OperatorMissing")
			}
			return
		}
	}
	t.Errorf("Degraded condition missing")
}

func TestComputeConditionsV2_SyncWaiting_ProgressingTrue(t *testing.T) {
	y := &yanetv2alpha1.YanetV2{
		Status: yanetv2alpha1.YanetStatus{
			Sync: yanetv2alpha1.SyncStatus{SyncWaiting: []string{"cp"}},
		},
	}
	got := computeConditionsV2(y, nil)
	for _, c := range got {
		if c.Type == "Progressing" {
			if c.Status != metav1.ConditionTrue {
				t.Errorf("Progressing must be True with SyncWaiting, got %v", c.Status)
			}
			if c.Reason != "WaitingForUpdateWindow" {
				t.Errorf("Progressing reason: got %q want WaitingForUpdateWindow", c.Reason)
			}
			return
		}
	}
	t.Errorf("Progressing condition missing")
}

func TestComputeConditionsV2_PreservesLastTransitionTime(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	y := &yanetv2alpha1.YanetV2{
		Status: yanetv2alpha1.YanetStatus{
			Conditions: []metav1.Condition{{
				Type:               "Available",
				Status:             metav1.ConditionTrue,
				Reason:             "AllSynced",
				LastTransitionTime: old,
			}},
		},
	}
	got := computeConditionsV2(y, nil)
	for _, c := range got {
		if c.Type == "Available" {
			if !c.LastTransitionTime.Equal(&old) {
				t.Errorf("LastTransitionTime must be preserved when Status/Reason unchanged: got %v want %v", c.LastTransitionTime, old)
			}
			return
		}
	}
}

func TestSetConditionsV2Degraded_OnlyTouchesDegraded(t *testing.T) {
	y := &yanetv2alpha1.YanetV2{}
	setConditionsV2Degraded(y, "ConfigNotLoaded", "snap empty")
	if len(y.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(y.Status.Conditions))
	}
	c := y.Status.Conditions[0]
	if c.Type != "Degraded" || c.Status != metav1.ConditionTrue || c.Reason != "ConfigNotLoaded" {
		t.Errorf("unexpected condition: %+v", c)
	}
}

// ---------------------------------------------------------------------------
// H5: pod aggregation
// ---------------------------------------------------------------------------

func TestCollectPodsV2_GroupsByPhase(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	pods := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-running", Namespace: "yanet",
				Labels: map[string]string{manifests.LabelYanet: "y"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-pending", Namespace: "yanet",
				Labels: map[string]string{manifests.LabelYanet: "y"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-foreign", Namespace: "yanet",
				Labels: map[string]string{manifests.LabelYanet: "another-yanet"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	}
	r, _ := makeReconcilerEnv(t, append(pods, yanet)...)
	out := collectPodsV2(context.Background(), r.Client, yanet, silentLogger())

	if got := out[corev1.PodRunning]; len(got) != 1 || got[0] != "p-running" {
		t.Errorf("Running bucket: got %v want [p-running]", got)
	}
	if got := out[corev1.PodPending]; len(got) != 1 || got[0] != "p-pending" {
		t.Errorf("Pending bucket: got %v want [p-pending]", got)
	}
}

// ---------------------------------------------------------------------------
// H1+H2 end-to-end via reconcile
// ---------------------------------------------------------------------------

func TestReconcileV2_AutoSyncOn_PrunesOrphanDeployment(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
			AutoSync:     &autoSync,
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	// Pre-existing orphan Deployment that would not be regenerated.
	orphan := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y-old-component",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node, orphan)
	snap.Config = minimalConfigV2()

	reconcileTwice(t, r, yanet)

	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y-old-component", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected orphan Deployment deleted, got err=%v", err)
	}
}
