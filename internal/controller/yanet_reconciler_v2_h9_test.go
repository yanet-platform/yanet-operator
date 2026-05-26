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
	"sync/atomic"
	"testing"
	"time"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ---------------------------------------------------------------------------
// F23: updateStatusV2 — RetryOnConflict on Status().Update
// ---------------------------------------------------------------------------

// TestUpdateStatusV2_RetriesOnConflict verifies that a transient 409
// Conflict from Status().Update is retried with a fresh Get and the
// final write succeeds with the latest ResourceVersion.
func TestUpdateStatusV2_RetriesOnConflict(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", ResourceVersion: "1",
		},
	}
	s := newSchemeForTest(t)

	var statusUpdates int32
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(yanet).
		WithStatusSubresource(&yanetv2alpha1.YanetV2{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(
				ctx context.Context,
				c client.Client,
				subResourceName string,
				obj client.Object,
				opts ...client.SubResourceUpdateOption,
			) error {
				if subResourceName != "status" {
					return c.Status().Update(ctx, obj, opts...)
				}
				if atomic.AddInt32(&statusUpdates, 1) == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "yanet.yanet-platform.io", Resource: "yanets"},
						obj.GetName(),
						errConflict("first attempt"),
					)
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()

	r := &YanetV2Reconciler{
		Client:         cl,
		Scheme:         s,
		GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
	}

	mutateCalls := 0
	err := r.updateStatusV2(context.Background(), yanet, func(fresh *yanetv2alpha1.YanetV2) {
		mutateCalls++
		fresh.Status.Services = []string{"svc"}
	})
	if err != nil {
		t.Fatalf("updateStatusV2: %v", err)
	}
	if got := atomic.LoadInt32(&statusUpdates); got != 2 {
		t.Errorf("expected 2 status update attempts (1 conflict + 1 success), got %d", got)
	}
	if mutateCalls != 2 {
		t.Errorf("mutate must be invoked once per attempt, got %d", mutateCalls)
	}
	// Verify the final write landed.
	got := &yanetv2alpha1.YanetV2{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Services) != 1 || got.Status.Services[0] != "svc" {
		t.Errorf("status not persisted, got Services=%v", got.Status.Services)
	}
	if yanet.Status.Services == nil {
		t.Errorf("local Status not synced from server response")
	}
}

// errConflict is a tiny helper because k8s NewConflict expects a
// concrete error and we don't want to import fmt for one line.
type errConflict string

func (e errConflict) Error() string { return string(e) }

// newSchemeForTest builds the scheme used by the F23 tests.
func newSchemeForTest(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := yanetv2alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v2 scheme: %v", err)
	}
	if err := yanetv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("v1 scheme: %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("apps: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("core: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// mergeManagedKV — table-driven
// ---------------------------------------------------------------------------

func TestMergeManagedKV_TableDriven(t *testing.T) {
	cases := []struct {
		name        string
		existing    map[string]string
		desired     map[string]string
		prevManaged []string
		want        map[string]string
	}{
		{
			name: "nil_nil_returns_nil",
			want: nil,
		},
		{
			name:     "empty_empty_returns_nil",
			existing: map[string]string{},
			desired:  map[string]string{},
			want:     nil,
		},
		{
			name:     "disjoint_keys_kept",
			existing: map[string]string{"a": "1"},
			desired:  map[string]string{"b": "2"},
			want:     map[string]string{"a": "1", "b": "2"},
		},
		{
			name:     "desired_overrides_existing",
			existing: map[string]string{"a": "old"},
			desired:  map[string]string{"a": "new"},
			want:     map[string]string{"a": "new"},
		},
		{
			name:     "foreign_keys_preserved",
			existing: map[string]string{"sidecar.istio.io/inject": "true", "owned": "old"},
			desired:  map[string]string{"owned": "new"},
			want:     map[string]string{"sidecar.istio.io/inject": "true", "owned": "new"},
		},
		{
			name:     "empty_desired_keeps_existing_when_no_prev",
			existing: map[string]string{"a": "1"},
			want:     map[string]string{"a": "1"},
		},
		{
			name:        "prev_managed_key_removed_when_retracted",
			existing:    map[string]string{"a": "1", "b": "2", "foreign": "z"},
			desired:     map[string]string{"a": "1"},
			prevManaged: []string{"a", "b"},
			want:        map[string]string{"a": "1", "foreign": "z"},
		},
		{
			name:        "prev_managed_kept_when_still_desired",
			existing:    map[string]string{"a": "old", "b": "2"},
			desired:     map[string]string{"a": "new", "b": "2"},
			prevManaged: []string{"a", "b"},
			want:        map[string]string{"a": "new", "b": "2"},
		},
		{
			name:        "foreign_kept_even_when_prev_listed_other_keys",
			existing:    map[string]string{"a": "1", "sidecar.istio.io/inject": "true"},
			desired:     map[string]string{},
			prevManaged: []string{"a"},
			want:        map[string]string{"sidecar.istio.io/inject": "true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeManagedKV(tc.existing, tc.desired, tc.prevManaged)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %q want %q", k, got[k], v)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// mergeManagedMeta — tracking-annotation flow
// ---------------------------------------------------------------------------

func TestMergeManagedMeta_FirstReconcile(t *testing.T) {
	fresh := &metav1.ObjectMeta{}
	desired := &metav1.ObjectMeta{
		Labels:      map[string]string{"a": "1", "b": "2"},
		Annotations: map[string]string{"x": "X"},
	}
	mergeManagedMeta(fresh, desired)

	if fresh.Labels["a"] != "1" || fresh.Labels["b"] != "2" {
		t.Errorf("labels not applied: %v", fresh.Labels)
	}
	if fresh.Annotations["x"] != "X" {
		t.Errorf("annotation not applied: %v", fresh.Annotations)
	}
	if got := fresh.Annotations[manifests.AnnotationManagedLabels]; got != "a,b" {
		t.Errorf("managed-labels tracker: got %q want %q", got, "a,b")
	}
	if got := fresh.Annotations[manifests.AnnotationManagedAnnotations]; got != "x" {
		t.Errorf("managed-annotations tracker: got %q want %q", got, "x")
	}
}

func TestMergeManagedMeta_RetractedKeyRemoved(t *testing.T) {
	fresh := &metav1.ObjectMeta{
		Labels: map[string]string{"a": "1", "b": "2", "sidecar.istio.io/inject": "true"},
		Annotations: map[string]string{
			manifests.AnnotationManagedLabels: "a,b",
		},
	}
	desired := &metav1.ObjectMeta{
		Labels: map[string]string{"a": "1"},
	}
	mergeManagedMeta(fresh, desired)

	if _, ok := fresh.Labels["b"]; ok {
		t.Errorf("retracted label %q must be removed: %v", "b", fresh.Labels)
	}
	if fresh.Labels["a"] != "1" {
		t.Errorf("kept label dropped: %v", fresh.Labels)
	}
	if fresh.Labels["sidecar.istio.io/inject"] != "true" {
		t.Errorf("foreign label dropped: %v", fresh.Labels)
	}
	if got := fresh.Annotations[manifests.AnnotationManagedLabels]; got != "a" {
		t.Errorf("tracker not refreshed: got %q want %q", got, "a")
	}
}

func TestMergeManagedMeta_AllKeysRetracted_TrackerCleared(t *testing.T) {
	fresh := &metav1.ObjectMeta{
		Labels: map[string]string{"a": "1", "foreign": "z"},
		Annotations: map[string]string{
			manifests.AnnotationManagedLabels: "a",
		},
	}
	desired := &metav1.ObjectMeta{}
	mergeManagedMeta(fresh, desired)

	if _, ok := fresh.Labels["a"]; ok {
		t.Errorf("retracted label must be removed: %v", fresh.Labels)
	}
	if fresh.Labels["foreign"] != "z" {
		t.Errorf("foreign label must survive: %v", fresh.Labels)
	}
	if _, ok := fresh.Annotations[manifests.AnnotationManagedLabels]; ok {
		t.Errorf("tracker must be cleared when desired has no labels: %v", fresh.Annotations)
	}
}

func TestMergeManagedMeta_PreTrackingResource_NoRemoval(t *testing.T) {
	// Resource created before tracking annotations existed: prev is
	// empty, so no key is removed even if it disappears from desired.
	fresh := &metav1.ObjectMeta{
		Labels: map[string]string{"a": "1", "b": "2"},
	}
	desired := &metav1.ObjectMeta{
		Labels: map[string]string{"a": "1"},
	}
	mergeManagedMeta(fresh, desired)

	if fresh.Labels["b"] != "2" {
		t.Errorf("pre-tracking key must NOT be removed on first reconcile: %v", fresh.Labels)
	}
	if got := fresh.Annotations[manifests.AnnotationManagedLabels]; got != "a" {
		t.Errorf("tracker should now reflect current desired set: got %q want %q", got, "a")
	}
}

func TestMergeManagedMeta_DeterministicTrackerOrder(t *testing.T) {
	desired := &metav1.ObjectMeta{
		Labels: map[string]string{"z": "Z", "a": "A", "m": "M"},
	}
	fresh := &metav1.ObjectMeta{}
	mergeManagedMeta(fresh, desired)
	if got := fresh.Annotations[manifests.AnnotationManagedLabels]; got != "a,m,z" {
		t.Errorf("tracker must be sorted: got %q want %q", got, "a,m,z")
	}
}

// ---------------------------------------------------------------------------
// F23: applyDeploymentV2 / applyServiceV2 RetryOnConflict + label merge (R9+R10)
// ---------------------------------------------------------------------------

// TestApplyDeploymentV2_RetriesConflictAndMergesLabels: the first
// Update returns 409 Conflict; the retry must succeed AND foreign
// labels (e.g. istio sidecar injection) must survive the apply.
func TestApplyDeploymentV2_RetriesConflictAndMergesLabels(t *testing.T) {
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp", Namespace: "yanet", ResourceVersion: "10",
			Labels: map[string]string{
				"sidecar.istio.io/inject": "true",
				manifests.LabelYanet:      "y",
				"v":                       "old",
			},
		},
	}
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp", Namespace: "yanet",
			Labels: map[string]string{
				manifests.LabelYanet: "y",
				"v":                  "new",
			},
		},
	}

	s := newSchemeForTest(t)
	var updates int32
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if atomic.AddInt32(&updates, 1) == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "apps", Resource: "deployments"},
						obj.GetName(),
						errConflict("first attempt"),
					)
				}
				return c.Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &YanetV2Reconciler{Client: cl, Scheme: s}

	state, requeue := r.applyDeploymentV2(
		context.Background(),
		desired.DeepCopy(),
		true, // autoSync
		0,    // updateWindow disabled
		"node-A",
		silentLogger(),
	)
	if state != "synced" {
		t.Errorf("state=%q want synced (requeue=%v)", state, requeue)
	}
	if got := atomic.LoadInt32(&updates); got != 2 {
		t.Errorf("expected 2 update attempts (1 conflict + 1 success), got %d", got)
	}
	got := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cp", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Labels["v"] != "new" {
		t.Errorf("desired label not applied: %v", got.Labels)
	}
	if got.Labels["sidecar.istio.io/inject"] != "true" {
		t.Errorf("foreign label dropped (R9 regression): %v", got.Labels)
	}
}

// TestApplyServiceV2_RefusesEmptyPorts ensures the R15 guard prevents
// wiping ports of an existing Service when the builder accidentally
// returns an empty Ports slice.
func TestApplyServiceV2_RefusesEmptyPorts(t *testing.T) {
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "yanet"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{manifests.LabelYanet: "y"},
			Ports: []corev1.ServicePort{
				{Name: "p1", Port: 80},
				{Name: "p2", Port: 443},
			},
		},
	}
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "yanet"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{manifests.LabelYanet: "y"},
			Ports:    nil, // bug from builder
		},
	}
	s := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(existing).Build()
	rec := events.NewFakeRecorder(8)
	r := &YanetV2Reconciler{Client: cl, Scheme: s, Recorder: rec}

	if err := r.applyServiceV2(context.Background(), desired, true, silentLogger()); err != nil {
		t.Fatalf("applyServiceV2 must not bubble error: %v", err)
	}
	got := &corev1.Service{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "svc", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Spec.Ports) != 2 {
		t.Errorf("existing Service must keep its 2 Ports, got %d (%+v)", len(got.Spec.Ports), got.Spec.Ports)
	}
	// Confirm a Warning event was emitted.
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "ServiceInvalid") {
			t.Errorf("expected ServiceInvalid event, got %q", e)
		}
	default:
		t.Errorf("expected at least one event")
	}
}

// TestApplyServiceV2_RefusesEmptySelector mirrors the above but for
// the Selector guard.
func TestApplyServiceV2_RefusesEmptySelector(t *testing.T) {
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "yanet"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{manifests.LabelYanet: "y"},
			Ports:    []corev1.ServicePort{{Port: 80}},
		},
	}
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "yanet"},
		Spec: corev1.ServiceSpec{
			Ports:    []corev1.ServicePort{{Port: 80}},
			Selector: nil,
		},
	}
	s := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(existing).Build()
	rec := events.NewFakeRecorder(8)
	r := &YanetV2Reconciler{Client: cl, Scheme: s, Recorder: rec}

	if err := r.applyServiceV2(context.Background(), desired, true, silentLogger()); err != nil {
		t.Fatalf("applyServiceV2 must not bubble error: %v", err)
	}
	got := &corev1.Service{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "svc", Namespace: "yanet"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Spec.Selector) != 1 {
		t.Errorf("existing Selector must remain, got %v", got.Spec.Selector)
	}
}

// ---------------------------------------------------------------------------
// F23: Recorder events from the reconcile path (R16)
// ---------------------------------------------------------------------------

// TestReconcileV2_EmitsConfigNotLoadedEvent: when the snapshot is
// empty, reconcile must record a ConfigNotLoaded warning and back off.
func TestReconcileV2_EmitsConfigNotLoadedEvent(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet",
			Finalizers: []string{yanetFinalizer}, // skip add-finalizer requeue
		},
		Spec: yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	r, _ := makeReconcilerEnv(t, yanet)
	rec := events.NewFakeRecorder(8)
	r.Recorder = rec

	res, err := r.reconcileYanetV2(context.Background(), yanet)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected backoff requeue when config not loaded")
	}
	select {
	case e := <-rec.Events:
		if !strings.Contains(e, "ConfigNotLoaded") {
			t.Errorf("expected ConfigNotLoaded, got %q", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("expected ConfigNotLoaded event")
	}
}

// TestReconcileV2_EmitsBoxTypeNotFoundEvent: a YanetV2 pointing at an
// unknown boxType must surface a BoxTypeNotFound event.
func TestReconcileV2_EmitsBoxTypeNotFoundEvent(t *testing.T) {
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet",
			Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{BoxType: "no-such-box"},
	}
	r, snap := makeReconcilerEnv(t, yanet)
	snap.Config = minimalConfigV2() // only "release" exists
	rec := events.NewFakeRecorder(8)
	r.Recorder = rec

	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var found bool
	for {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, "BoxTypeNotFound") {
				found = true
			}
			continue
		case <-time.After(50 * time.Millisecond):
		}
		break
	}
	if !found {
		t.Errorf("expected BoxTypeNotFound event")
	}
}
