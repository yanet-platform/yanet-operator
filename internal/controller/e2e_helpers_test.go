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
	"fmt"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

// resourceMustParse is a thin wrapper around resource.MustParse used by
// the e2e suites to set node hugepages capacity.
func resourceMustParse(s string) resource.Quantity {
	return resource.MustParse(s)
}

// countDeployments returns the number of Deployments in the given
// namespace. Errors are treated as zero so it can be used directly in
// Eventually/Consistently polling closures.
func countDeployments(ctx context.Context, ns string) int {
	depList := &appsv1.DeploymentList{}
	if err := k8sClient.List(ctx, depList, client.InNamespace(ns)); err != nil {
		return 0
	}
	return len(depList.Items)
}

// cleanupDeployments best-effort deletes every Deployment in ns.
func cleanupDeployments(ctx context.Context, ns string) {
	depList := &appsv1.DeploymentList{}
	if err := k8sClient.List(ctx, depList, client.InNamespace(ns)); err == nil {
		for i := range depList.Items {
			_ = k8sClient.Delete(ctx, &depList.Items[i])
		}
	}
}

// cleanupServices best-effort deletes every Service in ns.
func cleanupServices(ctx context.Context, ns string) {
	svcList := &corev1.ServiceList{}
	if err := k8sClient.List(ctx, svcList, client.InNamespace(ns)); err == nil {
		for i := range svcList.Items {
			_ = k8sClient.Delete(ctx, &svcList.Items[i])
		}
	}
}

// ensureNamespace creates a namespace if it doesn't exist.
func ensureNamespace(ctx context.Context, ns string) {
	namespace := &corev1.Namespace{}
	namespace.Name = ns
	_ = k8sClient.Create(ctx, namespace)
}

// waitForGlobalConfigV1 polls GlobalConfig until it has non-zero UpdateWindow,
// indicating YanetConfigReconciler has updated the snapshot. Use instead of
// time.Sleep for reliable synchronization in tests.
func waitForGlobalConfigV1(timeout time.Duration) error {
	if globalConfig == nil {
		return fmt.Errorf("globalConfig is nil")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		globalConfig.Lock.Lock()
		hasConfig := globalConfig.Config.UpdateWindow > 0
		globalConfig.Lock.Unlock()
		if hasConfig {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for GlobalConfig v1")
}

// waitForGlobalConfigV2 polls GlobalConfigV2 until it has at least one BoxType,
// indicating YanetConfigReconcilerV2 has updated the snapshot.
func waitForGlobalConfigV2(timeout time.Duration) error {
	if globalConfigV2 == nil {
		return fmt.Errorf("globalConfigV2 is nil")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		globalConfigV2.Lock.Lock()
		hasConfig := len(globalConfigV2.Config.BoxTypes) > 0
		globalConfigV2.Lock.Unlock()
		if hasConfig {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for GlobalConfig v2")
}

// cleanupYanetV1 best-effort deletes every v1 Yanet CR in ns. The
// finalizer is removed by the reconciler; here we just issue Delete.
func cleanupYanetV1(ctx context.Context, ns string) {
	list := &yanetv1alpha1.YanetList{}
	if err := k8sClient.List(ctx, list, client.InNamespace(ns)); err == nil {
		for i := range list.Items {
			_ = k8sClient.Delete(ctx, &list.Items[i])
		}
	}
}

// cleanupYanetV2 completes test teardown without relying on garbage collection:
// envtest has neither a Deployment controller nor a garbage-collector controller.
func cleanupYanetV2(ctx context.Context, ns string) {
	Expect(cleanupYanetV2ResourcesForTest(ctx, k8sClient, ns)).To(Succeed())
}

func cleanupYanetV2ResourcesForTest(ctx context.Context, c client.Client, ns string) error {
	list := &yanetv2alpha1.YanetV2List{}
	if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
		return err
	}
	for i := range list.Items {
		if err := c.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
			return false, err
		}
		if len(list.Items) == 0 {
			return true, nil
		}
		deployments := &appsv1.DeploymentList{}
		if err := c.List(ctx, deployments, client.InNamespace(ns)); err != nil {
			return false, err
		}
		for i := range list.Items {
			yanet := &list.Items[i]
			pending := false
			for j := range deployments.Items {
				deployment := &deployments.Items[j]
				if !controlledByYanetV2(deployment, yanet) {
					continue
				}
				pending = true
				if err := c.Delete(ctx, deployment, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
					return false, err
				}
				if err := removeFinalizerForEnvtest(ctx, c, deployment, metav1.FinalizerDeleteDependents); err != nil {
					return false, err
				}
			}
			if !pending {
				configMaps := &corev1.ConfigMapList{}
				if err := c.List(ctx, configMaps, client.InNamespace(ns)); err != nil {
					return false, err
				}
				for j := range configMaps.Items {
					cm := &configMaps.Items[j]
					if controlledByYanetV2(cm, yanet) {
						if err := c.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
							return false, err
						}
					}
				}
				// Only teardown removes the operator's finalizer explicitly; the
				// production reconciler must still wait for real foreground GC.
				if err := removeFinalizerForEnvtest(ctx, c, yanet, yanetFinalizer); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	})
}

func removeFinalizerForEnvtest(ctx context.Context, c client.Client, object client.Object, finalizer string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := object.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, client.ObjectKeyFromObject(object), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.GetDeletionTimestamp().IsZero() || !controllerutil.ContainsFinalizer(fresh, finalizer) {
			return nil
		}
		controllerutil.RemoveFinalizer(fresh, finalizer)
		if err := c.Update(ctx, fresh); !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	})
}
