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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
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

func TestReconcileSharedServicesV2AggregatesInstallationsAndPrunesOwnedOrphans(t *testing.T) {
	config := &yanetv2alpha1.YanetConfigV2{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetConfigV2"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv2alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfigV2(),
	}
	config.Spec.Components.Operators = []yanetv2alpha1.OperatorSpec{{
		Name: "route",
		Containers: []yanetv2alpha1.OperatorContainer{{
			Name:  "route",
			Image: yanetv2alpha1.ImageRef{Name: "route", Tag: "v1"},
		}},
	}}
	config.Spec.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"route": {}}
	disabled := false
	installations := []client.Object{
		&yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "yanet"},
			Spec: yanetv2alpha1.YanetSpec{
				BoxType:      "release",
				NodeSelector: map[string]string{"pool": "first"},
				Enabled:      &disabled,
			},
		},
		&yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "yanet"},
			Spec: yanetv2alpha1.YanetSpec{
				BoxType:      "release",
				NodeSelector: map[string]string{"pool": "second"},
			},
		},
	}
	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
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
				"pool": "first", yanetv2alpha1.NFDNumaCountLabel: "2",
			},
		}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "node-b", Labels: map[string]string{
				"pool": "second", yanetv2alpha1.NFDNumaCountLabel: "3",
			},
		}},
		stale,
		foreign,
	}
	objects = append(objects, installations...)
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme}

	if err := r.reconcileSharedServicesV2(context.Background(), config, silentLogger()); err != nil {
		t.Fatalf("reconcileSharedServicesV2: %v", err)
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

func TestReconcileSharedServicesV2ConvergesValidPlansWithoutPruningOnPlanError(t *testing.T) {
	config := &yanetv2alpha1.YanetConfigV2{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetConfigV2"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv2alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfigV2(),
	}
	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
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
	valid := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	invalid := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "broken"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "missing"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(config, stale, unrelatedStale, valid, invalid).
		Build()
	r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme}

	if err := r.reconcileSharedServicesV2(context.Background(), config, silentLogger()); err == nil {
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

func TestYanetConfigReconcileV2StopPreservesSharedServices(t *testing.T) {
	config := &yanetv2alpha1.YanetConfigV2{
		TypeMeta: metav1.TypeMeta{APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetConfigV2"},
		ObjectMeta: metav1.ObjectMeta{
			Name: yanetv2alpha1.YanetConfigName,
			UID:  types.UID("config-uid"),
		},
		Spec: minimalConfigV2(),
	}
	config.Spec.Stop = true
	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
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
	installation := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, existing, installation).Build()
	snapshot := &yanetv2alpha1.MutexYanetConfigSpec{}
	r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}

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

func TestReconcileV2NoNodesReportsFallbackSharedService(t *testing.T) {
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "edge", Namespace: "yanet", UID: types.UID("yanet-uid"), Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", NodeSelector: map[string]string{"missing": "node"}, AutoSync: &autoSync,
		},
	}
	r, snapshot := makeReconcilerEnv(t, yanet)
	snapshot.Config = minimalConfigV2()

	if _, err := r.reconcileYanetV2(context.Background(), yanet); err != nil {
		t.Fatalf("reconcileYanetV2: %v", err)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(yanet), got); err != nil {
		t.Fatalf("get YanetV2: %v", err)
	}
	want := []string{"yanet-release-controlplane-numa0"}
	if !reflect.DeepEqual(got.Status.Services, want) {
		t.Fatalf("unexpected fallback Service status: got %v want %v", got.Status.Services, want)
	}
}

func TestValidateExclusiveNodesV2ReportsOtherInstallation(t *testing.T) {
	current := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "yanet"},
		Spec: yanetv2alpha1.YanetSpec{
			NodeSelector: map[string]string{"pool": "edge"},
		},
	}
	other := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "other-ns"},
		Spec: yanetv2alpha1.YanetSpec{
			NodeSelector: map[string]string{"zone": "a"},
		},
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a", Labels: map[string]string{"pool": "edge", "zone": "a"},
	}}
	r, _ := makeReconcilerEnv(t, current, other)

	err := r.validateExclusiveNodesV2(context.Background(), current, []corev1.Node{node})
	if err == nil || !strings.Contains(err.Error(), "node node-a is also selected by YanetV2 other-ns/other") {
		t.Fatalf("expected node selection conflict, got %v", err)
	}
}

func TestValidateExclusiveNodesV2UsesDeterministicWinner(t *testing.T) {
	current := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: "yanet",
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)),
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other)

	if err := r.validateExclusiveNodesV2(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("older installation must remain the deterministic winner: %v", err)
	}
	if err := r.validateExclusiveNodesV2(context.Background(), other, []corev1.Node{node}); err == nil {
		t.Fatal("newer installation must lose the deterministic node claim")
	}
}

func TestValidateExclusiveNodesV2KeepsExistingWorkloadOwner(t *testing.T) {
	controller := true
	current := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "yanet", UID: "current-uid"},
		Spec:       yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "yanet", UID: "other-uid"},
		Spec:       yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	workload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      "current-workload",
		Namespace: current.Namespace,
		Labels:    map[string]string{manifests.LabelNode: "node-a"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetV2",
			Name: current.Name, UID: current.UID, Controller: &controller,
		}},
	}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other, workload)

	if err := r.validateExclusiveNodesV2(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("existing workload owner must keep its node claim: %v", err)
	}
	if err := r.validateExclusiveNodesV2(context.Background(), other, []corev1.Node{node}); err == nil {
		t.Fatal("installation without workloads must not displace the incumbent")
	}
}

func TestValidateExclusiveNodesV2PrunesDeterministicLoserWhenBothHaveWorkloads(t *testing.T) {
	controller := true
	current := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet", UID: "current-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	other := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: "yanet", UID: "other-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)),
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	workload := func(name string, installation *yanetv2alpha1.YanetV2) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: installation.Namespace,
			Labels: map[string]string{
				manifests.LabelYanet: installation.Name,
				manifests.LabelNode:  "node-a",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetV2",
				Name: installation.Name, UID: installation.UID, Controller: &controller,
			}},
		}}
	}
	currentWorkload := workload("current-workload", current)
	otherWorkload := workload("other-workload", other)
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, other, currentWorkload, otherWorkload)

	if err := r.validateExclusiveNodesV2(context.Background(), current, []corev1.Node{node}); err != nil {
		t.Fatalf("deterministic winner must continue reconciling: %v", err)
	}
	err := r.validateExclusiveNodesV2(context.Background(), other, []corev1.Node{node})
	conflict, ok := err.(*nodeSelectionConflictV2)
	if !ok {
		t.Fatalf("deterministic loser must receive node conflict, got %T: %v", err, err)
	}
	if err := r.pruneConflictingDeploymentsV2(context.Background(), other, conflict.cleanupNodeNames, silentLogger()); err != nil {
		t.Fatalf("prune conflicting loser: %v", err)
	}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(otherWorkload), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("losing workload must be deleted, got %v", err)
	}
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(currentWorkload), &appsv1.Deployment{}); err != nil {
		t.Fatalf("winning workload must remain: %v", err)
	}
}

func TestValidateExclusiveNodesV2WaitsForDeletingInstallation(t *testing.T) {
	controller := true
	now := metav1.Now()
	current := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current", Namespace: "yanet", UID: "current-uid",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	deleting := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deleting", Namespace: "yanet", Finalizers: []string{yanetFinalizer},
			CreationTimestamp: metav1.NewTime(time.Unix(2, 0)), DeletionTimestamp: &now,
		},
		Spec: yanetv2alpha1.YanetSpec{NodeSelector: map[string]string{"zone": "a"}},
	}
	currentWorkload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      "current-workload",
		Namespace: current.Namespace,
		Labels: map[string]string{
			manifests.LabelYanet: current.Name,
			manifests.LabelNode:  "node-a",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: yanetv2alpha1.GroupVersion.String(), Kind: "YanetV2",
			Name: current.Name, UID: current.UID, Controller: &controller,
		}},
	}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"zone": "a"}}}
	r, _ := makeReconcilerEnv(t, current, deleting, currentWorkload)

	err := r.validateExclusiveNodesV2(context.Background(), current, []corev1.Node{node})
	if err == nil {
		t.Fatal("replacement must wait until the deleting installation object is gone")
	}
	conflict := err.(*nodeSelectionConflictV2)
	if len(conflict.cleanupNodeNames) != 0 {
		t.Fatalf("temporary wait for deleting CR must not prune the current winner: %v", conflict.cleanupNodeNames)
	}
}
