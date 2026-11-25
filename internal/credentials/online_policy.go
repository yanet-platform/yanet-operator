package credentials

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PolicyBundleKey is a single projected file, so revision and content cannot
// come from distinct kubelet projections.
const PolicyBundleKey = "bundle.json"

// MaxPolicyBundleSize leaves headroom below the ConfigMap payload limit.
const MaxPolicyBundleSize = 900 * 1024

const maxPolicyFileSize = 2 * 1024 * 1024

// OnlinePolicyBundle is public policy data intended for one installation/node.
type OnlinePolicyBundle struct {
	Epoch     string           `json:"epoch"`
	NodeUID   string           `json:"node_uid"`
	Sequence  uint64           `json:"sequence"`
	Namespace string           `json:"namespace"`
	Domain    string           `json:"domain"`
	Roles     []string         `json:"roles"`
	Accounts  []GatewayAccount `json:"accounts"`
	SSHTrust  []byte           `json:"ssh_trust,omitempty"`
}

type policyScope struct {
	Epoch     string `json:"epoch"`
	NodeUID   string `json:"node_uid"`
	ServerURI string `json:"server_uri"`
}

type onlinePolicyManifest struct {
	Version  int         `json:"version"`
	Scope    policyScope `json:"scope"`
	Sequence uint64      `json:"sequence"`
	Identity string      `json:"identities_sha256"`
	Grants   string      `json:"permissions_sha256"`
	SSHTrust string      `json:"ssh_sha256,omitempty"`
}

func digestPolicy(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// EncodeOnlinePolicyBundle checks the projected payload before API writes.
func EncodeOnlinePolicyBundle(bundle OnlinePolicyBundle) ([]byte, error) {
	if _, err := bundleFiles(bundle); err != nil {
		return nil, err
	}
	value, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(value) > MaxPolicyBundleSize {
		return nil, fmt.Errorf("gateway policy is %d bytes; maximum is %d", len(value), MaxPolicyBundleSize)
	}
	return value, nil
}

// DecodeOnlinePolicyBundle requires exactly the canonical projected payload.
func DecodeOnlinePolicyBundle(value []byte) (OnlinePolicyBundle, error) {
	var bundle OnlinePolicyBundle
	if len(value) > MaxPolicyBundleSize {
		return bundle, fmt.Errorf("gateway policy bundle exceeds size limit")
	}
	if err := json.Unmarshal(value, &bundle); err != nil {
		return bundle, fmt.Errorf("decode gateway policy bundle: %w", err)
	}
	canonical, err := json.Marshal(bundle)
	if err != nil || !bytes.Equal(value, canonical) {
		return bundle, fmt.Errorf("gateway policy bundle is not canonical")
	}
	return bundle, nil
}

func bundleFiles(bundle OnlinePolicyBundle) (map[string][]byte, error) {
	if bundle.Epoch == "" || bundle.NodeUID == "" || bundle.Sequence == 0 ||
		!utf8.ValidString(bundle.Epoch) || !utf8.ValidString(bundle.NodeUID) ||
		len(validation.IsDNS1123Label(bundle.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(bundle.Domain)) != 0 {
		return nil, fmt.Errorf("invalid policy bundle identity or revision")
	}
	if bundle.Accounts == nil || len(bundle.Roles) == 0 || len(bundle.Roles) > 32 {
		return nil, fmt.Errorf("policy requires explicit accounts and 1..32 server roles")
	}
	roles := map[string]bool{}
	for _, role := range bundle.Roles {
		if len(role) > 40 || len(validation.IsDNS1123Label(role)) != 0 || roles[role] {
			return nil, fmt.Errorf("invalid or duplicate policy server role %q", role)
		}
		roles[role] = true
	}
	prefix := strings.TrimSuffix(api.CertificateURI(bundle.Domain, bundle.Namespace, bundle.Epoch, bundle.NodeUID, "placeholder"), "placeholder")
	for _, account := range bundle.Accounts {
		if api.ValidGatewayUsername(account.Username) {
			continue
		}
		role, ok := strings.CutPrefix(account.Username, prefix)
		if !ok || len(role) > 40 || len(validation.IsDNS1123Label(role)) != 0 {
			return nil, fmt.Errorf("workload account is outside policy scope")
		}
	}
	files, err := accountFiles(bundle.Accounts)
	if err != nil {
		return nil, err
	}
	if len(bundle.SSHTrust) != 0 {
		if err := ValidateSSHTrust(bundle.SSHTrust); err != nil {
			return nil, err
		}
		files["ssh-ca.pub"] = bundle.SSHTrust
	}
	for name, data := range files {
		if len(data) > maxPolicyFileSize {
			return nil, fmt.Errorf("rendered policy file exceeds reader size limit: %s", name)
		}
	}
	return files, nil
}

type onlinePolicyGeneration struct {
	role     string
	name     string
	manifest onlinePolicyManifest
	files    map[string][]byte
}

func onlinePolicyGenerations(bundle OnlinePolicyBundle, files map[string][]byte) ([]onlinePolicyGeneration, error) {
	template := onlinePolicyManifest{
		Version: 1, Sequence: bundle.Sequence,
		Identity: digestPolicy(files["identities.yaml"]), Grants: digestPolicy(files["permissions.yaml"]),
	}
	if trust := files["ssh-ca.pub"]; trust != nil {
		template.SSHTrust = digestPolicy(trust)
	}
	generations := make([]onlinePolicyGeneration, 0, len(bundle.Roles))
	for _, role := range bundle.Roles {
		manifest := template
		manifest.Scope = policyScope{
			Epoch: bundle.Epoch, NodeUID: bundle.NodeUID,
			ServerURI: api.CertificateURI(bundle.Domain, bundle.Namespace, bundle.Epoch, bundle.NodeUID, role),
		}
		value, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		candidate := map[string][]byte{policyFilename: value}
		for name, data := range files {
			candidate[name] = data
		}
		generations = append(generations, onlinePolicyGeneration{role: role, name: digestPolicy(value), manifest: manifest, files: candidate})
	}
	return generations, nil
}

func readOnlinePolicyFile(root *os.Root, name string, limit int64) ([]byte, error) {
	file, info, err := openPrivateFile(root, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > limit {
		return nil, fmt.Errorf("policy file exceeds size limit: %s", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("policy file exceeds size limit: %s", name)
	}
	return data, err
}

func readOnlinePolicy(root *os.Root, name string, expected policyScope) (onlinePolicyManifest, error) {
	var manifest onlinePolicyManifest
	if !isPolicyGeneration(name) {
		return manifest, fmt.Errorf("invalid policy generation name")
	}
	generation, err := openPrivateGeneration(root, name)
	if err != nil {
		return manifest, err
	}
	defer generation.Close()
	data, err := readOnlinePolicyFile(generation, policyFilename, 4096)
	if err != nil {
		return manifest, err
	}
	if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil {
		return manifest, decodeErr
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, data) || digestPolicy(data) != name ||
		manifest.Version != 1 || manifest.Scope != expected || manifest.Sequence == 0 {
		return manifest, fmt.Errorf("invalid installed policy metadata or scope")
	}
	for file, hash := range map[string]string{
		"identities.yaml": manifest.Identity, "permissions.yaml": manifest.Grants, "ssh-ca.pub": manifest.SSHTrust,
	} {
		if file == "ssh-ca.pub" && hash == "" {
			if _, err := generation.Lstat(file); !os.IsNotExist(err) {
				return manifest, fmt.Errorf("unexpected SSH trust in installed policy")
			}
			continue
		}
		data, err := readOnlinePolicyFile(generation, file, maxPolicyFileSize)
		if err != nil {
			return manifest, err
		}
		if digestPolicy(data) != hash {
			return manifest, fmt.Errorf("installed policy hash mismatch: %s", file)
		}
	}
	return manifest, nil
}

func isPolicyGeneration(name string) bool {
	return len(name) == 2*sha256.Size && strings.Trim(name, "0123456789abcdef") == ""
}

// publishOnlinePolicy reports durability independently of post-commit cleanup.
func publishOnlinePolicy(destination string, candidate onlinePolicyGeneration, durable *durableCurrent) (committed bool, resultErr error) {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return false, err
	}
	if err := os.Chmod(destination, 0o700); err != nil { // #nosec G302 -- This is an owner-only directory, which requires search permission.
		return false, err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return false, err
	}
	defer root.Close()
	lock, err := lockPublication(root, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return false, err
	}
	current, err := root.Readlink("current")
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	name, sequence := candidate.name, candidate.manifest.Sequence
	fence := func(generation string) error {
		installed, readErr := readOnlinePolicy(root, generation, candidate.manifest.Scope)
		if readErr != nil {
			return readErr
		}
		if installed.Sequence > sequence || (installed.Sequence == sequence && generation != name) {
			return fmt.Errorf("refusing older or conflicting gateway policy revision")
		}
		return nil
	}
	if current != "" {
		if err := fence(current); err != nil {
			return false, err
		}
	} else {
		// A crash during first publication can leave a staged generation with
		// no current. Its revision still prevents a stale projection from
		// bootstrapping an older generation if current was lost.
		for _, entry := range entries {
			if isPolicyGeneration(entry.Name()) {
				if err := fence(entry.Name()); err != nil {
					return false, err
				}
			}
		}
	}
	if current != name {
		if _, statErr := root.Lstat(name); os.IsNotExist(statErr) {
			if stageErr := stageGeneration(root, name, "", candidate.files, nil); stageErr != nil {
				return false, stageErr
			}
		} else if statErr != nil {
			return false, statErr
		} else if _, readErr := readOnlinePolicy(root, name, candidate.manifest.Scope); readErr != nil {
			return false, readErr
		}
		// Persist the role entry before exposing a newly written current.
		if syncErr := syncDirectory(filepath.Dir(destination)); syncErr != nil {
			return false, syncErr
		}
	}
	if err := switchCurrent(root, current, name, durable); err != nil {
		return false, err
	}
	// Readers pin only current and finish disk reads under the same lock.
	// The new current is durable, so earlier stages no longer need retention.
	for _, entry := range entries {
		old := entry.Name()
		if old != name && (isPolicyGeneration(old) || strings.HasPrefix(old, ".generation-")) {
			if err := root.RemoveAll(old); err != nil {
				return true, fmt.Errorf("policy is installed but cleanup of %s failed: %w", old, err)
			}
		}
	}
	return true, nil
}

func checkOnlinePolicy(destination string, scope policyScope, expectedGeneration string) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := lockPublication(root, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer lock.Close()
	generation, err := root.Readlink("current")
	if err != nil {
		return err
	}
	if expectedGeneration != "" && generation != expectedGeneration {
		return fmt.Errorf("local policy is not the projected revision")
	}
	if _, err := readOnlinePolicy(root, generation, scope); err != nil {
		return err
	}
	return syncRoot(root)
}
