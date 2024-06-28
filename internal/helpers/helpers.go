package helpers

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/names"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// GetPodNames returns the pod names of the array of pods passed in
func GetPodNames(pods []v1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
	}
	return podNames
}
func GetLabeledNodes(nodeList *v1.NodeList) []v1.Node {
	var labeledNodes []v1.Node
	for _, node := range nodeList.Items {
		if len(node.Labels) > 0 {
			labeledNodes = append(labeledNodes, node)
		}
	}
	return labeledNodes
}

func GetTypeOpts(opts yanetv1alpha1.EnabledOpts, t string) (bool, yanetv1alpha1.DepOpts) {
	switch t {
	case names.Release:
		return true, opts.Release
	case names.FireWall:
		return true, opts.FireWall
	case names.Balancer:
		return true, opts.Balancer
	default:
		return false, yanetv1alpha1.DepOpts{}
	}
}

func UniqueSliceElements[T comparable](inputSlice []T) []T {
	uniqueSlice := make([]T, 0, len(inputSlice))
	seen := make(map[T]bool, len(inputSlice))
	for _, element := range inputSlice {
		if !seen[element] {
			uniqueSlice = append(uniqueSlice, element)
			seen[element] = true
		}
	}
	return uniqueSlice
}

func GetNodeNames(nodeList *v1.NodeList) []string {
	var nodeNames []string
	for _, node := range nodeList.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	return nodeNames
}

// DeploymentDiff make partial diff for deployments
func DeploymentDiff(ctx context.Context, first *appsv1.Deployment, second *appsv1.Deployment) bool {
	Log := log.FromContext(ctx)
	// Check Volumes
	if diff := cmp.Diff(first.Spec.Template.Spec.Volumes, second.Spec.Template.Spec.Volumes); diff != "" {
		Log.Info(fmt.Sprintf("Detect Volumes diff (-want +got):\n%s", diff))
		return true
	}
	// Check containers Spec.Template.Spec.Containers
	if diff := cmp.Diff(first.Spec.Template.Spec.Containers, second.Spec.Template.Spec.Containers); diff != "" {
		Log.Info(fmt.Sprintf("Detect Containers spec diff (-want +got):\n%s", diff))
		return true
	}
	// Check containers Spec.Template.Spec.InitContainers
	if diff := cmp.Diff(first.Spec.Template.Spec.InitContainers, second.Spec.Template.Spec.InitContainers); diff != "" {
		Log.Info(fmt.Sprintf("Detect InitContainers spec diff (-want +got):\n%s", diff))
		return true
	}
	// Check replicas
	if diff := cmp.Diff(first.Spec.Replicas, second.Spec.Replicas); diff != "" {
		Log.Info(fmt.Sprintf("Detect replicas diff (-want +got):\n%s", diff))
		return true
	}
	// Check Object Meta
	if diff := cmp.Diff(first.Spec.Template.ObjectMeta, second.Spec.Template.ObjectMeta); diff != "" {
		Log.Info(fmt.Sprintf("Detect Object Meta diff (-want +got):\n%s", diff))
		return true
	}
	return false
}

func GetNodes(c client.Client) (v1.NodeList, error) {
	nodeList := &v1.NodeList{}
	err := c.List(context.Background(), nodeList)
	if err != nil {
		return *nodeList, err
	}
	return *nodeList, nil
}
