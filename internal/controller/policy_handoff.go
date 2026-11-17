package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const policyStateAnnotation = "yanet.yanet-platform.io/policy-state"
const policyAckAnnotation = "yanet.yanet-platform.io/policy-checkpoints-ready"

// Persisted on the installation, so controller restarts and ABA policy changes
// cannot reuse a revision. Only the explicit UID-scoped acknowledgment retires
// legacy checkpoints; Deployment readiness alone cannot observe their refresh.
type policyState struct {
	UID              string `json:"uid"`
	Sequence         uint64 `json:"sequence"`
	Hash             string `json:"hash"`
	ConfigUID        string `json:"configUID"`
	ConfigGeneration int64  `json:"configGeneration"`
	YanetGeneration  int64  `json:"yanetGeneration"`
	Activated        bool   `json:"activated"`
}

func gatewayPolicyArgs(args []string) []string {
	var policy []string
	for _, arg := range args {
		if arg == "--gateway-policy" || strings.HasPrefix(arg, "--gateway-account=") || strings.HasPrefix(arg, "--uri=") || strings.HasPrefix(arg, "--ssh-trust=") {
			if strings.HasPrefix(arg, "--ssh-trust=") {
				arg = "--ssh-trust"
			}
			policy = append(policy, arg)
		}
	}
	return policy
}

func isGatewayPolicyContainer(d *appsv1.Deployment, c *corev1.Container) bool {
	return len(c.Args) > 0 && c.Args[0] == "credential-sync" && len(c.Command) == 1 && c.Command[0] == "/manager" && slices.Contains(c.Args, "--gateway-policy") &&
		(d.Labels[manifests.LabelComponent] == "credential-delivery" || (strings.HasPrefix(c.Name, "yanet-credential-") && strings.HasSuffix(c.Name, "-check")))
}

func gatewayContainers(d *appsv1.Deployment) map[string][]string {
	result := map[string][]string{}
	for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
		for _, c := range containers {
			if isGatewayPolicyContainer(d, &c) {
				for _, arg := range c.Args {
					if strings.HasPrefix(arg, "--uri=") {
						result[arg] = gatewayPolicyArgs(c.Args)
					}
				}
			}
		}
	}
	return result
}

func addPolicyRevision(d *appsv1.Deployment, state policyState, sshHash string) {
	for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
		for i := range containers {
			c := &containers[i]
			if !isGatewayPolicyContainer(d, c) {
				continue
			}
			c.Args = slices.DeleteFunc(c.Args, func(arg string) bool {
				return strings.HasPrefix(arg, "--policy-epoch=") || strings.HasPrefix(arg, "--policy-revision=") || strings.HasPrefix(arg, "--ssh-trust-sha256=")
			})
			c.Args = append(c.Args, "--policy-epoch="+state.UID, "--policy-revision="+strconv.FormatUint(state.Sequence, 10), "--ssh-trust-sha256="+sshHash)
			if c.ReadinessProbe != nil && c.ReadinessProbe.Exec != nil {
				command := slices.DeleteFunc(c.ReadinessProbe.Exec.Command, func(arg string) bool {
					return strings.HasPrefix(arg, "--policy-epoch=") || strings.HasPrefix(arg, "--policy-revision=") || arg == "--exact-policy-revision" || strings.HasPrefix(arg, "--ssh-trust-sha256=")
				})
				command = append(command, "--policy-epoch="+state.UID, "--policy-revision="+strconv.FormatUint(state.Sequence, 10), "--exact-policy-revision", "--ssh-trust-sha256="+sshHash)
				c.ReadinessProbe.Exec.Command = command
			}
		}
	}
}

func sshPolicySource(cm *corev1.ConfigMap, key string) (string, error) {
	if value, ok := cm.Data[key]; ok {
		return value, nil
	}
	if value, ok := cm.BinaryData[key]; ok {
		return string(value), nil
	}
	return "", fmt.Errorf("SSH policy ConfigMap %s/%s is missing key %s", cm.Namespace, cm.Name, key)
}

func (r *YanetReconciler) preparePolicyHandoff(ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, plans map[string]manifests.CertificatePlan, workloads map[string][]renderedWorkload, nodes []corev1.Node, autoSync bool) error {
	if cfg.Certificates == nil {
		return nil
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	} // Test clients are uncached; managers always set APIReader.
	var desired []*appsv1.Deployment
	for _, plan := range plans {
		if plan.Delivery != nil {
			desired = append(desired, plan.Delivery)
		}
	}
	for _, nodeWorkloads := range workloads {
		for _, w := range nodeWorkloads {
			desired = append(desired, w.deployment)
		}
	}
	policies := map[string]map[string][]string{}
	for _, d := range desired {
		if p := gatewayContainers(d); len(p) > 0 {
			policies[d.Name] = p
		}
	}
	if len(policies) == 0 {
		return nil
	}
	// Source fingerprints contain only authorization/identity, not leaf material,
	// issuer readiness, polling time, or exporter image. Renewal is not a rollout.
	policyInput := struct {
		Policies map[string]map[string][]string
		SSHTrust string
	}{Policies: policies}
	if trust := cfg.Certificates.SSHTrustBundle; trust != nil {
		cm := &corev1.ConfigMap{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, cm); err != nil {
			return err
		}
		value, err := sshPolicySource(cm, trust.Key)
		if err != nil {
			return err
		}
		policyInput.SSHTrust = value
	}
	expectedSSHHash := "none"
	if cfg.Certificates.SSHTrustBundle != nil {
		sshDigest := sha256.Sum256([]byte(policyInput.SSHTrust))
		expectedSSHHash = hex.EncodeToString(sshDigest[:])
	}
	encoded, marshalErr := json.Marshal(policyInput)
	if marshalErr != nil {
		return marshalErr
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	var state policyState
	allocationErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Verify complete source specs and identities through uncached reads before
		// allocating. Incomparable config UIDs are never ordered numerically.
		config := &api.YanetConfig{}
		if err := reader.Get(ctx, client.ObjectKey{Name: api.YanetConfigName}, config); err != nil {
			return err
		}
		fresh := &api.Yanet{}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(y), fresh); err != nil {
			return err
		}
		if config.UID == "" || config.Generation < 1 || fresh.UID != y.UID || fresh.Generation < 1 || !apiequality.Semantic.DeepEqual(config.Spec, *cfg) || !apiequality.Semantic.DeepEqual(fresh.Spec, y.Spec) {
			return fmt.Errorf("policy source changed or lacks API identity/generation; retry with current configuration")
		}
		for i := range nodes {
			current := &corev1.Node{}
			if err := reader.Get(ctx, client.ObjectKey{Name: nodes[i].Name}, current); err != nil {
				return err
			}
			if current.UID != nodes[i].UID || !apiequality.Semantic.DeepEqual(current.Labels, nodes[i].Labels) {
				return fmt.Errorf("policy Node source changed: %s", nodes[i].Name)
			}
		}
		// In particular, a config read before recreation must not be used with
		// a Yanet revision read after recreation. Revalidate after reading the
		// CAS object; a later concurrent allocation then conflicts on its RV.
		verified := &api.YanetConfig{}
		if err := reader.Get(ctx, client.ObjectKey{Name: api.YanetConfigName}, verified); err != nil {
			return err
		}
		if verified.UID != config.UID || verified.Generation != config.Generation {
			return fmt.Errorf("policy source changed while reading installation revision")
		}
		if trust := cfg.Certificates.SSHTrustBundle; trust != nil {
			current := &corev1.ConfigMap{}
			if err := reader.Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, current); err != nil {
				return err
			}
			value, err := sshPolicySource(current, trust.Key)
			if err != nil {
				return err
			}
			if value != policyInput.SSHTrust {
				return fmt.Errorf("SSH policy source changed while allocating revision")
			}
		}
		state = policyState{}
		stored := fresh.Annotations[policyStateAnnotation]
		if stored != "" {
			if err := json.Unmarshal([]byte(stored), &state); err != nil {
				return fmt.Errorf("invalid persisted policy state: %w", err)
			}
		}
		if state.UID != "" && (state.UID != string(y.UID) || state.Sequence == 0) {
			return fmt.Errorf("policy state belongs to a different installation or has invalid revision")
		}
		if stored != "" {
			decoded, decodeErr := hex.DecodeString(state.Hash)
			if state.UID == "" || state.Sequence == 0 || decodeErr != nil || len(decoded) != sha256.Size || state.ConfigUID == "" || state.ConfigGeneration < 1 || state.YanetGeneration < 1 {
				return fmt.Errorf("invalid persisted policy state; refuse to reset ordering")
			}
		}
		if state.UID == "" {
			// An upgrade is allowed to add revision metadata only to the unchanged
			// existing policy. Never infer the old authorization from a new config.
			existing := &appsv1.DeploymentList{}
			if err := reader.List(ctx, existing, client.InNamespace(y.Namespace)); err != nil {
				return err
			}
			for i := range existing.Items {
				d := &existing.Items[i]
				if !controlledByYanet(d, y) {
					continue
				}
				old := gatewayContainers(d)
				if len(old) == 0 {
					continue
				}
				if !apiequality.Semantic.DeepEqual(old, policies[d.Name]) {
					return fmt.Errorf("gateway policy bootstrap requires the unchanged v3.1.0 policy; restore policy before upgrading gates/checkpoints")
				}
				for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
					for _, c := range containers {
						if !isGatewayPolicyContainer(d, &c) {
							continue
						}
						for _, arg := range c.Args {
							if strings.HasPrefix(arg, "--policy-revision=") {
								return fmt.Errorf("managed policy state is missing; refuse to reset revisions")
							}
						}
					}
				}
			}
			state = policyState{UID: string(y.UID), Sequence: 1, Hash: hash}
		} else {
			if state.YanetGeneration > fresh.Generation || (state.ConfigUID == string(config.UID) && state.ConfigGeneration > config.Generation) {
				return fmt.Errorf("stale policy source generation")
			}
			changed := state.Hash != hash || (state.Activated && state.ConfigUID != string(config.UID))
			if changed {
				if !state.Activated {
					return fmt.Errorf("policy changes are blocked until revision-aware gates and checkpoints are ready; acknowledge %s=%s/1 after refreshing checkpoints", policyAckAnnotation, y.UID)
				}
				if state.Sequence == ^uint64(0) {
					return fmt.Errorf("policy revision exhausted")
				}
				state.Sequence++
				state.Hash = hash
			}
		}
		state.ConfigUID = string(config.UID)
		state.ConfigGeneration = config.Generation
		state.YanetGeneration = fresh.Generation
		for _, d := range desired {
			addPolicyRevision(d, state, expectedSSHHash)
		}
		if !state.Activated && fresh.Annotations[policyAckAnnotation] == string(y.UID)+"/1" {
			ready := true
			for _, d := range desired {
				if len(gatewayContainers(d)) == 0 {
					continue
				}
				existing := &appsv1.Deployment{}
				getErr := reader.Get(ctx, client.ObjectKeyFromObject(d), existing)
				if apierrors.IsNotFound(getErr) {
					ready = false
					continue
				}
				if getErr != nil {
					return getErr
				}
				if err := validateDeploymentOwnership(existing, d); err != nil {
					return err
				}
				_, changed, normalizeErr := r.desiredDeploymentUpdate(ctx, existing, d)
				if normalizeErr != nil {
					return normalizeErr
				}
				replicas := int32(1)
				if d.Spec.Replicas != nil {
					replicas = *d.Spec.Replicas
				}
				if changed || !existing.DeletionTimestamp.IsZero() || existing.Status.ObservedGeneration < existing.Generation || existing.Status.UpdatedReplicas != replicas || existing.Status.ReadyReplicas != replicas || existing.Status.AvailableReplicas != replicas || existing.Status.Replicas != replicas {
					ready = false
				}
			}
			state.Activated = ready
		}
		value, stateMarshalErr := json.Marshal(state)
		if stateMarshalErr != nil {
			return stateMarshalErr
		}
		if string(value) == stored || !autoSync {
			return nil
		}
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[policyStateAnnotation] = string(value)
		return r.Update(ctx, fresh)
	})
	return allocationErr
}
