package controller

import (
	"context"
	"fmt"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Online policy starts with a new installation UID, not an in-place conversion
// of readers which may still boot from legacy credentials.
func (r *YanetReconciler) verifyNewOnlineScope(ctx context.Context, y *api.Yanet) error {
	if y.Annotations[policyStateAnnotation] != "" {
		return fmt.Errorf("online gateway policy requires a fresh installation; legacy policy is configured")
	}
	live := &appsv1.DeploymentList{}
	if err := r.liveReader().List(ctx, live, client.InNamespace(y.Namespace)); err != nil {
		return err
	}
	for i := range live.Items {
		d := &live.Items[i]
		if controlledByYanet(d, y) {
			return fmt.Errorf("online gateway policy requires a fresh installation; deployment %s already exists without online policy state", d.Name)
		}
	}
	return nil
}

// Dropping the server profile must not silently restart a surviving endpoint
// without its operator-managed authorization.
func (r *YanetReconciler) verifyOnlinePolicyTargets(ctx context.Context, y *api.Yanet, workloads map[string][]renderedWorkload) error {
	desired := map[string]*appsv1.Deployment{}
	for _, node := range workloads {
		for _, workload := range node {
			desired[workload.deployment.Name] = workload.deployment
		}
	}
	live := &appsv1.DeploymentList{}
	if err := r.liveReader().List(ctx, live, client.InNamespace(y.Namespace)); err != nil {
		return err
	}
	for i := range live.Items {
		old := &live.Items[i]
		next := desired[old.Name]
		if next == nil || !controlledByYanet(old, y) {
			continue
		}
		previous := manifests.OnlinePolicyTargets(old)
		current := manifests.OnlinePolicyTargets(next)
		pod := &next.Spec.Template.Spec
		for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
			for _, c := range containers {
				if previous[c.Name] && !current[c.Name] {
					return fmt.Errorf("cannot remove online policy from surviving container %s in %s", c.Name, old.Name)
				}
			}
		}
	}
	return nil
}
