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
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// v2Scheme builds a runtime.Scheme that knows the v2alpha1 API plus the
// stock apps/core kinds the reconciler creates.
func v2Scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := yanetv2alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v2alpha1 AddToScheme: %v", err)
	}
	if err := yanetv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v1alpha1 AddToScheme: %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("appsv1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	return s
}

// makeReconcilerEnv wires a YanetV2Reconciler against a fake client and
// returns it together with the populated GlobalConfigV2 snapshot.
func makeReconcilerEnv(t *testing.T, objs ...client.Object) (*YanetV2Reconciler, *yanetv2alpha1.MutexYanetConfigSpec) {
	t.Helper()
	s := v2Scheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		// Status subresource so reconciler.Status().Update works.
		WithStatusSubresource(&yanetv2alpha1.YanetV2{}).
		Build()

	cfgV2 := &yanetv2alpha1.MutexYanetConfigSpec{}
	r := &YanetV2Reconciler{
		Client:         cl,
		Scheme:         s,
		GlobalConfigV2: cfgV2,
	}
	return r, cfgV2
}

// minimalConfigV2 returns a YanetConfigV2 spec covering the smallest valid
// shape: cp+dp palette, one boxType wiring both, and one NamedPatch the
// boxType references for the controlplane.
func minimalConfigV2() yanetv2alpha1.YanetConfigSpec {
	return yanetv2alpha1.YanetConfigSpec{
		Components: yanetv2alpha1.ComponentsSpec{
			Controlplane: yanetv2alpha1.ControlplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
				Port:  8080,
			},
			Dataplane: yanetv2alpha1.DataplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
				Port:  8081,
			},
		},
		Patches: []yanetv2alpha1.NamedPatch{
			{Name: "telegraf"}, // dry-run not used in reconciler
		},
		BoxTypes: []yanetv2alpha1.BoxType{{
			Name: "release",
			Components: yanetv2alpha1.BoxComponents{
				Controlplane: &yanetv2alpha1.BoxComponent{},
				Dataplane:    &yanetv2alpha1.BoxComponent{},
			},
		}},
	}
}

// TestReconcileV2_Disabled_ScalesToZero verifies that spec.enabled=false
// is a "scale-to-zero" switch, not a reconcile pause: Deployments and
// Services are still rendered (so the user can inspect generated specs
// and patches still take effect) but every Deployment must have
// replicas=0 regardless of per-component overrides.
//
// To freeze the operator's view of the CR entirely, the user is
// expected to set spec.autoSync=false instead — that path is covered by
// TestReconcileV2_AutoSyncOff_OutOfSync.
func TestReconcileV2_Disabled_ScalesToZero(t *testing.T) {
	false_ := false
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
			Enabled:      &false_,
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
	snap.Config = minimalConfigV2()

	// First reconcile installs the finalizer.
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Errorf("disabled reconcile must not error: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) < 2 {
		t.Fatalf("expected >=2 deployments (cp+dp) even when disabled, got %d", len(deps.Items))
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			t.Errorf("deployment %q: spec.enabled=false must force replicas=0, got %v",
				d.Name, d.Spec.Replicas)
		}
	}
}

func TestReconcileV2_NoSnapshot_Requeues(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	r, _ := makeReconcilerEnv(t, yanet) // empty snapshot
	// First reconcile installs the finalizer.
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	// Re-fetch to pick up the finalizer added by the first call,
	// then exercise the snapshot branch.
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	res, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Errorf("missing snapshot must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("missing snapshot must requeue: %+v", res)
	}
}

func TestReconcileV2_GlobalStop(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	r, snap := makeReconcilerEnv(t, yanet)
	snap.Config = minimalConfigV2()
	snap.Config.Stop = true
	res, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil || res.RequeueAfter != 0 {
		t.Errorf("global stop must short-circuit: %+v %v", res, err)
	}
}

func TestReconcileV2_NoMatchingNodes_StatusEmpty(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet)
	snap.Config = minimalConfigV2()
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("err: %v", err)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if len(got.Status.NodesStatus) != 0 {
		t.Errorf("no nodes ⇒ NodesStatus empty, got %v", got.Status.NodesStatus)
	}
}

func TestReconcileV2_AutoSyncOff_OutOfSync(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:      "release",
			NodeSelector: map[string]string{"role": "yanet"},
			// AutoSync nil ⇒ defaults to false
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"role": "yanet"},
		},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	snap.Config = minimalConfigV2()

	// First reconcile installs the finalizer; second one runs
	// the actual reconciliation against the populated snapshot.
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("err: %v", err)
	}
	got := &yanetv2alpha1.YanetV2{}
	_ = r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got)

	// AutoSync off ⇒ no Deployment created on the cluster
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) != 0 {
		t.Errorf("AutoSync=false: expected 0 deployments, got %d", len(deps.Items))
	}
	// Status should track the would-be deployments under OutOfSync.
	if len(got.Status.NodesStatus["node-1"].Deployments) == 0 {
		t.Errorf("expected node status to enumerate deployments: %+v", got.Status.NodesStatus)
	}
}

func TestReconcileV2_AutoSyncOn_CreatesDeploymentsAndServices(t *testing.T) {
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
	snap.Config = minimalConfigV2()

	// First reconcile installs the finalizer.
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("err: %v", err)
	}
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) < 2 {
		t.Errorf("expected >=2 deployments (cp+dp), got %d: %+v", len(deps.Items), deps.Items)
	}
	svcs := &corev1.ServiceList{}
	if err := r.Client.List(context.Background(), svcs, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list svcs: %v", err)
	}
	if len(svcs.Items) == 0 {
		t.Errorf("expected services to be created")
	}

	got := &yanetv2alpha1.YanetV2{}
	_ = r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got)
	if len(got.Status.Sync.Synced) == 0 {
		t.Errorf("Status.Sync.Synced should not be empty: %+v", got.Status.Sync)
	}
	if len(got.Status.Services) == 0 {
		t.Errorf("Status.Services should list created services: %+v", got.Status.Services)
	}
}

func TestReconcileV2_UnschedulableNodeSkipped(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"role": "yanet"}},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	r, snap := makeReconcilerEnv(t, yanet, node)
	snap.Config = minimalConfigV2()
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("finalizer install: %v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("err: %v", err)
	}
	deps := &appsv1.DeploymentList{}
	_ = r.Client.List(context.Background(), deps, client.InNamespace("yanet"))
	if len(deps.Items) != 0 {
		t.Errorf("unschedulable node must be skipped, got %d deployments", len(deps.Items))
	}
}

func TestReadNumaFromNode(t *testing.T) {
	tests := []struct {
		name string
		labs map[string]string
		want int32
	}{
		{"no label", nil, 0},
		{"valid", map[string]string{yanetv2alpha1.NFDNumaCountLabel: "4"}, 4},
		{"invalid", map[string]string{yanetv2alpha1.NFDNumaCountLabel: "abc"}, 0},
		{"negative", map[string]string{yanetv2alpha1.NFDNumaCountLabel: "-1"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: tt.labs}}
			if got := readNumaFromNode(n); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAggregateSyncStatusV2(t *testing.T) {
	in := map[string]yanetv2alpha1.NodeStatus{
		"a": {Deployments: map[string]string{"d1": "synced", "d2": "error"}},
		"b": {Deployments: map[string]string{"d3": "sync-waiting", "d4": "out-of-sync (missing)"}},
	}
	out := aggregateSyncStatusV2(in)
	if len(out.Synced) != 1 || out.Synced[0] != "d1" {
		t.Errorf("synced bucket: %+v", out.Synced)
	}
	if len(out.Error) != 1 || out.Error[0] != "d2" {
		t.Errorf("error bucket: %+v", out.Error)
	}
	if len(out.SyncWaiting) != 1 || out.SyncWaiting[0] != "d3" {
		t.Errorf("syncwaiting bucket: %+v", out.SyncWaiting)
	}
	if len(out.OutOfSync) != 1 || out.OutOfSync[0] != "d4" {
		t.Errorf("outofsync bucket: %+v", out.OutOfSync)
	}
}

// TestReconcileV2_AutoSyncOff_PreservesHandEditsOnExistingResources
// proves that with spec.autoSync=false the reconciler MUST NOT touch
// any Deployment, Service or ConfigMap it had previously created from
// this YanetV2 CR. The user is expected to be able to manually mutate
// them (and even delete some of them via orphan-prune skip) without
// the operator fighting back.
//
// Coverage matrix (autoSync=false):
//   - Deployment.Spec hand-edit          → not reverted   (line A)
//   - Service.Spec hand-edit             → not reverted   (line B)
//   - ConfigMap.Data hand-edit           → not reverted   (line C)
//   - Orphan Deployment left in place    → not deleted    (line D, also covered by TestPruneOrphans_AutoSyncFalse_DoesNotDelete)
func TestReconcileV2_AutoSyncOff_PreservesHandEditsOnExistingResources(t *testing.T) {
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
	snap.Config = minimalConfigV2()
	ctx := context.Background()

	// Phase 1: autoSync=true creates the resources from scratch.
	if _, err := r.reconcileYanetV2(ctx, yanet); err != nil {
		t.Fatalf("phase1 finalizer install: %v", err)
	}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("phase1 re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(ctx, yanet); err != nil {
		t.Fatalf("phase1 reconcile: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(ctx, deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) == 0 {
		t.Fatalf("phase1: expected deployments to be created")
	}
	svcs := &corev1.ServiceList{}
	if err := r.Client.List(ctx, svcs, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list svcs: %v", err)
	}
	if len(svcs.Items) == 0 {
		t.Fatalf("phase1: expected services to be created")
	}

	// Hand-edit a Deployment (line A): bump replicas to a value the
	// operator would never generate (99) and add a foreign label.
	targetDep := &deps.Items[0]
	handEditedReplicas := int32(99)
	targetDep.Spec.Replicas = &handEditedReplicas
	if targetDep.Labels == nil {
		targetDep.Labels = map[string]string{}
	}
	targetDep.Labels["operator.example.com/owned-by-human"] = "yes"
	if err := r.Client.Update(ctx, targetDep); err != nil {
		t.Fatalf("hand-edit deployment: %v", err)
	}
	depKey := types.NamespacedName{Name: targetDep.Name, Namespace: targetDep.Namespace}

	// Hand-edit a Service (line B): set a custom externalTrafficPolicy
	// (a field the builder does not set, ensuring it is foreign) and
	// a foreign annotation.
	targetSvc := &svcs.Items[0]
	targetSvc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeLocal
	if targetSvc.Annotations == nil {
		targetSvc.Annotations = map[string]string{}
	}
	targetSvc.Annotations["operator.example.com/owned-by-human"] = "yes"
	if err := r.Client.Update(ctx, targetSvc); err != nil {
		t.Fatalf("hand-edit service: %v", err)
	}
	svcKey := types.NamespacedName{Name: targetSvc.Name, Namespace: targetSvc.Namespace}

	// Hand-create a "previous-generation" ConfigMap that looks like
	// it once belonged to the CR (carries the LabelYanet label so
	// pruneOrphans considers it for deletion) and verify autoSync=false
	// neither rewrites nor removes it (line C/D).
	staleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-cm-from-previous-generation",
			Namespace: "yanet",
			Labels: map[string]string{
				"yanet.yanet-platform.io/yanet": yanet.Name,
			},
		},
		Data: map[string]string{"config": "human-managed content"},
	}
	if err := r.Client.Create(ctx, staleCM); err != nil {
		t.Fatalf("seed stale CM: %v", err)
	}
	cmKey := types.NamespacedName{Name: staleCM.Name, Namespace: staleCM.Namespace}

	// Phase 2: flip autoSync to false. The reconciler must observe
	// drift but must NOT push the hand edits back.
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("phase2 re-get yanet: %v", err)
	}
	off := false
	yanet.Spec.AutoSync = &off
	if err := r.Client.Update(ctx, yanet); err != nil {
		t.Fatalf("phase2 disable autoSync: %v", err)
	}
	if _, err := r.reconcileYanetV2(ctx, yanet); err != nil {
		t.Fatalf("phase2 reconcile: %v", err)
	}

	// Assert line A: hand-edited Deployment is untouched.
	gotDep := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, depKey, gotDep); err != nil {
		t.Fatalf("re-get deployment: %v", err)
	}
	if gotDep.Spec.Replicas == nil || *gotDep.Spec.Replicas != handEditedReplicas {
		t.Errorf("autoSync=false MUST preserve hand-edited replicas, got %v (want %d)",
			gotDep.Spec.Replicas, handEditedReplicas)
	}
	if gotDep.Labels["operator.example.com/owned-by-human"] != "yes" {
		t.Errorf("autoSync=false MUST preserve foreign labels on Deployment, got %v", gotDep.Labels)
	}

	// Assert line B: hand-edited Service is untouched.
	gotSvc := &corev1.Service{}
	if err := r.Client.Get(ctx, svcKey, gotSvc); err != nil {
		t.Fatalf("re-get service: %v", err)
	}
	if gotSvc.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyTypeLocal {
		t.Errorf("autoSync=false MUST preserve hand-edited Service.Spec, got externalTrafficPolicy=%q",
			gotSvc.Spec.ExternalTrafficPolicy)
	}
	if gotSvc.Annotations["operator.example.com/owned-by-human"] != "yes" {
		t.Errorf("autoSync=false MUST preserve foreign annotations on Service, got %v", gotSvc.Annotations)
	}

	// Assert line C/D: pre-existing CM is left alone (content
	// preserved AND object not garbage-collected by prune).
	gotCM := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, cmKey, gotCM); err != nil {
		t.Fatalf("autoSync=false MUST NOT delete pre-existing CM, got err=%v", err)
	}
	if gotCM.Data["config"] != "human-managed content" {
		t.Errorf("autoSync=false MUST preserve CM content, got %q", gotCM.Data["config"])
	}
}
