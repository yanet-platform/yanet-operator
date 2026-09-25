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

package manifests

import (
	"fmt"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	ListenerGRPC    = "grpc"
	ListenerHTTP    = "http"
	ServiceGRPCPort = api.RuntimeGRPCPort
	ServiceHTTPPort = api.RuntimeHTTPPort
)

// ListenerPort describes Service exposure independently of runtime port slots.
type ListenerPort struct {
	Name           string
	TargetPortName string
	ServicePort    int32
}

// ListenerPorts returns the explicitly declared Service protocols for a role.
func ListenerPorts(component *helpers.ResolvedComponent) []ListenerPort {
	if component == nil || component.Kind == helpers.KindDataplane {
		return nil
	}
	names := component.ListenerNames
	if component.Kind == helpers.KindControlplane {
		names = []string{ListenerGRPC, ListenerHTTP}
	} else if names == nil {
		names = []string{ListenerGRPC}
	}
	var listeners []ListenerPort
	for _, name := range names {
		port := ServiceGRPCPort
		if name == ListenerHTTP {
			port = ServiceHTTPPort
		}
		listener := ListenerPort{Name: name, ServicePort: port}
		if component.IsColocated() {
			listener.TargetPortName = colocatedTargetPort(component.Name, name)
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

// ListenerContainerName returns the logical owner of a role's Service protocols.
func ListenerContainerName(component *helpers.ResolvedComponent) string {
	if component == nil || component.Kind == helpers.KindDataplane {
		return ""
	}
	if component.Kind == helpers.KindOperator {
		if len(component.Containers) == 0 {
			return ""
		}
		return component.Containers[0].Name
	}
	if component.IsColocated() {
		return component.Name
	}
	return toLowerKebab(string(component.Kind))
}

// ConfigureListeners reasserts owned named ports after final Pod patches.
func ConfigureListeners(deployment *appsv1.Deployment, component *helpers.ResolvedComponent) error {
	if err := configureComponentListeners(deployment, component, false); err != nil {
		return err
	}
	for _, sidecar := range component.Sidecars {
		owner := scopedOperatorName(sidecar.Name, sidecar.Name)
		for _, protocol := range []string{ListenerGRPC, ListenerHTTP} {
			if err := validatePortNameOwner(deployment, owner, colocatedTargetPort(sidecar.Name, protocol)); err != nil {
				return err
			}
		}
		if sidecar.Enabled {
			if err := configureComponentListeners(deployment, sidecar, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func configureComponentListeners(deployment *appsv1.Deployment, component *helpers.ResolvedComponent, composed bool) error {
	listeners := ListenerPorts(component)
	if len(listeners) == 0 {
		return nil
	}
	name := ListenerContainerName(component)
	if composed {
		name = scopedOperatorName(component.Name, name)
	}
	container, err := findContainer(deployment, name)
	if err != nil {
		return err
	}
	grpcPort, httpPort, err := runtimePortPair(component)
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	ports := make([]corev1.ContainerPort, 0, len(container.Ports)+len(listeners))
	for _, listener := range listeners {
		target := listenerTargetPortName(listener)
		if err := validatePortNameOwner(deployment, name, target); err != nil {
			return err
		}
		port := grpcPort
		if listener.Name == ListenerHTTP {
			port = httpPort
		}
		owned[target] = true
		ports = append(ports, corev1.ContainerPort{Name: target, ContainerPort: port, Protocol: corev1.ProtocolTCP})
	}
	for _, port := range container.Ports {
		if !owned[port.Name] {
			ports = append(ports, port)
		}
	}
	container.Ports = ports
	return nil
}

func runtimePortPair(component *helpers.ResolvedComponent) (grpc, http int32, err error) {
	offset := int32(0)
	if component.IsColocated() {
		if component.PortIndex < 0 || component.PortIndex >= api.MaxDataplaneSidecars {
			return 0, 0, fmt.Errorf("sidecar %q has invalid port slot %d", component.Name, component.PortIndex)
		}
		offset = 2 * int32(component.PortIndex)
	}
	return ServiceGRPCPort + offset, ServiceHTTPPort + offset, nil
}

func findContainer(deployment *appsv1.Deployment, name string) (*corev1.Container, error) {
	if deployment == nil {
		return nil, fmt.Errorf("cannot find container %q in a nil Deployment", name)
	}
	var found *corev1.Container
	for _, containers := range [][]corev1.Container{deployment.Spec.Template.Spec.Containers, deployment.Spec.Template.Spec.InitContainers} {
		for i := range containers {
			if containers[i].Name != name {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("deployment %s has duplicate container %q", deployment.Name, name)
			}
			found = &containers[i]
		}
	}
	if found == nil {
		return nil, fmt.Errorf("deployment %s has no container %q", deployment.Name, name)
	}
	return found, nil
}

func validatePortNameOwner(deployment *appsv1.Deployment, owner, portName string) error {
	for _, containers := range [][]corev1.Container{deployment.Spec.Template.Spec.Containers, deployment.Spec.Template.Spec.InitContainers} {
		for _, container := range containers {
			if container.Name == owner {
				continue
			}
			for _, port := range container.Ports {
				if port.Name == portName {
					return fmt.Errorf("deployment %s listener target port name %q is reserved for %q, but used by container %q", deployment.Name, portName, owner, container.Name)
				}
			}
		}
	}
	return nil
}

func listenerTargetPortName(listener ListenerPort) string {
	if listener.TargetPortName != "" {
		return listener.TargetPortName
	}
	return listener.Name
}
