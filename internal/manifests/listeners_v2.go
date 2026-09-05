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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	ListenerGRPC = "grpc"
	ListenerHTTP = "http"
	// NetlinkGRPCTargetPort is sidecar-specific so a patched dataplane port
	// cannot capture the Service's named target port.
	NetlinkGRPCTargetPort = "netlink-grpc"

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

	envNetlinkServerEndpoint          = "YANET_SERVER_ENDPOINT"
	envNetlinkServerAdvertiseEndpoint = "YANET_SERVER_ADVERTISE_ENDPOINT"
)

// ListenerPort describes one operator-owned application listener.
type ListenerPort struct {
	Name           string
	TargetPortName string
	ServicePort    int32
	EnvName        string
}

// ListenerPorts returns the fixed listener contract for a workload. The
// dataplane itself and BIRD have no application listener. When present, the
// netlink dataplane sidecar exposes one gRPC listener for gateway callbacks.
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
	case helpers.KindDataplane:
		if hasNativeSidecar(component, yanetv2alpha1.NetlinkDataplaneSidecarContainerName) {
			return []ListenerPort{{
				Name:           ListenerGRPC,
				TargetPortName: NetlinkGRPCTargetPort,
				ServicePort:    ServiceGRPCPort,
				EnvName:        EnvKubernetesGRPCPort,
			}}
		}
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

// ListenerContainerName returns the container that owns the workload's
// application listeners. The dataplane delegates its callback listener to the
// netlink native sidecar. Dynamic operators expose their first container.
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
	if component.Kind == helpers.KindDataplane &&
		hasNativeSidecar(component, yanetv2alpha1.NetlinkDataplaneSidecarContainerName) {
		return yanetv2alpha1.NetlinkDataplaneSidecarContainerName
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
	configureHostNetworkDNS(deployment)
	listeners := ListenerPorts(component)
	if len(listeners) == 0 {
		return nil
	}
	containerName := ListenerContainerName(component)
	container, err := findContainer(deployment, containerName)
	if err != nil {
		return err
	}

	managedPorts := make(map[string]struct{}, len(listeners))
	managedEnv := make(map[string]struct{}, len(listeners))
	ports := make([]corev1.ContainerPort, 0, len(container.Ports)+len(listeners))
	env := make([]corev1.EnvVar, 0, len(container.Env)+len(listeners))
	for _, listener := range listeners {
		targetPortName := listenerTargetPortName(listener)
		if owner := findPortNameOwner(deployment, containerName, targetPortName); owner != "" {
			return fmt.Errorf(
				"deployment %s listener target port name %q is also used by container %q",
				deployment.Name,
				targetPortName,
				owner,
			)
		}
		port := listener.ServicePort
		if override := overrides[listener.Name]; override != 0 {
			port = override
		}
		if port <= 0 || port > 65535 {
			return fmt.Errorf("Deployment %s listener %q has invalid port %d", deployment.Name, listener.Name, port)
		}
		managedPorts[targetPortName] = struct{}{}
		managedEnv[listener.EnvName] = struct{}{}
		ports = append(ports, corev1.ContainerPort{
			Name:          targetPortName,
			ContainerPort: port,
			Protocol:      corev1.ProtocolTCP,
		})
		env = append(env, corev1.EnvVar{Name: listener.EnvName, Value: strconv.FormatInt(int64(port), 10)})
	}
	if component.Kind == helpers.KindDataplane {
		boxType := deployment.Spec.Template.Labels[labelBoxType]
		managedEnv[envNetlinkServerEndpoint] = struct{}{}
		managedEnv[envNetlinkServerAdvertiseEndpoint] = struct{}{}
		env = append(env,
			corev1.EnvVar{
				Name:  envNetlinkServerEndpoint,
				Value: "[::]:$(" + EnvKubernetesGRPCPort + ")",
			},
			corev1.EnvVar{
				Name: envNetlinkServerAdvertiseEndpoint,
				Value: fmt.Sprintf(
					"%s:%d",
					SharedServiceName(boxType, yanetv2alpha1.NetlinkDataplaneSidecarContainerName, nil),
					ServiceGRPCPort,
				),
			},
		)
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

func configureHostNetworkDNS(deployment *appsv1.Deployment) {
	if deployment == nil || !deployment.Spec.Template.Spec.HostNetwork {
		return
	}
	if deployment.Spec.Template.Spec.DNSPolicy == "" ||
		deployment.Spec.Template.Spec.DNSPolicy == corev1.DNSClusterFirst {
		deployment.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
	}
}

func findContainer(deployment *appsv1.Deployment, name string) (*corev1.Container, error) {
	if deployment == nil {
		return nil, fmt.Errorf("cannot find listener container %q in a nil Deployment", name)
	}
	var found *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == name {
			found = &deployment.Spec.Template.Spec.Containers[i]
		}
	}
	for i := range deployment.Spec.Template.Spec.InitContainers {
		if deployment.Spec.Template.Spec.InitContainers[i].Name == name {
			if found != nil {
				return nil, fmt.Errorf("deployment %s has duplicate listener container %q", deployment.Name, name)
			}
			found = &deployment.Spec.Template.Spec.InitContainers[i]
		}
	}
	if found == nil {
		return nil, fmt.Errorf("deployment %s has no listener container %q", deployment.Name, name)
	}
	return found, nil
}

func findPortNameOwner(deployment *appsv1.Deployment, listenerContainer, portName string) string {
	find := func(containers []corev1.Container) string {
		for i := range containers {
			container := &containers[i]
			if container.Name == listenerContainer {
				continue
			}
			for j := range container.Ports {
				if container.Ports[j].Name == portName {
					return container.Name
				}
			}
		}
		return ""
	}
	if owner := find(deployment.Spec.Template.Spec.Containers); owner != "" {
		return owner
	}
	return find(deployment.Spec.Template.Spec.InitContainers)
}

func listenerTargetPortName(listener ListenerPort) string {
	if listener.TargetPortName != "" {
		return listener.TargetPortName
	}
	return listener.Name
}

func hasNativeSidecar(component *helpers.ResolvedComponent, name string) bool {
	if component == nil {
		return false
	}
	for i := range component.NativeSidecars {
		if component.NativeSidecars[i].Name == name {
			return true
		}
	}
	return false
}

// IsManagedListener reports whether a named ContainerPort belongs to the fixed
// application-listener contract for component.
func IsManagedListener(component *helpers.ResolvedComponent, name string) bool {
	for _, listener := range ListenerPorts(component) {
		if listenerTargetPortName(listener) == name {
			return true
		}
	}
	return false
}
