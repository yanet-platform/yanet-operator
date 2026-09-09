package v2alpha1

import (
	"encoding/json"
	"fmt"
)

// ValidateOperatorPlacement also guards reconciliation when admission is bypassed.
func ValidateOperatorPlacement(placement OperatorPlacement) error {
	switch placement {
	case "", OperatorPlacementStandalone, OperatorPlacementDataplane:
		return nil
	default:
		return fmt.Errorf("unsupported operator placement %q", placement)
	}
}

// ValidateOperatorListeners rejects ambiguous or unknown listener contracts.
func ValidateOperatorListeners(operator *OperatorSpec) error {
	if operator.Listeners == nil {
		return nil
	}
	seen := map[OperatorListener]bool{}
	for _, listener := range *operator.Listeners {
		if listener != "grpc" && listener != "http" || seen[listener] {
			return fmt.Errorf("operator %q has unsupported or duplicate listener %q", operator.Name, listener)
		}
		seen[listener] = true
	}
	return nil
}

// ValidateColocatedOperatorPatch permits only container/volume-scoped changes.
// Pod and Deployment settings cannot be composed into another workload without
// changing their meaning. Reject them rather than silently discarding them.
func ValidateColocatedOperatorPatch(raw []byte) error {
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
			return fmt.Errorf("colocated operator patches support only spec.template.spec containers, initContainers and volumes")
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
			return fmt.Errorf("colocated operator patch cannot set pod field %q", key)
		}
	}
	return nil
}
