package credentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PolicyBundleKey carries the accounts and optional SSH trust in one projection.
const PolicyBundleKey = "bundle.json"

// MaxPolicyBundleSize leaves headroom below the ConfigMap payload limit.
const MaxPolicyBundleSize = 900 * 1024

// OnlinePolicyBundle supplies the same public policy to every gateway on a Node.
type OnlinePolicyBundle struct {
	Accounts []GatewayAccount `json:"accounts"`
	SSHTrust []byte           `json:"ssh_trust,omitempty"`
}

// EncodeOnlinePolicyBundle validates the complete update before API writes.
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

// DecodeOnlinePolicyBundle accepts an explicit account list, including empty.
func DecodeOnlinePolicyBundle(value []byte) (OnlinePolicyBundle, error) {
	var bundle OnlinePolicyBundle
	if len(value) > MaxPolicyBundleSize {
		return bundle, fmt.Errorf("gateway policy bundle exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return bundle, fmt.Errorf("decode gateway policy bundle: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return bundle, fmt.Errorf("gateway policy must contain exactly one JSON object")
	}
	_, err := bundleFiles(bundle)
	return bundle, err
}

func bundleFiles(bundle OnlinePolicyBundle) (map[string][]byte, error) {
	if bundle.Accounts == nil {
		return nil, fmt.Errorf("gateway policy requires an explicit accounts list")
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
	return files, nil
}

func readProjectedPolicy(source string) (map[string][]byte, error) {
	file, err := os.Open(source) // #nosec G304 -- Path is configured by the delivery workload.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	value, err := io.ReadAll(io.LimitReader(file, MaxPolicyBundleSize+1))
	if err != nil {
		return nil, err
	}
	bundle, err := DecodeOnlinePolicyBundle(value)
	if err != nil {
		return nil, err
	}
	return bundleFiles(bundle)
}

// Each file is replaced atomically. Readers refresh files independently.
func publishPolicyFiles(destination string, files map[string][]byte) (bool, error) {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return false, err
	}
	changed := false
	for _, name := range []string{"identities.yaml", "permissions.yaml", "ssh-ca.pub"} {
		filename := filepath.Join(destination, name)
		data, exists := files[name]
		current, err := os.ReadFile(filename) // #nosec G304 -- Fixed filenames under the configured persistent mount.
		if err != nil && !os.IsNotExist(err) {
			return changed, err
		}
		if !exists {
			if os.IsNotExist(err) {
				continue
			}
			if removeErr := os.Remove(filename); removeErr != nil {
				return changed, removeErr
			}
		} else {
			if err == nil && bytes.Equal(current, data) {
				continue
			}
			staging, err := os.CreateTemp(destination, ".policy-")
			if err != nil {
				return changed, err
			}
			temp := staging.Name()
			if _, err = staging.Write(data); err == nil {
				err = staging.Sync()
			}
			if closeErr := staging.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(temp, filename)
			}
			if err != nil {
				_ = os.Remove(temp)
				return changed, err
			}
		}
		changed = true
	}
	return changed, syncDirectory(destination)
}

func checkPolicyFiles(destination string, expected map[string][]byte) error {
	for _, name := range []string{"identities.yaml", "permissions.yaml", "ssh-ca.pub"} {
		if expected == nil && name == "ssh-ca.pub" {
			continue
		}
		value, err := os.ReadFile(filepath.Join(destination, name)) // #nosec G304 -- Fixed filenames under the configured persistent mount.
		want, exists := expected[name]
		if expected != nil && !exists && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if expected != nil && (!exists || !bytes.Equal(value, want)) {
			return fmt.Errorf("local policy differs from the projection: %s", name)
		}
	}
	return nil
}
