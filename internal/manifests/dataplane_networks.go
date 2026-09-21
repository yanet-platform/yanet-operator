package manifests

import (
	"encoding/json"
	"fmt"
	"slices"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const multusNetworksAnnotation = "k8s.v1.cni.cncf.io/networks"

type networkSelection struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Interface string `json:"interface"`
}

// configureDataplaneNetworks runs after patches. Declared network fields have
// one owner: patches cannot provide a competing NAD list or device quantities.
func configureDataplaneNetworks(deployment *appsv1.Deployment, component *helpers.ResolvedComponent) error {
	if component.Networks == nil {
		return nil
	}
	if _, exists := deployment.Spec.Template.Annotations[multusNetworksAnnotation]; exists {
		return fmt.Errorf("dataplane networks conflict with a patched %s annotation", multusNetworksAnnotation)
	}
	pod := &deployment.Spec.Template.Spec
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			for _, name := range component.NetworkResources {
				for _, quantities := range []corev1.ResourceList{container.Resources.Requests, container.Resources.Limits} {
					if _, exists := quantities[corev1.ResourceName(name)]; exists {
						return fmt.Errorf("dataplane network resource %q conflicts with patched resources in container %q", name, container.Name)
					}
				}
			}
		}
	}
	networks := slices.Clone(component.Networks)
	for index := range networks {
		if networks[index].Namespace == "" {
			networks[index].Namespace = deployment.Namespace
		}
	}
	if err := api.ValidateNetworkAttachments(networks); err != nil {
		return err
	}
	if len(networks) == 0 {
		return nil
	}
	selections := make([]networkSelection, 0, len(networks))
	counts := make(map[corev1.ResourceName]int64)
	for _, network := range networks {
		selections = append(selections, networkSelection{Name: network.Name, Namespace: network.Namespace, Interface: network.Interface})
		counts[corev1.ResourceName(network.ResourceName)]++
	}
	payload, err := json.Marshal(selections)
	if err != nil {
		return err
	}
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = make(map[string]string)
	}
	deployment.Spec.Template.Annotations[multusNetworksAnnotation] = string(payload)
	resources := &pod.Containers[0].Resources
	if resources.Requests == nil {
		resources.Requests = make(corev1.ResourceList)
	}
	if resources.Limits == nil {
		resources.Limits = make(corev1.ResourceList)
	}
	for name, count := range counts {
		quantity := resource.NewQuantity(count, resource.DecimalSI)
		resources.Requests[name] = quantity.DeepCopy()
		resources.Limits[name] = quantity.DeepCopy()
	}
	return nil
}
