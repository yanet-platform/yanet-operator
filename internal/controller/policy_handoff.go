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
	"github.com/yanet-platform/yanet-operator/internal/credentials"
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
const policyPreviewDestination = "--destination=/persist/.policy-preview"
const deferPolicyReadiness = "--defer-policy-readiness"

// SSH source failures may freeze credential-bearing updates without blocking
// independently safe lifecycle operations. Inventory and bridge errors are not
// in this category.
type sshPolicyUnavailableError struct{ err error }

func (e *sshPolicyUnavailableError) Error() string { return e.err.Error() }
func (e *sshPolicyUnavailableError) Unwrap() error { return e.err }

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
	// Keep the original per-role inputs across attempts, including departed
	// scopes. Drop them only when allocating past the activated bridge.
	BridgeScopes map[string]policyBridgeScope `json:"bridgeScopes,omitempty"`
	// Once a mixed bridge permits new readers, its verified SSH bytes are
	// fixed until ACK, even across subsequent membership changes.
	BridgeSSHHash string `json:"bridgeSSHHash,omitempty"`
}

type policyBridgeScope struct {
	Path    string `json:"path"`
	Policy  string `json:"policy"`
	Legacy  bool   `json:"legacy,omitempty"`
	Preview bool   `json:"preview,omitempty"`
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
				return strings.HasPrefix(arg, "--policy-epoch=") || strings.HasPrefix(arg, "--policy-revision=") || strings.HasPrefix(arg, "--ssh-trust-sha256=") || strings.HasPrefix(arg, "--policy-bridge=") || arg == policyPreviewDestination
			})
			c.Args = append(c.Args, "--policy-epoch="+state.UID, "--policy-revision="+strconv.FormatUint(state.Sequence, 10), "--ssh-trust-sha256="+sshHash)
			preview := false
			if d.Labels[manifests.LabelComponent] == "credential-delivery" && state.BridgeScopes != nil {
				mode := "initial"
				for _, arg := range c.Args {
					scope := state.BridgeScopes[arg]
					if scope.Legacy {
						mode = "legacy"
					}
					preview = preview || (scope.Preview && state.BridgeSSHHash != sshHash)
				}
				if preview {
					// The writer can supersede a preview monotonically, but no
					// consumer or exact readiness check uses this directory.
					c.Args = append(c.Args, policyPreviewDestination)
				} else {
					c.Args = append(c.Args, "--policy-bridge="+mode)
				}
			}
			if c.ReadinessProbe != nil && c.ReadinessProbe.Exec != nil {
				command := slices.DeleteFunc(c.ReadinessProbe.Exec.Command, func(arg string) bool {
					return strings.HasPrefix(arg, "--policy-epoch=") || strings.HasPrefix(arg, "--policy-revision=") || arg == "--exact-policy-revision" || strings.HasPrefix(arg, "--ssh-trust-sha256=") || arg == deferPolicyReadiness
				})
				command = append(command, "--policy-epoch="+state.UID, "--policy-revision="+strconv.FormatUint(state.Sequence, 10), "--exact-policy-revision", "--ssh-trust-sha256="+sshHash)
				if preview {
					command = append(command, deferPolicyReadiness)
				}
				c.ReadinessProbe.Exec.Command = command
			}
		}
	}
}

// collectBridgeScopes merges the writer and NUMA gate templates by the exact
// certificate URI and documents which private path a legacy reader uses.
func collectBridgeScopes(d *appsv1.Deployment, scopes map[string]policyBridgeScope, missingState bool) error {
	for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
		for i := range containers {
			c := &containers[i]
			if !isGatewayPolicyContainer(d, c) {
				continue
			}
			var uri, path string
			versioned := false
			for _, arg := range c.Args {
				if strings.HasPrefix(arg, "--uri=") {
					uri = arg
				}
				versioned = versioned || strings.HasPrefix(arg, "--policy-revision=")
			}
			if missingState && versioned {
				return fmt.Errorf("managed policy state is missing; refuse to reset revisions")
			}
			for _, mount := range c.VolumeMounts {
				if mount.MountPath != "/persist" {
					continue
				}
				for _, volume := range d.Spec.Template.Spec.Volumes {
					if volume.Name == mount.Name && volume.HostPath != nil {
						path = volume.HostPath.Path
					}
				}
			}
			if uri == "" || path == "" {
				return fmt.Errorf("gateway policy scope in %s lacks a URI or private HostPath", d.Name)
			}
			// Account order is preserved too: v3.1.0 checks the generated
			// files byte-for-byte, even for otherwise equivalent grants.
			// The consumer re-specifies --ssh-trust with its installed path.
			policy := sha256.Sum256([]byte(strings.Join(slices.Compact(gatewayPolicyArgs(c.Args)), "\x00")))
			policyHash := hex.EncodeToString(policy[:])
			old, exists := scopes[uri]
			if exists && (old.Path != path || old.Policy != policyHash) {
				return fmt.Errorf("gateway policy changes are blocked until the bridge completes; restore the unchanged v3.1.0 policy for %s", uri)
			}
			scopes[uri] = policyBridgeScope{Path: path, Policy: policyHash, Legacy: old.Legacy || !versioned, Preview: old.Preview && versioned}
		}
	}
	return nil
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

func bridgeNodeUID(uri string) string {
	parts := strings.Split(uri, "/")
	// CertificateURI ends in /node/<UID>/role/<profile>.
	if len(parts) >= 5 && parts[len(parts)-4] == "node" && parts[len(parts)-2] == "role" {
		return parts[len(parts)-3]
	}
	return ""
}

func pendingBridgeScopes(ctx context.Context, reader client.Reader, y *api.Yanet, desired []*appsv1.Deployment, nodes []corev1.Node, state policyState) (map[string]policyBridgeScope, error) {
	// Re-read all selected Node UIDs, including the absence of a departing
	// Node. A cache that silently omits a live Node cannot retire its scope.
	selected := &corev1.NodeList{}
	if err := reader.List(ctx, selected, client.MatchingLabels(y.Spec.NodeSelector)); err != nil {
		return nil, err
	}
	selectedUIDs := make(map[string]bool, len(selected.Items))
	for _, node := range selected.Items {
		selectedUIDs[string(node.UID)] = true
	}
	if len(nodes) != len(selected.Items) {
		return nil, fmt.Errorf("policy Node source changed: selected membership differs from rendered inventory")
	}
	for _, node := range nodes {
		if !selectedUIDs[string(node.UID)] {
			return nil, fmt.Errorf("policy Node source changed: %s", node.Name)
		}
	}

	live := &appsv1.DeploymentList{}
	if err := reader.List(ctx, live, client.InNamespace(y.Namespace)); err != nil {
		return nil, err
	}
	original := state.BridgeScopes
	if original == nil {
		original = make(map[string]policyBridgeScope)
	}
	for i := range live.Items {
		d := &live.Items[i]
		if controlledByYanet(d, y) {
			if err := collectBridgeScopes(d, original, state.UID == ""); err != nil {
				return nil, err
			}
		}
	}
	candidate := make(map[string]policyBridgeScope)
	for _, d := range desired {
		if err := collectBridgeScopes(d, candidate, false); err != nil {
			return nil, err
		}
	}
	retained, hasLegacy := false, false
	originalNodes := make(map[string]bool, len(original))
	for uri, old := range original {
		uid := bridgeNodeUID(uri)
		originalNodes[uid] = true
		hasLegacy = hasLegacy || old.Legacy
		if target, ok := candidate[uri]; ok {
			if target.Path != old.Path || target.Policy != old.Policy {
				return nil, fmt.Errorf("gateway policy changes are blocked until the bridge completes; restore the unchanged v3.1.0 policy for %s", uri)
			}
			retained = true
			continue
		}
		// A reusable Node name is not the private destination's identity.
		if uid == "" || selectedUIDs[uid] {
			return nil, fmt.Errorf("gateway policy removal is blocked until the checkpoint bridge completes for %s", uri)
		}
	}
	if len(original) != 0 && len(nodes) != 0 && !retained {
		return nil, fmt.Errorf("no original gateway scope remains for checkpoint bridge; restore a reachable scope or explicitly retire the installation")
	}
	for uri, scope := range candidate {
		if _, ok := original[uri]; !ok {
			if originalNodes[bridgeNodeUID(uri)] {
				return nil, fmt.Errorf("new gateway roles on existing Node UIDs are blocked until the checkpoint bridge completes: %s", uri)
			}
			// Desired templates are not evidence that a legacy publication exists.
			scope.Legacy = false
			scope.Preview = hasLegacy
			original[uri] = scope
		}
	}
	return original, nil
}

func (r *YanetReconciler) policyRolloutReady(ctx context.Context, reader client.Reader, desired *appsv1.Deployment) (bool, error) {
	live := &appsv1.Deployment{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(desired), live); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if err := validateDeploymentOwnership(live, desired); err != nil {
		return false, err
	}
	_, changed, err := r.desiredDeploymentUpdate(ctx, live, desired)
	if err != nil {
		return false, err
	}
	replicas := int32(1)
	if desired.Spec.Replicas != nil {
		replicas = *desired.Spec.Replicas
	}
	status := live.Status
	return !changed && live.DeletionTimestamp.IsZero() && status.ObservedGeneration >= live.Generation && status.UpdatedReplicas == replicas && status.ReadyReplicas == replicas && status.AvailableReplicas == replicas && status.Replicas == replicas, nil
}

// A preview writer is never a witness for the policy it is proposing. The
// original private paths must prove byte continuity through exact readiness.
func (r *YanetReconciler) previewContinuityReady(ctx context.Context, reader client.Reader, desired []*appsv1.Deployment, state policyState) (bool, error) {
	needsProof, hasLegacy := false, false
	var witnesses []*appsv1.Deployment
	for _, d := range desired {
		if d.Labels[manifests.LabelComponent] != "credential-delivery" || (d.Spec.Replicas != nil && *d.Spec.Replicas == 0) {
			continue
		}
		legacy, preview := false, false
		policies := gatewayContainers(d)
		for uri := range policies {
			legacy = legacy || state.BridgeScopes[uri].Legacy
			preview = preview || state.BridgeScopes[uri].Preview
		}
		needsProof = needsProof || preview
		if len(policies) != 0 && !preview {
			hasLegacy = hasLegacy || legacy
			witnesses = append(witnesses, d)
		}
	}
	if !needsProof || !hasLegacy {
		return false, nil
	}
	for _, d := range witnesses {
		if ready, readyErr := r.policyRolloutReady(ctx, reader, d); readyErr != nil || !ready {
			return false, readyErr
		}
	}
	return true, nil
}

func (r *YanetReconciler) preparePolicyHandoff(ctx context.Context, y *api.Yanet, cfg *api.YanetConfigSpec, plans map[string]manifests.CertificatePlan, workloads map[string][]renderedWorkload, nodes []corev1.Node, autoSync bool) error {
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
	// Source fingerprints contain only authorization/identity, not leaf material,
	// issuer readiness, polling time, or exporter image. Renewal is not a rollout.
	policyInput := struct {
		Policies map[string]map[string][]string
		SSHTrust string
	}{Policies: policies}
	var trust *corev1.ConfigMapKeySelector
	if len(policies) > 0 && cfg.Certificates != nil {
		trust = cfg.Certificates.SSHTrustBundle
	}
	if trust != nil {
		cm := &corev1.ConfigMap{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, cm); err != nil {
			return &sshPolicyUnavailableError{err}
		}
		value, err := sshPolicySource(cm, trust.Key)
		if err != nil {
			return &sshPolicyUnavailableError{err}
		}
		if err := credentials.ValidateSSHTrust([]byte(value)); err != nil {
			return &sshPolicyUnavailableError{fmt.Errorf("invalid SSH policy ConfigMap %s/%s key %s: %w", cm.Namespace, cm.Name, trust.Key, err)}
		}
		policyInput.SSHTrust = value
	}
	expectedSSHHash := "none"
	if trust != nil {
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
		// No-op for an unmanaged installation, but never infer that from an
		// empty desired set: a palette edit can remove all desired gates.
		if len(policies) == 0 {
			current := &api.Yanet{}
			if err := reader.Get(ctx, client.ObjectKeyFromObject(y), current); err != nil {
				return err
			}
			if current.UID != y.UID {
				return fmt.Errorf("policy installation UID changed")
			}
			if current.Annotations[policyStateAnnotation] == "" {
				live := &appsv1.DeploymentList{}
				if err := reader.List(ctx, live, client.InNamespace(y.Namespace)); err != nil {
					return err
				}
				managed := false
				for i := range live.Items {
					d := &live.Items[i]
					if controlledByYanet(d, y) && len(gatewayContainers(d)) != 0 {
						managed = true
						break
					}
				}
				if !managed {
					return nil
				}
			}
		}
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
		if trust != nil {
			current := &corev1.ConfigMap{}
			if err := reader.Get(ctx, client.ObjectKey{Namespace: y.Namespace, Name: trust.Name}, current); err != nil {
				return &sshPolicyUnavailableError{err}
			}
			value, err := sshPolicySource(current, trust.Key)
			if err != nil {
				return &sshPolicyUnavailableError{err}
			}
			if value != policyInput.SSHTrust {
				return &sshPolicyUnavailableError{fmt.Errorf("SSH policy source changed while allocating revision")}
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
		if !state.Activated {
			if len(policies) == 0 && len(nodes) != 0 {
				return fmt.Errorf("managed gateway removal is blocked until the checkpoint bridge completes")
			}
			scopes, err := pendingBridgeScopes(ctx, reader, y, desired, nodes, state)
			if err != nil {
				return err
			}
			state.BridgeScopes = scopes
		}
		if state.UID == "" {
			state.UID, state.Sequence, state.Hash = string(y.UID), 1, hash
		} else {
			if state.YanetGeneration > fresh.Generation || (state.ConfigUID == string(config.UID) && state.ConfigGeneration > config.Generation) {
				return fmt.Errorf("stale policy source generation")
			}
			changed := state.Hash != hash || (state.Activated && state.ConfigUID != string(config.UID))
			if changed {
				// A new provisional attempt never reuses a number. Until ACK,
				// its writer still verifies the original on-disk authorization.
				if state.Sequence == ^uint64(0) {
					return fmt.Errorf("policy revision exhausted")
				}
				state.Sequence++
				state.Hash = hash
				if state.Activated {
					state.BridgeScopes = nil
					state.BridgeSSHHash = ""
				}
			}
		}
		state.ConfigUID = string(config.UID)
		state.ConfigGeneration = config.Generation
		state.YanetGeneration = fresh.Generation
		for _, d := range desired {
			addPolicyRevision(d, state, expectedSSHHash)
		}
		if !state.Activated && state.BridgeSSHHash == "" {
			verified, verifyErr := r.previewContinuityReady(ctx, reader, desired, state)
			if verifyErr != nil {
				return verifyErr
			}
			if verified {
				state.BridgeSSHHash = expectedSSHHash
				for _, d := range desired {
					addPolicyRevision(d, state, expectedSSHHash)
				}
			}
		}
		if !state.Activated && len(policies) != 0 && fresh.Annotations[policyAckAnnotation] == string(y.UID)+"/"+strconv.FormatUint(state.Sequence, 10) {
			ready := true
			for _, d := range desired {
				if len(gatewayContainers(d)) == 0 {
					continue
				}
				rolledOut, readyErr := r.policyRolloutReady(ctx, reader, d)
				if readyErr != nil {
					return readyErr
				}
				ready = ready && rolledOut
				// A preview is never part of a completed rollout, even if
				// someone has supplied replica status for its Deployment.
				for uri := range gatewayContainers(d) {
					ready = ready && (!state.BridgeScopes[uri].Preview || state.BridgeSSHHash == expectedSSHHash)
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
