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
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ---------------------------------------------------------------------------
// Tests for handleYanetDeletion
// ---------------------------------------------------------------------------

// TestHandleYanetDeletion_NoFinalizer_ReturnsImmediately verifies that
// when the finalizer is not present, handleYanetDeletion returns
// immediately without attempting cleanup.
func TestHandleYanetDeletion_NoFinalizer_ReturnsImmediately(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y",
			Namespace: "yanet",
			// No finalizer - function should return immediately
		},
	}
	r, _ := makeReconcilerEnv(t, yanet)

	result, err := r.handleYanetDeletion(context.Background(), yanet, silentLogger())
	if err != nil {
		t.Fatalf("handleYanetDeletion: %v", err)
	}

	// Should return immediately with no requeue
	if result.RequeueAfter > 0 {
		t.Errorf("expected no requeue when finalizer absent, got %+v", result)
	}
}

// TestHandleYanetDeletion_WithFinalizer_CleansUpResources verifies that
// when the finalizer is present, handleYanetDeletion prunes all owned
// resources and removes the finalizer. Shared Services are owned by
// YanetConfig and must not be deleted here.
func TestHandleYanetDeletion_WithFinalizer_CleansUpResources(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "y",
			Namespace:  "yanet",
			Finalizers: []string{yanetFinalizer},
		},
	}

	// Create some owned resources
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-dep",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-svc",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-cm",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}

	dep.OwnerReferences = []metav1.OwnerReference{yanetOwnerReferenceForTest()}
	cm.OwnerReferences = []metav1.OwnerReference{yanetOwnerReferenceForTest()}
	r, _ := makeReconcilerEnv(t, yanet, dep, svc, cm)

	result, err := r.handleYanetDeletion(context.Background(), yanet, silentLogger())
	if err != nil {
		t.Fatalf("handleYanetDeletion: %v", err)
	}

	// Should return with no requeue
	if result.RequeueAfter > 0 {
		t.Errorf("expected no requeue after successful cleanup, got %+v", result)
	}

	// Verify Yanet-owned resources were deleted.
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "owned-dep", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("owned Deployment must be deleted, got err=%v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "owned-svc", Namespace: "yanet"}, &corev1.Service{}); err != nil {
		t.Errorf("Service must be left for YanetConfig reconciliation, got err=%v", err)
	}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "owned-cm", Namespace: "yanet"}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Errorf("owned ConfigMap must be deleted, got err=%v", err)
	}

	// Verify finalizer was removed
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "y", Namespace: "yanet"}, yanet); err != nil {
		t.Fatalf("re-get yanet: %v", err)
	}
	if controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
		t.Errorf("finalizer must be removed after cleanup, got finalizers=%v", yanet.Finalizers)
	}
}

// TestHandleYanetDeletion_ForeignResources_NotDeleted verifies that
// resources not owned by this Yanet are not deleted during cleanup.
func TestHandleYanetDeletion_ForeignResources_NotDeleted(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "y",
			Namespace:  "yanet",
			Finalizers: []string{yanetFinalizer},
		},
	}

	// Create owned resource
	owned := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "y"},
		},
	}

	// Create foreign resource (different label)
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foreign",
			Namespace: "yanet",
			Labels:    map[string]string{manifests.LabelYanet: "other-yanet"},
		},
	}

	owned.OwnerReferences = []metav1.OwnerReference{yanetOwnerReferenceForTest()}
	r, _ := makeReconcilerEnv(t, yanet, owned, foreign)

	result, err := r.handleYanetDeletion(context.Background(), yanet, silentLogger())
	if err != nil {
		t.Fatalf("handleYanetDeletion: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}

	// Verify owned resource was deleted
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "owned", Namespace: "yanet"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("owned Deployment must be deleted")
	}

	// Verify foreign resource was NOT deleted
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "foreign", Namespace: "yanet"}, &appsv1.Deployment{}); err != nil {
		t.Errorf("foreign Deployment must not be deleted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests for applyInlineConfigMaps
// ---------------------------------------------------------------------------

// TestApplyInlineConfigMaps_NoInlineConfigs_ReturnsEmpty verifies that
// when there are no inline configs, the function returns empty list.
func TestApplyInlineConfigMaps_NoInlineConfigs_ReturnsEmpty(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	r, _ := makeReconcilerEnv(t, yanet)

	buildCtx := manifests.BuildContext{
		YanetName: "y",
		Namespace: "yanet",
		NodeName:  "node-1",
	}

	// ResolvedComponent with no inline config
	rc := &helpers.ResolvedComponent{
		Name: "controlplane",
		// No Config.Inline set
	}

	names, _, err := r.applyInlineConfigMaps(context.Background(), yanet, buildCtx, rc, true)
	if err != nil {
		t.Fatalf("applyInlineConfigMaps: %v", err)
	}

	if len(names) != 0 {
		t.Errorf("expected empty list when no inline configs, got %v", names)
	}
}

// TestApplyInlineConfigMaps_AutoSyncTrue_CreatesConfigMaps verifies that
// when autoSync=true, inline ConfigMaps are created.
func TestApplyInlineConfigMaps_AutoSyncTrue_CreatesConfigMaps(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "y",
			Namespace: "yanet",
			UID:       "test-uid",
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: yanetv1alpha1.GroupVersion.String(),
			Kind:       "Yanet",
		},
	}
	r, _ := makeReconcilerEnv(t, yanet)

	buildCtx := manifests.BuildContext{
		YanetName: "y",
		Namespace: "yanet",
		NodeName:  "node-1",
	}

	// ResolvedComponent with inline config
	rc := &helpers.ResolvedComponent{
		Name: "controlplane",
		Config: &yanetv1alpha1.ConfigSource{
			Inline: "logging: { level: info }",
		},
	}

	names, _, err := r.applyInlineConfigMaps(context.Background(), yanet, buildCtx, rc, true)
	if err != nil {
		t.Fatalf("applyInlineConfigMaps: %v", err)
	}

	if len(names) == 0 {
		t.Fatalf("expected at least one ConfigMap name, got empty")
	}

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: names[0], Namespace: "yanet"}, cm); err != nil {
		t.Fatalf("ConfigMap must be created: %v", err)
	}

	// Verify labels
	if cm.Labels[manifests.LabelYanet] != "y" {
		t.Errorf("expected LabelYanet=y, got %q", cm.Labels[manifests.LabelYanet])
	}
	if cm.Labels[manifests.LabelComponent] != "controlplane" {
		t.Errorf("expected LabelComponent=controlplane, got %q", cm.Labels[manifests.LabelComponent])
	}

	// Verify data
	if cm.Data["config"] != "logging: { level: info }" {
		t.Errorf("unexpected config data: %q", cm.Data["config"])
	}
}

// TestApplyInlineConfigMaps_AutoSyncFalse_SkipsCreation verifies that
// when autoSync=false and ConfigMap doesn't exist, creation is skipped.
func TestApplyInlineConfigMaps_AutoSyncFalse_SkipsCreation(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	r, _ := makeReconcilerEnv(t, yanet)

	buildCtx := manifests.BuildContext{
		YanetName: "y",
		Namespace: "yanet",
		NodeName:  "node-1",
	}

	rc := &helpers.ResolvedComponent{
		Name: "controlplane",
		Config: &yanetv1alpha1.ConfigSource{
			Inline: "logging: { level: info }",
		},
	}

	// autoSync=false
	names, drift, err := r.applyInlineConfigMaps(context.Background(), yanet, buildCtx, rc, false)
	if err != nil {
		t.Fatalf("applyInlineConfigMaps: %v", err)
	}

	// Names are still returned (for tracking in desired set)
	if len(names) == 0 {
		t.Fatalf("expected ConfigMap names to be returned even with autoSync=false")
	}
	if !drift {
		t.Fatal("missing inline ConfigMap must report drift")
	}

	// Verify ConfigMap was NOT created
	cms := &corev1.ConfigMapList{}
	if err := r.Client.List(context.Background(), cms, client.InNamespace("yanet")); err != nil {
		t.Fatalf("list ConfigMaps: %v", err)
	}
	if len(cms.Items) != 0 {
		t.Errorf("expected no ConfigMaps created when autoSync=false, got %d", len(cms.Items))
	}
}
