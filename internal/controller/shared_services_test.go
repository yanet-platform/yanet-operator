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
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcileSharedServicesAggregatesInstallationsAndPrunesOwnedOrphans(t *testing.T) {
	config := &yanetv1alpha1.YanetConfig{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "YanetConfig"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv1alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfig(),
	}
	numa := int32(3)
	config.Spec.Components.Controlplane.Numa = &numa
	config.Spec.Components.Dataplane.Sidecars = []yanetv1alpha1.SidecarSpec{
		{Name: "netlink-dataplane-sidecar",
			Image: yanetv1alpha1.ImageRef{Name: "netlink-dataplane-sidecar", Tag: "v1"},
		},
	}
	config.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]yanetv1alpha1.BoxDataplaneSidecar{
		"netlink-dataplane-sidecar": {},
	}
	config.Spec.Components.Operators = []yanetv1alpha1.OperatorSpec{{
		Name: "route",
		Containers: []yanetv1alpha1.OperatorContainer{{
			Name:  "route",
			Image: yanetv1alpha1.ImageRef{Name: "route", Tag: "v1"},
		}},
	}}
	config.Spec.BoxTypes[0].Operators = map[string]yanetv1alpha1.BoxOperator{"route": {}}
	disabled := false
	installations := []client.Object{
		&yanetv1alpha1.Yanet{
			ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "yanet"},
			Spec: yanetv1alpha1.YanetSpec{
				BoxType:      "release",
				NodeSelector: map[string]string{"pool": "first"},
				Enabled:      &disabled,
			},
		},
		&yanetv1alpha1.Yanet{
			ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "yanet"},
			Spec: yanetv1alpha1.YanetSpec{
				BoxType:      "release",
				NodeSelector: map[string]string{"pool": "second"},
			},
		},
	}
	owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
	stale := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "stale",
			Namespace:       "yanet",
			Labels:          map[string]string{manifests.LabelSharedService: "true"},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "stale"},
			Ports:    []corev1.ServicePort{{Name: "grpc", Port: 8080}},
		},
	}
	foreign := stale.DeepCopy()
	foreign.Name = "foreign"
	foreign.OwnerReferences = nil
	objects := []client.Object{
		config,
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "node-a", Labels: map[string]string{
				"pool": "first",
			},
		}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "node-b", Labels: map[string]string{
				"pool": "second",
			},
		}},
		stale,
		foreign,
	}
	objects = append(objects, installations...)
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &YanetConfigReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileSharedServices(context.Background(), config, silentLogger()); err != nil {
		t.Fatalf("reconcileSharedServices: %v", err)
	}

	services := &corev1.ServiceList{}
	if err := cl.List(context.Background(), services, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Services: %v", err)
	}
	var managedNames []string
	for index := range services.Items {
		service := &services.Items[index]
		if metav1.IsControlledBy(service, config) {
			managedNames = append(managedNames, service.Name)
			if service.Spec.InternalTrafficPolicy == nil ||
				*service.Spec.InternalTrafficPolicy != corev1.ServiceInternalTrafficPolicyLocal {
				t.Errorf("Service %s must use internalTrafficPolicy=Local", service.Name)
			}
			if _, installationScoped := service.Spec.Selector[manifests.LabelYanet]; installationScoped {
				t.Errorf("shared Service %s selector contains installation identity: %v", service.Name, service.Spec.Selector)
			}
		}
	}
	sort.Strings(managedNames)
	wantNames := []string{
		"yanet-release-controlplane-numa0",
		"yanet-release-controlplane-numa1",
		"yanet-release-controlplane-numa2",
		"yanet-release-netlink-dataplane-sidecar",
		"yanet-release-route",
	}
	if !reflect.DeepEqual(managedNames, wantNames) {
		t.Fatalf("unexpected shared Services: got %v want %v", managedNames, wantNames)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: stale.Name, Namespace: stale.Namespace}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Errorf("owned orphan Service must be pruned, got %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: foreign.Name, Namespace: foreign.Namespace}, &corev1.Service{}); err != nil {
		t.Errorf("foreign Service must not be pruned: %v", err)
	}
}

func TestReconcileSharedServicesConvergesValidPlansWithoutPruningOnPlanError(t *testing.T) {
	config := &yanetv1alpha1.YanetConfig{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "YanetConfig"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv1alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfig(),
	}
	owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
	stale := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      "previously-valid",
		Namespace: "broken",
		Labels: map[string]string{
			manifests.LabelSharedService: "true",
			manifests.LabelBoxType:       "missing",
		},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	unrelatedStale := stale.DeepCopy()
	unrelatedStale.Name = "unrelated-stale"
	unrelatedStale.Namespace = "unrelated"
	unrelatedStale.Labels[manifests.LabelBoxType] = "release"
	valid := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: "yanet"},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
	}
	invalid := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "broken"},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "missing"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(config, stale, unrelatedStale, valid, invalid).
		Build()
	r := &YanetConfigReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileSharedServices(context.Background(), config, silentLogger()); err == nil {
		t.Fatal("invalid installation must still report its planning error")
	}
	if err := cl.Get(context.Background(), client.ObjectKey{
		Name: "yanet-release-controlplane-numa0", Namespace: "yanet",
	}, &corev1.Service{}); err != nil {
		t.Fatalf("valid installation Service did not converge: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(stale), &corev1.Service{}); err != nil {
		t.Fatalf("planning error must suppress destructive pruning: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(unrelatedStale), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("planning error must not suppress unrelated pruning, got %v", err)
	}
}

func TestYanetConfigReconcileStopPreservesSharedServices(t *testing.T) {
	config := &yanetv1alpha1.YanetConfig{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "YanetConfig"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv1alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfig(),
	}
	config.Spec.Stop = true
	owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "existing",
			Namespace:       "yanet",
			Labels:          map[string]string{manifests.LabelSharedService: "true"},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "existing"},
			Ports:    []corev1.ServicePort{{Name: "grpc", Port: 8080}},
		},
	}
	installation := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "yanet"},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, existing, installation).Build()
	snapshot := &yanetv1alpha1.MutexYanetConfigSpec{}
	r := &YanetConfigReconciler{Client: cl, Scheme: scheme, GlobalConfig: snapshot}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: config.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), &corev1.Service{}); err != nil {
		t.Fatalf("global stop must preserve existing Services: %v", err)
	}
	generated := &corev1.ServiceList{}
	if err := cl.List(context.Background(), generated, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list Services: %v", err)
	}
	if len(generated.Items) != 1 {
		t.Fatalf("global stop must not create Services, got %d", len(generated.Items))
	}
	if !snapshot.Config.Stop {
		t.Fatal("global stop config was not published to the snapshot")
	}
}

func TestReconcileNoNodesReportsFallbackSharedService(t *testing.T) {
	autoSync := true
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "edge", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv1alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"missing": "node"}, AutoSync: &autoSync,
		},
	}
	r, snapshot := makeReconcilerEnv(t, yanet)
	snapshot.Config = minimalConfig()

	if _, err := r.reconcileYanet(context.Background(), yanet); err != nil {
		t.Fatalf("reconcileYanet: %v", err)
	}
	got := &yanetv1alpha1.Yanet{}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(yanet), got); err != nil {
		t.Fatalf("get Yanet: %v", err)
	}
	want := []string{"yanet-release-controlplane-numa0"}
	if !reflect.DeepEqual(got.Status.Services, want) {
		t.Fatalf("unexpected fallback Service status: got %v want %v", got.Status.Services, want)
	}
}

func TestValidateExclusiveNodesReportsOtherInstallation(t *testing.T) {
	current := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "yanet"},
		Spec: yanetv1alpha1.YanetSpec{
			NodeSelector: map[string]string{"pool": "edge"},
		},
	}
	other := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "other-ns"},
		Spec: yanetv1alpha1.YanetSpec{
			NodeSelector: map[string]string{"zone": "a"},
		},
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a", Labels: map[string]string{"pool": "edge", "zone": "a"},
	}}
	r, _ := makeReconcilerEnv(t, current, other)

	err := r.validateExclusiveNodes(context.Background(), current, []corev1.Node{node})
	if err == nil || !strings.Contains(err.Error(), "node node-a is also selected by Yanet other-ns/other") {
		t.Fatalf("expected node selection conflict, got %v", err)
	}
}

func TestValidateExclusiveNodesUsesDeterministicWinner(t *testing.T) {
	current := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: "yanet",
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)),
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other)

	if err := r.validateExclusiveNodes(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("older installation must remain the deterministic winner: %v", err)
	}
	if err := r.validateExclusiveNodes(context.Background(), other, []corev1.Node{node}); err == nil {
		t.Fatal("newer installation must lose the deterministic node claim")
	}
}

func TestValidateExclusiveNodesKeepsExistingWorkloadOwner(t *testing.T) {
	controller := true
	current := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "yanet", UID: "current-uid"},
		Spec:       yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "yanet", UID: "other-uid"},
		Spec:       yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	workload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      "current-workload",
		Namespace: current.Namespace,
		Labels:    map[string]string{manifests.LabelNode: "node-a"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "Yanet",
			Name: current.Name, UID: current.UID, Controller: &controller,
		}},
	}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other, workload)

	if err := r.validateExclusiveNodes(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("existing workload owner must keep its node claim: %v", err)
	}
	if err := r.validateExclusiveNodes(context.Background(), other, []corev1.Node{node}); err == nil {
		t.Fatal("installation without workloads must not displace the incumbent")
	}
}

func TestValidateExclusiveNodesPrunesDeterministicLoserWhenBothHaveWorkloads(t *testing.T) {
	controller := true
	current := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet", UID: "current-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: "yanet", UID: "other-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)),
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	workload := func(name string, installation *yanetv1alpha1.Yanet) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: installation.Namespace,
			Labels: map[string]string{
				manifests.LabelYanet: installation.Name,
				manifests.LabelNode:  "node-a",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "Yanet",
				Name: installation.Name, UID: installation.UID, Controller: &controller,
			}},
		}}
	}
	currentWorkload := workload("current-workload", current)
	otherWorkload := workload("other-workload", other)
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other, currentWorkload, otherWorkload)

	if err := r.validateExclusiveNodes(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("deterministic winner must continue reconciling: %v", err)
	}
	err := r.validateExclusiveNodes(context.Background(), other, []corev1.Node{node})
	conflict, ok := err.(*nodeSelectionConflict)
	if !ok {
		t.Fatalf("deterministic loser must receive node conflict, got %T: %v", err, err)
	}
	if err := r.pruneConflictingDeployments(context.Background(), other, conflict.cleanupNodeNames, silentLogger()); err != nil {
		t.Fatalf("prune conflicting loser: %v", err)
	}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(otherWorkload), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("losing workload must be deleted, got %v", err)
	}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(currentWorkload), &appsv1.Deployment{}); err != nil {
		t.Fatalf("winning workload must remain: %v", err)
	}
}

func TestValidateExclusiveNodesWaitsForDeletingInstallation(t *testing.T) {
	controller := true
	now := metav1.Now()
	current := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet", UID: "current-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	deleting := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deleting", Namespace: "yanet", Finalizers: []string{yanetFinalizer},
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)), DeletionTimestamp: &now,
		},
		Spec: yanetv1alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	currentWorkload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      "current-workload",
		Namespace: current.Namespace,
		Labels: map[string]string{
			manifests.LabelYanet: current.Name,
			manifests.LabelNode:  "node-a",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: yanetv1alpha1.GroupVersion.String(), Kind: "Yanet",
			Name: current.Name, UID: current.UID, Controller: &controller,
		}},
	}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, deleting, currentWorkload)

	err := r.validateExclusiveNodes(context.Background(), current, []corev1.Node{node})
	if err == nil {
		t.Fatal("replacement must wait until the deleting installation object is gone")
	}
	conflict := err.(*nodeSelectionConflict)
	if len(conflict.cleanupNodeNames) != 0 {
		t.Fatalf("temporary wait for deleting CR must not prune the current winner: %v", conflict.cleanupNodeNames)
	}
}
