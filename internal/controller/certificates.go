package controller

import (
	"context"
	"fmt"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newCertificate() *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(manifests.CertificateGVK)
	return c
}

func (r *YanetReconciler) applyCertificates(ctx context.Context, yanet *api.Yanet, plan manifests.CertificatePlan, autoSync bool) (bool, error) {
	ready := true
	for _, desired := range plan.Certificates {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing := newCertificate()
			err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
			if apierrors.IsNotFound(err) {
				ready = false
				if !autoSync {
					return nil
				}
				if stopErr := r.checkGlobalStop(); stopErr != nil {
					return stopErr
				}
				return r.Create(ctx, desired.DeepCopy())
			}
			if err != nil {
				return err
			}
			if !controlledByYanet(existing, yanet) {
				return fmt.Errorf("Certificate %s/%s has a different controller owner", existing.GetNamespace(), existing.GetName())
			}
			if !existing.GetDeletionTimestamp().IsZero() {
				ready = false
				return nil
			}
			existingSpec, normalizeErr := normalizedCertificateSpec(existing)
			if normalizeErr != nil {
				return normalizeErr
			}
			desiredSpec, normalizeErr := normalizedCertificateSpec(desired)
			if normalizeErr != nil {
				return normalizeErr
			}
			if !apiequality.Semantic.DeepEqual(existingSpec, desiredSpec) {
				ready = false
				if !autoSync {
					return nil
				}
				existing.Object["spec"] = desired.DeepCopy().Object["spec"]
				if stopErr := r.checkGlobalStop(); stopErr != nil {
					return stopErr
				}
				return r.Update(ctx, existing)
			}
			conditions, _, err := unstructured.NestedSlice(existing.Object, "status", "conditions")
			if err != nil {
				return err
			}
			issued := false
			for _, value := range conditions {
				condition, ok := value.(map[string]any)
				if !ok {
					continue
				}
				observed, _, conditionErr := unstructured.NestedInt64(condition, "observedGeneration")
				if conditionErr != nil {
					return conditionErr
				}
				issued = issued || condition["type"] == "Ready" && condition["status"] == "True" && observed == existing.GetGeneration()
			}
			ready = ready && issued
			return nil
		})
		if err != nil {
			return false, fmt.Errorf("reconcile Certificate %s: %w", desired.GetName(), err)
		}
	}
	return ready, nil
}

// cert-manager admission may serialize metav1.Duration canonically and omit
// zero-value isCA. Those representation changes must not retrigger issuance.
func normalizedCertificateSpec(certificate *unstructured.Unstructured) (map[string]any, error) {
	spec, _, err := unstructured.NestedMap(certificate.Object, "spec")
	if err != nil {
		return nil, err
	}
	if _, present := spec["isCA"]; !present {
		spec["isCA"] = false
	}
	for _, field := range []string{"duration", "renewBefore"} {
		if value, ok := spec[field].(string); ok {
			duration, parseErr := time.ParseDuration(value)
			if parseErr != nil {
				return nil, fmt.Errorf("Certificate %s %s: %w", certificate.GetName(), field, parseErr)
			}
			spec[field] = duration.String()
		}
	}
	return spec, nil
}

func (r *YanetReconciler) certificateDeliveryReady(ctx context.Context, desired *appsv1.Deployment) (bool, error) {
	existing := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if err := validateDeploymentOwnership(existing, desired); err != nil {
		return false, err
	}
	_, changed, err := r.desiredDeploymentUpdate(ctx, existing, desired)
	if err != nil {
		return false, err
	}
	return !changed && existing.DeletionTimestamp.IsZero() && existing.Status.ObservedGeneration >= existing.Generation &&
		existing.Status.ReadyReplicas == 1 && existing.Status.UpdatedReplicas == 1 && existing.Status.Replicas == 1, nil
}

func setCertificateConditions(yanet *api.Yanet, enabled, issued, delivered bool) {
	for _, condition := range []struct {
		name             string
		ready            bool
		success, waiting string
	}{
		{"CertificatesIssued", issued, "Issued", "WaitingForIssuer"},
		{"CredentialsDelivered", delivered, "Delivered", "WaitingForDelivery"},
	} {
		if !enabled {
			apimeta.RemoveStatusCondition(&yanet.Status.Conditions, condition.name)
			continue
		}
		status, reason := metav1.ConditionFalse, condition.waiting
		if condition.ready {
			status, reason = metav1.ConditionTrue, condition.success
		}
		apimeta.SetStatusCondition(&yanet.Status.Conditions, metav1.Condition{Type: condition.name, Status: status, ObservedGeneration: yanet.Generation, Reason: reason, Message: "Issuance and durable delivery do not establish application credential loading"})
	}
}
