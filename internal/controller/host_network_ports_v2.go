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
	"fmt"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type renderedWorkloadV2 struct {
	deployment *appsv1.Deployment
	component  *helpers.ResolvedComponent
}

// listenerPortAssignmentsV2 is keyed by Deployment name and listener name.
type listenerPortAssignmentsV2 map[string]map[string]int32

// allocateHostNetworkPortsV2 deterministically assigns target ports to the
// service-backed listeners of host-network workloads on one node. Fixed ports
// such as BIRD's BGP/BFD listeners and ports added by patches are reserved
// first, so a range may safely include them as long as enough other ports are
// available. Explicit hostPorts of Pod-network workloads reserve node ports too.
func allocateHostNetworkPortsV2(
	workloads []renderedWorkloadV2,
	portRange *yanetv2alpha1.HostNetworkPortRange,
) (listenerPortAssignmentsV2, error) {
	assignments := make(listenerPortAssignmentsV2)
	reserved := make(map[hostPortKey]hostPortOwnerV2)

	for _, workload := range workloads {
		deployment := workload.deployment
		if deploymentReplicasAreZero(deployment) {
			continue
		}
		if err := validateIntraPodHostPortsV2(deployment, workload.component); err != nil {
			return nil, err
		}
		listenerContainer := manifests.ListenerContainerName(workload.component)
		reserveContainerPorts := func(containers []corev1.Container) error {
			for containerIndex := range containers {
				container := &containers[containerIndex]
				for portIndex := range container.Ports {
					port := container.Ports[portIndex]
					if container.Name == listenerContainer &&
						manifests.IsManagedListener(workload.component, port.Name) {
						continue
					}
					if !deployment.Spec.Template.Spec.HostNetwork {
						port.ContainerPort = port.HostPort
						if port.HostPort > 0 && deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 1 {
							return fmt.Errorf("Deployment %s exposes hostPort %d with %d replicas pinned to one node",
								deployment.Name, port.HostPort, *deployment.Spec.Replicas)
						}
					}
					if err := reserveHostPortV2(reserved, hostPortOwnerV2{
						deployment: deployment.Name,
						container:  container.Name,
					}, &port); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := reserveContainerPorts(deployment.Spec.Template.Spec.Containers); err != nil {
			return nil, err
		}
		if err := reserveContainerPorts(deployment.Spec.Template.Spec.InitContainers); err != nil {
			return nil, err
		}
	}

	next := int32(0)
	if portRange != nil {
		next = portRange.Start
	}
	for _, workload := range workloads {
		deployment := workload.deployment
		listeners := manifests.ListenerPorts(workload.component)
		if !deployment.Spec.Template.Spec.HostNetwork || len(listeners) == 0 ||
			deploymentReplicasAreZero(deployment) {
			if err := manifests.ConfigureListeners(deployment, workload.component, nil); err != nil {
				return nil, err
			}
			if !deploymentReplicasAreZero(deployment) {
				if err := validateIntraPodHostPortsV2(deployment, nil); err != nil {
					return nil, err
				}
			}
			continue
		}
		if portRange == nil {
			return nil, fmt.Errorf(
				"Deployment %s uses hostNetwork with service-backed listeners but spec.hostNetworkPortRange is not configured",
				deployment.Name,
			)
		}
		concurrentPorts := listenerConcurrentHostPortsV2(deployment, workload.component)
		deploymentAssignments := make(map[string]int32, len(listeners))
		for _, listener := range listeners {
			for next <= portRange.End {
				key := hostPortKey{port: next, protocol: corev1.ProtocolTCP}
				owner, used := reserved[key]
				_, localConflict := concurrentPorts[key]
				if !used || owner.deployment == deployment.Name && !localConflict {
					break
				}
				next++
			}
			if next > portRange.End {
				return nil, fmt.Errorf(
					"spec.hostNetworkPortRange %d..%d is exhausted while allocating %s listener %q",
					portRange.Start,
					portRange.End,
					deployment.Name,
					listener.Name,
				)
			}
			deploymentAssignments[listener.Name] = next
			reserved[hostPortKey{port: next, protocol: corev1.ProtocolTCP}] = hostPortOwnerV2{
				deployment: deployment.Name,
				container:  manifests.ListenerContainerName(workload.component),
			}
			next++
		}
		assignments[deployment.Name] = deploymentAssignments
		if err := manifests.ConfigureListeners(deployment, workload.component, deploymentAssignments); err != nil {
			return nil, err
		}
		if err := validateIntraPodHostPortsV2(deployment, nil); err != nil {
			return nil, err
		}
	}
	return assignments, nil
}

func listenerConcurrentHostPortsV2(
	deployment *appsv1.Deployment,
	component *helpers.ResolvedComponent,
) map[hostPortKey]struct{} {
	ports := make(map[hostPortKey]struct{})
	listenerContainer := manifests.ListenerContainerName(component)
	listenerInitIndex := -1
	for i := range deployment.Spec.Template.Spec.InitContainers {
		if deployment.Spec.Template.Spec.InitContainers[i].Name == listenerContainer {
			listenerInitIndex = i
			break
		}
	}
	add := func(container *corev1.Container) {
		for i := range container.Ports {
			port := &container.Ports[i]
			if port.ContainerPort <= 0 || container.Name == listenerContainer &&
				manifests.IsManagedListener(component, port.Name) {
				continue
			}
			protocol := port.Protocol
			if protocol == "" {
				protocol = corev1.ProtocolTCP
			}
			ports[hostPortKey{port: port.ContainerPort, protocol: protocol}] = struct{}{}
		}
	}
	for i := range deployment.Spec.Template.Spec.Containers {
		add(&deployment.Spec.Template.Spec.Containers[i])
	}
	for i := range deployment.Spec.Template.Spec.InitContainers {
		container := &deployment.Spec.Template.Spec.InitContainers[i]
		restartable := container.RestartPolicy != nil &&
			*container.RestartPolicy == corev1.ContainerRestartPolicyAlways
		if restartable || listenerInitIndex >= 0 && i > listenerInitIndex {
			add(container)
		}
	}
	return ports
}

func reserveHostPortV2(
	reserved map[hostPortKey]hostPortOwnerV2,
	owner hostPortOwnerV2,
	port *corev1.ContainerPort,
) error {
	if port.ContainerPort <= 0 {
		return nil
	}
	protocol := port.Protocol
	if protocol == "" {
		protocol = corev1.ProtocolTCP
	}
	key := hostPortKey{port: port.ContainerPort, protocol: protocol}
	if previous, conflict := reserved[key]; conflict && previous.deployment != owner.deployment {
		return fmt.Errorf(
			"containers %s/%s and %s/%s use the same %s port %d on the node",
			previous.deployment,
			previous.container,
			owner.deployment,
			owner.container,
			protocol,
			port.ContainerPort,
		)
	}
	reserved[key] = owner
	return nil
}

func validateIntraPodHostPortsV2(
	deployment *appsv1.Deployment,
	provisionalListeners *helpers.ResolvedComponent,
) error {
	if err := validateConcurrentPortsV2(deployment, provisionalListeners, false); err != nil {
		return err
	}
	if !deployment.Spec.Template.Spec.HostNetwork {
		return validateConcurrentPortsV2(deployment, provisionalListeners, true)
	}
	return nil
}

// Pod-network containers share a Pod port space and may additionally publish
// distinct container ports to the same host port. Check those spaces separately.
func validateConcurrentPortsV2(
	deployment *appsv1.Deployment,
	provisionalListeners *helpers.ResolvedComponent,
	hostPorts bool,
) error {
	persistent := make(map[hostPortKey]string)
	sidecars := make(map[hostPortKey]string)
	listenerContainer := manifests.ListenerContainerName(provisionalListeners)
	provisional := func(container *corev1.Container, port *corev1.ContainerPort) bool {
		return provisionalListeners != nil && container.Name == listenerContainer &&
			manifests.IsManagedListener(provisionalListeners, port.Name)
	}
	portKey := func(port *corev1.ContainerPort) hostPortKey {
		number := port.ContainerPort
		if hostPorts {
			number = port.HostPort
		}
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		return hostPortKey{port: number, protocol: protocol}
	}
	check := func(ports map[hostPortKey]string, container *corev1.Container) error {
		for i := range container.Ports {
			port := &container.Ports[i]
			key := portKey(port)
			if key.port <= 0 || provisional(container, port) {
				continue
			}
			if previous, conflict := ports[key]; conflict && previous != container.Name {
				return fmt.Errorf(
					"containers %s/%s and %s/%s run concurrently and use the same %s port %d (hostPort=%t)",
					deployment.Name,
					previous,
					deployment.Name,
					container.Name,
					key.protocol,
					key.port,
					hostPorts,
				)
			}
		}
		return nil
	}
	reserve := func(ports map[hostPortKey]string, container *corev1.Container) {
		for i := range container.Ports {
			port := &container.Ports[i]
			key := portKey(port)
			if key.port <= 0 || provisional(container, port) {
				continue
			}
			ports[key] = container.Name
		}
	}

	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		if err := check(persistent, container); err != nil {
			return err
		}
		reserve(persistent, container)
	}
	for i := range deployment.Spec.Template.Spec.InitContainers {
		container := &deployment.Spec.Template.Spec.InitContainers[i]
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			if err := check(persistent, container); err != nil {
				return err
			}
			reserve(persistent, container)
			reserve(sidecars, container)
			continue
		}
		// A regular init container overlaps only restartable init containers
		// declared before it, not application containers or later init containers.
		if err := check(sidecars, container); err != nil {
			return err
		}
	}
	return nil
}

type hostPortOwnerV2 struct {
	deployment string
	container  string
}

func deploymentReplicasAreZero(deployment *appsv1.Deployment) bool {
	return deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0
}
