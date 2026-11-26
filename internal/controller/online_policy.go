package controller

import (
	"context"
	"fmt"
	"slices"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type onlinePolicyPlan struct {
	configMaps map[string]*corev1.ConfigMap
}

type sshPolicyUnavailableError struct{ err error }

func (e *sshPolicyUnavailableError) Error() string { return e.err.Error() }
func (e *sshPolicyUnavailableError) Unwrap() error { return e.err }

func sshPolicySource(cm *corev1.ConfigMap, key string) (string, error) {
	if value, ok := cm.Data[key]; ok {
		return value, nil
	}
	if value, ok := cm.BinaryData[key]; ok {
		return string(value), nil
	}
	return "", fmt.Errorf("SSH trust ConfigMap %s/%s has no key %s", cm.Namespace, cm.Name, key)
}

func normalizedAccounts(accounts []credentials.GatewayAccount) []credentials.GatewayAccount {
	for i := range accounts {
		permissions := slices.Clone(accounts[i].Permissions)
		slices.Sort(permissions)
		accounts[i].Permissions = slices.Compact(permissions)
	}
	slices.SortFunc(accounts, func(a, b credentials.GatewayAccount) int {
		if a.Username < b.Username {
			return -1
		}
		if a.Username > b.Username {
			return 1
		}
		return 0
	})
	return accounts
}

func (r *YanetReconciler) readOnlineTrust(ctx context.Context, y *api.Yanet, spec *api.CertificateManagementSpec) ([]byte, error) {
	if spec.SSHTrustBundle == nil {
		return nil, nil
	}
	source := &corev1.ConfigMap{}
	trust := spec.SSHTrustBundle
	if err := r.liveReader().Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, source); err != nil {
		return nil, &sshPolicyUnavailableError{err: fmt.Errorf("read online SSH trust: %w", err)}
	}
	value, err := sshPolicySource(source, trust.Key)
	if err != nil {
		return nil, &sshPolicyUnavailableError{err: err}
	}
	if err := credentials.ValidateSSHTrust([]byte(value)); err != nil {
		return nil, &sshPolicyUnavailableError{err: fmt.Errorf("validate online SSH trust: %w", err)}
	}
	return []byte(value), nil
}

func (r *YanetReconciler) prepareOnlinePolicy(
	ctx context.Context, y *api.Yanet, spec *api.CertificateManagementSpec,
	nodes []corev1.Node, plans map[string]manifests.CertificatePlan,
) (onlinePolicyPlan, error) {
	result := onlinePolicyPlan{configMaps: map[string]*corev1.ConfigMap{}}
	if err := r.verifyOnlineNodes(ctx, y, nodes); err != nil {
		return result, err
	}
	for _, node := range nodes {
		delivery := plans[node.Name].PolicyDelivery
		if delivery == nil {
			continue
		}
		trust, err := r.readOnlineTrust(ctx, y, spec)
		if err != nil {
			return result, err
		}
		bundle := credentials.OnlinePolicyBundle{
			Accounts: normalizedAccounts(manifests.GatewayAccounts(spec, y.Namespace, string(y.UID), string(node.UID))),
			SSHTrust: trust,
		}
		encoded, err := credentials.EncodeOnlinePolicyBundle(bundle)
		if err != nil {
			return result, fmt.Errorf("render online policy for Node %s: %w", node.Name, err)
		}
		result.configMaps[node.Name] = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: manifests.PolicyConfigMapName(string(y.UID), string(node.UID)), Namespace: y.Namespace,
				Labels:          map[string]string{manifests.LabelYanet: y.Name, manifests.LabelNode: node.Name, manifests.LabelComponent: manifests.PolicyDeliveryComponent},
				OwnerReferences: slices.Clone(delivery.OwnerReferences),
			},
			Data: map[string]string{credentials.PolicyBundleKey: string(encoded)},
		}
	}
	return result, nil
}

func (r *YanetReconciler) applyOnlinePolicy(ctx context.Context, y *api.Yanet, node *corev1.Node, plan onlinePolicyPlan, autoSync bool) (bool, error) {
	desired := plan.configMaps[node.Name]
	if desired == nil {
		return false, nil
	}
	drift := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &corev1.ConfigMap{}
		key := client.ObjectKeyFromObject(desired)
		getErr := r.liveReader().Get(ctx, key, existing)
		found := getErr == nil
		if !found && !apierrors.IsNotFound(getErr) {
			return getErr
		}
		if found {
			if !controlledByYanet(existing, y) || !existing.DeletionTimestamp.IsZero() {
				return fmt.Errorf("policy ConfigMap %s is not an active object owned by this installation", key)
			}
			if apiequality.Semantic.DeepEqual(existing.Data, desired.Data) && len(existing.BinaryData) == 0 {
				drift = false
				return nil
			}
		}
		drift = true
		if !autoSync {
			return nil
		}
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		if !found {
			return r.Create(ctx, desired.DeepCopy())
		}
		existing.Data = desired.Data
		existing.BinaryData = nil
		return r.Update(ctx, existing)
	})
	return drift && !autoSync, err
}
