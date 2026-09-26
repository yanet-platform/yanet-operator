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
	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// pruneConflictingDeployments removes only this installation's Deployments
// from former nodes or nodes won by another Yanet, and drains remaining owned
// Deployments when explicitly disabled. ConfigMaps remain until a
// later ordinary prune, while deleting the Deployments terminates conflicting
// Pods that still use the node's devices and shared memory.
func (r *YanetReconciler) pruneConflictingDeployments(
	ctx context.Context,
	yanet *yanetv1alpha1.Yanet,
	nodeNames map[string]struct{},
	logger logr.Logger,
) error {
	deployments := &appsv1.DeploymentList{}
	if err := r.Client.List(
		ctx,
		deployments,
		client.InNamespace(yanet.Namespace),
	); err != nil {
		return fmt.Errorf("list conflicting Deployments: %w", err)
	}
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		if !controlledByYanet(deployment, yanet) || !deployment.DeletionTimestamp.IsZero() {
			continue
		}
		if _, conflict := nodeNames[deployment.Labels[manifests.LabelNode]]; !conflict {
			// A blocked acquisition must not prevent an explicit drain of the
			// installation's other nodes. Never render or acquire new workloads here.
			if helpers.BoolValue(yanet.Spec.Enabled, true) || (deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0) {
				continue
			}
			zero := int32(0)
			deployment.Spec.Replicas = &zero
			if err := r.checkGlobalStop(); err != nil {
				return err
			}
			if err := r.Update(ctx, deployment); err != nil {
				return fmt.Errorf("drain Deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
			}
			continue
		}
		logger.Info("deleting Deployment from released or conflicting node",
			"deployment", deployment.Name,
			"node", deployment.Labels[manifests.LabelNode],
		)
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		if err := r.Client.Delete(ctx, deployment,
			client.PropagationPolicy(metav1.DeletePropagationForeground),
			client.Preconditions{UID: &deployment.UID, ResourceVersion: &deployment.ResourceVersion},
		); err != nil {
			if !isNotFoundOrGone(err) {
				return fmt.Errorf("delete conflicting Deployment %s/%s: %w", deployment.Namespace, deployment.Name, err)
			}
			continue
		}
		yanetOrphansPruned.WithLabelValues(yanet.Name, yanet.Namespace).Inc()
	}
	return nil
}

func controlledByYanet(object client.Object, yanet *yanetv1alpha1.Yanet) bool {
	if object.GetNamespace() != yanet.Namespace {
		return false
	}
	owner := metav1.GetControllerOf(object)
	if owner == nil || owner.APIVersion != yanetv1alpha1.GroupVersion.String() ||
		owner.Kind != "Yanet" || owner.Name != yanet.Name {
		return false
	}
	return yanet.UID == "" || owner.UID == yanet.UID
}

// desiredSet groups names of resources the reconciler intends to keep
// for a single Yanet installation. Owned resources that are NOT in this
// set are considered orphans.
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

type pruneResult struct {
	Deleted             int
	RetainedDeployments []appsv1.Deployment
	RetainedConfigMaps  []string
}

// pruneOrphans deletes every Deployment or ConfigMap that
//   - is controlled by this exact Yanet instance, AND
//   - is NOT present in the desired set.
//
// Deployment ownership, not its mutable labels, determines cleanup eligibility.
// ConfigMaps additionally carry LabelYanet=<yanet.Name>.
//
// When autoSync=false, retained orphans are returned for drift reporting.
// Deleted counts only successful deletion requests, not already terminating
// resources. Foreground deletion may still be in progress after a request.
// Errors from individual deletes do not stop the loop; the first is returned.
func (r *YanetReconciler) pruneOrphans(
	ctx context.Context,
	yanet *yanetv1alpha1.Yanet,
	desired desiredSet,
	autoSync bool,
	logger logr.Logger,
) (pruneResult, error) {
	selector := client.MatchingLabels{manifests.LabelYanet: yanet.Name}
	ns := client.InNamespace(yanet.Namespace)

	var firstErr error
	var result pruneResult
	deletionRequested := false

	// Deployments ---------------------------------------------
	deps := &appsv1.DeploymentList{}
	if err := r.Client.List(ctx, deps, ns); err != nil {
		return result, err
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if !controlledByYanet(d, yanet) {
			continue
		}
		if _, keep := desired.Deployments[d.Name]; keep {
			continue
		}
		if !autoSync {
			result.RetainedDeployments = append(result.RetainedDeployments, *d)
			logger.Info("orphan Deployment detected (autoSync=false, not deleting)",
				"deployment", d.Name)
			continue
		}
		if !d.DeletionTimestamp.IsZero() {
			continue
		}
		logger.Info("deleting orphan Deployment", "deployment", d.Name)
		if err := r.checkGlobalStop(); err != nil {
			return result, err
		}
		deletionRequested = true
		if err := r.Client.Delete(ctx, d,
			client.PropagationPolicy(metav1.DeletePropagationForeground),
			client.Preconditions{UID: &d.UID, ResourceVersion: &d.ResourceVersion},
		); err == nil {
			result.Deleted++
		} else if !isNotFoundOrGone(err) {
			logger.Error(err, "delete Deployment failed", "deployment", d.Name)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	// Re-read after deletion so completed foreground cleanup does not keep
	// otherwise unused ConfigMaps alive on the strength of the old snapshot.
	if deletionRequested {
		if err := r.List(ctx, deps, ns); err != nil {
			return result, err
		}
	}

	// ConfigMaps -----------------------------------------------
	cms := &corev1.ConfigMapList{}
	if err := r.Client.List(ctx, cms, ns, selector); err != nil {
		return result, err
	}
	// Desired ConfigMap names alone are insufficient: throttled Deployments
	// and Pods from an unfinished rollout can still mount a previous hash.
	referenced := make(map[string]struct{})
	rolloutPending := false
	for i := range deps.Items {
		d := &deps.Items[i]
		if controlledByYanet(d, yanet) {
			collectPodConfigMapRefs(&d.Spec.Template.Spec, referenced)
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			// Until the Deployment controller has observed and completed the
			// rollout, an old ReplicaSet may still recreate a Pod even when the
			// Pod list is empty. Wait rather than deleting its inline config.
			if !d.DeletionTimestamp.IsZero() || d.Status.ObservedGeneration < d.Generation ||
				d.Status.UpdatedReplicas != replicas || d.Status.Replicas != replicas {
				rolloutPending = true
			}
		}
	}
	if len(cms.Items) > 0 {
		pods := &corev1.PodList{}
		if err := r.List(ctx, pods, ns, selector); err != nil {
			return result, err
		}
		for i := range pods.Items {
			collectPodConfigMapRefs(&pods.Items[i].Spec, referenced)
		}
	}
	for i := range cms.Items {
		c := &cms.Items[i]
		if !controlledByYanet(c, yanet) {
			continue
		}
		if _, keep := desired.ConfigMaps[c.Name]; keep {
			continue
		}
		if _, inUse := referenced[c.Name]; inUse {
			continue
		}
		if rolloutPending {
			continue
		}
		if !autoSync {
			result.RetainedConfigMaps = append(result.RetainedConfigMaps, c.Name)
			logger.Info("orphan ConfigMap detected (autoSync=false, not deleting)",
				"configmap", c.Name)
			continue
		}
		if !c.DeletionTimestamp.IsZero() {
			continue
		}
		logger.Info("deleting orphan ConfigMap", "configmap", c.Name)
		if err := r.checkGlobalStop(); err != nil {
			return result, err
		}
		if err := r.Client.Delete(ctx, c,
			client.Preconditions{UID: &c.UID, ResourceVersion: &c.ResourceVersion},
		); err == nil {
			result.Deleted++
		} else if !isNotFoundOrGone(err) {
			logger.Error(err, "delete ConfigMap failed", "configmap", c.Name)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return result, firstErr
}

func collectPodConfigMapRefs(pod *corev1.PodSpec, referenced map[string]struct{}) {
	for _, volume := range pod.Volumes {
		if volume.ConfigMap != nil {
			referenced[volume.ConfigMap.Name] = struct{}{}
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil {
					referenced[source.ConfigMap.Name] = struct{}{}
				}
			}
		}
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			for _, env := range container.EnvFrom {
				if env.ConfigMapRef != nil {
					referenced[env.ConfigMapRef.Name] = struct{}{}
				}
			}
			for _, env := range container.Env {
				if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
					referenced[env.ValueFrom.ConfigMapKeyRef.Name] = struct{}{}
				}
			}
		}
	}
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
