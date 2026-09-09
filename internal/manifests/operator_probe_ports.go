package manifests

import (
	"fmt"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// rewriteColocatedProbePorts runs once, after the operator's own patches/listener
// configuration and before composition. Only the declared listener owner gets
// logical listener rewrites: kubelet resolves named probes within that container,
// not against another container's ports. No health checks are synthesized.
func rewriteColocatedProbePorts(deployment *appsv1.Deployment, operator *helpers.ResolvedComponent) error {
	container, err := findContainer(deployment, ListenerContainerName(operator))
	if err != nil {
		return err
	}
	names := map[string]string{}
	numbers := map[int32]int32{}
	for _, listener := range ListenerPorts(operator) {
		for _, port := range container.Ports {
			if port.Name == listenerTargetPortName(listener) {
				names[listener.Name] = port.Name
				numbers[listener.ServicePort] = port.ContainerPort
			}
		}
	}
	visitContainerPortActions(container, func(port *intstr.IntOrString, host string) {
		if port.Type == intstr.String {
			if name, managed := names[port.StrVal]; managed {
				*port = intstr.FromString(name)
			}
		} else if number, managed := numbers[port.IntVal]; managed && host == "" {
			// Numeric local HTTP/TCP actions using the logical default listener
			// follow it too. Explicit remote-host numeric actions remain untouched.
			*port = intstr.FromInt32(number)
		}
	})
	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		if probe != nil && probe.GRPC != nil && probe.GRPC.Port == ServiceGRPCPort {
			if number, managed := numbers[ServiceGRPCPort]; managed {
				probe.GRPC.Port = number
			}
		}
	}
	return nil
}

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

// Validate after dataplane patches as well. A malformed name must not hang a
// native startup probe, and a numeric gRPC probe must not hit netlink or a sibling.
func validateContainerProbePorts(container *corev1.Container, colocated bool) error {
	names := map[string]bool{}
	numbers := map[int32]bool{}
	for _, port := range container.Ports {
		if port.Protocol == "" || port.Protocol == corev1.ProtocolTCP {
			names[port.Name] = true
			numbers[port.ContainerPort] = true
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
			if port < 1 || port > 65535 || colocated && !numbers[port] {
				return fmt.Errorf("container %q gRPC probe port %d must reference its own declared TCP listener, not another dataplane container", container.Name, port)
			}
		}
	}
	return nil
}
