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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
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

func TestYanetConfigReconcileV2IsolatesServiceApplyFailures(t *testing.T) {
	ctx := context.Background()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfigV2(),
	}
	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
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
		&yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "broken"},
			Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
		},
		&yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "healthy"},
			Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
		},
	).Build()
	r := &YanetConfigReconcilerV2{
		Client: cl, Scheme: scheme, GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err == nil {
		t.Fatal("foreign Service must produce a retryable reconciliation error")
	}
	if err := cl.Get(ctx, client.ObjectKey{
		Name: foreign.Name, Namespace: "healthy",
	}, &corev1.Service{}); err != nil {
		t.Fatalf("unrelated Service must still converge: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(stale), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unrelated orphan must still be pruned: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(protected), &corev1.Service{}); err != nil {
		t.Fatalf("failed scope must retain its previous Services: %v", err)
	}
	gotForeign := &corev1.Service{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(foreign), gotForeign); err != nil {
		t.Fatalf("foreign Service must remain: %v", err)
	}
	if metav1.GetControllerOf(gotForeign) != nil {
		t.Fatal("foreign Service was adopted")
	}
}

func TestYanetConfigReconcileV2PreservesAmbiguousServiceAcrossBoxTypes(t *testing.T) {
	ctx := context.Background()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfigV2(),
	}
	for _, name := range []string{"b-c", "c"} {
		config.Spec.Components.Operators = append(config.Spec.Components.Operators, yanetv2alpha1.OperatorSpec{
			Name: name,
			Containers: []yanetv2alpha1.OperatorContainer{{
				Name: name, Image: yanetv2alpha1.ImageRef{Name: "operator", Tag: "v1"},
			}},
		})
	}
	config.Spec.BoxTypes[0].Name = "a"
	config.Spec.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"b-c": {}}
	config.Spec.BoxTypes = append(config.Spec.BoxTypes, yanetv2alpha1.BoxType{
		Name: "a-b", Components: config.Spec.BoxTypes[0].Components,
		Operators: map[string]yanetv2alpha1.BoxOperator{"c": {}},
	})
	first := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "a"},
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, first).Build()
	r := &YanetConfigReconcilerV2{
		Client: cl, Scheme: scheme, GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	key := client.ObjectKey{Name: "yanet-a-b-c", Namespace: "yanet"}
	previous := &corev1.Service{}
	if err := cl.Get(ctx, key, previous); err != nil {
		t.Fatalf("get initial Service: %v", err)
	}
	second := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "yanet"},
		Spec:       yanetv2alpha1.YanetSpec{BoxType: "a-b"},
	}
	if err := cl.Create(ctx, second); err != nil {
		t.Fatalf("create colliding installation: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err == nil || !strings.Contains(err.Error(), "conflicting shared Service plans") {
		t.Fatalf("expected cross-box name collision, got %v", err)
	}
	current := &corev1.Service{}
	if err := cl.Get(ctx, key, current); err != nil {
		t.Fatalf("ambiguous Service must be preserved regardless of planning order: %v", err)
	}
	if current.Spec.Selector[manifests.LabelBoxType] != "a" || current.UID != previous.UID {
		t.Fatalf("ambiguous Service was replaced: %+v", current)
	}
}

func TestYanetConfigReconcileV2PruningChecksObservedServiceVersion(t *testing.T) {
	ctx := context.Background()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfigV2(),
	}
	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
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
	r := &YanetConfigReconcilerV2{
		Client: cl, Scheme: scheme, GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err == nil {
		t.Fatal("stale ownership check must result in a retry, not deletion")
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(stale), &corev1.Service{}); err != nil {
		t.Fatalf("Service whose ownership changed during pruning must remain: %v", err)
	}
}

func TestYanetConfigReconcileV2ScopesNUMAByNamespaceBoxAndMatchingNodes(t *testing.T) {
	ctx := context.Background()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
		Spec:       minimalConfigV2(),
	}
	config.Spec.Components.Operators = []yanetv2alpha1.OperatorSpec{{
		Name: "route", Containers: []yanetv2alpha1.OperatorContainer{{
			Name: "route", Image: yanetv2alpha1.ImageRef{Name: "route", Tag: "v1"},
		}},
	}}
	config.Spec.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"route": {}}
	config.Spec.BoxTypes = append(config.Spec.BoxTypes, yanetv2alpha1.BoxType{
		Name: "other", Components: config.Spec.BoxTypes[0].Components,
	})
	selected := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "selected", Labels: map[string]string{"pool": "", yanetv2alpha1.NFDNumaCountLabel: "2"},
	}}
	missingLabel := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "missing-label", Labels: map[string]string{yanetv2alpha1.NFDNumaCountLabel: "3"},
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
		objects = append(objects, &yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: installation.name, Namespace: installation.namespace},
			Spec: yanetv2alpha1.YanetSpec{
				BoxType: installation.box, NodeSelector: installation.selector,
			},
		})
	}
	scheme := newSchemeForTest(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &YanetConfigReconcilerV2{
		Client: cl, Scheme: scheme, GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
	}
	assertServices := func(want []string) {
		t.Helper()
		services := &corev1.ServiceList{}
		if err := cl.List(ctx, services); err != nil {
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
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	assertServices([]string{
		"left/yanet-other-controlplane-numa0",
		"left/yanet-release-controlplane-numa0",
		"left/yanet-release-controlplane-numa1",
		"left/yanet-release-route",
		"right/yanet-release-controlplane-numa0",
		"right/yanet-release-controlplane-numa1",
		"right/yanet-release-controlplane-numa2",
		"right/yanet-release-route",
	})
	if err := cl.Delete(ctx, selected); err != nil {
		t.Fatalf("delete selected node: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		t.Fatalf("reconcile after node deletion: %v", err)
	}
	assertServices([]string{
		"left/yanet-other-controlplane-numa0",
		"left/yanet-release-controlplane-numa0",
		"left/yanet-release-route",
		"right/yanet-release-controlplane-numa0",
		"right/yanet-release-controlplane-numa1",
		"right/yanet-release-controlplane-numa2",
		"right/yanet-release-route",
	})
}

func TestYanetConfigReconcileV2HonorsStopPublishedBeforeServiceWrite(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
				Spec:       minimalConfigV2(),
			}
			owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
			objects := []client.Object{config}
			if operation != "create" {
				objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{
					Name: "yanet-release-controlplane-numa0", Namespace: "yanet",
					Labels:          map[string]string{manifests.LabelSharedService: "true"},
					OwnerReferences: []metav1.OwnerReference{owner},
				}})
			}
			if operation != "delete" {
				objects = append(objects, &yanetv2alpha1.YanetV2{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "yanet"},
					Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
				})
			}
			snapshot := &yanetv2alpha1.MutexYanetConfigSpec{}
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
			r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if writes != 0 {
				t.Fatalf("stop published during Service read must prevent %s; got %d writes", operation, writes)
			}
		})
	}
}

func TestYanetConfigReconcileV2KeepsDeclaredNetlinkServiceWhenDisabled(t *testing.T) {
	configuredNuma := int32(2)
	for _, tt := range []struct {
		name       string
		numa       *int32
		boxEnabled bool
		wantNuma   int
	}{
		{name: "detected NUMA", boxEnabled: true, wantNuma: 3},
		{name: "configured NUMA and disabled box sidecar", numa: &configuredNuma, wantNuma: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-uid"},
				Spec:       minimalConfigV2(),
			}
			config.Spec.Components.Controlplane.Numa = tt.numa
			config.Spec.Components.Controlplane.DisabledNuma = []int32{0}
			config.Spec.Components.Dataplane.Sidecars = &yanetv2alpha1.DataplaneSidecarsSpec{
				NetlinkDataplaneSidecar: &yanetv2alpha1.DataplaneSidecarSpec{
					Image: yanetv2alpha1.ImageRef{Name: "netlink", Tag: "v1"},
				},
			}
			config.Spec.BoxTypes[0].Components.Dataplane.Sidecars = &yanetv2alpha1.BoxDataplaneSidecars{
				NetlinkDataplaneSidecar: &yanetv2alpha1.BoxDataplaneSidecar{Enabled: &tt.boxEnabled},
			}
			enabled := true
			installation := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "yanet"},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "release",
					Components: &yanetv2alpha1.YanetComponentsOverride{
						Controlplane: &yanetv2alpha1.YanetControlplaneOverride{DisabledNuma: []int32{1}},
						Dataplane: &yanetv2alpha1.YanetComponentOverride{
							Containers: map[string]yanetv2alpha1.YanetContainerOverride{
								yanetv2alpha1.NetlinkDataplaneSidecarContainerName: {Enabled: &enabled},
							},
						},
					},
				},
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "test-node", Labels: map[string]string{yanetv2alpha1.NFDNumaCountLabel: "3"},
			}}
			scheme := newSchemeForTest(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, installation, node).Build()
			r := &YanetConfigReconcilerV2{
				Client: cl, Scheme: scheme, GlobalConfigV2: &yanetv2alpha1.MutexYanetConfigSpec{},
			}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
					t.Fatalf("reconcile shared Services: %v", err)
				}
			}
			key := client.ObjectKey{Name: "yanet-release-netlink-dataplane-sidecar", Namespace: "yanet"}
			getNetlinkService := func() *corev1.Service {
				t.Helper()
				service := &corev1.Service{}
				if err := cl.Get(ctx, key, service); err != nil {
					t.Fatalf("declared netlink Service must exist: %v", err)
				}
				return service
			}
			reconcile()
			reconcile()
			previous := getNetlinkService()
			if err := cl.Get(ctx, client.ObjectKeyFromObject(installation), installation); err != nil {
				t.Fatalf("get installation: %v", err)
			}
			disabled := false
			installation.Spec.Components.Dataplane.Containers[yanetv2alpha1.NetlinkDataplaneSidecarContainerName] =
				yanetv2alpha1.YanetContainerOverride{Enabled: &disabled}
			if err := cl.Update(ctx, installation); err != nil {
				t.Fatalf("disable last netlink sidecar: %v", err)
			}
			reconcile()
			current := getNetlinkService()
			if current.UID != previous.UID || !reflect.DeepEqual(current.Spec, previous.Spec) {
				t.Fatal("disabling the last sidecar must not replace or change the shared Service")
			}
			component, err := helpers.ResolveBoxComponent(&config.Spec, &installation.Spec, helpers.KindDataplane, "")
			if err != nil || component == nil || len(component.NativeSidecars) != 0 {
				t.Fatalf("Service planning must not enable workload sidecars: component=%+v err=%v", component, err)
			}
			controlplane, err := helpers.ResolveBoxComponent(&config.Spec, &installation.Spec, helpers.KindControlplane, "")
			if err != nil {
				t.Fatalf("resolve controlplane: %v", err)
			}
			buildCtx := manifests.BuildContextV2{Namespace: "yanet", BoxType: "release", NumaCount: 3}
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
			if err := cl.List(ctx, services, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil {
				t.Fatalf("list controlplane Services: %v", err)
			}
			if len(services.Items) != tt.wantNuma {
				t.Fatalf("disabled NUMA Services must remain unconditional: got %d, want %d", len(services.Items), tt.wantNuma)
			}
			if err := cl.Delete(ctx, current); err != nil {
				t.Fatalf("delete netlink Service: %v", err)
			}
			reconcile()
			getNetlinkService()
			if err := cl.Get(ctx, client.ObjectKeyFromObject(config), config); err != nil {
				t.Fatalf("get config: %v", err)
			}
			config.Spec.BoxTypes[0].Components.Dataplane.Sidecars.NetlinkDataplaneSidecar = nil
			if err := cl.Update(ctx, config); err != nil {
				t.Fatalf("remove netlink wiring: %v", err)
			}
			reconcile()
			if err := cl.Get(ctx, key, &corev1.Service{}); !apierrors.IsNotFound(err) {
				t.Fatalf("removing box wiring must still prune the netlink Service: %v", err)
			}
		})
	}
}
