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
	"errors"
	"testing"
	"time"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconcileDeletionIgnoresLabelDrift(t *testing.T) {
	for _, label := range []string{"", "another-installation"} {
		t.Run("label="+label, func(t *testing.T) {
			testContext := context.Background()
			yanet := &yanetv1alpha1.Yanet{ObjectMeta: metav1.ObjectMeta{
				Name: "y", Namespace: "yanet", UID: "current-owner", Finalizers: []string{yanetFinalizer},
			}}
			owner := *metav1.NewControllerRef(yanet, yanetv1alpha1.GroupVersion.WithKind("Yanet"))
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
				Name: "owned", Namespace: yanet.Namespace, UID: "deployment-uid",
				OwnerReferences: []metav1.OwnerReference{owner}, Finalizers: []string{"test.example/hold"},
			}}
			if label != "" {
				deployment.Labels = map[string]string{manifests.LabelYanet: label}
			}
			foreign := deployment.DeepCopy()
			foreign.Name, foreign.UID = "foreign", "foreign-uid"
			foreign.OwnerReferences[0].UID = "previous-owner"
			foreign.Labels = map[string]string{manifests.LabelYanet: yanet.Name}
			r, _ := makeReconcilerEnv(t, yanet, deployment, foreign)
			if err := r.Delete(testContext, yanet); err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}
			result, err := r.Reconcile(testContext, request)
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("waiting for dependent deletion: result=%+v err=%v", result, err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(deployment), deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.DeletionTimestamp.IsZero() {
				t.Fatal("owned Deployment with label drift must begin deletion")
			}
			if err := r.Get(testContext, request.NamespacedName, yanet); err != nil || len(yanet.Finalizers) == 0 {
				t.Fatalf("owner must retain finalizer while dependent exists: %v", err)
			}
			// The fake client has no garbage collector. Release the dependent only
			// after asserting the public reconcile initiated deletion and waited.
			deployment.Finalizers = nil
			if err := r.Update(testContext, deployment); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(testContext, request); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(testContext, request.NamespacedName, &yanetv1alpha1.Yanet{}); !apierrors.IsNotFound(err) {
				t.Fatalf("owner must finish deletion: %v", err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(foreign), foreign); err != nil || !foreign.DeletionTimestamp.IsZero() {
				t.Fatalf("same-name owner with different UID must be preserved: %v", err)
			}
		})
	}
}

func TestReconcileDeletionRetriesFailedCleanup(t *testing.T) {
	testContext := context.Background()
	yanet := &yanetv1alpha1.Yanet{ObjectMeta: metav1.ObjectMeta{
		Name: "y", Namespace: "yanet", UID: "owner", Finalizers: []string{yanetFinalizer},
	}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "owned", Namespace: yanet.Namespace,
		Labels:          map[string]string{manifests.LabelYanet: yanet.Name},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(yanet, yanetv1alpha1.GroupVersion.WithKind("Yanet"))},
	}}
	r, _ := makeReconcilerEnv(t, yanet, deployment)
	if err := r.Delete(testContext, yanet); err != nil {
		t.Fatal(err)
	}
	base := r.Client
	failure := errors.New("delete temporarily unavailable")
	r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return failure },
	})
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}
	if _, err := r.Reconcile(testContext, request); !errors.Is(err, failure) {
		t.Fatalf("cleanup failure must be retryable: %v", err)
	}
	if err := base.Get(testContext, request.NamespacedName, yanet); err != nil || len(yanet.Finalizers) == 0 {
		t.Fatalf("failed cleanup must retain finalizer: %v", err)
	}
	if err := base.Get(testContext, client.ObjectKeyFromObject(deployment), deployment); err != nil {
		t.Fatalf("failed deletion must preserve dependent: %v", err)
	}
	r.Client = base
	if _, err := r.Reconcile(testContext, request); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(testContext, request.NamespacedName, &yanetv1alpha1.Yanet{}); !apierrors.IsNotFound(err) {
		t.Fatalf("successful retry must finish deletion: %v", err)
	}
}

func TestReconcileThrottledNodeEventuallyUpdates(t *testing.T) {
	testContext := context.Background()
	enabled := true
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet", UID: "owner", Finalizers: []string{yanetFinalizer}},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "release", AutoSync: &enabled},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = minimalConfig()
	snapshot.Config.UpdateWindow = 3600
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}
	if _, err := r.Reconcile(testContext, request); err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Components.Dataplane.Image.Tag = "v2"
	// Set the protected throttle timestamp as in the existing throttle tests;
	// no wall-clock waiting or production clock change is necessary.
	r.lock.Lock()
	r.lastUpdateHost, r.lastUpdateTS = "node-1", time.Now()
	r.lock.Unlock()
	result, err := r.Reconcile(testContext, request)
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("different-node update must wait: result=%+v err=%v", result, err)
	}
	assertImage := func(want string) {
		t.Helper()
		deployments := &appsv1.DeploymentList{}
		if listErr := r.List(testContext, deployments, client.MatchingLabels{manifests.LabelComponent: "dataplane"}); listErr != nil {
			t.Fatal(listErr)
		}
		if len(deployments.Items) != 1 {
			t.Fatalf("expected one dataplane, got %d", len(deployments.Items))
		}
		if got := deployments.Items[0].Spec.Template.Spec.Containers[0].Image; got != want {
			t.Fatalf("dataplane image: got %q, want %q", got, want)
		}
	}
	assertImage("dp:v1")
	r.lock.Lock()
	r.lastUpdateTS = time.Now().Add(-2 * time.Hour)
	r.lock.Unlock()
	result, err = r.Reconcile(testContext, request)
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("expired window must converge: result=%+v err=%v", result, err)
	}
	assertImage("dp:v2")
}
