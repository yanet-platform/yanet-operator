package manifests

import (
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// applyInstallationOverrides restores only explicitly selected installation
// fields after palette patches. Unrelated image/resource patches remain intact.
// Both preflight and apply use this final rendering path.
func applyInstallationOverrides(deployment, unpatched *appsv1.Deployment, component *helpers.ResolvedComponent) error {
	if !component.Enabled || component.Overrides != nil && component.Overrides.Enabled != nil {
		deployment.Spec.Replicas = replicasFor(component)
	}
	if component.Overrides != nil {
		for name, override := range component.Overrides.Containers {
			image := component.Image
			declared := component.Kind != helpers.KindOperator
			for _, container := range component.Containers {
				if container.Name == name {
					image = container.Image
					declared = true
					break
				}
			}
			// Palette updates may remove containers while installations retain
			// their overrides. Match effective validation and ignore those entries.
			if !declared {
				continue
			}
			if err := applyImageOverride(deployment, name, image, &override); err != nil {
				return err
			}
		}
	}
	for _, sidecar := range component.Sidecars {
		if sidecar.Enabled {
			if err := applyImageOverride(deployment, scopedOperatorName(sidecar.Name, sidecar.Name), sidecar.Image, sidecar.SidecarOverride); err != nil {
				return err
			}
		}
	}
	if component.Kind == helpers.KindDataplane {
		original := &unpatched.Spec.Template.Spec.Containers[0]
		// An inferred or explicit hugepage reservation owns its resource keys.
		// Remove stale page sizes while preserving ordinary patched resources.
		if hasHugepages(original.Resources.Requests) {
			container, err := findContainer(deployment, api.DataplaneContainerName)
			if err != nil {
				return err
			}
			container.Resources.Requests = restoreHugepages(container.Resources.Requests, original.Resources.Requests)
			container.Resources.Limits = restoreHugepages(container.Resources.Limits, original.Resources.Limits)
		}
	}
	return nil
}

func applyImageOverride(deployment *appsv1.Deployment, name string, image helpers.ResolvedImage, override *api.YanetContainerOverride) error {
	if override == nil || override.Name == "" && override.Tag == "" {
		return nil
	}
	container, err := findContainer(deployment, name)
	if err != nil {
		return err
	}
	container.Image = image.FullPath()
	return nil
}

func hasHugepages(resources corev1.ResourceList) bool {
	for name := range resources {
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			return true
		}
	}
	return false
}

func restoreHugepages(patched, original corev1.ResourceList) corev1.ResourceList {
	if patched == nil {
		patched = make(corev1.ResourceList)
	}
	for name := range patched {
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			delete(patched, name)
		}
	}
	for name, quantity := range original {
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			patched[name] = quantity.DeepCopy()
		}
	}
	return patched
}
