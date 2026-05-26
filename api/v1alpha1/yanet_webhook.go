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
var yanetlog = logf.Log.WithName("yanet-webhook")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *Yanet) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-yanet-yanet-platform-io-v1alpha1-yanet,mutating=false,failurePolicy=fail,sideEffects=None,groups=yanet.yanet-platform.io,resources=yanets,verbs=create;update,versions=v1alpha1,name=vyanet.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*Yanet] = &Yanet{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Yanet) ValidateCreate(ctx context.Context, obj *Yanet) (admission.Warnings, error) {
	yanetlog.Info("validate create", "name", obj.Name)

	return obj.validateYanet()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Yanet) ValidateUpdate(ctx context.Context, oldObj, newObj *Yanet) (admission.Warnings, error) {
	yanetlog.Info("validate update", "name", newObj.Name)

	// Skip validation when the object is being deleted (e.g. finalizer removal update).
	// The controller needs to patch the object to remove the finalizer; blocking that
	// would leave the object stuck in Terminating forever.
	if newObj.DeletionTimestamp != nil {
		return nil, nil
	}

	// Check immutable fields
	if newObj.Spec.NodeName != oldObj.Spec.NodeName {
		return nil, fmt.Errorf("spec.nodename is immutable")
	}

	return newObj.validateYanet()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Yanet) ValidateDelete(ctx context.Context, obj *Yanet) (admission.Warnings, error) {
	yanetlog.Info("validate delete", "name", obj.Name)

	// No validation needed for delete
	return nil, nil
}

// validateYanet performs common validation for Yanet
func (r *Yanet) validateYanet() (admission.Warnings, error) {
	// Validate Type field
	if r.Spec.Type != "" && r.Spec.Type != "release" && r.Spec.Type != "balancer" {
		return nil, fmt.Errorf("spec.type must be either 'release' or 'balancer', got '%s'", r.Spec.Type)
	}

	// Validate NodeName is not empty
	if r.Spec.NodeName == "" {
		return nil, fmt.Errorf("spec.nodename cannot be empty")
	}

	return nil, nil
}
