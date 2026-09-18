package v2alpha1

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	// RuntimeGRPCPort and RuntimeHTTPPort are fixed Service ports and slot bases.
	RuntimeGRPCPort int32 = 8080
	RuntimeHTTPPort int32 = 8081
	// MaxDataplaneSidecars keeps the last two-port slot within the TCP port range.
	MaxDataplaneSidecars = (65535-int(RuntimeHTTPPort))/2 + 1
)

// ValidateOperatorListeners rejects ambiguous or unknown listener contracts.
func ValidateOperatorListeners(operator *OperatorSpec) error {
	return ValidateListeners(operator.Name, operator.Listeners)
}

// ValidateListeners is shared by standalone operators and single-container sidecars.
func ValidateListeners(name string, listeners *[]OperatorListener) error {
	if listeners == nil {
		return nil
	}
	seen := map[OperatorListener]bool{}
	for _, listener := range *listeners {
		if listener != "grpc" && listener != "http" || seen[listener] {
			return fmt.Errorf("role %q has unsupported or duplicate listener %q", name, listener)
		}
		seen[listener] = true
	}
	return nil
}

// ValidateSidecarPatch permits only container/volume-scoped changes.
// Pod and Deployment settings cannot be composed into another workload without
// changing their meaning. Reject them rather than silently discarding them.
func ValidateSidecarPatch(raw []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	for _, level := range []string{"spec", "template", "spec"} {
		if len(object) == 0 {
			return nil
		}
		child, ok := object[level]
		if !ok || len(object) != 1 || string(child) == "null" {
			return fmt.Errorf("sidecar patches support only spec.template.spec containers, initContainers and volumes")
		}
		object = nil
		if err := json.Unmarshal(child, &object); err != nil {
			return err
		}
	}
	for key, value := range object {
		switch key {
		case "containers", "initContainers", "volumes", "$setElementOrder/containers", "$setElementOrder/initContainers", "$setElementOrder/volumes":
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return fmt.Errorf("invalid %s: %w", key, err)
			}
			seen := map[string]bool{}
			for _, item := range items {
				var name string
				if err := json.Unmarshal(item["name"], &name); err != nil || name == "" || seen[name] {
					return fmt.Errorf("%s requires unique nonempty names", key)
				}
				seen[name] = true
			}
		default:
			return fmt.Errorf("sidecar patch cannot set pod field %q", key)
		}
	}
	return nil
}

// ValidatePrivatePodNetwork enforces v2 networking after all patches.
func ValidatePrivatePodNetwork(pod *corev1.PodSpec) error {
	if pod.HostNetwork {
		return fmt.Errorf("v2 requires a private network namespace; hostNetwork is unsupported")
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			for _, port := range container.Ports {
				if port.HostPort != 0 {
					return fmt.Errorf("v2 container %q must not declare hostPort %d", container.Name, port.HostPort)
				}
			}
		}
	}
	return nil
}
