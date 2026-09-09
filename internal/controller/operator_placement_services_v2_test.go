package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func setMonitorPlacementV2(config *api.YanetConfigV2, colocated bool) {
	slot := config.Spec.BoxTypes[0].Operators["monalive"]
	slot.Placement = api.OperatorPlacementStandalone
	if colocated {
		slot.Placement = api.OperatorPlacementDataplane
	}
	config.Spec.BoxTypes[0].Operators["monalive"] = slot
}

func TestOperatorPlacementServiceCutoverWaitsForScopeDrain(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, autoSync := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%t/autoSync=%t", reverse, autoSync), func(t *testing.T) {
				testContext := context.Background()
				yanet := reviewYanetV2()
				config := &api.YanetConfigV2{ObjectMeta: metav1.ObjectMeta{Name: "config", UID: "config-owner"}, Spec: colocatedConfigV2(t)}
				setMonitorPlacementV2(config, reverse)
				otherBox := config.Spec.BoxTypes[0].DeepCopy()
				otherBox.Name = "other"
				config.Spec.BoxTypes = append(config.Spec.BoxTypes, *otherBox)
				// A second installation shares the Service but has no producers.
				// It must not overwrite a refusal caused by the first installation.
				second := reviewYanetV2()
				second.Name, second.UID = "second", "second-owner"
				second.Spec.NodeSelector = map[string]string{"pool": "unused"}
				healthy := second.DeepCopy()
				healthy.Namespace = "healthy"
				otherInstallation := second.DeepCopy()
				otherInstallation.Name, otherInstallation.Spec.BoxType = "other", "other"
				r, snapshot := makeReconcilerEnv(t, yanet, second, healthy, otherInstallation, reviewNodeV2(), config)
				shared := &YanetConfigReconcilerV2{Client: r.Client, Scheme: r.Scheme, GlobalConfigV2: snapshot}
				if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
					t.Fatal(err)
				}
				if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
					t.Fatal(err)
				}
				before := placementServiceV2(t, r, "monalive")
				orphan := before.DeepCopy()
				orphan.Name, orphan.ResourceVersion, orphan.UID = "old-scope-service", "", ""
				if err := r.Create(testContext, orphan); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
					t.Fatal(err)
				}
				yanet.Spec.AutoSync = &autoSync
				if err := r.Update(testContext, yanet); err != nil {
					t.Fatal(err)
				}
				setMonitorPlacementV2(config, !reverse)
				config.Spec.BoxTypes[1].Operators["monalive"] = config.Spec.BoxTypes[0].Operators["monalive"]
				if err := r.Update(testContext, config); err != nil {
					t.Fatal(err)
				}
				_, sharedErr := shared.Reconcile(testContext, ctrl.Request{})
				after := placementServiceV2(t, r, "monalive")
				if !reflect.DeepEqual(before.Spec, after.Spec) {
					t.Fatalf("Service routing changed before old producers drained: old=%+v new=%+v", before.Spec, after.Spec)
				}
				if sharedErr == nil || !strings.Contains(sharedErr.Error(), "drain") {
					t.Fatalf("blocked Service cutover must request a retry: %v", sharedErr)
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(orphan), orphan); err != nil {
					t.Fatalf("blocked scope must be protected from pruning: %v", err)
				}
				other := &corev1.Service{}
				if err := r.Get(testContext, client.ObjectKey{Namespace: "healthy", Name: before.Name}, other); err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(before.Spec.Selector, other.Spec.Selector) {
					t.Fatal("a drained namespace must not be blocked by another namespace")
				}
				if err := r.Get(testContext, client.ObjectKey{Namespace: yanet.Namespace, Name: "yanet-other-monalive"}, other); err != nil {
					t.Fatal(err)
				}
				if other.Spec.Selector[manifests.LabelComponent] == before.Spec.Selector[manifests.LabelComponent] {
					t.Fatal("a drained box in the same namespace must not be blocked")
				}
				if snapshot.Config.BoxTypes[0].Operators["monalive"].Placement != config.Spec.BoxTypes[0].Operators["monalive"].Placement {
					t.Fatal("Service gate blocked publication of the new config snapshot")
				}
				if _, err := reviewReconcileV2(testContext, r, yanet); err == nil {
					t.Fatal("workload transition must still require explicit drain")
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
					t.Fatal(err)
				}
				yanet.Spec.Enabled, yanet.Spec.AutoSync = helpers.PtrFalse(), helpers.PtrTrue()
				if err := r.Update(testContext, yanet); err != nil {
					t.Fatal(err)
				}
				if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
					t.Fatalf("new snapshot must allow zero-replica drain despite the Service gate: %v", err)
				}
				if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
					t.Fatalf("drained scope must permit Service cutover: %v", err)
				}
				after = placementServiceV2(t, r, "monalive")
				if reflect.DeepEqual(before.Spec.Selector, after.Spec.Selector) {
					t.Fatal("drained Service did not switch to the new placement")
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(orphan), orphan); !apierrors.IsNotFound(err) {
					t.Fatalf("pruning must resume after drain: %v", err)
				}
			})
		}
	}
}

func TestOperatorPlacementServiceCutoverResidualProducers(t *testing.T) {
	for _, kind := range []string{"Deployment", "ReplicaSet", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			config := &api.YanetConfigV2{ObjectMeta: metav1.ObjectMeta{Name: "config", UID: "config-owner"}, Spec: colocatedConfigV2(t)}
			setMonitorPlacementV2(config, false)
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2(), config)
			shared := &YanetConfigReconcilerV2{Client: r.Client, Scheme: r.Scheme, GlobalConfigV2: snapshot}
			if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
				t.Fatal(err)
			}
			before := placementServiceV2(t, r, "monalive")
			// A disappeared/deleting installation on a no-longer-selected node
			// still reserves the shared routing scope until its producers drain.
			template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				manifests.LabelBoxType: "release", manifests.LabelComponent: "monalive", manifests.LabelYanet: "old-installation",
			}}, Spec: corev1.PodSpec{NodeName: "old-node"}}
			meta := metav1.ObjectMeta{Name: "residual", Namespace: yanet.Namespace}
			var old client.Object
			switch kind {
			case "Deployment":
				meta.Generation = 2
				old = &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: helpers.Int32Ptr(0), Template: template}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1}}
			case "ReplicaSet":
				old = &appsv1.ReplicaSet{ObjectMeta: meta, Spec: appsv1.ReplicaSetSpec{Replicas: helpers.Int32Ptr(0), Template: template}, Status: appsv1.ReplicaSetStatus{Replicas: 1}}
			case "Pod":
				meta.Labels, meta.Finalizers = template.Labels, []string{"test/retain"}
				old = &corev1.Pod{ObjectMeta: meta, Spec: template.Spec}
			}
			if err := r.Create(testContext, old); err != nil {
				t.Fatal(err)
			}
			if kind == "Pod" {
				if err := r.Delete(testContext, old); err != nil {
					t.Fatal(err)
				}
			}
			setMonitorPlacementV2(config, true)
			if err := r.Update(testContext, config); err != nil {
				t.Fatal(err)
			}
			if _, err := shared.Reconcile(testContext, ctrl.Request{}); err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("residual %s must block shared cutover: %v", kind, err)
			}
			if !reflect.DeepEqual(before.Spec, placementServiceV2(t, r, "monalive").Spec) {
				t.Fatal("routing changed while residual producer remained")
			}
		})
	}
}

func TestOperatorPlacementServiceCutoverListFailure(t *testing.T) {
	for _, kind := range []string{"DeploymentList", "ReplicaSetList", "PodList"} {
		t.Run(kind, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			config := &api.YanetConfigV2{ObjectMeta: metav1.ObjectMeta{Name: "config", UID: "config-owner"}, Spec: colocatedConfigV2(t)}
			setMonitorPlacementV2(config, false)
			healthy := yanet.DeepCopy()
			healthy.Namespace = "healthy"
			r, snapshot := makeReconcilerEnv(t, yanet, healthy, config)
			shared := &YanetConfigReconcilerV2{Client: r.Client, Scheme: r.Scheme, GlobalConfigV2: snapshot}
			if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
				t.Fatal(err)
			}
			before := placementServiceV2(t, r, "monalive")
			setMonitorPlacementV2(config, true)
			if err := r.Update(testContext, config); err != nil {
				t.Fatal(err)
			}
			shared.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				options := &client.ListOptions{}
				options.ApplyOptions(opts)
				if options.Namespace == yanet.Namespace && reflect.TypeOf(list).Elem().Name() == kind {
					return errors.New("live workload read denied")
				}
				return cl.List(ctx, list, opts...)
			}})
			if _, err := shared.Reconcile(testContext, ctrl.Request{}); err == nil || !strings.Contains(err.Error(), "read denied") {
				t.Fatalf("cannot prove safe Service cutover without live reads: %v", err)
			}
			if !reflect.DeepEqual(before.Spec, placementServiceV2(t, r, "monalive").Spec) {
				t.Fatal("read failure changed existing routing")
			}
			other := &corev1.Service{}
			if err := r.Get(testContext, client.ObjectKey{Namespace: "healthy", Name: before.Name}, other); err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(before.Spec.Selector, other.Spec.Selector) {
				t.Fatal("namespace-local read failure blocked a healthy namespace")
			}
		})
	}
}
