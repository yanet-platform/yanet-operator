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
	"strconv"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	ListenerGRPC = "grpc"
	ListenerHTTP = "http"

	// ServiceGRPCPort and ServiceHTTPPort are the stable cluster contract. They
	// do not change when a host-network Pod receives a different target port.
	ServiceGRPCPort int32 = 8080
	ServiceHTTPPort int32 = 8081

	// EnvKubernetesGRPCPort and EnvKubernetesHTTPPort are injected before user
	// patch env entries. A patch can therefore set a runtime-specific endpoint,
	// for example "[::]:$(YANET_KUBERNETES_GRPC_PORT)", without duplicating the
	// host-network allocator in configuration.
	EnvKubernetesGRPCPort = "YANET_KUBERNETES_GRPC_PORT"
	EnvKubernetesHTTPPort = "YANET_KUBERNETES_HTTP_PORT"
)

// ListenerPort describes one operator-owned application listener.
type ListenerPort struct {
	Name        string
	ServicePort int32
	EnvName     string
}

// ListenerPorts returns the fixed listener contract for a component. The
// dataplane has no application listener and BIRD is addressed directly on the
// host BGP/BFD ports, so neither receives a Kubernetes Service.
func ListenerPorts(component *helpers.ResolvedComponent) []ListenerPort {
	if component == nil {
		return nil
	}
	switch component.Kind {
	case helpers.KindControlplane:
		return []ListenerPort{
			{Name: ListenerGRPC, ServicePort: ServiceGRPCPort, EnvName: EnvKubernetesGRPCPort},
			{Name: ListenerHTTP, ServicePort: ServiceHTTPPort, EnvName: EnvKubernetesHTTPPort},
		}
	case helpers.KindDataplane, helpers.KindBird:
		return nil
	case helpers.KindOperator:
		if component.Name == "metrics" {
			return []ListenerPort{{Name: ListenerHTTP, ServicePort: ServiceHTTPPort, EnvName: EnvKubernetesHTTPPort}}
		}
		return []ListenerPort{{Name: ListenerGRPC, ServicePort: ServiceGRPCPort, EnvName: EnvKubernetesGRPCPort}}
	default:
		return []ListenerPort{{Name: ListenerGRPC, ServicePort: ServiceGRPCPort, EnvName: EnvKubernetesGRPCPort}}
	}
}

// ListenerContainerName returns the container that owns the component's
// application listeners. Dynamic operators expose the first declared
// container; hardcoded components have one container named after their kind.
func ListenerContainerName(component *helpers.ResolvedComponent) string {
	if component == nil {
		return ""
	}
	if component.Kind == helpers.KindOperator {
		if len(component.Containers) == 0 {
			return ""
		}
		return component.Containers[0].Name
	}
	return toLowerKebab(string(component.Kind))
}

// ConfigureListeners reasserts the operator-owned named ContainerPorts after
// strategic patches and injects their effective numeric values for endpoint
// patches. Missing overrides use the stable Service port, which is correct for
// ordinary Pod network namespaces.
func ConfigureListeners(
	deployment *appsv1.Deployment,
	component *helpers.ResolvedComponent,
	overrides map[string]int32,
) error {
	listeners := ListenerPorts(component)
	if len(listeners) == 0 {
		return nil
	}
	containerName := ListenerContainerName(component)
	container := findContainer(deployment, containerName)
	if container == nil {
		return fmt.Errorf("Deployment %s has no listener container %q", deployment.Name, containerName)
	}

	managedPorts := make(map[string]struct{}, len(listeners))
	managedEnv := make(map[string]struct{}, len(listeners))
	ports := make([]corev1.ContainerPort, 0, len(container.Ports)+len(listeners))
	env := make([]corev1.EnvVar, 0, len(container.Env)+len(listeners))
	for _, listener := range listeners {
		port := listener.ServicePort
		if override := overrides[listener.Name]; override != 0 {
			port = override
		}
		if port <= 0 || port > 65535 {
			return fmt.Errorf("Deployment %s listener %q has invalid port %d", deployment.Name, listener.Name, port)
		}
		managedPorts[listener.Name] = struct{}{}
		managedEnv[listener.EnvName] = struct{}{}
		ports = append(ports, corev1.ContainerPort{
			Name:          listener.Name,
			ContainerPort: port,
			Protocol:      corev1.ProtocolTCP,
		})
		env = append(env, corev1.EnvVar{Name: listener.EnvName, Value: strconv.FormatInt(int64(port), 10)})
	}
	for _, port := range container.Ports {
		if _, managed := managedPorts[port.Name]; managed {
			continue
		}
		ports = append(ports, port)
	}
	for _, variable := range container.Env {
		if _, managed := managedEnv[variable.Name]; managed {
			continue
		}
		env = append(env, variable)
	}
	container.Ports = ports
	container.Env = env
	return nil
}

func findContainer(deployment *appsv1.Deployment, name string) *corev1.Container {
	if deployment == nil {
		return nil
	}
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == name {
			return &deployment.Spec.Template.Spec.Containers[i]
		}
	}
	return nil
}

// IsManagedListener reports whether a named ContainerPort belongs to the fixed
// application-listener contract for component.
func IsManagedListener(component *helpers.ResolvedComponent, name string) bool {
	for _, listener := range ListenerPorts(component) {
		if listener.Name == name {
			return true
		}
	}
	return false
}
