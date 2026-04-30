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

package v2alpha1

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// yanetConfigLog is the package-level logger for YanetConfigV2 webhook.
var yanetConfigLog = logf.Log.WithName("yanetconfig-v2-webhook")

// YanetConfigCustomValidator validates a YanetConfigV2 CR against the
// final model: unique names, cross-references between boxTypes /
// patches / operators, and a dry-run of every strategic-merge patch
// against an empty appsv1.Deployment.
//
// The validator does not need a Kubernetes client: a YanetConfigV2 is
// fully self-contained.
type YanetConfigCustomValidator struct{}

var _ admission.Validator[*YanetConfigV2] = &YanetConfigCustomValidator{}

// SetupYanetConfigWebhookWithManager wires the YanetConfigV2 validating
// webhook to the controller manager.
func SetupYanetConfigWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &YanetConfigV2{}).
		WithValidator(&YanetConfigCustomValidator{}).
		Complete()
}

//+kubebuilder:webhook:path=/validate-yanet-yanet-platform-io-v2alpha1-yanetconfigv2,mutating=false,failurePolicy=fail,sideEffects=None,groups=yanet.yanet-platform.io,resources=yanetconfigsv2,verbs=create;update,versions=v2alpha1,name=vyanetconfigv2.kb.io,admissionReviewVersions=v1

// ValidateCreate implements admission.Validator.
func (v *YanetConfigCustomValidator) ValidateCreate(ctx context.Context, cfg *YanetConfigV2) (admission.Warnings, error) {
	yanetConfigLog.Info("validate create", "name", cfg.Name)
	return nil, validateYanetConfig(&cfg.Spec)
}

// ValidateUpdate implements admission.Validator.
func (v *YanetConfigCustomValidator) ValidateUpdate(ctx context.Context, _, cfg *YanetConfigV2) (admission.Warnings, error) {
	yanetConfigLog.Info("validate update", "name", cfg.Name)
	return nil, validateYanetConfig(&cfg.Spec)
}

// ValidateDelete implements admission.Validator. Deletes are always
// allowed.
func (v *YanetConfigCustomValidator) ValidateDelete(ctx context.Context, _ *YanetConfigV2) (admission.Warnings, error) {
	return nil, nil
}

// validateYanetConfig runs the full v2 model check: name uniqueness,
// cross-references, and a strategic-merge dry-run for every patch.
//
// On the first error the function bails out — the caller (admission)
// rejects the request with a single message.
func validateYanetConfig(spec *YanetConfigSpec) error {
	if spec.UpdateWindow < 0 {
		return fmt.Errorf("spec.updateWindow must be >= 0, got %d", spec.UpdateWindow)
	}
	if err := validatePatchUniqueness(spec.Patches); err != nil {
		return err
	}
	if err := validateOperatorUniqueness(spec.Components.Operators); err != nil {
		return err
	}
	if err := validateBoxTypeUniqueness(spec.BoxTypes); err != nil {
		return err
	}
	if err := validateBoxTypeRefs(spec); err != nil {
		return err
	}
	if err := validatePortRanges(&spec.Components); err != nil {
		return err
	}
	if err := dryRunPatches(spec.Patches); err != nil {
		return err
	}
	return nil
}

func validatePatchUniqueness(patches []NamedPatch) error {
	seen := make(map[string]struct{}, len(patches))
	for i := range patches {
		name := patches[i].Name
		if name == "" {
			return fmt.Errorf("spec.patches[%d].name is empty", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("spec.patches[%d].name %q is duplicated", i, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateOperatorUniqueness(ops []OperatorSpec) error {
	seen := make(map[string]struct{}, len(ops))
	for i := range ops {
		name := ops[i].Name
		if name == "" {
			return fmt.Errorf("spec.components.operators[%d].name is empty", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("spec.components.operators[%d].name %q is duplicated", i, name)
		}
		seen[name] = struct{}{}

		containerNames := make(map[string]struct{}, len(ops[i].Containers))
		for j := range ops[i].Containers {
			cname := ops[i].Containers[j].Name
			if cname == "" {
				return fmt.Errorf("spec.components.operators[%d:%s].containers[%d].name is required", i, name, j)
			}
			if _, dup := containerNames[cname]; dup {
				return fmt.Errorf("spec.components.operators[%d:%s].containers[%d].name %q is duplicated", i, name, j, cname)
			}
			containerNames[cname] = struct{}{}
		}
	}
	return nil
}

func validateBoxTypeUniqueness(boxes []BoxType) error {
	seen := make(map[string]struct{}, len(boxes))
	for i := range boxes {
		name := boxes[i].Name
		if name == "" {
			return fmt.Errorf("spec.boxTypes[%d].name is empty", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("spec.boxTypes[%d].name %q is duplicated", i, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// validateBoxTypeRefs ensures every patch name listed in a boxType
// component or operator slot exists in the patch registry, and every
// operator key in box.operators[] exists in components.operators[].
//
// It also enforces the box-shape contract: a box must wire at least
// controlplane and dataplane (other components are optional).
func validateBoxTypeRefs(spec *YanetConfigSpec) error {
	patchSet := make(map[string]struct{}, len(spec.Patches))
	for i := range spec.Patches {
		patchSet[spec.Patches[i].Name] = struct{}{}
	}
	operatorSet := make(map[string]struct{}, len(spec.Components.Operators))
	for i := range spec.Components.Operators {
		operatorSet[spec.Components.Operators[i].Name] = struct{}{}
	}

	for i := range spec.BoxTypes {
		box := &spec.BoxTypes[i]
		path := fmt.Sprintf("spec.boxTypes[%d:%s]", i, box.Name)

		if box.Components.Controlplane == nil {
			return fmt.Errorf("%s.components.controlplane is required", path)
		}
		if box.Components.Dataplane == nil {
			return fmt.Errorf("%s.components.dataplane is required", path)
		}

		// hardcoded slots
		if err := assertPatchesExist(path+".components.controlplane.patches", box.Components.Controlplane.Patches, patchSet); err != nil {
			return err
		}
		if err := assertPatchesExist(path+".components.dataplane.patches", box.Components.Dataplane.Patches, patchSet); err != nil {
			return err
		}
		if box.Components.Bird != nil {
			if err := assertPatchesExist(path+".components.bird.patches", box.Components.Bird.Patches, patchSet); err != nil {
				return err
			}
		}
		if box.Components.BirdAdapter != nil {
			if err := assertPatchesExist(path+".components.birdAdapter.patches", box.Components.BirdAdapter.Patches, patchSet); err != nil {
				return err
			}
		}
		if box.Components.Announcer != nil {
			if err := assertPatchesExist(path+".components.announcer.patches", box.Components.Announcer.Patches, patchSet); err != nil {
				return err
			}
		}
		// operators
		for opName, opSlot := range box.Operators {
			if _, ok := operatorSet[opName]; !ok {
				return fmt.Errorf("%s.operators[%s]: operator is not declared in spec.components.operators", path, opName)
			}
			if err := assertPatchesExist(path+".operators["+opName+"].patches", opSlot.Patches, patchSet); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePortRanges checks that the listen-port intervals of the
// declared components do not overlap. The controlplane occupies
// Port..Port+PortRange-1 (per-NUMA fan-out); every other component
// occupies a single Port. A Port of 0 means the component has no
// listener and is skipped.
func validatePortRanges(comps *ComponentsSpec) error {
	type interval struct {
		path     string
		from, to int32 // inclusive
	}

	cp := comps.Controlplane
	if cp.Port < 0 || cp.Port > 65535 {
		return fmt.Errorf("spec.components.controlplane.port must be in 0..65535, got %d", cp.Port)
	}
	if cp.PortRange < 0 {
		return fmt.Errorf("spec.components.controlplane.portRange must be >= 0, got %d", cp.PortRange)
	}
	if cp.Port > 0 && cp.PortRange > 0 && int64(cp.Port)+int64(cp.PortRange)-1 > 65535 {
		return fmt.Errorf("spec.components.controlplane: port range %d..%d exceeds 65535",
			cp.Port, int64(cp.Port)+int64(cp.PortRange)-1)
	}

	var intervals []interval
	if cp.Port > 0 {
		end := cp.Port
		if cp.PortRange > 1 {
			end = cp.Port + cp.PortRange - 1
		}
		intervals = append(intervals, interval{
			path: "spec.components.controlplane",
			from: cp.Port, to: end,
		})
	}
	add := func(path string, port int32) error {
		if port == 0 {
			return nil
		}
		if port < 0 || port > 65535 {
			return fmt.Errorf("%s.port must be in 0..65535, got %d", path, port)
		}
		intervals = append(intervals, interval{path: path, from: port, to: port})
		return nil
	}
	if err := add("spec.components.dataplane", comps.Dataplane.Port); err != nil {
		return err
	}
	if comps.Bird != nil {
		if err := add("spec.components.bird", comps.Bird.Port); err != nil {
			return err
		}
	}
	if comps.BirdAdapter != nil {
		if err := add("spec.components.birdAdapter", comps.BirdAdapter.Port); err != nil {
			return err
		}
	}
	if comps.Announcer != nil {
		if err := add("spec.components.announcer", comps.Announcer.Port); err != nil {
			return err
		}
	}
	for i := range comps.Operators {
		op := &comps.Operators[i]
		if err := add(fmt.Sprintf("spec.components.operators[%s]", op.Name), op.Port); err != nil {
			return err
		}
	}

	// O(n^2) but n is tiny (≤5+operators).
	for i := range intervals {
		a := intervals[i]
		for j := i + 1; j < len(intervals); j++ {
			b := intervals[j]
			if a.from <= b.to && b.from <= a.to {
				return fmt.Errorf("port overlap between %s (%d..%d) and %s (%d..%d)",
					a.path, a.from, a.to, b.path, b.from, b.to)
			}
		}
	}
	return nil
}

func assertPatchesExist(path string, refs []string, registry map[string]struct{}) error {
	for i, name := range refs {
		if _, ok := registry[name]; !ok {
			return fmt.Errorf("%s[%d]: patch %q is not defined in spec.patches", path, i, name)
		}
	}
	return nil
}

// dryRunPatches verifies that each patch is valid JSON/YAML and that
// it can be merged into an empty appsv1.Deployment via the strategic
// merge algorithm. A failure here means the patch references a field
// that does not exist in appsv1.Deployment.
func dryRunPatches(patches []NamedPatch) error {
	skeleton, err := json.Marshal(&appsv1.Deployment{})
	if err != nil {
		return fmt.Errorf("internal: marshal empty Deployment: %w", err)
	}
	for i := range patches {
		raw := patches[i].Patch.Raw
		if len(raw) == 0 {
			return fmt.Errorf("spec.patches[%d:%s].patch is empty", i, patches[i].Name)
		}
		// runtime.RawExtension stores arbitrary JSON; ensure it
		// parses by re-marshalling.
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			return fmt.Errorf("spec.patches[%d:%s].patch is not valid JSON: %w", i, patches[i].Name, err)
		}
		patchBytes, err := json.Marshal(probe)
		if err != nil {
			return fmt.Errorf("spec.patches[%d:%s].patch re-marshal failed: %w", i, patches[i].Name, err)
		}
		if _, err := strategicpatch.StrategicMergePatch(skeleton, patchBytes, appsv1.Deployment{}); err != nil {
			return fmt.Errorf("spec.patches[%d:%s].patch is not a valid strategic merge fragment of appsv1.Deployment: %w", i, patches[i].Name, err)
		}
	}
	return nil
}
