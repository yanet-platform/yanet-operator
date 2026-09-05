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
	"strings"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
			},
			Dataplane: yanetv2alpha1.DataplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
			},
		},
		Patches: []yanetv2alpha1.NamedPatch{
			{Name: "telegraf"}, // dry-run not used in reconciler
		},
		BoxTypes: []yanetv2alpha1.BoxType{{
			Name: "release",
			Components: yanetv2alpha1.BoxComponents{
				Controlplane: &yanetv2alpha1.BoxComponent{},
				Dataplane:    &yanetv2alpha1.BoxDataplane{},
			},
		}},
	}
}

func serviceCollisionConfigV2() yanetv2alpha1.YanetConfigSpec {
	cfg := minimalConfigV2()
	cfg.Components.Operators = []yanetv2alpha1.OperatorSpec{{
		Name: "controlplane-numa0",
		Containers: []yanetv2alpha1.OperatorContainer{{
			Name: "operator", Image: yanetv2alpha1.ImageRef{Name: "operator", Tag: "v1"},
		}},
	}}
	cfg.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"controlplane-numa0": {}}
	return cfg
}

func TestReconcileV2_ServicePlanCollisionFailsBeforeApplyAndClearsReady(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetV2"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"role": "yanet"}, AutoSync: &autoSync,
		},
		Status: yanetv2alpha1.YanetStatus{Conditions: []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "AllChecksPassed",
		}}},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"role": "yanet"}}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = serviceCollisionConfigV2()

	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "conflicting Service plans") {
		t.Fatalf("expected Service plan collision, got result=%+v err=%v", result, err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("preflight collision must prevent all Deployment changes, got %d", len(deployments.Items))
	}
	services := &corev1.ServiceList{}
	if err := r.Client.List(context.Background(), services, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Services: %v", err)
	}
	if len(services.Items) != 0 {
		t.Fatalf("preflight collision must prevent both Service plans, got %d", len(services.Items))
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get YanetV2: %v", err)
	}
	conditions := make(map[string]metav1.Condition, len(got.Status.Conditions))
	for _, condition := range got.Status.Conditions {
		conditions[condition.Type] = condition
	}
	if condition := conditions["Degraded"]; condition.Status != metav1.ConditionTrue || condition.Reason != "ResourcePreflightFailed" {
		t.Errorf("unexpected Degraded condition: %+v", condition)
	}
	if condition := conditions["Ready"]; condition.Status != metav1.ConditionFalse {
		t.Errorf("Ready must be false after preflight failure: %+v", condition)
	}
}

func TestReconcileV2_RevalidatesComponentOverrides(t *testing.T) {
	autoSync := true
	disabled := false
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType:  "release",
			AutoSync: &autoSync,
			Components: &yanetv2alpha1.YanetComponentsOverride{
				Dataplane: &yanetv2alpha1.YanetComponentOverride{
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						yanetv2alpha1.DataplaneContainerName: {Enabled: &disabled},
					},
				},
			},
		},
	}
	r, snapshot := makeReconcilerEnv(t, yanet)
	snapshot.Config = minimalConfigV2()

	result, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("invalid persisted override result=%+v err=%v", result, err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("invalid override must fail before applying Deployments: %+v", deployments.Items)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Get(
		context.Background(),
		types.NamespacedName{Name: yanet.Name, Namespace: yanet.Namespace},
		got,
	); err != nil {
		t.Fatalf("get YanetV2: %v", err)
	}
	for _, condition := range got.Status.Conditions {
		if condition.Type == "Degraded" && condition.Reason == "OverridesInvalid" {
			return
		}
	}
	t.Fatalf("OverridesInvalid condition not found: %+v", got.Status.Conditions)
}

func TestReconcileV2_InvalidPatchFailsBeforeApply(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"role": "yanet"}, AutoSync: &autoSync,
		},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"role": "yanet"}}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Patches = []yanetv2alpha1.NamedPatch{{
		Name: "invalid", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"replicas":"not-an-integer"}}`)},
	}}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"invalid"}

	_, err := r.reconcileYanetV2(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "decode merged Deployment") {
		t.Fatalf("expected invalid patch preflight error, got %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deployments, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("invalid patch preflight must prevent partial rollout, got %d Deployments", len(deployments.Items))
	}
}

func TestReconcileV2_CrossListContainerNameCollisionFailsBeforeApply(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"role": "yanet"}, AutoSync: &autoSync,
		},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"role": "yanet"}}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Dataplane.Sidecars = &yanetv2alpha1.DataplaneSidecarsSpec{
		Bird: &yanetv2alpha1.DataplaneSidecarSpec{
			Image: yanetv2alpha1.ImageRef{Name: "bird", Tag: "v1"},
		},
	}
	snapshot.Config.Patches = []yanetv2alpha1.NamedPatch{{
		Name: "regular-bird", Patch: runtime.RawExtension{Raw: []byte(
			`{"spec":{"template":{"spec":{"containers":[{"name":"bird","image":"bird:v1"}]}}}}`,
		)},
	}}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Sidecars = &yanetv2alpha1.BoxDataplaneSidecars{
		Bird: &yanetv2alpha1.BoxDataplaneSidecar{},
	}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"regular-bird"}

	_, err := r.reconcileYanetV2(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "bird") || !strings.Contains(err.Error(), "init") {
		t.Fatalf("expected cross-list container name collision, got %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("container-name preflight must prevent partial rollout, got %d Deployments", len(deployments.Items))
	}
}

func TestReconcileV2_HostNetworkListenerWithoutRangeFailsBeforeApply(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"role": "yanet"}, AutoSync: &autoSync,
		},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"role": "yanet"}}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Operators = []yanetv2alpha1.OperatorSpec{{
		Name: "route",
		Containers: []yanetv2alpha1.OperatorContainer{{
			Name: "route", Image: yanetv2alpha1.ImageRef{Name: "route", Tag: "v1"},
		}},
	}}
	snapshot.Config.Patches = []yanetv2alpha1.NamedPatch{{
		Name: "host-network", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"hostNetwork":true}}}}`)},
	}}
	snapshot.Config.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{
		"route": {Patches: []string{"host-network"}},
	}

	_, err := r.reconcileYanetV2(context.Background(), yanet)
	if err == nil || !strings.Contains(err.Error(), "hostNetworkPortRange is not configured") {
		t.Fatalf("expected missing host-network range error, got %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.Client.List(context.Background(), deployments, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("host-port preflight must prevent partial rollout, got %d Deployments", len(deployments.Items))
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
	if err != nil || res != (ctrl.Result{}) {
		t.Errorf("global stop must short-circuit: %+v %v", res, err)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(yanet), got); err != nil {
		t.Fatalf("get YanetV2: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, yanetFinalizer) {
		t.Fatal("global stop must not install a finalizer")
	}
}

func TestReconcileV2_GlobalStopLoadedFromAPIBeforeSnapshot(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
		Spec:       minimalConfigV2(),
	}
	config.Spec.Stop = true
	r, _ := makeReconcilerEnv(t, yanet, config)

	res, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil || res != (ctrl.Result{}) {
		t.Fatalf("persisted global stop must short-circuit before snapshot startup: %+v %v", res, err)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(yanet), got); err != nil {
		t.Fatalf("get YanetV2: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, yanetFinalizer) {
		t.Fatal("persisted global stop must prevent finalizer installation before snapshot startup")
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

func TestReconcileV2_AutoSyncOn_CreatesDeploymentsAndReportsSharedServices(t *testing.T) {
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
	if len(svcs.Items) != 0 {
		t.Errorf("YanetV2 reconciler must not own shared Services, got %d", len(svcs.Items))
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
// any Deployment or ConfigMap it had previously created from
// this YanetV2 CR. The user is expected to be able to manually mutate
// them (and even delete some of them via orphan-prune skip) without
// the operator fighting back.
//
// Coverage matrix (autoSync=false):
//   - Deployment.Spec hand-edit          → not reverted   (line A)
//   - ConfigMap.Data hand-edit           → not reverted   (line B)
//   - Orphan Deployment left in place    → not deleted    (line C, also covered by TestPruneOrphans_AutoSyncFalse_DoesNotDelete)
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
	testContext := context.Background()

	// Phase 1: autoSync=true creates the resources from scratch.
	if _, err := r.reconcileYanetV2(testContext, yanet); err != nil {
		t.Fatalf("phase1 finalizer install: %v", err)
	}
	if err := r.Client.Get(testContext, types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("phase1 re-get: %v", err)
	}
	if _, err := r.reconcileYanetV2(testContext, yanet); err != nil {
		t.Fatalf("phase1 reconcile: %v", err)
	}

	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(testContext, deps, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list deps: %v", err)
	}
	if len(deps.Items) == 0 {
		t.Fatalf("phase1: expected deployments to be created")
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
	if err := r.Client.Update(testContext, targetDep); err != nil {
		t.Fatalf("hand-edit deployment: %v", err)
	}
	depKey := types.NamespacedName{Name: targetDep.Name, Namespace: targetDep.Namespace}

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
	if err := r.Client.Create(testContext, staleCM); err != nil {
		t.Fatalf("seed stale CM: %v", err)
	}
	cmKey := types.NamespacedName{Name: staleCM.Name, Namespace: staleCM.Namespace}

	// Phase 2: flip autoSync to false. The reconciler must observe
	// drift but must NOT push the hand edits back.
	if err := r.Client.Get(testContext, types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("phase2 re-get yanet: %v", err)
	}
	off := false
	yanet.Spec.AutoSync = &off
	if err := r.Client.Update(testContext, yanet); err != nil {
		t.Fatalf("phase2 disable autoSync: %v", err)
	}
	if _, err := r.reconcileYanetV2(testContext, yanet); err != nil {
		t.Fatalf("phase2 reconcile: %v", err)
	}

	// Assert line A: hand-edited Deployment is untouched.
	gotDep := &appsv1.Deployment{}
	if err := r.Client.Get(testContext, depKey, gotDep); err != nil {
		t.Fatalf("re-get deployment: %v", err)
	}
	if gotDep.Spec.Replicas == nil || *gotDep.Spec.Replicas != handEditedReplicas {
		t.Errorf("autoSync=false MUST preserve hand-edited replicas, got %v (want %d)",
			gotDep.Spec.Replicas, handEditedReplicas)
	}
	if gotDep.Labels["operator.example.com/owned-by-human"] != "yes" {
		t.Errorf("autoSync=false MUST preserve foreign labels on Deployment, got %v", gotDep.Labels)
	}

	// Assert line B/C: pre-existing CM is left alone (content
	// preserved AND object not garbage-collected by prune).
	gotCM := &corev1.ConfigMap{}
	if err := r.Client.Get(testContext, cmKey, gotCM); err != nil {
		t.Fatalf("autoSync=false MUST NOT delete pre-existing CM, got err=%v", err)
	}
	if gotCM.Data["config"] != "human-managed content" {
		t.Errorf("autoSync=false MUST preserve CM content, got %q", gotCM.Data["config"])
	}
}
