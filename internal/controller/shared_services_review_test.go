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

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestYanetConfigReconcileIsolatesServiceApplyFailures(t *testing.T) {
	testContext := context.Background()
	config := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfig(),
	}
	owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
	foreign := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: "yanet-release-controlplane-numa0", Namespace: "broken",
	}}
	protected := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: "previous-service", Namespace: "broken",
		Labels: map[string]string{
			manifests.LabelSharedService: "true", manifests.LabelBoxType: "release",
		},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	stale := protected.DeepCopy()
	stale.Namespace = "healthy"
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		config, foreign, protected, stale,
		&yanetv1alpha1.Yanet{
			ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "broken"},
			Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
		},
		&yanetv1alpha1.Yanet{
			ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "healthy"},
			Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
		},
	).Build()
	r := &YanetConfigReconciler{
		Client: cl, Scheme: scheme, GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err == nil {
		t.Fatal("foreign Service must produce a retryable reconciliation error")
	}
	if err := cl.Get(testContext, client.ObjectKey{
		Name: foreign.Name, Namespace: "healthy",
	}, &corev1.Service{}); err != nil {
		t.Fatalf("unrelated Service must still converge: %v", err)
	}
	if err := cl.Get(testContext, client.ObjectKeyFromObject(stale), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unrelated orphan must still be pruned: %v", err)
	}
	if err := cl.Get(testContext, client.ObjectKeyFromObject(protected), &corev1.Service{}); err != nil {
		t.Fatalf("failed scope must retain its previous Services: %v", err)
	}
	gotForeign := &corev1.Service{}
	if err := cl.Get(testContext, client.ObjectKeyFromObject(foreign), gotForeign); err != nil {
		t.Fatalf("foreign Service must remain: %v", err)
	}
	if metav1.GetControllerOf(gotForeign) != nil {
		t.Fatal("foreign Service was adopted")
	}
}

func TestYanetConfigReconcilePreservesAmbiguousServiceAcrossBoxTypes(t *testing.T) {
	testContext := context.Background()
	config := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfig(),
	}
	for _, name := range []string{"b-c", "c"} {
		config.Spec.Components.Operators = append(config.Spec.Components.Operators, yanetv1alpha1.OperatorSpec{
			Name: name,
			Containers: []yanetv1alpha1.OperatorContainer{{
				Name: name, Image: yanetv1alpha1.ImageRef{Name: "operator", Tag: "v1"},
			}},
		})
	}
	config.Spec.BoxTypes[0].Name = "a"
	config.Spec.BoxTypes[0].Operators = map[string]yanetv1alpha1.BoxOperator{"b-c": {}}
	config.Spec.BoxTypes = append(config.Spec.BoxTypes, yanetv1alpha1.BoxType{
		Name: "a-b", Components: config.Spec.BoxTypes[0].Components,
		Operators: map[string]yanetv1alpha1.BoxOperator{"c": {}},
	})
	first := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "yanet"},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "a"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, first).Build()
	r := &YanetConfigReconciler{
		Client: cl, Scheme: scheme, GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	key := client.ObjectKey{Name: "yanet-a-b-c", Namespace: "yanet"}
	previous := &corev1.Service{}
	if err := cl.Get(testContext, key, previous); err != nil {
		t.Fatalf("get initial Service: %v", err)
	}
	second := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "yanet"},
		Spec:       yanetv1alpha1.YanetSpec{BoxType: "a-b"},
	}
	if err := cl.Create(testContext, second); err != nil {
		t.Fatalf("create colliding installation: %v", err)
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err == nil || !strings.Contains(err.Error(), "conflicting shared Service plans") {
		t.Fatalf("expected cross-box name collision, got %v", err)
	}
	current := &corev1.Service{}
	if err := cl.Get(testContext, key, current); err != nil {
		t.Fatalf("ambiguous Service must be preserved regardless of planning order: %v", err)
	}
	if current.Spec.Selector[manifests.LabelBoxType] != "a" || current.UID != previous.UID {
		t.Fatalf("ambiguous Service was replaced: %+v", current)
	}
}

func TestYanetConfigReconcilePruningChecksObservedServiceVersion(t *testing.T) {
	testContext := context.Background()
	config := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfig(),
	}
	owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
	stale := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: "stale", Namespace: "yanet", UID: "service-uid",
		Labels:          map[string]string{manifests.LabelSharedService: "true"},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, stale).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				fresh := &corev1.Service{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
					return err
				}
				fresh.OwnerReferences = nil
				if err := c.Update(ctx, fresh); err != nil {
					return err
				}
				options := &client.DeleteOptions{}
				for _, option := range opts {
					option.ApplyToDelete(options)
				}
				if options.Preconditions != nil && options.Preconditions.UID != nil &&
					*options.Preconditions.UID == obj.GetUID() && options.Preconditions.ResourceVersion != nil &&
					*options.Preconditions.ResourceVersion == obj.GetResourceVersion() {
					// Model the API server rejecting deletion after the ownership update.
					return apierrors.NewConflict(schema.GroupResource{Resource: "services"}, obj.GetName(), errConflict("ownership changed"))
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &YanetConfigReconciler{
		Client: cl, Scheme: scheme, GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err == nil {
		t.Fatal("stale ownership check must result in a retry, not deletion")
	}
	if err := cl.Get(testContext, client.ObjectKeyFromObject(stale), &corev1.Service{}); err != nil {
		t.Fatalf("Service whose ownership changed during pruning must remain: %v", err)
	}
}

func TestYanetConfigReconcileScopesNUMAByNamespaceBoxIndependentlyOfNodes(t *testing.T) {
	testContext := context.Background()
	config := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfig(),
	}
	config.Spec.Components.Operators = []yanetv1alpha1.OperatorSpec{{
		Name: "route", Containers: []yanetv1alpha1.OperatorContainer{{
			Name: "route", Image: yanetv1alpha1.ImageRef{Name: "route", Tag: "v1"},
		}},
	}}
	config.Spec.BoxTypes[0].Operators = map[string]yanetv1alpha1.BoxOperator{"route": {}}
	numa := int32(2)
	config.Spec.Components.Controlplane.Numa = &numa
	config.Spec.BoxTypes = append(config.Spec.BoxTypes, yanetv1alpha1.BoxType{
		Name: "other", Components: config.Spec.BoxTypes[0].Components,
	})
	selected := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "selected", Labels: map[string]string{"pool": ""},
	}}
	missingLabel := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "missing-label",
	}}
	objects := []client.Object{config, selected, missingLabel}
	for _, installation := range []struct {
		namespace string
		name      string
		box       string
		selector  map[string]string
	}{
		{namespace: "left", name: "release", box: "release", selector: map[string]string{"pool": ""}},
		{namespace: "right", name: "release", box: "release"},
		{namespace: "left", name: "other", box: "other", selector: map[string]string{"absent": "node"}},
	} {
		objects = append(objects, &yanetv1alpha1.Yanet{
			ObjectMeta: metav1.ObjectMeta{Name: installation.name, Namespace: installation.namespace},
			Spec: yanetv1alpha1.YanetSpec{
				BoxType: installation.box, NodeSelector: installation.selector,
			},
		})
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &YanetConfigReconciler{
		Client: cl, Scheme: scheme, GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
	}
	assertServices := func(want []string) {
		t.Helper()
		services := &corev1.ServiceList{}
		if err := cl.List(testContext, services); err != nil {
			t.Fatalf("list Services: %v", err)
		}
		var names []string
		for _, service := range services.Items {
			names = append(names, service.Namespace+"/"+service.Name)
			if !metav1.IsControlledBy(&service, config) {
				t.Errorf("Service %s must be owned by the config singleton", service.Name)
			}
		}
		sort.Strings(names)
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("unexpected namespace/box/NUMA Service scopes: got %v, want %v", names, want)
		}
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	wantServices := []string{
		"left/yanet-other-controlplane-numa0",
		"left/yanet-other-controlplane-numa1",
		"left/yanet-release-controlplane-numa0",
		"left/yanet-release-controlplane-numa1",
		"left/yanet-release-route",
		"right/yanet-release-controlplane-numa0",
		"right/yanet-release-controlplane-numa1",
		"right/yanet-release-route",
	}
	assertServices(wantServices)
	if err := cl.Delete(testContext, selected); err != nil {
		t.Fatalf("delete selected node: %v", err)
	}
	if _, err := r.Reconcile(testContext, ctrl.Request{}); err != nil {
		t.Fatalf("reconcile after node deletion: %v", err)
	}
	assertServices(wantServices)
}

func TestYanetConfigReconcileHonorsStopPublishedBeforeServiceWrite(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
				Spec:       minimalConfig(),
			}
			owner := *metav1.NewControllerRef(config, yanetv1alpha1.GroupVersion.WithKind("YanetConfig"))
			objects := []client.Object{config}
			if operation != "create" {
				objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{
					Name: "yanet-release-controlplane-numa0", Namespace: "yanet",
					Labels:          map[string]string{manifests.LabelSharedService: "true"},
					OwnerReferences: []metav1.OwnerReference{owner},
				}})
			}
			if operation != "delete" {
				objects = append(objects, &yanetv1alpha1.Yanet{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "yanet"},
					Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
				})
			}
			snapshot := &yanetv1alpha1.MutexYanetConfigSpec{}
			publishStop := func() {
				snapshot.Lock.Lock()
				defer snapshot.Lock.Unlock()
				snapshot.Config.Stop = true
			}
			writes := 0
			scheme := newSchemeForTest(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						err := c.Get(ctx, key, obj, opts...)
						if _, ok := obj.(*corev1.Service); ok {
							publishStop()
						}
						return err
					},
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						err := c.List(ctx, list, opts...)
						if _, ok := list.(*corev1.ServiceList); ok && operation == "delete" {
							publishStop()
						}
						return err
					},
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						writes++
						return c.Create(ctx, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						writes++
						return c.Update(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						writes++
						return c.Delete(ctx, obj, opts...)
					},
				}).Build()
			r := &YanetConfigReconciler{Client: cl, Scheme: scheme, GlobalConfig: snapshot}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if writes != 0 {
				t.Fatalf("stop published during Service read must prevent %s; got %d writes", operation, writes)
			}
		})
	}
}

func TestYanetConfigReconcileKeepsDeclaredNetlinkServiceWhenDisabled(t *testing.T) {
	configuredNuma := int32(2)
	threeNuma := int32(3)
	for _, tt := range []struct {
		name       string
		numa       *int32
		boxEnabled bool
		wantNuma   int
	}{
		{name: "three configured domains", numa: &threeNuma, boxEnabled: true, wantNuma: 3},
		{name: "configured NUMA and disabled box sidecar", numa: &configuredNuma, wantNuma: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testContext := context.Background()
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName, UID: "config-uid"},
				Spec:       minimalConfig(),
			}
			config.Spec.Components.Controlplane.Numa = tt.numa
			config.Spec.Components.Controlplane.DisabledNuma = []int32{0}
			config.Spec.Components.Dataplane.Sidecars = []yanetv1alpha1.SidecarSpec{
				{Name: "netlink-dataplane-sidecar",
					Image: yanetv1alpha1.ImageRef{Name: "netlink", Tag: "v1"},
				},
			}
			config.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]yanetv1alpha1.BoxDataplaneSidecar{
				"netlink-dataplane-sidecar": {Enabled: &tt.boxEnabled},
			}
			enabled := true
			installation := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "yanet"},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType: "release",
					Components: &yanetv1alpha1.YanetComponentsOverride{
						Controlplane: &yanetv1alpha1.YanetControlplaneOverride{DisabledNuma: []int32{1}},
						Dataplane: &yanetv1alpha1.YanetDataplaneOverride{
							Sidecars: map[string]yanetv1alpha1.YanetContainerOverride{
								"netlink-dataplane-sidecar": {Enabled: &enabled},
							},
						},
					},
				},
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			}}
			scheme := newSchemeForTest(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, installation, node).Build()
			r := &YanetConfigReconciler{
				Client: cl, Scheme: scheme, GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
			}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(testContext, ctrl.Request{}); err != nil {
					t.Fatalf("reconcile shared Services: %v", err)
				}
			}
			key := client.ObjectKey{Name: "yanet-release-netlink-dataplane-sidecar", Namespace: "yanet"}
			getNetlinkService := func() *corev1.Service {
				t.Helper()
				service := &corev1.Service{}
				if err := cl.Get(testContext, key, service); err != nil {
					t.Fatalf("declared netlink Service must exist: %v", err)
				}
				return service
			}
			reconcile()
			reconcile()
			previous := getNetlinkService()
			if err := cl.Get(testContext, client.ObjectKeyFromObject(installation), installation); err != nil {
				t.Fatalf("get installation: %v", err)
			}
			disabled := false
			installation.Spec.Components.Dataplane.Sidecars["netlink-dataplane-sidecar"] =
				yanetv1alpha1.YanetContainerOverride{Enabled: &disabled}
			if err := cl.Update(testContext, installation); err != nil {
				t.Fatalf("disable last netlink sidecar: %v", err)
			}
			reconcile()
			current := getNetlinkService()
			if current.UID != previous.UID || !reflect.DeepEqual(current.Spec, previous.Spec) {
				t.Fatal("disabling the last sidecar must not replace or change the shared Service")
			}
			component, err := helpers.ResolveBoxComponent(&config.Spec, &installation.Spec, helpers.KindDataplane, "")
			if err != nil || component == nil || len(component.Sidecars) != 1 || component.Sidecars[0].Enabled {
				t.Fatalf("Service planning must not enable workload sidecars: component=%+v err=%v", component, err)
			}
			controlplane, err := helpers.ResolveBoxComponent(&config.Spec, &installation.Spec, helpers.KindControlplane, "")
			if err != nil {
				t.Fatalf("resolve controlplane: %v", err)
			}
			buildCtx := manifests.BuildContext{Namespace: "yanet", BoxType: "release"}
			deployments, err := manifests.BuildDeployments(buildCtx, controlplane)
			if err != nil || len(deployments) != tt.wantNuma-1 {
				t.Fatalf("workload disabledNUMA override changed: deployments=%d err=%v", len(deployments), err)
			}
			for _, deployment := range deployments {
				if deployment.Labels[manifests.LabelNuma] == "1" {
					t.Fatal("per-installation disabledNUMA must still replace the cluster-wide list")
				}
			}
			services := &corev1.ServiceList{}
			if err := cl.List(testContext, services, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil {
				t.Fatalf("list controlplane Services: %v", err)
			}
			if len(services.Items) != tt.wantNuma {
				t.Fatalf("disabled NUMA Services must remain unconditional: got %d, want %d", len(services.Items), tt.wantNuma)
			}
			if err := cl.Delete(testContext, current); err != nil {
				t.Fatalf("delete netlink Service: %v", err)
			}
			reconcile()
			getNetlinkService()
			if err := cl.Get(testContext, client.ObjectKeyFromObject(config), config); err != nil {
				t.Fatalf("get config: %v", err)
			}
			delete(config.Spec.BoxTypes[0].Components.Dataplane.Sidecars, "netlink-dataplane-sidecar")
			if err := cl.Update(testContext, config); err != nil {
				t.Fatalf("remove netlink wiring: %v", err)
			}
			reconcile()
			if err := cl.Get(testContext, key, &corev1.Service{}); !apierrors.IsNotFound(err) {
				t.Fatalf("removing box wiring must still prune the netlink Service: %v", err)
			}
		})
	}
}
