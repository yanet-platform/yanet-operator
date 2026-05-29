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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ---------------------------------------------------------------------------
// Edge cases for reconcileYanetV2
// ---------------------------------------------------------------------------

// TestReconcileV2_EmptyNodeSelector_NoDeployments verifies that when
// nodeSelector matches no nodes, reconcile succeeds but creates no
// Deployments or Services.
func TestReconcileV2_EmptyNodeSelector_NoDeployments(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "non-existent"},
		},
	}
	// No nodes match the selector
	r, snap := makeReconcilerEnv(t, yanet)
	snap.Config = minimalConfigV2()

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// Reconcile with no matching nodes
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Verify no Deployments created
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) != 0 {
		t.Errorf("expected 0 deployments when no nodes match, got %d", len(deps.Items))
	}

	// Verify no Services created
	svcs := &corev1.ServiceList{}
	if err := r.Client.List(context.Background(), svcs, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list svcs: %v", err)
	}
	if len(svcs.Items) != 0 {
		t.Errorf("expected 0 services when no nodes match, got %d", len(svcs.Items))
	}
}

// TestReconcileV2_ConfigNotLoaded_Requeues verifies that when
// YanetConfigV2 snapshot is empty, reconcile sets Degraded condition
// and requeues.
func TestReconcileV2_ConfigNotLoaded_Requeues(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, _ := makeReconcilerEnv(t, yanet, node)
	// Leave GlobalConfigV2.Config empty to simulate config not loaded

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// Reconcile with empty config
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Verify requeue is scheduled
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 when config not loaded, got %v", result.RequeueAfter)
	}

	// Verify status has Degraded condition
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get after reconcile: %v", err)
	}

	foundDegraded := false
	for _, cond := range yanet.Status.Conditions {
		if cond.Type == "Degraded" && cond.Status == metav1.ConditionTrue {
			foundDegraded = true
			if cond.Reason != "ConfigNotLoaded" {
				t.Errorf("expected Degraded reason=ConfigNotLoaded, got %q", cond.Reason)
			}
		}
	}
	if !foundDegraded {
		t.Errorf("expected Degraded=True condition when config not loaded")
	}
}

// TestReconcileV2_BoxTypeNotFound_Requeues verifies that when the
// referenced boxType doesn't exist in YanetConfigV2, reconcile sets
// Degraded condition and requeues.
func TestReconcileV2_BoxTypeNotFound_Requeues(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "non-existent",
			NodeSelector: map[string]string{"role": "yanet"},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	snap.Config = minimalConfigV2() // has "release" boxType, not "non-existent"

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// Reconcile with non-existent boxType
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Verify requeue is scheduled
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 when boxType not found, got %v", result.RequeueAfter)
	}

	// Verify status has Degraded condition with BoxTypeNotFound reason
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get after reconcile: %v", err)
	}

	foundDegraded := false
	for _, cond := range yanet.Status.Conditions {
		if cond.Type == "Degraded" && cond.Status == metav1.ConditionTrue {
			foundDegraded = true
			if cond.Reason != "BoxTypeNotFound" {
				t.Errorf("expected Degraded reason=BoxTypeNotFound, got %q", cond.Reason)
			}
		}
	}
	if !foundDegraded {
		t.Errorf("expected Degraded=True condition when boxType not found")
	}
}

// TestReconcileV2_StopEnabled_SkipsReconcile verifies that when
// YanetConfigV2.spec.stop=true, reconcile is skipped entirely.
func TestReconcileV2_StopEnabled_SkipsReconcile(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	cfg := minimalConfigV2()
	cfg.Stop = true
	snap.Config = cfg

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// Reconcile with stop=true
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Verify no requeue
	if result.RequeueAfter > 0 {
		t.Errorf("expected no requeue when stop=true, got %+v", result)
	}

	// Verify no Deployments created
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) != 0 {
		t.Errorf("expected 0 deployments when stop=true, got %d", len(deps.Items))
	}
}

// ---------------------------------------------------------------------------
// UpdateWindow throttling edge cases
// ---------------------------------------------------------------------------

// TestUpdateWindow_SameNode_NoThrottle verifies that updates on the same
// node are not throttled even when updateWindow is set.
func TestUpdateWindow_SameNode_NoThrottle(t *testing.T) {
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
	r, snap := makeReconcilerEnv(t, yanet, node)
	cfg := minimalConfigV2()
	cfg.UpdateWindow = 3600 // 1 hour
	snap.Config = cfg

	// Install finalizer first
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// First reconcile creates resources
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Verify Deployments were created
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) < 2 {
		t.Fatalf("expected at least 2 deployments (cp+dp), got %d", len(deps.Items))
	}

	// Now simulate a recent update on node-1 and reconcile again
	r.lastUpdateTS = time.Now().Add(-5 * time.Minute)
	r.lastUpdateHost = "node-1"

	// Second reconcile - should NOT be throttled because it's the same node
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// Verify no throttle requeue
	if result.RequeueAfter > 0 {
		t.Errorf("expected no throttle requeue for same node, got RequeueAfter=%v", result.RequeueAfter)
	}
}

// TestUpdateWindow_DifferentNode_Throttled verifies that updates on a
// different node within the updateWindow are throttled.
func TestUpdateWindow_DifferentNode_Throttled(t *testing.T) {
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
			Name:   "node-2",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	cfg := minimalConfigV2()
	cfg.UpdateWindow = 3600 // 1 hour
	snap.Config = cfg

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// First reconcile creates resources on node-2
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Now simulate a recent update on node-1 (different from current node-2)
	// and trigger drift by modifying a Deployment
	r.lastUpdateTS = time.Now().Add(-5 * time.Minute)
	r.lastUpdateHost = "node-1"

	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) > 0 {
		// Modify first deployment to create drift
		dep := &deps.Items[0]
		replicas := int32(99)
		dep.Spec.Replicas = &replicas
		if err := r.Client.Update(context.Background(), dep); err != nil {
			t.Fatalf("update dep: %v", err)
		}
	}

	// Second reconcile - should be throttled because it's a different node
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// Verify throttle requeue is set
	if result.RequeueAfter == 0 {
		t.Errorf("expected throttle requeue for different node within updateWindow")
	}

	// Verify status shows sync-waiting
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get after reconcile: %v", err)
	}

	if len(yanet.Status.Sync.SyncWaiting) == 0 {
		t.Errorf("expected SyncWaiting to be non-empty when throttled")
	}
}

// TestUpdateWindow_Expired_NoThrottle verifies that when updateWindow
// has expired, updates on different nodes are not throttled.
func TestUpdateWindow_Expired_NoThrottle(t *testing.T) {
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
			Name:   "node-2",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	cfg := minimalConfigV2()
	cfg.UpdateWindow = 300 // 5 minutes
	snap.Config = cfg

	// Simulate an old update on node-1 (more than updateWindow ago)
	r.lastUpdateTS = time.Now().Add(-10 * time.Minute)
	r.lastUpdateHost = "node-1"

	// Install finalizer
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}

	// Reconcile - should NOT be throttled because window expired
	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Verify no throttle requeue
	if result.RequeueAfter > 0 {
		t.Errorf("expected no throttle requeue when updateWindow expired, got RequeueAfter=%v", result.RequeueAfter)
	}

	// Verify Deployments were created (not throttled)
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) < 2 {
		t.Errorf("expected at least 2 deployments (cp+dp) to be created (not throttled), got %d", len(deps.Items))
	}
}

// ---------------------------------------------------------------------------
// Orphan cleanup edge cases
// ---------------------------------------------------------------------------

// TestOrphanCleanup_MultipleResourceTypes verifies that pruneOrphans
// correctly handles orphans across all three resource types
// (Deployments, Services, ConfigMaps) in a single call.
func TestOrphanCleanup_MultipleResourceTypes(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}

	// Create orphans of each type
	orphanDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-dep", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	orphanSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-svc", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	orphanCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-cm", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}

	// Create resources to keep
	keepDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "keep-dep", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}

	r, _ := makeReconcilerEnv(t, yanet, orphanDep, orphanSvc, orphanCM, keepDep)

	// Desired set only includes keepDep
	desired := newDesiredSet()
	desired.Deployments["keep-dep"] = struct{}{}

	// Prune with autoSync=true
	count, err := r.pruneOrphans(context.Background(), yanet, desired, true, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Verify count includes all three orphans
	if count != 3 {
		t.Errorf("expected 3 orphans deleted (dep+svc+cm), got %d", count)
	}

	// Verify orphans are deleted
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "orphan-dep", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("orphan Deployment must be deleted")
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "orphan-svc", Namespace: "yanet"}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Errorf("orphan Service must be deleted")
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "orphan-cm", Namespace: "yanet"}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Errorf("orphan ConfigMap must be deleted")
	}

	// Verify kept resource remains
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "keep-dep", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("kept Deployment must remain: %v", err)
	}
}

// TestOrphanCleanup_ForeignLabels_NotTouched verifies that resources
// with different LabelYanet values are not touched by pruneOrphans.
func TestOrphanCleanup_ForeignLabels_NotTouched(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}

	// Create resources belonging to different Yanet installations
	foreign1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foreign-1", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "other-yanet"},
		},
	}
	foreign2 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foreign-2", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "another-yanet"},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	noLabel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "no-label", Namespace: "yanet",
			// No LabelYanet at all
		},
	}

	r, _ := makeReconcilerEnv(t, yanet, foreign1, foreign2, noLabel)

	// Empty desired set - would delete everything if labels weren't checked
	desired := newDesiredSet()

	// Prune with autoSync=true
	count, err := r.pruneOrphans(context.Background(), yanet, desired, true, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Verify nothing was deleted (count=0)
	if count != 0 {
		t.Errorf("expected 0 deletions for foreign labels, got %d", count)
	}

	// Verify all foreign resources remain
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "foreign-1", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("foreign Deployment must not be touched: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "foreign-2", Namespace: "yanet"}, &corev1.Service{}); err != nil {
		t.Errorf("foreign Service must not be touched: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "no-label", Namespace: "yanet"}, &corev1.ConfigMap{}); err != nil {
		t.Errorf("unlabeled ConfigMap must not be touched: %v", err)
	}
}

// TestOrphanCleanup_EmptyDesiredSet_DeletesAll verifies that when
// desired set is empty (e.g., during deletion), all owned resources
// are deleted.
func TestOrphanCleanup_EmptyDesiredSet_DeletesAll(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}

	// Create multiple resources owned by this Yanet
	dep1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dep-1", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	dep2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dep-2", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}

	r, _ := makeReconcilerEnv(t, yanet, dep1, dep2, svc)

	// Empty desired set (simulates deletion scenario)
	desired := newDesiredSet()

	// Prune with autoSync=true
	count, err := r.pruneOrphans(context.Background(), yanet, desired, true, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Verify all resources were deleted
	if count != 3 {
		t.Errorf("expected 3 deletions (2 deps + 1 svc), got %d", count)
	}

	// Verify resources are gone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "dep-1", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("dep-1 must be deleted")
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "dep-2", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("dep-2 must be deleted")
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "svc", Namespace: "yanet"}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Errorf("svc must be deleted")
	}
}

// TestOrphanCleanup_AutoSyncFalse_OnlyCounts verifies that when
// autoSync=false, pruneOrphans only counts orphans but doesn't delete them.
func TestOrphanCleanup_AutoSyncFalse_OnlyCounts(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}

	orphan := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan", Namespace: "yanet",
			Labels: map[string]string{manifests.LabelYanet: "y"},
		},
	}

	r, _ := makeReconcilerEnv(t, yanet, orphan)

	// Empty desired set
	desired := newDesiredSet()

	// Prune with autoSync=false
	count, err := r.pruneOrphans(context.Background(), yanet, desired, false, silentLogger())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Verify orphan was counted
	if count != 1 {
		t.Errorf("expected 1 orphan counted, got %d", count)
	}

	// Verify orphan was NOT deleted
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "orphan", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("orphan must remain when autoSync=false: %v", err)
	}
}
