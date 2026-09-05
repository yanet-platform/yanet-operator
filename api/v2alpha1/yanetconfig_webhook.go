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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
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
// +kubebuilder:object:generate=false
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
	if err := validateYanetConfigIdentity(cfg); err != nil {
		return nil, err
	}
	return nil, validateYanetConfig(&cfg.Spec)
}

// ValidateUpdate implements admission.Validator.
func (v *YanetConfigCustomValidator) ValidateUpdate(ctx context.Context, _, cfg *YanetConfigV2) (admission.Warnings, error) {
	yanetConfigLog.Info("validate update", "name", cfg.Name)
	if err := validateYanetConfigIdentity(cfg); err != nil {
		return nil, err
	}
	return nil, validateYanetConfig(&cfg.Spec)
}

// ValidateDelete implements admission.Validator. Deletes are always
// allowed.
func (v *YanetConfigCustomValidator) ValidateDelete(ctx context.Context, _ *YanetConfigV2) (admission.Warnings, error) {
	return nil, nil
}

func validateYanetConfigIdentity(cfg *YanetConfigV2) error {
	if cfg.Name != YanetConfigName {
		return fmt.Errorf("metadata.name must be %q for the cluster-wide YanetConfigV2 singleton", YanetConfigName)
	}
	return nil
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
	if err := validateHostNetworkPortRange(spec.HostNetworkPortRange); err != nil {
		return err
	}
	if err := validateHugepages(spec.Components.Dataplane.Hugepages); err != nil {
		return err
	}
	if err := validateComponentImages(&spec.Components); err != nil {
		return err
	}
	if err := validateConfigSources(&spec.Components); err != nil {
		return err
	}
	if err := validateDisabledNuma(&spec.Components.Controlplane); err != nil {
		return err
	}
	if err := dryRunPatches(spec.Patches); err != nil {
		return err
	}
	return nil
}

func validateComponentImages(components *ComponentsSpec) error {
	validate := func(path string, image ImageRef) error {
		if image.Name == "" {
			return fmt.Errorf("%s.name is required", path)
		}
		return nil
	}
	if err := validate("spec.components.controlplane.image", components.Controlplane.Image); err != nil {
		return err
	}
	if err := validate("spec.components.dataplane.image", components.Dataplane.Image); err != nil {
		return err
	}
	if components.Dataplane.Sidecars != nil {
		if components.Dataplane.Sidecars.Bird != nil {
			if err := validate(
				"spec.components.dataplane.sidecars.bird.image",
				components.Dataplane.Sidecars.Bird.Image,
			); err != nil {
				return err
			}
		}
		if components.Dataplane.Sidecars.NetlinkDataplaneSidecar != nil {
			if err := validate(
				"spec.components.dataplane.sidecars.netlinkDataplaneSidecar.image",
				components.Dataplane.Sidecars.NetlinkDataplaneSidecar.Image,
			); err != nil {
				return err
			}
		}
	}
	if components.BirdAdapter != nil {
		if err := validate("spec.components.birdAdapter.image", components.BirdAdapter.Image); err != nil {
			return err
		}
	}
	if components.Announcer != nil {
		if err := validate("spec.components.announcer.image", components.Announcer.Image); err != nil {
			return err
		}
	}
	for i := range components.Operators {
		for j := range components.Operators[i].Containers {
			if err := validate(
				fmt.Sprintf("spec.components.operators[%d].containers[%d].image", i, j),
				components.Operators[i].Containers[j].Image,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfigSources(components *ComponentsSpec) error {
	validate := func(path string, source *ConfigSource) error {
		if source == nil {
			return nil
		}
		if variants := source.VariantsSet(); variants != 1 {
			return fmt.Errorf("%s must define exactly one of inline, hostPath or url, got %d", path, variants)
		}
		return nil
	}

	if err := validate("spec.components.controlplane.config", components.Controlplane.Config); err != nil {
		return err
	}
	if err := validate("spec.components.dataplane.config", components.Dataplane.Config); err != nil {
		return err
	}
	if components.Dataplane.Sidecars != nil {
		if components.Dataplane.Sidecars.Bird != nil {
			if err := validate(
				"spec.components.dataplane.sidecars.bird.config",
				components.Dataplane.Sidecars.Bird.Config,
			); err != nil {
				return err
			}
		}
		if components.Dataplane.Sidecars.NetlinkDataplaneSidecar != nil {
			if err := validate(
				"spec.components.dataplane.sidecars.netlinkDataplaneSidecar.config",
				components.Dataplane.Sidecars.NetlinkDataplaneSidecar.Config,
			); err != nil {
				return err
			}
		}
	}
	if components.BirdAdapter != nil {
		if err := validate("spec.components.birdAdapter.config", components.BirdAdapter.Config); err != nil {
			return err
		}
	}
	if components.Announcer != nil {
		if err := validate("spec.components.announcer.config", components.Announcer.Config); err != nil {
			return err
		}
	}
	for i := range components.Operators {
		operator := &components.Operators[i]
		for j := range operator.Containers {
			container := &operator.Containers[j]
			path := fmt.Sprintf("spec.components.operators[%d:%s].containers[%d:%s].config", i, operator.Name, j, container.Name)
			if err := validate(path, container.Config); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateDisabledNuma checks the cluster-wide controlplane NUMA
// opt-out list: indices must be non-negative, and the list must not
// disable every NUMA domain the fan-out would produce (that would
// leave the installation without any controlplane at all — use the
// boxType or `enabled: false` to drop the component instead).
//
// The check against the fan-out count is only possible when `numa` is
// pinned explicitly. With NFD auto-detection the count is a per-node
// runtime property, so the equivalent guard lives in the reconciler.
func validateDisabledNuma(cp *ControlplaneSpec) error {
	if len(cp.DisabledNuma) == 0 {
		return nil
	}
	seen := make(map[int32]struct{}, len(cp.DisabledNuma))
	for _, n := range cp.DisabledNuma {
		if n < 0 {
			return fmt.Errorf(
				"spec.components.controlplane.disabledNuma must contain non-negative indices, got %d", n)
		}
		seen[n] = struct{}{}
	}
	if cp.Numa == nil {
		return nil
	}
	count := *cp.Numa
	disabled := int32(0)
	for i := int32(0); i < count; i++ {
		if _, ok := seen[i]; ok {
			disabled++
		}
	}
	if disabled >= count {
		return fmt.Errorf(
			"spec.components.controlplane.disabledNuma disables every one of the %d NUMA domains; "+
				"drop the controlplane from the boxType instead", count)
	}
	return nil
}

func validateHugepages(hugepages *Hugepages) error {
	if hugepages == nil {
		return nil
	}
	if _, err := hugepages.TotalQuantity(); err != nil {
		return fmt.Errorf("spec.components.dataplane.hugepages.%w", err)
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
	reservedNames := map[string]struct{}{
		"controlplane":              {},
		"dataplane":                 {},
		"bird":                      {},
		"bird-adapter":              {},
		"netlink-dataplane-sidecar": {},
		"announcer":                 {},
	}
	seen := make(map[string]struct{}, len(ops))
	for i := range ops {
		name := ops[i].Name
		if name == "" {
			return fmt.Errorf("spec.components.operators[%d].name is empty", i)
		}
		if _, reserved := reservedNames[name]; reserved {
			return fmt.Errorf("spec.components.operators[%d].name %q is reserved for a built-in component", i, name)
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
		if errs := k8svalidation.IsDNS1123Label(name); len(errs) > 0 {
			return fmt.Errorf("spec.boxTypes[%d].name %q is invalid: %s", i, name, strings.Join(errs, "; "))
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
		if err := validateDataplaneSidecarRefs(path, &spec.Components.Dataplane, box.Components.Dataplane); err != nil {
			return err
		}
		if box.Components.BirdAdapter != nil {
			if spec.Components.BirdAdapter == nil {
				return fmt.Errorf("%s.components.birdAdapter has no matching spec.components.birdAdapter", path)
			}
			if err := assertPatchesExist(path+".components.birdAdapter.patches", box.Components.BirdAdapter.Patches, patchSet); err != nil {
				return err
			}
		}
		if box.Components.Announcer != nil {
			if spec.Components.Announcer == nil {
				return fmt.Errorf("%s.components.announcer has no matching spec.components.announcer", path)
			}
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

func validateDataplaneSidecarRefs(path string, palette *DataplaneSpec, box *BoxDataplane) error {
	if box.Sidecars == nil {
		return nil
	}
	if box.Sidecars.Bird != nil && (palette.Sidecars == nil || palette.Sidecars.Bird == nil) {
		return fmt.Errorf(
			"%s.components.dataplane.sidecars.bird has no matching spec.components.dataplane.sidecars.bird",
			path,
		)
	}
	if box.Sidecars.NetlinkDataplaneSidecar != nil &&
		(palette.Sidecars == nil || palette.Sidecars.NetlinkDataplaneSidecar == nil) {
		return fmt.Errorf(
			"%s.components.dataplane.sidecars.netlinkDataplaneSidecar has no matching "+
				"spec.components.dataplane.sidecars.netlinkDataplaneSidecar",
			path,
		)
	}
	return nil
}

func validateHostNetworkPortRange(portRange *HostNetworkPortRange) error {
	if portRange == nil {
		return nil
	}
	if portRange.Start <= 0 || portRange.Start > 65535 {
		return fmt.Errorf("spec.hostNetworkPortRange.start must be in 1..65535, got %d", portRange.Start)
	}
	if portRange.End <= 0 || portRange.End > 65535 {
		return fmt.Errorf("spec.hostNetworkPortRange.end must be in 1..65535, got %d", portRange.End)
	}
	if portRange.Start > portRange.End {
		return fmt.Errorf(
			"spec.hostNetworkPortRange.start %d must not exceed end %d",
			portRange.Start,
			portRange.End,
		)
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
