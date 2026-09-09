package controller

import (
	"context"
	"fmt"
	"sort"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// validateOperatorPlacementTransitionV2 refuses cross-workload migrations until
// the previous producer cannot create Pods and every old Pod has terminated.
// Recreate serializes only one Deployment, not a standalone-to-sidecar move.
// A zero desired replica count intentionally bypasses the guard to allow drain.
func (r *YanetV2Reconciler) validateOperatorPlacementTransitionV2(
	ctx context.Context, yanet *api.YanetV2, nodes []corev1.Node, workloads map[string][]renderedWorkloadV2,
) error {
	type role struct {
		name      string
		colocated bool
		node      corev1.Node
	}
	var desired []role
	for _, node := range nodes {
		for _, workload := range workloads[node.Name] {
			if deploymentReplicasAreZero(workload.deployment) {
				continue
			}
			if workload.component.Kind == helpers.KindOperator {
				desired = append(desired, role{name: workload.component.Name, node: node})
			}
			for _, operator := range workload.component.ColocatedOperators {
				if operator.Enabled {
					desired = append(desired, role{name: operator.Name, colocated: true, node: node})
				}
			}
		}
	}
	if len(desired) == 0 {
		return nil
	}
	producers, err := listPlacementProducersV2(ctx, r.Client, yanet.Namespace)
	if err != nil {
		return err
	}
	sort.Slice(desired, func(i, j int) bool {
		return desired[i].node.Name+"/"+desired[i].name < desired[j].node.Name+"/"+desired[j].name
	})
	for _, target := range desired {
		check := func(kind, name string, pod corev1.PodTemplateSpec) error {
			matches := podMayUseNodeV2(&pod.Spec, &target.node)
			if pod.Spec.NodeName == "" && pod.Labels[manifests.LabelNode] != "" {
				matches = pod.Labels[manifests.LabelNode] == target.node.Name
			}
			if pod.Labels[manifests.LabelYanet] != yanet.Name || !matches {
				return nil
			}
			if incompatibleOperatorPlacementV2(pod.Labels, target.name, target.colocated) {
				return fmt.Errorf("unsafe operator placement migration for %q on node %s: old producer in %s %s; explicitly drain (YanetV2 spec.enabled=false with autoSync=true), wait for zero Deployment/ReplicaSet replicas and terminated Pods, then enable the new placement",
					target.name, target.node.Name, kind, name)
			}
			return nil
		}
		for _, producer := range producers {
			if err := check(producer.kind, producer.name, producer.template); err != nil {
				return err
			}
		}
	}
	return nil
}

type placementProducerV2 struct {
	kind     string
	name     string
	template corev1.PodTemplateSpec
}

// Both migration gates use the same definition of drain, without calling each
// other's controller or relying on desired enablement/autoSync flags. Pending
// scale-downs, old ReplicaSets and terminating nonterminal Pods still produce.
func listPlacementProducersV2(ctx context.Context, reader client.Reader, namespace string) ([]placementProducerV2, error) {
	deps := &appsv1.DeploymentList{}
	sets := &appsv1.ReplicaSetList{}
	pods := &corev1.PodList{}
	for _, list := range []client.ObjectList{deps, sets, pods} {
		if err := reader.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("list live workloads in namespace %q before operator placement migration: %w", namespace, err)
		}
	}
	var producers []placementProducerV2
	for _, deployment := range deps.Items {
		if !deploymentReplicasAreZero(&deployment) || deployment.Status.Replicas != 0 || deployment.Status.ObservedGeneration < deployment.Generation {
			producers = append(producers, placementProducerV2{kind: "Deployment", name: deployment.Name, template: deployment.Spec.Template})
		}
	}
	for _, set := range sets.Items {
		if set.Spec.Replicas == nil || *set.Spec.Replicas != 0 || set.Status.Replicas != 0 || set.Status.ObservedGeneration < set.Generation {
			producers = append(producers, placementProducerV2{kind: "ReplicaSet", name: set.Name, template: set.Spec.Template})
		}
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			producers = append(producers, placementProducerV2{kind: "Pod", name: pod.Name, template: corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec}})
		}
	}
	sort.Slice(producers, func(i, j int) bool {
		return producers[i].kind+"/"+producers[i].name < producers[j].kind+"/"+producers[j].name
	})
	return producers, nil
}

func incompatibleOperatorPlacementV2(labels map[string]string, name string, colocated bool) bool {
	standaloneProducer := labels[manifests.LabelComponent] == name
	colocatedProducer := labels[manifests.OperatorMembershipLabel(name)] == name
	return colocated && standaloneProducer || !colocated && colocatedProducer
}
