package controller

import (
	"context"
	"reflect"
	"testing"

	dto "github.com/prometheus/client_model/go"
	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func reviewInstallation(name, node string) *yanetv1alpha1.Yanet {
	autoSync := true
	return &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "yanet", UID: types.UID(name), Finalizers: []string{yanetFinalizer}},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "release", AutoSync: &autoSync, NodeSelector: map[string]string{"kubernetes.io/hostname": node}},
	}
}

func reconcileAndRead(t *testing.T, r *YanetReconciler, y *yanetv1alpha1.Yanet) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
}

func reviewDeployments(t *testing.T, r *YanetReconciler) []appsv1.Deployment {
	t.Helper()
	list := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestReconcileReviewSelectorSwapReleasesFormerNodes(t *testing.T) {
	testCtx := context.Background()
	a, b := reviewInstallation("a", "node-1"), reviewInstallation("b", "node-2")
	n1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: a.Spec.NodeSelector}}
	n2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2", Labels: b.Spec.NodeSelector}}
	r, cfg := makeReconcilerEnv(t, a, b, n1, n2)
	cfg.Config = minimalConfig()
	reconcileAndRead(t, r, a)
	reconcileAndRead(t, r, b)
	deps := reviewDeployments(t, r)
	if len(deps) != 4 {
		t.Fatalf("baseline deployments=%d", len(deps))
	}
	for i := range deps {
		deps[i].Finalizers = []string{"test.example/hold"}
		if err := r.Update(testCtx, &deps[i]); err != nil {
			t.Fatal(err)
		}
	}
	a.Spec.NodeSelector, b.Spec.NodeSelector = b.Spec.NodeSelector, a.Spec.NodeSelector
	for _, y := range []*yanetv1alpha1.Yanet{a, b} {
		if err := r.Update(testCtx, y); err != nil {
			t.Fatal(err)
		}
		reconcileAndRead(t, r, y)
	}
	deps = reviewDeployments(t, r)
	if len(deps) != 4 {
		t.Fatalf("foreground cleanup must retain four incumbents, got %d", len(deps))
	}
	for i := range deps {
		if deps[i].DeletionTimestamp.IsZero() {
			t.Errorf("former node deployment %s must begin cleanup", deps[i].Name)
		}
	}
	// Repeated reconciliation must not acquire either node while the old
	// workloads still exist. Fake client has no garbage collector.
	reconcileAndRead(t, r, a)
	reconcileAndRead(t, r, b)
	if got := len(reviewDeployments(t, r)); got != 4 {
		t.Fatalf("acquired node before foreground cleanup: %d", got)
	}
	for i := range deps {
		deps[i].Finalizers = nil
		if err := r.Update(testCtx, &deps[i]); err != nil {
			t.Fatal(err)
		}
	}
	reconcileAndRead(t, r, a)
	reconcileAndRead(t, r, b)
	deps = reviewDeployments(t, r)
	if len(deps) != 4 {
		t.Fatalf("swap did not converge: %d deployments", len(deps))
	}
	for _, d := range deps {
		want := "node-2"
		if metav1.GetControllerOf(&d).Name == "b" {
			want = "node-1"
		}
		if d.Labels[manifests.LabelNode] != want || !d.DeletionTimestamp.IsZero() {
			t.Errorf("unexpected swapped deployment: %s node=%s", d.Name, d.Labels[manifests.LabelNode])
		}
	}
}

func TestReconcileReviewDisabledConflictDrainsOwnedWorkloads(t *testing.T) {
	for _, mode := range []string{"drain", "report-only", "stop"} {
		t.Run(mode, func(t *testing.T) {
			testCtx := context.Background()
			a, b := reviewInstallation("a", "node-1"), reviewInstallation("b", "node-2")
			n1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: a.Spec.NodeSelector}}
			n2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2", Labels: b.Spec.NodeSelector}}
			r, cfg := makeReconcilerEnv(t, a, b, n1, n2)
			cfg.Config = minimalConfig()
			reconcileAndRead(t, r, a)
			reconcileAndRead(t, r, b)
			before := reviewDeployments(t, r)
			if len(before) != 4 {
				t.Fatalf("baseline deployments=%d", len(before))
			}
			disabled := false
			a.Spec.Enabled, a.Spec.NodeSelector = &disabled, nil
			if mode == "report-only" {
				a.Spec.AutoSync = &disabled
			}
			cfg.Config.Stop = mode == "stop"
			if err := r.Update(testCtx, a); err != nil {
				t.Fatal(err)
			}
			reconcileAndRead(t, r, a)
			after := reviewDeployments(t, r)
			if len(after) != 4 {
				t.Fatalf("drain must not acquire or remove selected workloads: %d", len(after))
			}
			if mode != "drain" && !reflect.DeepEqual(before, after) {
				t.Fatal("frozen/report-only workloads changed")
			}
			for _, d := range after {
				want := int32(1)
				if metav1.GetControllerOf(&d).Name == "a" && mode == "drain" {
					want = 0
				}
				if d.Spec.Replicas == nil || *d.Spec.Replicas != want {
					t.Errorf("%s replicas=%v, want %d", d.Name, d.Spec.Replicas, want)
				}
			}
		})
	}
}

func TestReconcileReviewRetainedOrphansReportDrift(t *testing.T) {
	testCtx := context.Background()
	y := reviewInstallation("orphan-review", "node-1")
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: y.Spec.NodeSelector}}
	r, cfg := makeReconcilerEnv(t, y, n)
	cfg.Config = minimalConfig()
	reconcileAndRead(t, r, y)
	before := reviewDeployments(t, r)
	if len(before) != 2 {
		t.Fatalf("baseline deployments=%d", len(before))
	}
	metric := yanetOrphansPruned.WithLabelValues(y.Name, y.Namespace)
	readPruned := func() float64 {
		t.Helper()
		value := &dto.Metric{}
		if err := metric.Write(value); err != nil {
			t.Fatal(err)
		}
		return value.GetCounter().GetValue()
	}
	pruned := readPruned()
	recorder := events.NewFakeRecorder(10)
	r.Recorder = recorder
	no := false
	y.Spec.AutoSync, y.Spec.NodeSelector = &no, map[string]string{"kubernetes.io/hostname": "absent"}
	if err := r.Update(testCtx, y); err != nil {
		t.Fatal(err)
	}
	reconcileAndRead(t, r, y)
	if !reflect.DeepEqual(before, reviewDeployments(t, r)) {
		t.Fatal("report-only reconcile changed workloads")
	}
	if got := readPruned(); got != pruned {
		t.Errorf("reported pruning without deleting: %v -> %v", pruned, got)
	}
	select {
	case event := <-recorder.Events:
		t.Errorf("report-only orphan detection must not emit a prune event: %s", event)
	default:
	}
	if len(y.Status.Sync.OutOfSync) != 2 || !meta.IsStatusConditionFalse(y.Status.Conditions, "Ready") {
		t.Fatalf("retained workloads must be reported as drift: %+v", y.Status)
	}
	// Foreground deletion is counted once, not on every reconcile while a
	// dependent's finalizer keeps it alive.
	for i := range before {
		before[i].Finalizers = []string{"test.example/hold"}
		if err := r.Update(testCtx, &before[i]); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	y.Spec.AutoSync = &yes
	if err := r.Update(testCtx, y); err != nil {
		t.Fatal(err)
	}
	reconcileAndRead(t, r, y)
	reconcileAndRead(t, r, y)
	if got := readPruned(); got != pruned+2 {
		t.Errorf("deletion requests must be counted once: got %v want %v", got, pruned+2)
	}
}

func TestReconcileReviewInlineConfigDrift(t *testing.T) {
	for _, drift := range []string{"missing", "data", "owner"} {
		t.Run(drift, func(t *testing.T) {
			testCtx := context.Background()
			y := reviewInstallation("inline-review", "node-1")
			n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: y.Spec.NodeSelector}}
			r, cfg := makeReconcilerEnv(t, y, n)
			// Model the API-assigned identity missing from the fake client's Create.
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Create: func(requestCtx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*corev1.ConfigMap); ok {
						obj.SetUID("inline-config-uid")
					}
					return c.Create(requestCtx, obj, opts...)
				},
			})
			cfg.Config = minimalConfig()
			cfg.Config.Components.Controlplane.Config = &yanetv1alpha1.ConfigSource{Inline: "logging: info"}
			reconcileAndRead(t, r, y)
			cms := &corev1.ConfigMapList{}
			if err := r.List(testCtx, cms); err != nil {
				t.Fatal(err)
			}
			if len(cms.Items) != 1 {
				t.Fatalf("baseline configmaps=%d", len(cms.Items))
			}
			cm := cms.Items[0].DeepCopy()
			key, uid := client.ObjectKeyFromObject(cm), cm.UID
			switch drift {
			case "missing":
				if err := r.Delete(testCtx, cm); err != nil {
					t.Fatal(err)
				}
			case "data":
				cm.Data["config"] = "manual change"
				if err := r.Update(testCtx, cm); err != nil {
					t.Fatal(err)
				}
			case "owner":
				cm.OwnerReferences[0].UID = "foreign-owner"
				if err := r.Update(testCtx, cm); err != nil {
					t.Fatal(err)
				}
			}
			before := cm.DeepCopy()
			no := false
			y.Spec.AutoSync = &no
			if err := r.Update(testCtx, y); err != nil {
				t.Fatal(err)
			}
			reconcileAndRead(t, r, y)
			err := r.Get(testCtx, key, cm)
			if drift == "missing" {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("missing CM was recreated: %v", err)
				}
			} else if err != nil || !reflect.DeepEqual(before, cm) {
				t.Fatalf("report-only changed ConfigMap: %v", err)
			}
			if len(y.Status.Sync.OutOfSync) != 1 || !meta.IsStatusConditionFalse(y.Status.Conditions, "Ready") {
				t.Fatalf("config drift not visible: %+v", y.Status)
			}
			if drift == "owner" {
				return
			}
			yes := true
			y.Spec.AutoSync = &yes
			if err := r.Update(testCtx, y); err != nil {
				t.Fatal(err)
			}
			reconcileAndRead(t, r, y)
			if err := r.Get(testCtx, key, cm); err != nil {
				t.Fatal(err)
			}
			if cm.Data["config"] != "logging: info" || (drift == "data" && cm.UID != uid) {
				t.Fatalf("repair did not preserve identity/content: %+v", cm)
			}
			if err := r.List(testCtx, cms); err != nil {
				t.Fatal(err)
			}
			if len(cms.Items) != 1 || len(y.Status.Sync.OutOfSync) != 0 {
				t.Fatalf("repair did not converge: CMs=%d status=%+v", len(cms.Items), y.Status.Sync)
			}
		})
	}
}

func TestReconcileReviewRetainedConfigMapReportsDrift(t *testing.T) {
	testCtx := context.Background()
	y := reviewInstallation("config-orphan-review", "node-1")
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: y.Spec.NodeSelector}}
	r, cfg := makeReconcilerEnv(t, y, n)
	cfg.Config = minimalConfig()
	cfg.Config.Components.Controlplane.Config = &yanetv1alpha1.ConfigSource{Inline: "logging: info"}
	reconcileAndRead(t, r, y)
	cms := &corev1.ConfigMapList{}
	if err := r.List(testCtx, cms); err != nil {
		t.Fatal(err)
	}
	if len(cms.Items) != 1 {
		t.Fatalf("baseline ConfigMaps=%d", len(cms.Items))
	}
	before := cms.Items[0].DeepCopy()
	// An administrator removed the old workloads, leaving only their config.
	for _, d := range reviewDeployments(t, r) {
		if err := r.Delete(testCtx, &d); err != nil {
			t.Fatal(err)
		}
	}
	no := false
	y.Spec.AutoSync, y.Spec.NodeSelector = &no, map[string]string{"kubernetes.io/hostname": "absent"}
	if err := r.Update(testCtx, y); err != nil {
		t.Fatal(err)
	}
	reconcileAndRead(t, r, y)
	if !meta.IsStatusConditionFalse(y.Status.Conditions, "Ready") {
		t.Fatalf("retained orphan ConfigMap must prevent Ready: %+v", y.Status.Conditions)
	}
	after := &corev1.ConfigMap{}
	if err := r.Get(testCtx, client.ObjectKeyFromObject(before), after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("report-only changed orphan ConfigMap")
	}
}
