package manifests

import (
	"fmt"
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	operatorContainerPrefix  = "op-"
	operatorMembershipPrefix = "yanet.yanet-platform.io/operator-"
)

// OperatorMembershipLabel identifies an enabled colocated role independently of
// the dataplane's immutable Deployment selector. Its value is the full role
// name, so a hash collision across config revisions cannot select another role.
// Disabled roles have no label.
func OperatorMembershipLabel(operator string) string {
	return operatorMembershipPrefix + shortHashStr(operator)
}

func scopedOperatorName(operator, name string) string {
	result := operatorContainerPrefix + shortHashStr(operator) + "-" + name
	if len(result) > 63 {
		result = strings.TrimRight(result[:54], "-") + "-" + shortHashStr(result)
	}
	return result
}

func colocatedTargetPort(operator, listener string) string {
	return operatorContainerPrefix + shortHashStr(operator) + "-" + listener[:1]
}

// RenderDeployments is the shared preflight/apply rendering path. Colocated
// operators use the existing operator/config renderer, but never become their
// own Deployment. Operator patches address logical names before namespacing;
// dataplane patches run after composition, with ownership restored afterwards.
func RenderDeployments(ctx BuildContextV2, component *helpers.ResolvedComponent, registry PatchRegistry) ([]*appsv1.Deployment, error) {
	if component.IsColocated() {
		return nil, nil
	}
	deployments, err := BuildDeployments(ctx, component)
	if err != nil {
		return nil, err
	}
	for _, deployment := range deployments {
		for _, operator := range component.ColocatedOperators {
			if err := composeOperator(ctx, deployment, operator, registry); err != nil {
				return nil, fmt.Errorf("compose operator %q: %w", operator.Name, err)
			}
		}
		identity := CaptureWorkloadIdentity(deployment)
		if err := ApplyPatches(deployment, component.Patches, registry); err != nil {
			return nil, err
		}
		RestoreWorkloadIdentity(deployment, identity)
		if len(component.ColocatedOperators) > 0 && deployment.Spec.Template.Spec.HostNetwork {
			return nil, fmt.Errorf("dataplane-placed operators require a private network namespace; hostNetwork is unsupported")
		}
		if err := ConfigureListeners(deployment, component, nil); err != nil {
			return nil, err
		}
		if err := ValidatePodContainerNames(deployment); err != nil {
			return nil, err
		}
		if len(component.ColocatedOperators) > 0 || component.Kind == helpers.KindOperator {
			if err := ValidateComposedPod(deployment); err != nil {
				return nil, err
			}
		}
		if len(component.ColocatedOperators) > 0 {
			if err := validateReservedColocatedPorts(deployment, component); err != nil {
				return nil, err
			}
		}
	}
	return deployments, nil
}

func composeOperator(ctx BuildContextV2, deployment *appsv1.Deployment, operator *helpers.ResolvedComponent, registry PatchRegistry) error {
	for _, name := range operator.Patches {
		patch, ok := registry[name]
		if !ok {
			return fmt.Errorf("patch %q is not defined", name)
		}
		if err := api.ValidateColocatedOperatorPatch(patch.Patch.Raw); err != nil {
			return fmt.Errorf("patch %q: %w", name, err)
		}
	}
	op := buildOperator(ctx, operator)
	if err := ApplyPatches(op, operator.Patches, registry); err != nil {
		return err
	}
	// Every declared container must remain in its original group/order. A patch
	// may add regular containers, which also become owned restartable sidecars.
	declared := make(map[string]int, len(operator.Containers))
	for i, container := range operator.Containers {
		declared[container.Name] = i
	}
	next := 0
	for _, container := range op.Spec.Template.Spec.Containers {
		if index, ok := declared[container.Name]; ok {
			if index != next {
				return fmt.Errorf("patch changed declared container order")
			}
			next++
		}
	}
	if next != len(declared) {
		return fmt.Errorf("patch removed a declared operator container")
	}
	if err := configureComponentListeners(op, operator, nil); err != nil {
		return err
	}
	if err := rewriteColocatedProbePorts(op, operator); err != nil {
		return err
	}
	if err := ValidateComposedPod(op); err != nil {
		return err
	}
	if !operator.Enabled {
		return nil
	}
	pod := &op.Spec.Template.Spec
	visitResourceFieldRefs(pod, func(ref *corev1.ResourceFieldSelector) {
		if ref.ContainerName != "" {
			ref.ContainerName = scopedOperatorName(operator.Name, ref.ContainerName)
		}
	})
	for _, volume := range pod.Volumes {
		volume.Name = scopedOperatorName(operator.Name, volume.Name)
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)
	}
	// Config downloader init containers keep their semantics and precede this
	// operator's native sidecars. HostIPC/shmem mounts come from buildOperator.
	for group, containers := range [][]corev1.Container{pod.InitContainers, pod.Containers} {
		for _, container := range containers {
			container.Name = scopedOperatorName(operator.Name, container.Name)
			if group == 1 {
				always := corev1.ContainerRestartPolicyAlways
				container.RestartPolicy = &always
			}
			for i := range container.VolumeMounts {
				container.VolumeMounts[i].Name = scopedOperatorName(operator.Name, container.VolumeMounts[i].Name)
			}
			for i := range container.VolumeDevices {
				container.VolumeDevices[i].Name = scopedOperatorName(operator.Name, container.VolumeDevices[i].Name)
			}
			deployment.Spec.Template.Spec.InitContainers = append(deployment.Spec.Template.Spec.InitContainers, container)
		}
	}
	deployment.Spec.Template.Labels[OperatorMembershipLabel(operator.Name)] = operator.Name
	return nil
}

// ValidateComposedPod checks identities/references before any resource writes.
// Numeric conflicts between concurrently running containers are additionally
// checked by the controller's shared Pod/host-port validator.
func ValidateComposedPod(deployment *appsv1.Deployment) error {
	if err := ValidatePodContainerNames(deployment); err != nil {
		return err
	}
	if problems := k8svalidation.IsDNS1123Subdomain(deployment.Name); len(problems) > 0 {
		return fmt.Errorf("invalid Deployment name %q: %v", deployment.Name, problems)
	}
	pod := &deployment.Spec.Template.Spec
	if len(pod.Containers) == 0 {
		return fmt.Errorf("deployment %q has no application containers", deployment.Name)
	}
	volumes := map[string]bool{}
	for _, volume := range pod.Volumes {
		if problems := k8svalidation.IsDNS1123Label(volume.Name); len(problems) > 0 || volumes[volume.Name] {
			return fmt.Errorf("invalid or duplicate volume name %q", volume.Name)
		}
		volumes[volume.Name] = true
	}
	ports := map[string]bool{}
	containerNames := map[string]bool{}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			containerNames[container.Name] = true
			if container.RestartPolicy != nil && *container.RestartPolicy != corev1.ContainerRestartPolicyAlways {
				return fmt.Errorf("container %q has unsupported restartPolicy %q", container.Name, *container.RestartPolicy)
			}
			if problems := k8svalidation.IsDNS1123Label(container.Name); len(problems) > 0 {
				return fmt.Errorf("invalid container name %q", container.Name)
			}
			for _, mount := range container.VolumeMounts {
				if !volumes[mount.Name] {
					return fmt.Errorf("container %q references missing volume %q", container.Name, mount.Name)
				}
			}
			for _, device := range container.VolumeDevices {
				if !volumes[device.Name] {
					return fmt.Errorf("container %q references missing volume device %q", container.Name, device.Name)
				}
			}
			for _, port := range container.Ports {
				if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP && port.Protocol != corev1.ProtocolUDP && port.Protocol != corev1.ProtocolSCTP {
					return fmt.Errorf("container %q has invalid port protocol %q", container.Name, port.Protocol)
				}
				if port.ContainerPort < 1 || port.ContainerPort > 65535 || port.HostPort < 0 || port.HostPort > 65535 {
					return fmt.Errorf("container %q has invalid port %d/%d", container.Name, port.ContainerPort, port.HostPort)
				}
				if port.Name != "" {
					if problems := k8svalidation.IsValidPortName(port.Name); len(problems) > 0 || ports[port.Name] {
						return fmt.Errorf("invalid or duplicate port name %q", port.Name)
					}
					ports[port.Name] = true
				}
			}
			colocated := deployment.Spec.Template.Labels[labelComponent] == string(helpers.KindDataplane) &&
				strings.HasPrefix(container.Name, operatorContainerPrefix)
			if err := validateContainerProbePorts(&container, colocated); err != nil {
				return err
			}
		}
	}
	var referenceErr error
	visitResourceFieldRefs(pod, func(ref *corev1.ResourceFieldSelector) {
		if ref.ContainerName != "" && !containerNames[ref.ContainerName] {
			referenceErr = fmt.Errorf("resourceFieldRef refers to missing container %q", ref.ContainerName)
		}
	})
	return referenceErr
}

func visitResourceFieldRefs(pod *corev1.PodSpec, visit func(*corev1.ResourceFieldSelector)) {
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for i := range containers {
			for _, variable := range containers[i].Env {
				if source := variable.ValueFrom; source != nil && source.ResourceFieldRef != nil {
					visit(source.ResourceFieldRef)
				}
			}
		}
	}
	items := func(items []corev1.DownwardAPIVolumeFile) {
		for _, item := range items {
			if item.ResourceFieldRef != nil {
				visit(item.ResourceFieldRef)
			}
		}
	}
	for _, volume := range pod.Volumes {
		if volume.DownwardAPI != nil {
			items(volume.DownwardAPI.Items)
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.DownwardAPI != nil {
					items(source.DownwardAPI.Items)
				}
			}
		}
	}
}

func validateReservedColocatedPorts(deployment *appsv1.Deployment, component *helpers.ResolvedComponent) error {
	owners := map[int32]string{8080: api.NetlinkDataplaneSidecarContainerName, 8081: api.NetlinkDataplaneSidecarContainerName}
	for _, operator := range component.ColocatedOperators {
		owner := scopedOperatorName(operator.Name, operator.Containers[0].Name)
		offset, err := colocatedPortOffset(operator.PortIndex)
		if err != nil {
			return err
		}
		base := ServiceGRPCPort + offset
		owners[base], owners[base+1] = owner, owner
	}
	for _, containers := range [][]corev1.Container{deployment.Spec.Template.Spec.Containers, deployment.Spec.Template.Spec.InitContainers} {
		for _, container := range containers {
			for _, port := range container.Ports {
				if owner, reserved := owners[port.ContainerPort]; reserved && owner != container.Name {
					return fmt.Errorf("port %d is reserved for %q, not %q", port.ContainerPort, owner, container.Name)
				}
			}
		}
	}
	return nil
}
