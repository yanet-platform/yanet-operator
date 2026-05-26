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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// yanetLog is the package-level logger for YanetV2 webhook.
var yanetLog = logf.Log.WithName("yanet-v2-webhook")

// YanetCustomValidator validates a YanetV2 CR against the cluster-wide
// YanetConfigV2. It needs a Kubernetes client to look up YanetConfigV2
// objects across namespaces (validation is best-effort: if no
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
// If no YanetConfigV2 is reachable in the same namespace, the webhook
// degrades gracefully: shape-only validation, plus a warning. This is
// intentional — bootstrapping the cluster sometimes creates the YanetV2
// before the YanetConfigV2.
func (v *YanetCustomValidator) validate(ctx context.Context, y *YanetV2) (admission.Warnings, error) {
	if y.Spec.BoxType == "" {
		return nil, fmt.Errorf("spec.boxType is required")
	}

	// Try to find a YanetConfigV2 in the same namespace; if none, fall
	// back to any YanetConfigV2 across the cluster.
	configs := &YanetConfigV2List{}
	if err := v.Client.List(ctx, configs, client.InNamespace(y.Namespace)); err != nil {
		return admission.Warnings{
			fmt.Sprintf("could not list YanetConfigV2 in namespace %q: %v — validating shape only", y.Namespace, err),
		}, nil
	}
	if len(configs.Items) == 0 {
		if err := v.Client.List(ctx, configs); err != nil {
			return admission.Warnings{
				fmt.Sprintf("could not list YanetConfigV2 cluster-wide: %v — validating shape only", err),
			}, nil
		}
	}
	if len(configs.Items) == 0 {
		return admission.Warnings{
			"no YanetConfigV2 found in the cluster — YanetV2.spec.boxType cannot be cross-validated; reconciler will reject if reference is missing",
		}, nil
	}

	// Search for a matching boxType in any YanetConfigV2 of the
	// candidate set. Multiple YanetConfigs are allowed; the
	// reconciler picks the one in YanetV2's namespace, falling back
	// to cluster-wide.
	for i := range configs.Items {
		spec := &configs.Items[i].Spec
		for j := range spec.BoxTypes {
			if spec.BoxTypes[j].Name == y.Spec.BoxType {
				return nil, validateOperatorOverrides(y.Spec.Components, spec.Components.Operators)
			}
		}
	}
	return nil, fmt.Errorf("spec.boxType %q is not defined in any YanetConfigV2 in the cluster", y.Spec.BoxType)
}

// validateOperatorOverrides checks that:
//   - every key in YanetV2.spec.components.operators corresponds to a
//     declared operator in YanetConfigV2.spec.components.operators;
//   - every per-container override key (in .containers map) matches
//     the rendered container name — for the 5 hardcoded components
//     the only allowed key is the kind name itself, for operators
//     it must be a declared OperatorContainer.Name.
func validateOperatorOverrides(overrides *YanetComponentsOverride, declared []OperatorSpec) error {
	if overrides == nil {
		return nil
	}
	if err := validateHardcodedContainerKeys("controlplane", overrides.Controlplane); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("dataplane", overrides.Dataplane); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("bird", overrides.Bird); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("birdAdapter", overrides.BirdAdapter); err != nil {
		return err
	}
	if err := validateHardcodedContainerKeys("announcer", overrides.Announcer); err != nil {
		return err
	}

	if len(overrides.Operators) == 0 {
		return nil
	}
	declaredSet := make(map[string]map[string]struct{}, len(declared))
	for i := range declared {
		names := make(map[string]struct{}, len(declared[i].Containers))
		for j := range declared[i].Containers {
			names[declared[i].Containers[j].Name] = struct{}{}
		}
		declaredSet[declared[i].Name] = names
	}
	for opName, ovr := range overrides.Operators {
		containerNames, ok := declaredSet[opName]
		if !ok {
			return fmt.Errorf("spec.components.operators[%q] is not declared in YanetConfigV2.spec.components.operators", opName)
		}
		for cname := range ovr.Containers {
			if _, ok := containerNames[cname]; !ok {
				return fmt.Errorf("spec.components.operators[%q].containers[%q] is not declared in YanetConfigV2.spec.components.operators[%q].containers", opName, cname, opName)
			}
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
