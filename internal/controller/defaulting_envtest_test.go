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
	"os"
	"reflect"
	"testing"
	"time"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Observe actual writes separately from admission/defaulting requests.
type defaultingObserver struct {
	client.Client
	writes, dryRuns int
	dryRunError     error
	hideServices    bool
}

func (c *defaultingObserver) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, service := obj.(*corev1.Service); service && c.hideServices {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *defaultingObserver) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	options := &client.UpdateOptions{}
	options.ApplyOptions(opts)
	if len(options.DryRun) != 0 {
		c.dryRuns++
		if c.dryRunError != nil {
			return c.dryRunError
		}
	} else {
		c.writes++
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestAPIServerDefaulting(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("requires envtest assets")
	}
	// No manager: these public reconciles must not race the suite's controllers.
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	restConfig, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := env.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})
	scheme := testScheme(t)
	apiClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	c := &defaultingObserver{Client: apiClient}
	testContext, cancelTest := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelTest()
	config := &yanetv1alpha1.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: "config"}, Spec: minimalConfig()}
	config.Spec.Patches = nil
	yanet := reviewYanet()
	yanet.UID = ""
	for _, obj := range []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: yanet.Namespace}}, reviewNode(), config, yanet} {
		if err := c.Create(testContext, obj); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("numa-bound", func(t *testing.T) {
		// This API server has the CRDs but no admission webhooks: exercise the
		// stored schema boundary independently of the Go validators and renderer.
		before := &yanetv1alpha1.YanetConfig{}
		key := client.ObjectKeyFromObject(config)
		if err := apiClient.Get(testContext, key, before); err != nil {
			t.Fatal(err)
		}
		candidate := before.DeepCopy()
		four := int32(4)
		candidate.Spec.Components.Controlplane.Numa = &four
		if err := apiClient.Update(testContext, candidate, client.DryRunAll); err != nil {
			t.Fatalf("CRD must accept NUMA=4: %v", err)
		}
		if candidate.Spec.Components.Controlplane.Numa == nil || *candidate.Spec.Components.Controlplane.Numa != 4 {
			t.Fatal("API server must preserve accepted NUMA=4")
		}
		candidate = before.DeepCopy()
		five := int32(5)
		candidate.Spec.Components.Controlplane.Numa = &five
		updateErr := apiClient.Update(testContext, candidate, client.DryRunAll)
		if !apierrors.IsInvalid(updateErr) {
			t.Fatalf("CRD must reject NUMA=5 as Invalid, got %v", updateErr)
		}
		var statusErr *apierrors.StatusError
		if !errors.As(updateErr, &statusErr) || statusErr.ErrStatus.Details == nil {
			t.Fatalf("expected field validation details, got %v", updateErr)
		}
		foundNumaCause := false
		for _, cause := range statusErr.ErrStatus.Details.Causes {
			if cause.Field == "spec.components.controlplane.numa" && cause.Type == metav1.CauseTypeFieldValueInvalid {
				foundNumaCause = true
			}
		}
		if !foundNumaCause {
			t.Fatalf("expected NUMA field validation cause, got %+v", statusErr.ErrStatus.Details.Causes)
		}
		after := &yanetv1alpha1.YanetConfig{}
		if err := apiClient.Get(testContext, key, after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("NUMA boundary requests changed stored config")
		}
	})
	snapshot := &yanetv1alpha1.MutexYanetConfigSpec{}
	r := &YanetReconciler{Client: c, Scheme: scheme, GlobalConfig: snapshot}
	cr := &YanetConfigReconciler{Client: c, APIReader: apiClient, Scheme: scheme, GlobalConfig: snapshot}
	reconcileConfig := func(t *testing.T) {
		t.Helper()
		if _, err := cr.Reconcile(testContext, ctrl.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	reconcile := func(t *testing.T) {
		t.Helper()
		if _, err := reviewReconcile(testContext, r, yanet); err != nil {
			t.Fatal(err)
		}
	}
	deployments := func(t *testing.T) []appsv1.Deployment {
		t.Helper()
		list := &appsv1.DeploymentList{}
		if err := c.List(testContext, list); err != nil {
			t.Fatal(err)
		}
		return list.Items
	}
	controlplane := func(t *testing.T) appsv1.Deployment {
		t.Helper()
		list := &appsv1.DeploymentList{}
		if err := c.List(testContext, list, client.InNamespace(yanet.Namespace),
			client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 {
			t.Fatalf("expected exactly one controlplane Deployment, got %d", len(list.Items))
		}
		return list.Items[0]
	}
	services := func(t *testing.T) []corev1.Service {
		t.Helper()
		list := &corev1.ServiceList{}
		if err := c.List(testContext, list, client.InNamespace(yanet.Namespace)); err != nil {
			t.Fatal(err)
		}
		return list.Items
	}
	checkVersions := func(t *testing.T, before []appsv1.Deployment) {
		t.Helper()
		for _, dep := range before {
			got := &appsv1.Deployment{}
			if err := c.Get(testContext, client.ObjectKeyFromObject(&dep), got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != dep.ResourceVersion {
				t.Errorf("no-op changed Deployment %s RV %s -> %s", dep.Name, dep.ResourceVersion, got.ResourceVersion)
			}
		}
	}
	checkSynced := func(t *testing.T) {
		t.Helper()
		got := &yanetv1alpha1.Yanet{}
		if err := c.Get(testContext, client.ObjectKeyFromObject(yanet), got); err != nil {
			t.Fatal(err)
		}
		if len(got.Status.Sync.Synced) != 2 || len(got.Status.Sync.OutOfSync) != 0 || len(got.Status.Sync.SyncWaiting) != 0 {
			t.Errorf("expected both Deployments synced: %+v", got.Status.Sync)
		}
	}
	reconcileConfig(t)
	reconcile(t)
	if len(deployments(t)) != 2 || len(services(t)) != 1 {
		t.Fatal("expected two Deployments and one shared Service")
	}
	t.Run("no-op", func(t *testing.T) {
		before, beforeServices := deployments(t), services(t)
		c.writes = 0
		reconcileConfig(t)
		reconcile(t)
		checkVersions(t, before)
		if services(t)[0].ResourceVersion != beforeServices[0].ResourceVersion {
			t.Error("no-op changed Service resourceVersion")
		}
		if c.writes != 0 {
			t.Errorf("no-op persisted %d updates", c.writes)
		}
		checkSynced(t)
	})
	t.Run("no-throttle", func(t *testing.T) {
		snapshot.Lock.Lock()
		snapshot.Config.UpdateWindow = 3600
		snapshot.Lock.Unlock()
		r.lock.Lock()
		r.lastUpdateTS, r.lastUpdateHost = time.Now(), "another-node"
		r.lock.Unlock()
		result, err := reviewReconcile(testContext, r, yanet)
		if err != nil || result.RequeueAfter != 0 {
			t.Errorf("no-op throttled: %+v, %v", result, err)
		}
		checkSynced(t)
		snapshot.Lock.Lock()
		snapshot.Config.UpdateWindow = 0
		snapshot.Lock.Unlock()
	})
	t.Run("service-cache-miss-after-create", func(t *testing.T) {
		before := services(t)[0]
		c.writes, c.hideServices = 0, true
		defer func() { c.hideServices = false }()
		reconcileConfig(t)
		if got := services(t)[0]; c.writes != 0 || got.ResourceVersion != before.ResourceVersion {
			t.Fatalf("cache lag must not recreate or rewrite an existing Service: writes=%d, RV=%s -> %s",
				c.writes, before.ResourceVersion, got.ResourceVersion)
		}
	})
	t.Run("autosync-off", func(t *testing.T) {
		if err := c.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
			t.Fatal(err)
		}
		no := false
		yanet.Spec.AutoSync = &no
		if err := c.Update(testContext, yanet); err != nil {
			t.Fatal(err)
		}
		before := deployments(t)
		c.writes = 0
		reconcile(t)
		checkVersions(t, before)
		checkSynced(t)
		if c.writes != 0 {
			t.Errorf("autosync-off persisted %d updates", c.writes)
		}
		snapshot.Lock.Lock()
		snapshot.Config.Components.Controlplane.Image.Tag = "v2"
		snapshot.Lock.Unlock()
		reconcile(t)
		checkVersions(t, before)
		got := &yanetv1alpha1.Yanet{}
		if err := c.Get(testContext, client.ObjectKeyFromObject(yanet), got); err != nil {
			t.Fatal(err)
		}
		if len(got.Status.Sync.OutOfSync) != 1 || c.writes != 0 {
			t.Errorf("report-only real drift: status=%+v writes=%d", got.Status.Sync, c.writes)
		}
		snapshot.Lock.Lock()
		snapshot.Config.Components.Controlplane.Image.Tag = "v1"
		snapshot.Lock.Unlock()
		if err := c.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
			t.Fatal(err)
		}
		yes := true
		yanet.Spec.AutoSync = &yes
		if err := c.Update(testContext, yanet); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("patch-and-removal", func(t *testing.T) {
		config.Spec.Patches = []yanetv1alpha1.NamedPatch{{Name: "defaults", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"revisionHistoryLimit":4,"progressDeadlineSeconds":120,"template":{"spec":{"containers":[{"name":"controlplane","image":"cp:v2","resources":{"limits":{"cpu":"1"}}}]}}}}`)}}}
		config.Spec.BoxTypes[0].Components.Controlplane.Patches = []string{"defaults"}
		if err := c.Update(testContext, config); err != nil {
			t.Fatal(err)
		}
		reconcileConfig(t)
		reconcile(t)
		dep := controlplane(t)
		if dep.Spec.Template.Spec.Containers[0].Image != "cp:v2" || *dep.Spec.RevisionHistoryLimit != 4 ||
			*dep.Spec.ProgressDeadlineSeconds != 120 || dep.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String() != "1" {
			t.Fatalf("patch did not persist: %+v", dep.Spec)
		}
		before := deployments(t)
		c.writes = 0
		reconcile(t)
		checkVersions(t, before)
		if c.writes != 0 {
			t.Errorf("limits-only patch caused %d no-op writes", c.writes)
		}
		config.Spec.Patches = nil
		config.Spec.BoxTypes[0].Components.Controlplane.Patches = nil
		if err := c.Update(testContext, config); err != nil {
			t.Fatal(err)
		}
		reconcileConfig(t)
		reconcile(t)
		dep = controlplane(t)
		if dep.Spec.Template.Spec.Containers[0].Image != "cp:v1" || *dep.Spec.RevisionHistoryLimit != 10 ||
			*dep.Spec.ProgressDeadlineSeconds != 600 || len(dep.Spec.Template.Spec.Containers[0].Resources.Limits) != 0 {
			t.Fatalf("removal did not restore defaults: %+v", dep.Spec)
		}
	})
	t.Run("dry-run-failure", func(t *testing.T) {
		before := deployments(t)
		c.writes = 0
		c.dryRuns = 0
		failure := errors.New("dry-run API unavailable")
		c.dryRunError = failure
		defer func() { c.dryRunError = nil }()
		_, err := reviewReconcile(testContext, r, yanet)
		if !errors.Is(err, failure) {
			t.Errorf("expected propagated dry-run error, got %v", err)
		}
		if c.writes != 0 || c.dryRuns == 0 {
			t.Errorf("writes=%d dryRuns=%d", c.writes, c.dryRuns)
		}
		checkVersions(t, before)
	})
}
