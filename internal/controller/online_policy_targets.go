package controller

import (
	"context"
	"fmt"
	"maps"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Node labels also determine exclusivity. Reject a stale render before any
// online policy or workload writes, including when no server profile is wired.
func (r *YanetReconciler) verifyOnlineNodes(ctx context.Context, y *api.Yanet, nodes []corev1.Node) error {
	selected := &corev1.NodeList{}
	if err := r.liveReader().List(ctx, selected, client.MatchingLabels(y.Spec.NodeSelector)); err != nil {
		return err
	}
	if len(selected.Items) != len(nodes) {
		return fmt.Errorf("online policy node selection changed")
	}
	rendered := make(map[string]corev1.Node, len(nodes))
	for _, node := range nodes {
		rendered[node.Name] = node
	}
	for _, node := range selected.Items {
		expected := rendered[node.Name]
		if node.UID == "" || node.UID != expected.UID || !node.DeletionTimestamp.IsZero() ||
			!maps.Equal(node.Labels, expected.Labels) {
			return fmt.Errorf("online policy Node scope changed: %s", node.Name)
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
