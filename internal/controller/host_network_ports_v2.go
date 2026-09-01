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
// available.
func allocateHostNetworkPortsV2(
	workloads []renderedWorkloadV2,
	portRange *yanetv2alpha1.HostNetworkPortRange,
) (listenerPortAssignmentsV2, error) {
	assignments := make(listenerPortAssignmentsV2)
	reserved := make(map[hostPortKey]string)

	for _, workload := range workloads {
		deployment := workload.deployment
		if !deployment.Spec.Template.Spec.HostNetwork {
			continue
		}
		listenerContainer := manifests.ListenerContainerName(workload.component)
		for containerIndex := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[containerIndex]
			for portIndex := range container.Ports {
				port := &container.Ports[portIndex]
				if container.Name == listenerContainer &&
					manifests.IsManagedListener(workload.component, port.Name) {
					continue
				}
				if err := reserveHostPortV2(reserved, deployment.Name, port); err != nil {
					return nil, err
				}
			}
		}
	}

	next := int32(0)
	if portRange != nil {
		next = portRange.Start
	}
	for _, workload := range workloads {
		deployment := workload.deployment
		listeners := manifests.ListenerPorts(workload.component)
		if !deployment.Spec.Template.Spec.HostNetwork || len(listeners) == 0 {
			if err := manifests.ConfigureListeners(deployment, workload.component, nil); err != nil {
				return nil, err
			}
			continue
		}
		if portRange == nil {
			return nil, fmt.Errorf(
				"Deployment %s uses hostNetwork with service-backed listeners but spec.hostNetworkPortRange is not configured",
				deployment.Name,
			)
		}
		deploymentAssignments := make(map[string]int32, len(listeners))
		for _, listener := range listeners {
			for next <= portRange.End {
				key := hostPortKey{port: next, protocol: corev1.ProtocolTCP}
				if _, used := reserved[key]; !used {
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
			reserved[hostPortKey{port: next, protocol: corev1.ProtocolTCP}] = deployment.Name
			next++
		}
		assignments[deployment.Name] = deploymentAssignments
		if err := manifests.ConfigureListeners(deployment, workload.component, deploymentAssignments); err != nil {
			return nil, err
		}
	}
	return assignments, nil
}

func reserveHostPortV2(
	reserved map[hostPortKey]string,
	deploymentName string,
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
	if previous, conflict := reserved[key]; conflict && previous != deploymentName {
		return fmt.Errorf(
			"Deployments %s and %s use hostNetwork with the same %s port %d",
			previous,
			deploymentName,
			protocol,
			port.ContainerPort,
		)
	}
	reserved[key] = deploymentName
	return nil
}
