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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// cleanupYanetV2 best-effort deletes every v2 YanetV2 CR in ns.
func cleanupYanetV2(ctx context.Context, ns string) {
	list := &yanetv2alpha1.YanetV2List{}
	if err := k8sClient.List(ctx, list, client.InNamespace(ns)); err == nil {
		for i := range list.Items {
			_ = k8sClient.Delete(ctx, &list.Items[i])
		}
	}
}
