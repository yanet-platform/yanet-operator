package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const onlinePolicyStateAnnotation = "yanet.yanet-platform.io/online-policy-state"

type onlinePolicyState struct {
	Epoch           string `json:"epoch"`
	Sequence        uint64 `json:"sequence"`
	Hash            string `json:"hash"`
	Domain          string `json:"domain"`
	HostPath        string `json:"hostPath"`
	ConfigUID       string `json:"configUID"`
	ConfigVersion   int64  `json:"configGeneration"`
	YanetGeneration int64  `json:"yanetGeneration"`
}

type onlinePolicyPlan struct {
	state      onlinePolicyState
	commitment string
	configMaps map[string]*corev1.ConfigMap
	sshTrust   string
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

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (r *YanetReconciler) readOnlineTrust(ctx context.Context, y *api.Yanet, spec *api.CertificateManagementSpec) (string, error) {
	if spec.SSHTrustBundle == nil {
		return "", nil
	}
	source := &corev1.ConfigMap{}
	trust := spec.SSHTrustBundle
	if err := r.liveReader().Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, source); err != nil {
		return "", &sshPolicyUnavailableError{err: fmt.Errorf("read online SSH trust: %w", err)}
	}
	value, err := sshPolicySource(source, trust.Key)
	if err != nil {
		return "", &sshPolicyUnavailableError{err: err}
	}
	if err := credentials.ValidateSSHTrust([]byte(value)); err != nil {
		return "", &sshPolicyUnavailableError{err: fmt.Errorf("validate online SSH trust: %w", err)}
	}
	return value, nil
}

// Read the CAS object before the input sources. A concurrent allocation after
// this read conflicts on the Yanet version; a changed source rejects the plan.
func (r *YanetReconciler) onlinePolicySources(ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, trust string, verifyTrust bool) (*api.Yanet, *api.YanetConfig, error) {
	fresh := &api.Yanet{}
	if err := r.liveReader().Get(ctx, client.ObjectKeyFromObject(y), fresh); err != nil {
		return nil, nil, err
	}
	config := &api.YanetConfig{}
	if err := r.liveReader().Get(ctx, client.ObjectKey{Name: api.YanetConfigName}, config); err != nil {
		return nil, nil, err
	}
	if config.UID == "" || config.Generation < 1 || config.Spec.Stop || fresh.UID == "" || fresh.UID != y.UID ||
		fresh.Generation < 1 || fresh.Generation != y.Generation || !fresh.DeletionTimestamp.IsZero() ||
		!apiequality.Semantic.DeepEqual(config.Spec, *cfg) || !apiequality.Semantic.DeepEqual(fresh.Spec, y.Spec) {
		return nil, nil, fmt.Errorf("online policy source changed; retry with current configuration")
	}
	if verifyTrust {
		current, err := r.readOnlineTrust(ctx, y, cfg.Certificates)
		if err != nil {
			return nil, nil, err
		}
		if current != trust {
			return nil, nil, fmt.Errorf("online SSH trust changed during reconciliation")
		}
	}
	return fresh, config, nil
}

func (r *YanetReconciler) prepareOnlinePolicy(
	ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, spec *api.CertificateManagementSpec,
	nodes []corev1.Node, plans map[string]manifests.CertificatePlan,
	autoSync bool,
) (onlinePolicyPlan, error) {
	var result onlinePolicyPlan
	var roles []string
	for _, profile := range spec.Profiles {
		if profile.Usage == "server" {
			roles = append(roles, profile.Name)
		}
	}
	slices.Sort(roles)
	hasTargets := len(roles) != 0 && len(nodes) != 0
	trust := ""
	if hasTargets {
		var err error
		trust, err = r.readOnlineTrust(ctx, y, spec)
		if err != nil {
			return result, err
		}
	}
	// Use a symbolic node so membership, enablement and NUMA cannot allocate
	// new revisions. The actual per-node bundles carry UID-bound identities.
	intent := struct {
		Accounts []credentials.GatewayAccount
		SSHTrust []byte
	}{normalizedAccounts(manifests.GatewayAccounts(spec, y.Namespace, string(y.UID), "policy-node")), []byte(trust)}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return result, err
	}
	hash := sha256Hex(encoded)
	allocateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, config, sourceErr := r.onlinePolicySources(ctx, y, cfg, trust, hasTargets)
		if sourceErr != nil {
			return sourceErr
		}
		state := onlinePolicyState{}
		stored := fresh.Annotations[onlinePolicyStateAnnotation]
		if err := r.verifyOnlineNodes(ctx, fresh, nodes); err != nil {
			return err
		}
		if stored == "" {
			if !hasTargets {
				return nil
			}
			if err := r.verifyNewOnlineScope(ctx, fresh); err != nil {
				return err
			}
			state = onlinePolicyState{
				Epoch: string(fresh.UID), Sequence: 1, Hash: hash, Domain: spec.TrustDomain, HostPath: spec.HostPath,
			}
		} else {
			if err := json.Unmarshal([]byte(stored), &state); err != nil {
				return fmt.Errorf("decode online policy state: %w", err)
			}
			digest, err := hex.DecodeString(state.Hash)
			if err != nil || len(digest) != sha256.Size || state.Epoch != string(fresh.UID) || state.Sequence == 0 ||
				state.ConfigUID == "" || state.ConfigVersion < 1 || state.YanetGeneration < 1 ||
				state.YanetGeneration > fresh.Generation ||
				(state.ConfigUID == string(config.UID) && state.ConfigVersion > config.Generation) {
				return fmt.Errorf("invalid or stale online policy state; refuse to reset revisions")
			}
			if state.Domain != spec.TrustDomain || state.HostPath != spec.HostPath {
				return fmt.Errorf("online policy trust domain and HostPath must remain stable")
			}
			if hasTargets && state.Hash != hash {
				if state.Sequence == ^uint64(0) {
					return fmt.Errorf("online policy revision exhausted")
				}
				state.Sequence++
				state.Hash = hash
			}
		}
		state.ConfigUID = string(config.UID)
		state.ConfigVersion = config.Generation
		state.YanetGeneration = fresh.Generation
		value, err := json.Marshal(state)
		if err != nil {
			return err
		}
		configMaps, err := onlinePolicyConfigMaps(fresh, spec, state, roles, trust, nodes, plans)
		if err != nil {
			return err
		}
		result = onlinePolicyPlan{state: state, commitment: string(value), configMaps: configMaps, sshTrust: trust}
		if !autoSync || stored == string(value) {
			return nil
		}
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[onlinePolicyStateAnnotation] = string(value)
		return r.Update(ctx, fresh)
	})
	return result, allocateErr
}

func (r *YanetReconciler) verifyOnlineNodes(ctx context.Context, y *api.Yanet, nodes []corev1.Node) error {
	selected := &corev1.NodeList{}
	if err := r.liveReader().List(ctx, selected, client.MatchingLabels(y.Spec.NodeSelector)); err != nil {
		return err
	}
	if len(selected.Items) != len(nodes) {
		return fmt.Errorf("online policy node selection changed")
	}
	rendered := make(map[string]corev1.Node, len(nodes))
	for _, node := range nodes {
		rendered[node.Name] = node
	}
	for _, node := range selected.Items {
		expected := rendered[node.Name]
		if node.UID == "" || node.UID != expected.UID || !node.DeletionTimestamp.IsZero() ||
			!maps.Equal(node.Labels, expected.Labels) {
			return fmt.Errorf("online policy Node scope changed: %s", node.Name)
		}
	}
	return nil
}

func onlinePolicyConfigMaps(y *api.Yanet, spec *api.CertificateManagementSpec, state onlinePolicyState, roles []string, trust string, nodes []corev1.Node, plans map[string]manifests.CertificatePlan) (map[string]*corev1.ConfigMap, error) {
	result := make(map[string]*corev1.ConfigMap, len(nodes))
	for _, node := range nodes {
		delivery := plans[node.Name].PolicyDelivery
		if delivery == nil {
			continue
		}
		bundle := credentials.OnlinePolicyBundle{
			Epoch: state.Epoch, NodeUID: string(node.UID), Sequence: state.Sequence,
			Namespace: y.Namespace, Domain: spec.TrustDomain, Roles: roles, SSHTrust: []byte(trust),
			Accounts: normalizedAccounts(manifests.GatewayAccounts(spec, y.Namespace, state.Epoch, string(node.UID))),
		}
		encoded, err := credentials.EncodeOnlinePolicyBundle(bundle)
		if err != nil {
			return nil, fmt.Errorf("render online policy for Node %s: %w", node.Name, err)
		}
		result[node.Name] = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: manifests.PolicyConfigMapName(state.Epoch, string(node.UID)), Namespace: y.Namespace,
				Labels:          map[string]string{manifests.LabelYanet: y.Name, manifests.LabelNode: node.Name, manifests.LabelComponent: manifests.PolicyDeliveryComponent},
				OwnerReferences: slices.Clone(delivery.OwnerReferences),
			},
			Data: map[string]string{credentials.PolicyBundleKey: string(encoded)},
		}
	}
	return result, nil
}

func (r *YanetReconciler) applyOnlinePolicy(ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, node *corev1.Node, plan onlinePolicyPlan, autoSync bool) (bool, error) {
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
		if found {
			if err := validateOnlinePolicyUpdate(existing, desired); err != nil {
				return fmt.Errorf("policy ConfigMap %s: %w", key, err)
			}
		}
		// Revalidate after reading the object being CASed, including equal
		// revision role inventory updates. Neither cache nor an old plan owns it.
		fresh, config, err := r.onlinePolicySources(ctx, y, cfg, plan.sshTrust, true)
		if err != nil {
			return err
		}
		if fresh.Annotations[onlinePolicyStateAnnotation] != plan.commitment ||
			string(config.UID) != plan.state.ConfigUID || config.Generation != plan.state.ConfigVersion {
			return fmt.Errorf("online policy revision or source changed before projecting on %s", node.Name)
		}
		current := &corev1.Node{}
		if err := r.liveReader().Get(ctx, client.ObjectKey{Name: node.Name}, current); err != nil {
			return err
		}
		if current.UID != node.UID || !current.DeletionTimestamp.IsZero() ||
			!maps.Equal(current.Labels, node.Labels) ||
			!labels.SelectorFromSet(fresh.Spec.NodeSelector).Matches(labels.Set(current.Labels)) {
			return fmt.Errorf("online policy Node scope changed: %s", node.Name)
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

func validateOnlinePolicyUpdate(existing, desired *corev1.ConfigMap) error {
	current, err := credentials.DecodeOnlinePolicyBundle([]byte(existing.Data[credentials.PolicyBundleKey]))
	if err != nil {
		return fmt.Errorf("invalid previous projection: %w", err)
	}
	next, err := credentials.DecodeOnlinePolicyBundle([]byte(desired.Data[credentials.PolicyBundleKey]))
	if err != nil {
		return err
	}
	if current.Epoch != next.Epoch || current.NodeUID != next.NodeUID || current.Namespace != next.Namespace ||
		current.Domain != next.Domain || current.Sequence > next.Sequence {
		return fmt.Errorf("refusing a different scope or an older revision")
	}
	current.Roles, next.Roles = nil, nil
	if current.Sequence == next.Sequence && !apiequality.Semantic.DeepEqual(current, next) {
		return fmt.Errorf("refusing different authorization under the same revision")
	}
	return nil
}
