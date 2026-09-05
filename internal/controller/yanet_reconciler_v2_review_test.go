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
	"reflect"
	"strings"
	"testing"
	"time"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func reviewYanetV2() *yanetv2alpha1.YanetV2 {
	autoSync := true
	return &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "y", Namespace: "yanet", UID: "owner", Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{
			BoxType: "release", AutoSync: &autoSync, NodeSelector: map[string]string{"pool": "edge"},
		},
	}
}

func reviewNodeV2() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "test-node", Labels: map[string]string{"pool": "edge"},
	}}
}

func reviewReconcileV2(ctx context.Context, r *YanetV2Reconciler, yanet *yanetv2alpha1.YanetV2) (ctrl.Result, error) {
	return r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)})
}

func TestReconcileV2Review_PruneRequiresControllerOwnership(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		name := "steady-state"
		if deleting {
			name = "deletion"
		}
		t.Run(name, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			if deleting {
				now := metav1.Now()
				yanet.DeletionTimestamp = &now
			}
			owner := yanetV2OwnerReferenceForTest()
			oldOwner := owner
			oldOwner.UID = "previous-instance"
			foreignOwner := owner
			foreignOwner.APIVersion = "example.com/v1"
			v1Owner := owner
			v1Owner.APIVersion = "yanet.yanet-platform.io/v1alpha1"
			v1Owner.Kind = "Yanet"
			objects := []client.Object{yanet}
			for i, refs := range [][]metav1.OwnerReference{nil, {oldOwner}, {foreignOwner}, {v1Owner}, {owner}} {
				meta := metav1.ObjectMeta{
					Name: "resource-" + string(rune('a'+i)), Namespace: yanet.Namespace,
					Labels: map[string]string{manifests.LabelYanet: yanet.Name}, OwnerReferences: refs,
				}
				objects = append(objects, &appsv1.Deployment{ObjectMeta: meta}, &corev1.ConfigMap{ObjectMeta: meta})
			}
			r, snapshot := makeReconcilerEnv(t, objects...)
			snapshot.Config = minimalConfigV2()
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			for i, object := range objects[1:] {
				err := r.Get(testContext, client.ObjectKeyFromObject(object), object)
				if i < 8 && err != nil {
					t.Errorf("foreign %T %s must survive: %v", object, object.GetName(), err)
				}
				if i >= 8 && !apierrors.IsNotFound(err) {
					t.Errorf("owned %T %s must be pruned: %v", object, object.GetName(), err)
				}
			}
		})
	}
}

func TestReconcileV2Review_SelectorChangeKeepsIncumbentClaim(t *testing.T) {
	testContext := context.Background()
	current := reviewYanetV2()
	incumbent := reviewYanetV2()
	incumbent.Name = "incumbent"
	incumbent.Namespace = "other-namespace"
	incumbent.UID = "incumbent-uid"
	incumbent.Spec.NodeSelector = map[string]string{"pool": "different"}
	owner := yanetV2OwnerReferenceForTest()
	owner.Name, owner.UID = incumbent.Name, incumbent.UID
	workload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "incumbent-dataplane", Namespace: incumbent.Namespace,
		Labels:          map[string]string{manifests.LabelYanet: incumbent.Name, manifests.LabelNode: "test-node"},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	r, snapshot := makeReconcilerEnv(t, current, incumbent, workload, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	result, err := reviewReconcileV2(testContext, r, current)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("expected a retry while incumbent workloads remain: %+v, %v", result, err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(testContext, deployments, client.InNamespace(current.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("selector change must not start a second dataplane before incumbent cleanup: %d Deployments", len(deployments.Items))
	}
}

func TestReconcileV2Review_EmptySelectorValueRequiresLabel(t *testing.T) {
	testContext := context.Background()
	current := reviewYanetV2()
	other := reviewYanetV2()
	other.Name = "older"
	other.UID = "other-uid"
	other.CreationTimestamp = metav1.NewTime(time.Unix(1, 0))
	current.CreationTimestamp = metav1.NewTime(time.Unix(2, 0))
	other.Spec.NodeSelector = map[string]string{"missing-label": ""}
	r, snapshot := makeReconcilerEnv(t, current, other, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	if result, err := reviewReconcileV2(testContext, r, current); err != nil || result.RequeueAfter != 0 {
		t.Fatalf("absent label must not match empty-valued selector: %+v, %v", result, err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(testContext, deployments, client.InNamespace(current.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(deployments.Items) != 2 {
		t.Fatalf("unrelated selector prevented rollout: got %d Deployments", len(deployments.Items))
	}
}

func TestReconcileV2Review_DeletionWaitsForForegroundCleanup(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	now := metav1.Now()
	yanet.DeletionTimestamp = &now
	workload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "old-dataplane", Namespace: yanet.Namespace, UID: "deployment-uid",
		Labels:          map[string]string{manifests.LabelYanet: yanet.Name},
		OwnerReferences: []metav1.OwnerReference{yanetV2OwnerReferenceForTest()},
		Finalizers:      []string{"example.com/wait-for-pods"},
	}}
	r, snapshot := makeReconcilerEnv(t, yanet, workload)
	snapshot.Config = minimalConfigV2()
	deletes := 0
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			if _, ok := obj.(*appsv1.Deployment); ok {
				deletes++
				if options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
					t.Error("Deployment cleanup must use foreground propagation")
				}
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != workload.UID {
					t.Error("cleanup must be conditional on the observed Deployment UID")
				}
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	result, err := reviewReconcileV2(testContext, r, yanet)
	if err != nil || result.RequeueAfter == 0 || deletes != 1 {
		t.Fatalf("cleanup must wait and retry: %+v, %v, deletes=%d", result, err, deletes)
	}
	got := &yanetv2alpha1.YanetV2{}
	if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), got); err != nil || len(got.Finalizers) == 0 {
		t.Fatalf("finalizer released before workload termination: %v, %v", got.Finalizers, err)
	}
	if err := r.Get(testContext, client.ObjectKeyFromObject(workload), workload); err != nil {
		t.Fatal(err)
	}
	workload.Finalizers = nil
	if err := r.Update(testContext, workload); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatalf("finish cleanup: %v", err)
	}
	if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), got); !apierrors.IsNotFound(err) {
		t.Fatalf("installation must be released after cleanup: %v", err)
	}
}

func TestReconcileV2Review_FinalizerConflictIsRetried(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	now := metav1.Now()
	yanet.DeletionTimestamp = &now
	r, snapshot := makeReconcilerEnv(t, yanet)
	snapshot.Config = minimalConfigV2()
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return apierrors.NewConflict(schema.GroupResource{Group: yanetv2alpha1.GroupVersion.Group, Resource: "yanetsv2"}, obj.GetName(), errors.New("concurrent finalizer writer"))
		},
	})
	result, err := reviewReconcileV2(testContext, r, yanet)
	if err == nil && result.RequeueAfter == 0 {
		t.Fatal("a finalizer conflict must not acknowledge deletion without a retry")
	}
}

func TestReconcileV2Review_StopObservedDuringWrites(t *testing.T) {
	for _, phase := range []string{"create", "update", "update-retry", "delete", "status"} {
		t.Run(phase, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
			snapshot.Config = minimalConfigV2()
			if phase != "create" {
				if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(phase, "update") {
				snapshot.Config.Components.Controlplane.Image.Tag = "v2"
				snapshot.Config.Components.Dataplane.Image.Tag = "v2"
			}
			if phase == "delete" {
				if err := r.Delete(testContext, yanet); err != nil {
					t.Fatal(err)
				}
			}
			stop := func() {
				snapshot.Lock.Lock()
				snapshot.Config.Stop = true
				snapshot.Lock.Unlock()
			}
			writes := 0
			yanetGets := 0
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					writes++
					err := c.Create(ctx, obj, opts...)
					stop()
					return err
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					writes++
					if phase == "update-retry" {
						stop()
						return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, obj.GetName(), errors.New("concurrent update"))
					}
					err := c.Update(ctx, obj, opts...)
					stop()
					return err
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					writes++
					err := c.Delete(ctx, obj, opts...)
					stop()
					return err
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					err := c.Get(ctx, key, obj, opts...)
					if _, ok := obj.(*yanetv2alpha1.YanetV2); ok && phase == "status" {
						yanetGets++
						// The initial read is followed by the normal stop check; the
						// status read must also be followed by a fresh stop check.
						if current := obj.(*yanetv2alpha1.YanetV2); yanetGets > 1 {
							current.Status.Conditions = nil
							stop()
						}
					}
					return err
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					writes++
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatalf("stopped reconcile: %v", err)
			}
			wantWrites := 1
			if phase == "status" {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatalf("writes after stop observation: got %d total, want %d", writes, wantWrites)
			}
		})
	}
}

func TestReconcileV2Review_ThrottledDeploymentKeepsInlineConfig(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "old content"}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	cms := &corev1.ConfigMapList{}
	if err := r.List(testContext, cms, client.InNamespace(yanet.Namespace)); err != nil || len(cms.Items) != 1 {
		t.Fatalf("initial inline config: %v, %d ConfigMaps", err, len(cms.Items))
	}
	oldKey := client.ObjectKeyFromObject(&cms.Items[0])
	snapshot.Config.Components.Dataplane.Config.Inline = "new content"
	snapshot.Config.UpdateWindow = 3600
	r.lock.Lock()
	r.lastUpdateTS, r.lastUpdateHost = time.Now(), "another-node"
	r.lock.Unlock()
	result, err := reviewReconcileV2(testContext, r, yanet)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("expected throttled update: %+v, %v", result, err)
	}
	if err := r.Get(testContext, oldKey, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("throttled Deployment must retain its mounted ConfigMap: %v", err)
	}
}

func TestReconcileV2Review_NoOpDoesNotWrite(t *testing.T) {
	for _, withNode := range []bool{false, true} {
		name := "no-nodes"
		if withNode {
			name = "with-node"
		}
		t.Run(name, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			objects := []client.Object{yanet}
			if withNode {
				objects = append(objects, reviewNodeV2())
			}
			r, snapshot := makeReconcilerEnv(t, objects...)
			snapshot.Config = minimalConfigV2()
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					t.Errorf("no-op issued Update for %T %s", obj, obj.GetName())
					return c.Update(ctx, obj, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					t.Errorf("no-op issued %s Update for %T", sub, obj)
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReconcileV2Review_PodListFailureRetries(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet)
	snapshot.Config = minimalConfigV2()
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				return errors.New("temporary Pod list failure")
			}
			return c.List(ctx, list, opts...)
		},
	})
	result, err := reviewReconcileV2(testContext, r, yanet)
	if err == nil || !strings.Contains(err.Error(), "Pod list failure") {
		t.Fatalf("Pod list failure must be retried, not reported as healthy: %+v, %v", result, err)
	}
}

func TestReconcileV2Review_InlineConfigRefusesUnownedConfigMap(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "desired content"}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	cms := &corev1.ConfigMapList{}
	if err := r.List(testContext, cms, client.InNamespace(yanet.Namespace)); err != nil || len(cms.Items) != 1 {
		t.Fatalf("expected one inline ConfigMap: %v, count=%d", err, len(cms.Items))
	}
	cm := &cms.Items[0]
	cm.OwnerReferences = nil
	cm.Data["config"] = "foreign content"
	if err := r.Update(testContext, cm); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "controller owner") {
		t.Fatalf("unowned ConfigMap must not be adopted: %v", err)
	}
	if err := r.Get(testContext, client.ObjectKeyFromObject(cm), cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["config"] != "foreign content" || len(cm.OwnerReferences) != 0 {
		t.Fatalf("foreign ConfigMap was changed: %+v", cm)
	}
}

func TestReconcileV2Review_RunningPodKeepsInlineConfig(t *testing.T) {
	for _, source := range []string{"volume", "projected", "env-from", "init-env"} {
		t.Run(source, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "previous-config", Namespace: yanet.Namespace,
				Labels:          map[string]string{manifests.LabelYanet: yanet.Name},
				OwnerReferences: []metav1.OwnerReference{yanetV2OwnerReferenceForTest()},
			}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "previous-pod", Namespace: yanet.Namespace,
				Labels: map[string]string{manifests.LabelYanet: yanet.Name},
			}}
			ref := &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}}
			switch source {
			case "volume":
				pod.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: ref}}}
			case "projected":
				pod.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
						ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: ref.LocalObjectReference},
					}}},
				}}}
			case "env-from":
				pod.Spec.Containers = []corev1.Container{{Name: "test", EnvFrom: []corev1.EnvFromSource{{
					ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: ref.LocalObjectReference},
				}}}}
			case "init-env":
				pod.Spec.InitContainers = []corev1.Container{{Name: "test", Env: []corev1.EnvVar{{
					Name: "CONFIG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: ref.LocalObjectReference, Key: "config",
					}},
				}}}}
			}
			r, snapshot := makeReconcilerEnv(t, yanet, cm, pod)
			snapshot.Config = minimalConfigV2()
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(cm), cm); err != nil {
				t.Fatalf("old Pod still needs ConfigMap: %v", err)
			}
			if err := r.Delete(testContext, pod); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(cm), cm); !apierrors.IsNotFound(err) {
				t.Fatalf("unused ConfigMap must be pruned after the Pod terminates: %v", err)
			}
		})
	}
}

func TestReconcileV2Review_RenderingDoesNotMutateSnapshot(t *testing.T) {
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Dataplane.Sidecars = &yanetv2alpha1.DataplaneSidecarsSpec{
		Bird: &yanetv2alpha1.DataplaneSidecarSpec{Image: yanetv2alpha1.ImageRef{Name: "bird", Tag: "v1"}},
	}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Sidecars = &yanetv2alpha1.BoxDataplaneSidecars{
		Bird: &yanetv2alpha1.BoxDataplaneSidecar{},
	}
	before := snapshot.Config.DeepCopy()
	if _, err := reviewReconcileV2(context.Background(), r, yanet); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, &snapshot.Config) {
		t.Fatal("rendering or applying workloads mutated the shared snapshot")
	}
}

func TestReconcileV2Review_PruneRejectsConcurrentOwnershipChange(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "old-workload", Namespace: yanet.Namespace, UID: "workload-uid",
		Labels:          map[string]string{manifests.LabelYanet: yanet.Name},
		OwnerReferences: []metav1.OwnerReference{yanetV2OwnerReferenceForTest()},
	}}
	r, snapshot := makeReconcilerEnv(t, yanet, deployment)
	snapshot.Config = minimalConfigV2()
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			fresh := &appsv1.Deployment{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
				return err
			}
			fresh.OwnerReferences[0].Name = "another-owner"
			if err := c.Update(ctx, fresh); err != nil {
				return err
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil {
		t.Fatal("ownership change between List and Delete must abort pruning")
	}
	if err := r.Get(testContext, client.ObjectKeyFromObject(deployment), deployment); err != nil {
		t.Fatalf("concurrently reowned Deployment was deleted: %v", err)
	}
}

func TestReconcileV2Review_InlineConfigWaitsForObservedRollout(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "old config"}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	markConverged := func() {
		list := &appsv1.DeploymentList{}
		if err := r.List(testContext, list); err != nil {
			t.Fatal(err)
		}
		for i := range list.Items {
			d := &list.Items[i]
			if d.Generation == 0 {
				d.Generation = 1
				if err := r.Update(testContext, d); err != nil {
					t.Fatal(err)
				}
			}
			d.Status.ObservedGeneration = d.Generation
			d.Status.Replicas, d.Status.UpdatedReplicas = 1, 1
			if err := r.Status().Update(testContext, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	markConverged()
	cms := &corev1.ConfigMapList{}
	if err := r.List(testContext, cms); err != nil || len(cms.Items) != 1 {
		t.Fatalf("expected one inline ConfigMap: %v, count=%d", err, len(cms.Items))
	}
	oldKey := client.ObjectKeyFromObject(&cms.Items[0])
	base := r.Client
	r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
			if _, ok := object.(*appsv1.Deployment); ok {
				// Emulate the apiserver generation bump, leaving the Deployment
				// controller's observed generation at the old template.
				object.SetGeneration(object.GetGeneration() + 1)
			}
			return c.Update(ctx, object, opts...)
		},
	})
	snapshot.Config.Components.Dataplane.Config.Inline = "new config"
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(testContext, oldKey, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("unobserved rollout may still recreate an old Pod even with an empty Pod list: %v", err)
	}
	r.Client = base
	markConverged()
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(testContext, oldKey, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old ConfigMap should be pruned after rollout completion: %v", err)
	}
}

func TestYanetV2EnvtestCleanupCompletesForegroundDeletion(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "owned", Namespace: yanet.Namespace, UID: "deployment-uid",
		Finalizers:      []string{metav1.FinalizerDeleteDependents},
		OwnerReferences: []metav1.OwnerReference{yanetV2OwnerReferenceForTest()},
	}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "owned-config", Namespace: yanet.Namespace, OwnerReferences: []metav1.OwnerReference{yanetV2OwnerReferenceForTest()},
	}}
	foreign := deployment.DeepCopy()
	foreign.Name, foreign.UID = "foreign", "foreign-uid"
	foreign.OwnerReferences[0].UID = "foreign-owner"
	r, _ := makeReconcilerEnv(t, yanet, deployment, cm, foreign)
	if err := cleanupYanetV2ResourcesForTest(testContext, r.Client, yanet.Namespace); err != nil {
		t.Fatalf("envtest cleanup must not wait for an absent garbage collector: %v", err)
	}
	for _, object := range []client.Object{yanet, deployment, cm} {
		if err := r.Get(testContext, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Errorf("owned %T %s not cleaned up: %v", object, object.GetName(), err)
		}
	}
	if err := r.Get(testContext, client.ObjectKeyFromObject(foreign), foreign); err != nil || !foreign.DeletionTimestamp.IsZero() {
		t.Fatalf("test cleanup must not delete a foreign Deployment: %v", err)
	}
}
