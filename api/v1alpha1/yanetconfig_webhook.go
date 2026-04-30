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

package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var yanetconfiglog = logf.Log.WithName("yanetconfig-webhook")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *YanetConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-yanet-yanet-platform-io-v1alpha1-yanetconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=yanet.yanet-platform.io,resources=yanetconfigs,verbs=create;update,versions=v1alpha1,name=vyanetconfig.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*YanetConfig] = &YanetConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *YanetConfig) ValidateCreate(ctx context.Context, obj *YanetConfig) (admission.Warnings, error) {
	yanetconfiglog.Info("validate create", "name", obj.Name)

	return obj.validateYanetConfig()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *YanetConfig) ValidateUpdate(ctx context.Context, oldObj, newObj *YanetConfig) (admission.Warnings, error) {
	yanetconfiglog.Info("validate update", "name", newObj.Name)

	return newObj.validateYanetConfig()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *YanetConfig) ValidateDelete(ctx context.Context, obj *YanetConfig) (admission.Warnings, error) {
	yanetconfiglog.Info("validate delete", "name", obj.Name)

	// No validation needed for delete
	return nil, nil
}

// validateYanetConfig performs common validation for YanetConfig
func (r *YanetConfig) validateYanetConfig() (admission.Warnings, error) {
	var warnings admission.Warnings

	// Validate UpdateWindow is non-negative
	if r.Spec.UpdateWindow < 0 {
		return nil, fmt.Errorf("spec.updatewindow must be >= 0, got %d", r.Spec.UpdateWindow)
	}

	// Warn if Stop is enabled
	if r.Spec.Stop {
		warnings = append(warnings, "Stop is enabled - operator will not reconcile resources")
	}

	// Warn if AutoDiscovery is enabled without required URIs
	if r.Spec.AutoDiscovery.Enable {
		if r.Spec.AutoDiscovery.TypeUri == "" {
			warnings = append(warnings, "AutoDiscovery is enabled but TypeUri is not set")
		}
		if r.Spec.AutoDiscovery.Namespace == "" {
			warnings = append(warnings, "AutoDiscovery is enabled but Namespace is not set, using default")
		}
	}

	return warnings, nil
}
