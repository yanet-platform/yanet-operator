package manifests

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func visitContainerPortActions(container *corev1.Container, visit func(*intstr.IntOrString, string)) {
	action := func(http *corev1.HTTPGetAction, tcp *corev1.TCPSocketAction) {
		if http != nil {
			visit(&http.Port, http.Host)
		}
		if tcp != nil {
			visit(&tcp.Port, tcp.Host)
		}
	}
	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		if probe != nil {
			action(probe.HTTPGet, probe.TCPSocket)
		}
	}
	if container.Lifecycle != nil {
		for _, hook := range []*corev1.LifecycleHandler{container.Lifecycle.PostStart, container.Lifecycle.PreStop} {
			if hook != nil {
				action(hook.HTTPGet, hook.TCPSocket)
			}
		}
	}
}

// Validate explicit references after patches without rewriting application ports.
func validateContainerProbePorts(container *corev1.Container) error {
	names := map[string]bool{}
	for _, port := range container.Ports {
		if port.Protocol == "" || port.Protocol == corev1.ProtocolTCP {
			names[port.Name] = true
		}
	}
	var portErr error
	visitContainerPortActions(container, func(port *intstr.IntOrString, _ string) {
		if port.Type == intstr.String {
			if port.StrVal == "" || !names[port.StrVal] {
				portErr = fmt.Errorf("container %q probe/lifecycle port %q does not name a TCP port in that container", container.Name, port.StrVal)
			}
		} else if port.IntVal < 1 || port.IntVal > 65535 {
			portErr = fmt.Errorf("container %q probe/lifecycle has invalid port %d", container.Name, port.IntVal)
		}
	})
	if portErr != nil {
		return portErr
	}
	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		if probe != nil && probe.GRPC != nil {
			port := probe.GRPC.Port
			if port < 1 || port > 65535 {
				return fmt.Errorf("container %q gRPC probe has invalid port %d", container.Name, port)
			}
		}
	}
	return nil
}

// Restartable sidecars overlap application containers and subsequent init
// containers. One-shot init containers do not overlap each other or the app.
func validateConcurrentPodPorts(pod *corev1.PodSpec) error {
	type portKey struct {
		number   int32
		protocol corev1.Protocol
	}
	key := func(port corev1.ContainerPort) portKey {
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		return portKey{number: port.ContainerPort, protocol: protocol}
	}
	check := func(occupied map[portKey]string, container corev1.Container) error {
		for _, port := range container.Ports {
			if owner, found := occupied[key(port)]; found && owner != container.Name {
				return fmt.Errorf("containers %q and %q run concurrently and use the same %s port %d", owner, container.Name, key(port).protocol, port.ContainerPort)
			}
		}
		return nil
	}
	reserve := func(occupied map[portKey]string, container corev1.Container) {
		for _, port := range container.Ports {
			occupied[key(port)] = container.Name
		}
	}
	persistent, sidecars := map[portKey]string{}, map[portKey]string{}
	for _, container := range pod.Containers {
		if err := check(persistent, container); err != nil {
			return err
		}
		reserve(persistent, container)
	}
	for _, container := range pod.InitContainers {
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			if err := check(persistent, container); err != nil {
				return err
			}
			reserve(persistent, container)
			reserve(sidecars, container)
		} else if err := check(sidecars, container); err != nil {
			return err
		}
	}
	return nil
}
