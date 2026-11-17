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
)

// PolicyRevision is an installation-scoped, controller-allocated ordering, not
// a content hash or a Kubernetes resourceVersion. Node/role binding remains the
// independently checked certificate identity and private destination directory.
type PolicyRevision struct {
	Epoch    string `json:"epoch"`
	Sequence uint64 `json:"sequence"`
}

const policyFilename = "policy.json"

type installedPolicy struct {
	Revision PolicyRevision   `json:"revision"`
	Accounts []GatewayAccount `json:"accounts"`
	SSHHash  string           `json:"sshHash,omitempty"`
}

func validateRevision(revision *PolicyRevision) error {
	if revision == nil || revision.Epoch == "" || revision.Sequence == 0 {
		return fmt.Errorf("policy epoch and positive revision are required together")
	}
	return nil
}

func encodePolicy(policy *GatewayPolicy, files map[string][]byte) ([]byte, error) {
	if err := validateRevision(policy.Revision); err != nil {
		return nil, err
	}
	metadata := installedPolicy{Revision: *policy.Revision, Accounts: policy.Accounts}
	if metadata.Accounts == nil {
		metadata.Accounts = []GatewayAccount{}
	}
	if data, ok := files["ssh-ca.pub"]; ok {
		hash := sha256.Sum256(data)
		metadata.SSHHash = hex.EncodeToString(hash[:])
	}
	return json.Marshal(metadata)
}

// readPolicy validates the complete installed authorization set, including the
// absence of removed SSH trust. Newer policies do not inherit old gate grants.
func readPolicy(generation string) (*installedPolicy, error) {
	root, openErr := os.OpenRoot(generation)
	if openErr != nil {
		return nil, openErr
	}
	defer func() { _ = root.Close() }()
	data, metadataErr := root.ReadFile(policyFilename)
	if metadataErr != nil {
		return nil, metadataErr
	}
	var metadata installedPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&metadata); decodeErr != nil {
		return nil, decodeErr
	}
	if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
		return nil, fmt.Errorf("policy metadata must be exactly one JSON object")
	}
	if revisionErr := validateRevision(&metadata.Revision); revisionErr != nil {
		return nil, revisionErr
	}
	accounts := metadata.Accounts
	if accounts == nil {
		accounts = []GatewayAccount{}
	}
	files, accountErr := accountFiles(accounts)
	if accountErr != nil {
		return nil, accountErr
	}
	for name, expected := range files {
		stored, readErr := root.ReadFile(name)
		if readErr != nil {
			return nil, readErr
		}
		if !bytes.Equal(stored, expected) {
			return nil, fmt.Errorf("installed policy is damaged: %s", name)
		}
	}
	sshData, sshErr := root.ReadFile("ssh-ca.pub")
	if metadata.SSHHash == "" {
		if !os.IsNotExist(sshErr) {
			return nil, fmt.Errorf("unexpected installed SSH trust: %v", sshErr)
		}
	} else {
		if sshErr != nil {
			return nil, sshErr
		}
		if validationErr := ValidateSSHTrust(sshData); validationErr != nil {
			return nil, validationErr
		}
		hash := sha256.Sum256(sshData)
		if hex.EncodeToString(hash[:]) != metadata.SSHHash {
			return nil, fmt.Errorf("installed SSH trust is damaged")
		}
	}
	var content []byte
	for _, name := range []string{"tls.crt", "ca.crt", "identities.yaml", "permissions.yaml", "ssh-ca.pub", policyFilename} {
		value, readErr := root.ReadFile(name)
		if os.IsNotExist(readErr) && name == "ssh-ca.pub" && metadata.SSHHash == "" {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		content = append(content, value...)
	}
	hash := sha256.Sum256(content)
	if filepath.Base(generation) != hex.EncodeToString(hash[:]) {
		return nil, fmt.Errorf("installed policy generation digest is damaged")
	}
	return &metadata, nil
}

func compareRevision(installed, requested PolicyRevision) error {
	if installed.Epoch != requested.Epoch {
		return fmt.Errorf("policy epoch mismatch")
	}
	if installed.Sequence < requested.Sequence {
		return fmt.Errorf("installed policy revision %d is older than required %d", installed.Sequence, requested.Sequence)
	}
	return nil
}

func authorizePublication(destination string, policy *GatewayPolicy, files map[string][]byte) error {
	current := filepath.Join(destination, "current")
	generation, resolveErr := filepath.EvalSymlinks(current)
	if os.IsNotExist(resolveErr) {
		// A broken current link is not an empty destination or permission to
		// reinitialize its ordering fence.
		if _, err := os.Lstat(current); !os.IsNotExist(err) {
			return resolveErr
		}
		if policy != nil && policy.requireCurrent {
			return fmt.Errorf("legacy policy bridge requires a durable current generation")
		}
		return nil
	}
	if resolveErr != nil {
		return resolveErr
	}
	root, openErr := os.OpenRoot(generation)
	if openErr != nil {
		return openErr
	}
	defer func() { _ = root.Close() }()
	_, metadataErr := root.Stat(policyFilename)
	if os.IsNotExist(metadataErr) {
		// Absence is a legacy format only when the directory's original content
		// address agrees. Removing policy.json from a versioned generation is
		// corruption, not permission to re-bootstrap or use an unversioned writer.
		var content []byte
		for _, name := range []string{"tls.crt", "ca.crt", "identities.yaml", "permissions.yaml", "ssh-ca.pub"} {
			value, readErr := root.ReadFile(name)
			if os.IsNotExist(readErr) && (name == "identities.yaml" || name == "permissions.yaml" || name == "ssh-ca.pub") {
				continue
			}
			if readErr != nil {
				return readErr
			}
			content = append(content, value...)
		}
		digest := sha256.Sum256(content)
		if filepath.Base(generation) != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("missing or damaged installed policy metadata")
		}
		// Only the initial, unchanged-policy bridge may upgrade a legacy
		// publication. Never initialize an arbitrary later sequence from it.
		if policy == nil || policy.Revision == nil {
			return nil
		}
		if policy.Revision.Sequence != 1 && !policy.bridge {
			return fmt.Errorf("missing installed policy revision; unchanged-policy bootstrap is required")
		}
		return unchangedAuthorization(root, files)
	}
	if metadataErr != nil {
		return metadataErr
	}
	installed, policyErr := readPolicy(generation)
	if policyErr != nil {
		return policyErr
	}
	if policy == nil || policy.Revision == nil {
		return fmt.Errorf("unversioned writer cannot overwrite a managed policy")
	}
	if installed.Revision.Epoch != policy.Revision.Epoch {
		return fmt.Errorf("policy epoch mismatch")
	}
	if policy.Revision.Sequence < installed.Revision.Sequence {
		return fmt.Errorf("stale policy writer cannot roll back revision %d", installed.Revision.Sequence)
	}
	if policy.Revision.Sequence == installed.Revision.Sequence {
		stored, readErr := root.ReadFile(policyFilename)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(stored, files[policyFilename]) {
			return fmt.Errorf("equal policy revisions have different authorization sets")
		}
	}
	if policy.bridge {
		return unchangedAuthorization(root, files)
	}
	return nil
}

func unchangedAuthorization(root *os.Root, files map[string][]byte) error {
	for _, name := range []string{"identities.yaml", "permissions.yaml", "ssh-ca.pub"} {
		stored, err := root.ReadFile(name)
		if os.IsNotExist(err) && files[name] == nil {
			continue
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, files[name]) {
			return fmt.Errorf("bootstrap cannot change installed policy: %s", name)
		}
	}
	return nil
}
