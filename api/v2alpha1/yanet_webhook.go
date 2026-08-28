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
	"fmt"
	"strconv"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// yanetLog is the package-level logger for YanetV2 webhook.
var yanetLog = logf.Log.WithName("yanet-v2-webhook")

// YanetCustomValidator validates a YanetV2 CR against the cluster-wide
// YanetConfigV2. It needs a Kubernetes client to look up the fixed
// cluster-scoped YanetConfigV2 object (validation is best-effort: if no
// YanetConfigV2 is reachable, the webhook only validates the local CR
// shape and lets the reconciler handle missing references).
// +kubebuilder:object:generate=false
type YanetCustomValidator struct {
	Client client.Client
}

var _ admission.Validator[*YanetV2] = &YanetCustomValidator{}

// SetupYanetWebhookWithManager wires the YanetV2 validating webhook to
// the controller manager.
func SetupYanetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &YanetV2{}).
		WithValidator(&YanetCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

//+kubebuilder:webhook:path=/validate-yanet-yanet-platform-io-v2alpha1-yanetv2,mutating=false,failurePolicy=fail,sideEffects=None,groups=yanet.yanet-platform.io,resources=yanetsv2,verbs=create;update,versions=v2alpha1,name=vyanetv2.kb.io,admissionReviewVersions=v1

// ValidateCreate implements admission.Validator.
func (v *YanetCustomValidator) ValidateCreate(ctx context.Context, y *YanetV2) (admission.Warnings, error) {
	yanetLog.Info("validate create", "name", y.Name, "namespace", y.Namespace)
	return v.validate(ctx, y)
}

// ValidateUpdate implements admission.Validator.
func (v *YanetCustomValidator) ValidateUpdate(ctx context.Context, old, y *YanetV2) (admission.Warnings, error) {
	yanetLog.Info("validate update", "name", y.Name, "namespace", y.Namespace)

	// Skip validation when the object is being deleted (e.g. finalizer removal update).
	// The controller needs to patch the object to remove the finalizer; blocking that
	// would leave the object stuck in Terminating forever.
	if y.DeletionTimestamp != nil {
		return nil, nil
	}

	// boxType is immutable — moving an installation between box
	// types implies a different deployment topology and should be
	// done via delete + recreate.
	if old.Spec.BoxType != y.Spec.BoxType {
		return nil, fmt.Errorf("spec.boxType is immutable (was %q, now %q)", old.Spec.BoxType, y.Spec.BoxType)
	}
	return v.validate(ctx, y)
}

// ValidateDelete implements admission.Validator. Deletes are always
// allowed.
func (v *YanetCustomValidator) ValidateDelete(ctx context.Context, _ *YanetV2) (admission.Warnings, error) {
	return nil, nil
}

// validate runs the full YanetV2-side checks:
//   - shape (boxType present)
//   - cross-references against the cluster-wide YanetConfigV2 (boxType
//     exists, YanetV2.spec.components.operators[<name>] references a
//     declared operator).
//
// If no cluster-wide YanetConfigV2 is reachable, the webhook degrades
// gracefully to shape-only validation with a warning. Bootstrapping may create
// the YanetV2 before the singleton YanetConfigV2.
func (v *YanetCustomValidator) validate(ctx context.Context, y *YanetV2) (admission.Warnings, error) {
	if y.Spec.BoxType == "" {
		return nil, fmt.Errorf("spec.boxType is required")
	}

	config := &YanetConfigV2{}
	if err := v.Client.Get(ctx, client.ObjectKey{Name: YanetConfigName}, config); err != nil {
		return admission.Warnings{
			fmt.Sprintf("could not get cluster-wide YanetConfigV2 %q: %v — validating shape only", YanetConfigName, err),
		}, nil
	}

	spec := &config.Spec
	for j := range spec.BoxTypes {
		if spec.BoxTypes[j].Name == y.Spec.BoxType {
			if err := validateYanetServiceNames(y, &spec.Components, &spec.BoxTypes[j]); err != nil {
				return nil, err
			}
			return nil, validateOperatorOverrides(y.Spec.Components, &spec.Components)
		}
	}
	return nil, fmt.Errorf("spec.boxType %q is not defined in the cluster YanetConfigV2", y.Spec.BoxType)
}

func validateYanetServiceNames(yanet *YanetV2, components *ComponentsSpec, box *BoxType) error {
	seen := make(map[string]string)
	controlplaneBase := ""
	controlplanePath := ""
	controlplaneCount := int64(0) // -1 means NFD auto-detection.
	disabledNuma := make(map[int64]struct{})
	for _, index := range components.Controlplane.DisabledNuma {
		disabledNuma[int64(index)] = struct{}{}
	}
	if yanet.Spec.Components != nil && yanet.Spec.Components.Controlplane != nil &&
		yanet.Spec.Components.Controlplane.DisabledNuma != nil {
		disabledNuma = make(map[int64]struct{}, len(yanet.Spec.Components.Controlplane.DisabledNuma))
		for _, index := range yanet.Spec.Components.Controlplane.DisabledNuma {
			disabledNuma[int64(index)] = struct{}{}
		}
	}
	controlplaneIndexEnabled := func(index int64) bool {
		if index < 0 {
			return false
		}
		if controlplaneCount >= 0 && index >= controlplaneCount {
			return false
		}
		_, disabled := disabledNuma[index]
		return !disabled
	}
	parseControlplaneIndex := func(name string) (int64, bool) {
		prefix := controlplaneBase + "-numa"
		if controlplaneBase == "" || !strings.HasPrefix(name, prefix) {
			return 0, false
		}
		suffix := strings.TrimPrefix(name, prefix)
		index, err := strconv.ParseInt(suffix, 10, 32)
		if err != nil || strconv.FormatInt(index, 10) != suffix {
			return 0, false
		}
		return index, controlplaneIndexEnabled(index)
	}
	add := func(path, component string, service *ServiceSpec) error {
		if !serviceEnabled(service) {
			return nil
		}
		name := service.ServiceName
		if name == "" {
			name = yanet.Name + "-" + component
		}
		if errs := k8svalidation.IsDNS1035Label(name); len(errs) > 0 {
			return fmt.Errorf("%s generates invalid Service name %q: %s", path, name, strings.Join(errs, "; "))
		}
		if _, collision := parseControlplaneIndex(name); collision {
			return fmt.Errorf("%s and %s generate the same Service name %q", controlplanePath, path, name)
		}
		if previous, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%s and %s generate the same Service name %q", previous, path, name)
		}
		seen[name] = path
		return nil
	}

	if box.Components.Controlplane != nil {
		service := components.Controlplane.Service
		if serviceEnabled(service) {
			controlplanePath = "spec.components.controlplane.service"
			controlplaneBase = service.ServiceName
			if controlplaneBase == "" {
				controlplaneBase = yanet.Name + "-controlplane"
			}
			controlplaneCount = -1
			if components.Controlplane.Numa != nil && *components.Controlplane.Numa > 0 {
				controlplaneCount = int64(*components.Controlplane.Numa)
			}
			firstIndex := int64(0)
			for controlplaneCount < 0 || firstIndex < controlplaneCount {
				if controlplaneIndexEnabled(firstIndex) {
					break
				}
				firstIndex++
			}
			if controlplaneCount >= 0 && firstIndex >= controlplaneCount {
				return fmt.Errorf("%s has no enabled NUMA index", controlplanePath)
			}
			lastIndex := firstIndex
			if controlplaneCount >= 0 {
				lastIndex = controlplaneCount - 1
			}
			for lastIndex > firstIndex && !controlplaneIndexEnabled(lastIndex) {
				lastIndex--
			}
			firstName := fmt.Sprintf("%s-numa%d", controlplaneBase, firstIndex)
			lastName := fmt.Sprintf("%s-numa%d", controlplaneBase, lastIndex)
			for _, name := range []string{firstName, lastName} {
				if errs := k8svalidation.IsDNS1035Label(name); len(errs) > 0 {
					return fmt.Errorf("%s generates invalid Service name %q: %s", controlplanePath, name, strings.Join(errs, "; "))
				}
			}
			seen[firstName] = controlplanePath
		}
	}
	if box.Components.BirdAdapter != nil && components.BirdAdapter != nil {
		if err := add("spec.components.birdAdapter.service", "bird-adapter", components.BirdAdapter.Service); err != nil {
			return err
		}
	}
	if box.Components.Announcer != nil && components.Announcer != nil {
		if err := add("spec.components.announcer.service", "announcer", components.Announcer.Service); err != nil {
			return err
		}
	}
	for i := range components.Operators {
		op := &components.Operators[i]
		if _, enabled := box.Operators[op.Name]; !enabled {
			continue
		}
		if err := add(
			fmt.Sprintf("spec.components.operators[%s].service", op.Name), op.Name, op.Service,
		); err != nil {
			return err
		}
	}
	return nil
}

// validateOperatorOverrides checks that:
//   - every key in YanetV2.spec.components.operators corresponds to a
//     declared operator in YanetConfigV2.spec.components.operators;
//   - every per-container override key (in .containers map) matches
//     the rendered container name — for the 5 hardcoded components
//     the only allowed key is the kind name itself, for operators
//     it must be a declared OperatorContainer.Name.
func validateOperatorOverrides(overrides *YanetComponentsOverride, declared *ComponentsSpec) error {
	if overrides == nil {
		return nil
	}
	if overrides.Controlplane != nil {
		override := &overrides.Controlplane.YanetComponentOverride
		if err := validateHardcodedContainerKeys(
			"controlplane", override); err != nil {
			return err
		}
		if err := validateNoContainerBindOverrides("controlplane", override); err != nil {
			return err
		}
		if err := validateOverrideDisabledNuma(overrides.Controlplane.DisabledNuma); err != nil {
			return err
		}
		bind := inheritedBind(declared.Controlplane.Bind, override.Bind)
		if err := validateComponentBinding(
			"spec.components.controlplane", bind, declared.Controlplane.Service,
			declared.Controlplane.GRPCPort, declared.Controlplane.HTTPPort,
		); err != nil {
			return err
		}
	}
	if err := validateHardcodedContainerKeys("dataplane", overrides.Dataplane); err != nil {
		return err
	}
	if err := validateUnsupportedBindOverride("dataplane", overrides.Dataplane); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("bird", overrides.Bird); err != nil {
		return err
	}
	if err := validateUnsupportedBindOverride("bird", overrides.Bird); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("birdAdapter", overrides.BirdAdapter); err != nil {
		return err
	}
	if overrides.BirdAdapter != nil {
		if err := validateNoContainerBindOverrides("birdAdapter", overrides.BirdAdapter); err != nil {
			return err
		}
		if declared.BirdAdapter == nil {
			return fmt.Errorf("spec.components.birdAdapter override has no matching YanetConfigV2 component")
		}
		component := declared.BirdAdapter
		bind := inheritedBind(component.Bind, overrides.BirdAdapter.Bind)
		if err := validateComponentBinding(
			"spec.components.birdAdapter", bind, component.Service, component.Port,
		); err != nil {
			return err
		}
	}
	if err := validateHardcodedContainerKeys("announcer", overrides.Announcer); err != nil {
		return err
	}
	if overrides.Announcer != nil {
		if err := validateNoContainerBindOverrides("announcer", overrides.Announcer); err != nil {
			return err
		}
		if declared.Announcer == nil {
			return fmt.Errorf("spec.components.announcer override has no matching YanetConfigV2 component")
		}
		component := declared.Announcer
		bind := inheritedBind(component.Bind, overrides.Announcer.Bind)
		if err := validateComponentBinding(
			"spec.components.announcer", bind, component.Service, component.Port,
		); err != nil {
			return err
		}
	}

	if len(overrides.Operators) == 0 {
		return nil
	}
	declaredSet := make(map[string]*OperatorSpec, len(declared.Operators))
	for i := range declared.Operators {
		op := &declared.Operators[i]
		declaredSet[op.Name] = op
	}
	for opName, ovr := range overrides.Operators {
		op, ok := declaredSet[opName]
		if !ok {
			return fmt.Errorf("spec.components.operators[%q] is not declared in YanetConfigV2.spec.components.operators", opName)
		}
		containerNames := make(map[string]struct{}, len(op.Containers))
		for i := range op.Containers {
			containerNames[op.Containers[i].Name] = struct{}{}
		}
		for cname := range ovr.Containers {
			if _, ok := containerNames[cname]; !ok {
				return fmt.Errorf("spec.components.operators[%q].containers[%q] is not declared in YanetConfigV2.spec.components.operators[%q].containers", opName, cname, opName)
			}
		}

		path := fmt.Sprintf("spec.components.operators[%q]", opName)
		opBind := inheritedBind(op.Bind, ovr.Bind)
		binds := []*BindSpec{opBind}
		if err := validateBindSpec(path+".bind", opBind, op.Service, op.Port); err != nil {
			return err
		}
		for i := range op.Containers {
			container := &op.Containers[i]
			containerBind := container.Bind
			if override, found := ovr.Containers[container.Name]; found {
				containerBind = inheritedBind(container.Bind, override.Bind)
			}
			binds = append(binds, containerBind)
			if err := validateBindSpec(
				fmt.Sprintf("%s.containers[%q].bind", path, container.Name),
				containerBind, op.Service, op.Port,
			); err != nil {
				return err
			}
		}
		if serviceEnabled(op.Service) && !anyBindConfigured(binds...) {
			return fmt.Errorf("%s.service.enabled requires a non-empty bind override", path)
		}
	}
	return nil
}

func inheritedBind(base, override *BindSpec) *BindSpec {
	if override != nil {
		return override
	}
	return base
}

func validateUnsupportedBindOverride(kind string, override *YanetComponentOverride) error {
	if override == nil {
		return nil
	}
	if override.Bind != nil {
		return fmt.Errorf("spec.components.%s.bind is not supported", kind)
	}
	return validateNoContainerBindOverrides(kind, override)
}

func validateNoContainerBindOverrides(kind string, override *YanetComponentOverride) error {
	if override == nil {
		return nil
	}
	for name, container := range override.Containers {
		if container.Bind != nil {
			return fmt.Errorf(
				"spec.components.%s.containers[%q].bind is only supported for dynamic operators",
				kind, name,
			)
		}
	}
	return nil
}

// validateOverrideDisabledNuma checks the per-installation controlplane
// NUMA opt-out list. Only the index domain is validated here: whether
// the list drains every NUMA domain depends on the per-node fan-out
// count (NFD label), which is a runtime property unavailable at admit
// time.
func validateOverrideDisabledNuma(disabled []int32) error {
	for _, n := range disabled {
		if n < 0 {
			return fmt.Errorf(
				"spec.components.controlplane.disabledNuma must contain non-negative indices, got %d", n)
		}
	}
	return nil
}

// validateHardcodedContainerKeys ensures the container key map of a
// hardcoded component override has at most one entry, and that entry
// matches the kind name (the only container the builder renders).
func validateHardcodedContainerKeys(kind string, ovr *YanetComponentOverride) error {
	if ovr == nil {
		return nil
	}
	for k := range ovr.Containers {
		if k != kind {
			return fmt.Errorf("spec.components.%s.containers[%q]: only key %q is allowed for hardcoded components", kind, k, kind)
		}
	}
	return nil
}
