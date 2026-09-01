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

	"github.com/go-logr/logr"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// pruneConflictingDeploymentsV2 removes only this installation's Deployments
// from nodes won by another YanetV2. ConfigMaps are harmless and remain until a
// later ordinary prune, while deleting the Deployments terminates conflicting
// host-network Pods.
func (r *YanetV2Reconciler) pruneConflictingDeploymentsV2(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	nodeNames map[string]struct{},
	logger logr.Logger,
) error {
	deployments := &appsv1.DeploymentList{}
	if err := r.Client.List(
		ctx,
		deployments,
		client.InNamespace(yanet.Namespace),
		client.MatchingLabels{manifests.LabelYanet: yanet.Name},
	); err != nil {
		return fmt.Errorf("list conflicting Deployments: %w", err)
	}
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		if _, conflict := nodeNames[deployment.Labels[manifests.LabelNode]]; !conflict ||
			!controlledByYanetV2(deployment, yanet) {
			continue
		}
		logger.Info("deleting Deployment from node claimed by another YanetV2",
			"deployment", deployment.Name,
			"node", deployment.Labels[manifests.LabelNode],
		)
		if err := r.Client.Delete(ctx, deployment); err != nil && !isNotFoundOrGone(err) {
			return fmt.Errorf("delete conflicting Deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
		}
		yanetOrphansPruned.WithLabelValues(yanet.Name, yanet.Namespace).Inc()
	}
	return nil
}

func controlledByYanetV2(object client.Object, yanet *yanetv2alpha1.YanetV2) bool {
	owner := metav1.GetControllerOf(object)
	if owner == nil || owner.APIVersion != yanetv2alpha1.GroupVersion.String() ||
		owner.Kind != "YanetV2" || owner.Name != yanet.Name {
		return false
	}
	return yanet.UID == "" || owner.UID == yanet.UID
}

// desiredSet groups names of resources the reconciler intends to keep
// for a single YanetV2 installation. Anything labelled as ours that is
// NOT in this set is considered an orphan.
type desiredSet struct {
	Deployments map[string]struct{}
	ConfigMaps  map[string]struct{}
}

// newDesiredSet returns an empty desiredSet ready to be populated.
func newDesiredSet() desiredSet {
	return desiredSet{
		Deployments: map[string]struct{}{},
		ConfigMaps:  map[string]struct{}{},
	}
}

// pruneOrphans deletes every Deployment or ConfigMap that
//   - carries the LabelYanet=<yanet.Name> label, AND
//   - is NOT present in the desired set.
//
// When autoSync=false the helper is a no-op for safety: orphans are
// just counted (the caller may surface the count via a metric or
// condition). When autoSync=true the helper performs the deletes.
//
// Returns the number of resources that were (or would have been)
// deleted. Errors from individual deletes are logged but do not stop
// the loop; the first error is returned at the end.
func (r *YanetV2Reconciler) pruneOrphans(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	desired desiredSet,
	autoSync bool,
	logger logr.Logger,
) (int, error) {
	selector := client.MatchingLabels{manifests.LabelYanet: yanet.Name}
	ns := client.InNamespace(yanet.Namespace)

	var firstErr error
	count := 0

	// Deployments ---------------------------------------------
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(ctx, deps, ns, selector); err != nil {
		return 0, err
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if _, keep := desired.Deployments[d.Name]; keep {
			continue
		}
		count++
		if !autoSync {
			logger.Info("orphan Deployment detected (autoSync=false, not deleting)",
				"deployment", d.Name)
			continue
		}
		logger.Info("deleting orphan Deployment", "deployment", d.Name)
		if err := r.Client.Delete(ctx, d); err != nil && !isNotFoundOrGone(err) {
			logger.Error(err, "delete Deployment failed", "deployment", d.Name)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	// ConfigMaps -----------------------------------------------
	cms := &corev1.ConfigMapList{}
	if err := r.Client.List(ctx, cms, ns, selector); err != nil {
		return count, err
	}
	for i := range cms.Items {
		c := &cms.Items[i]
		if _, keep := desired.ConfigMaps[c.Name]; keep {
			continue
		}
		count++
		if !autoSync {
			logger.Info("orphan ConfigMap detected (autoSync=false, not deleting)",
				"configmap", c.Name)
			continue
		}
		logger.Info("deleting orphan ConfigMap", "configmap", c.Name)
		if err := r.Client.Delete(ctx, c); err != nil && !isNotFoundOrGone(err) {
			logger.Error(err, "delete ConfigMap failed", "configmap", c.Name)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return count, firstErr
}

// isNotFoundOrGone is true when the error indicates the object is
// already gone (NotFound) or its API version disappeared. Both are
// idempotent successes for delete operations.
func isNotFoundOrGone(err error) bool {
	if err == nil {
		return true
	}
	return apierrors.IsNotFound(err) || apierrors.IsGone(err)
}
