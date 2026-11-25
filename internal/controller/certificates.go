package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

type credentialFreeze struct {
	nodes       map[string]bool
	deployments map[string]bool
	configMaps  map[string]struct{}
	secrets     map[string]bool
}

// The writer delivers multiple roles in one Pod. A gateway policy failure
// freezes that writer, its credential consumers and their issuance, but not
// unrelated deployments on the same node. Compare live as well as desired:
// switching a consumer to a non-managed template must not bypass the freeze.
func (r *YanetReconciler) planCredentialFreeze(ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, plans map[string]manifests.CertificatePlan, workloads map[string][]renderedWorkload, nodes []corev1.Node) (credentialFreeze, error) {
	frozen := credentialFreeze{nodes: map[string]bool{}, deployments: map[string]bool{}, configMaps: map[string]struct{}{}, secrets: map[string]bool{}}
	reader := r.liveReader()
	config := &api.YanetConfig{}
	if err := reader.Get(ctx, client.ObjectKey{Name: api.YanetConfigName}, config); err != nil {
		return frozen, err
	}
	current := &api.Yanet{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(y), current); err != nil {
		return frozen, err
	}
	if config.UID == "" || config.Generation < 1 || current.UID == "" || current.UID != y.UID || current.Generation != y.Generation ||
		!current.DeletionTimestamp.IsZero() || !apiequality.Semantic.DeepEqual(config.Spec, *cfg) || !apiequality.Semantic.DeepEqual(current.Spec, y.Spec) {
		return frozen, fmt.Errorf("policy source changed before SSH freeze planning")
	}
	if cfg.Certificates == nil || cfg.Certificates.SSHTrustBundle == nil {
		return frozen, fmt.Errorf("SSH policy failure without a configured source")
	}
	trust := cfg.Certificates.SSHTrustBundle
	frozen.configMaps[trust.Name] = struct{}{}
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, cm); err == nil {
		if value, keyErr := sshPolicySource(cm, trust.Key); keyErr == nil && credentials.ValidateSSHTrust([]byte(value)) == nil {
			return frozen, fmt.Errorf("SSH policy is available on reread; retry full preflight")
		}
	} else if !apierrors.IsNotFound(err) {
		return frozen, fmt.Errorf("could not verify SSH policy is missing: %w", err)
	}
	fresh := &corev1.NodeList{}
	if err := r.liveReader().List(ctx, fresh, client.MatchingLabels(y.Spec.NodeSelector)); err != nil {
		return frozen, fmt.Errorf("verify current node selection: %w", err)
	}
	selected := make(map[string]*corev1.Node, len(nodes))
	for i := range nodes {
		selected[nodes[i].Name] = &nodes[i]
	}
	if len(fresh.Items) != len(selected) {
		return frozen, fmt.Errorf("node selection changed during preflight")
	}
	for _, n := range fresh.Items {
		prev := selected[n.Name]
		if prev == nil || n.UID != prev.UID || !n.DeletionTimestamp.IsZero() ||
			!apiequality.Semantic.DeepEqual(n.Labels, prev.Labels) || !apiequality.Semantic.DeepEqual(n.Status.Allocatable, prev.Status.Allocatable) {
			return frozen, fmt.Errorf("node %s changed during preflight", n.Name)
		}
	}

	// Include every desired name in the collision check, even if the workload
	// is not itself bound to credentials.
	wanted := make(map[string]string)
	var rendered []*appsv1.Deployment
	for node, plan := range plans {
		if d := plan.Delivery; d != nil {
			rendered = append(rendered, d)
			wanted[d.Name] = node
			frozen.nodes[node] = len(gatewayContainers(d)) > 0
		}
		for _, w := range workloads[node] {
			rendered = append(rendered, w.deployment)
			wanted[w.deployment.Name] = node
			frozen.nodes[node] = frozen.nodes[node] || len(gatewayContainers(w.deployment)) > 0
		}
	}
	var state policyState
	if stored := current.Annotations[policyStateAnnotation]; stored != "" {
		if err := json.Unmarshal([]byte(stored), &state); err != nil {
			return frozen, fmt.Errorf("invalid persisted policy state: %w", err)
		}
		digest, err := hex.DecodeString(state.Hash)
		if err != nil || len(digest) != sha256.Size || state.UID != string(y.UID) || state.Sequence == 0 || state.ConfigUID == "" ||
			state.ConfigGeneration < 1 || state.YanetGeneration < 1 || state.YanetGeneration > y.Generation ||
			(state.ConfigUID == string(config.UID) && state.ConfigGeneration > config.Generation) {
			return frozen, fmt.Errorf("invalid or stale persisted policy state")
		}
	}
	if !state.Activated {
		if _, err := pendingBridgeScopes(ctx, reader, y, rendered, nodes, state); err != nil {
			return frozen, err
		}
	}
	live := &appsv1.DeploymentList{}
	if err := r.liveReader().List(ctx, live, client.InNamespace(y.Namespace)); err != nil {
		return frozen, fmt.Errorf("list live credential workloads: %w", err)
	}
	credentialNodes := make(map[string][]*appsv1.Deployment)
	for i := range live.Items {
		d := &live.Items[i]
		node, desired := wanted[d.Name]
		if !controlledByYanet(d, y) {
			if desired {
				return frozen, fmt.Errorf("deployment %s has a different controller owner", client.ObjectKeyFromObject(d))
			}
			continue
		}
		claim := deploymentClaimNode(d)
		if claim == "" || (desired && node != claim) {
			return frozen, fmt.Errorf("cannot establish node placement of live Deployment %s", client.ObjectKeyFromObject(d))
		}
		if selected[claim] == nil {
			continue
		}
		// Writer recognition must not depend on its mutable metadata labels.
		view := *d
		view.Labels = d.Spec.Selector.MatchLabels
		gateway := len(gatewayContainers(&view)) > 0
		if gateway || manifests.RequiresCertificateDelivery(d) || view.Labels[manifests.LabelComponent] == "credential-delivery" {
			credentialNodes[claim] = append(credentialNodes[claim], d)
		}
		frozen.nodes[claim] = frozen.nodes[claim] || gateway
	}
	for node, deployments := range credentialNodes {
		if !frozen.nodes[node] {
			continue
		}
		for _, d := range deployments {
			frozen.deployments[d.Name] = true
			collectPodConfigMapRefs(&d.Spec.Template.Spec, frozen.configMaps)
			collectCredentialSecrets(&d.Spec.Template.Spec, frozen.secrets)
		}
	}
	for node, plan := range plans {
		if !frozen.nodes[node] {
			continue
		}
		if plan.Delivery != nil {
			frozen.deployments[plan.Delivery.Name] = true
		}
		for _, w := range workloads[node] {
			if manifests.RequiresCertificateDelivery(w.deployment) || len(gatewayContainers(w.deployment)) > 0 {
				frozen.deployments[w.deployment.Name] = true
			}
		}
	}
	return frozen, nil
}

func (r *YanetReconciler) applyCertificates(ctx context.Context, yanet *api.Yanet, plan manifests.CertificatePlan, autoSync bool) (bool, error) {
	ready := true
	var errs []error
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
				return fmt.Errorf("certificate %s/%s has a different controller owner", existing.GetNamespace(), existing.GetName())
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
			if errors.Is(err, errGlobalStop) || ctx.Err() != nil {
				return false, errors.Join(append(errs, err)...)
			}
			errs = append(errs, fmt.Errorf("reconcile Certificate %s: %w", desired.GetName(), err))
		}
	}
	return ready && len(errs) == 0, errors.Join(errs...)
}

func setCertificateConditionsUnknown(y *api.Yanet, enabled bool, reason, message string) {
	for _, name := range []string{"CertificatesIssued", "CredentialsDelivered"} {
		if !enabled {
			apimeta.RemoveStatusCondition(&y.Status.Conditions, name)
			continue
		}
		apimeta.SetStatusCondition(&y.Status.Conditions, metav1.Condition{
			Type: name, Status: metav1.ConditionUnknown, ObservedGeneration: y.Generation, Reason: reason, Message: message,
		})
	}
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
				return nil, fmt.Errorf("certificate %s %s: %w", certificate.GetName(), field, parseErr)
			}
			spec[field] = duration.String()
		}
	}
	return spec, nil
}

// Select authorization profiles from box wiring, before workload enablement and
// replica patches affect which deployments and credentials run on a node.
func certificatesForWiredComponents(spec *api.CertificateManagementSpec, wired []helpers.ComponentRef) *api.CertificateManagementSpec {
	if spec == nil {
		return nil
	}
	components := make(map[string]bool, len(wired))
	for _, ref := range wired {
		name := ref.OperatorName
		if name == "" {
			name = string(ref.Kind)
		}
		components[name] = true
	}
	selected := *spec
	selected.Profiles = nil
	for _, profile := range spec.Profiles {
		if components[profile.Component] {
			selected.Profiles = append(selected.Profiles, profile)
		}
	}
	return &selected
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

// A failed Certificate API must not prevent an explicit drain. Only change the
// replica count of an existing owned credential workload: never create it or
// replace its writer policy or consumer gate with an unallocated revision.
func (r *YanetReconciler) drainCredentialWorkload(ctx context.Context, desired *appsv1.Deployment, autoSync bool) (string, error) {
	state := "synced"
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if err := validateDeploymentOwnership(existing, desired); err != nil {
			return err
		}
		if existing.Spec.Replicas != nil && *existing.Spec.Replicas == 0 {
			return nil
		}
		if !autoSync {
			state = "out-of-sync (replicas)"
			return nil
		}
		zero := int32(0)
		existing.Spec.Replicas = &zero
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		return r.Update(ctx, existing)
	})
	if err != nil {
		return "error", err
	}
	return state, nil
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
