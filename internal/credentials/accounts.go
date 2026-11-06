package credentials

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// GatewayAccount is an explicitly authorized workload or local login and its RPC permissions.
// Only administrative delivery configuration supplies these public policy data.
type GatewayAccount struct {
	Username    string   `json:"username" yaml:"username"`
	Permissions []string `json:"permissions" yaml:"permissions"`
}

// GatewayPolicy is public administrative policy delivered with a server role.
// A nil policy omits account files; a non-nil empty policy revokes all accounts.
type GatewayPolicy struct {
	Accounts     []GatewayAccount
	SSHTrustFile string
}

func gatewayFiles(policy *GatewayPolicy) (map[string][]byte, error) {
	if policy == nil {
		return nil, nil
	}
	accounts := policy.Accounts
	if accounts == nil {
		accounts = []GatewayAccount{}
	}
	files, err := accountFiles(accounts)
	if err != nil {
		return nil, err
	}
	if policy.SSHTrustFile == "" {
		return files, nil
	}
	data, err := os.ReadFile(policy.SSHTrustFile) // #nosec G304 -- The ConfigMap projection or persistent trust path is administrative input.
	if err != nil {
		return nil, fmt.Errorf("read SSH CA public keys: %w", err)
	}
	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || key.Type() != ssh.KeyAlgoECDSA256 || len(options) != 0 {
			return nil, fmt.Errorf("SSH CA bundle requires bare ECDSA P-256 public keys")
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSH CA public keys: %w", err)
	}
	if count == 0 {
		return nil, fmt.Errorf("SSH CA bundle contains no public keys")
	}
	files["ssh-ca.pub"] = data
	return files, nil
}

func accountFiles(accounts []GatewayAccount) (map[string][]byte, error) {
	if accounts == nil {
		return nil, nil
	}
	if len(accounts) > 64 {
		return nil, fmt.Errorf("at most 64 gateway accounts are supported")
	}
	type identity struct {
		Username string `yaml:"username"`
	}
	identities := struct {
		Identities []identity `yaml:"identities"`
	}{Identities: make([]identity, 0, len(accounts))}
	policy := struct {
		Permissions struct {
			Users []GatewayAccount `yaml:"users"`
		} `yaml:"permissions"`
	}{}
	policy.Permissions.Users = make([]GatewayAccount, 0, len(accounts))
	seen := map[string]bool{}
	for _, account := range accounts {
		uri, err := url.Parse(account.Username)
		workload := err == nil && uri.Scheme == "spiffe" && uri.Host != "" && uri.User == nil && uri.RawQuery == "" && uri.Fragment == ""
		if (!workload && !api.ValidGatewayUsername(account.Username)) || seen[account.Username] {
			return nil, fmt.Errorf("gateway accounts require unique SPIFFE or local usernames")
		}
		if err := api.ValidateGatewayPermissions(account.Permissions); err != nil {
			return nil, err
		}
		seen[account.Username] = true
		identities.Identities = append(identities.Identities, identity{Username: account.Username})
		policy.Permissions.Users = append(policy.Permissions.Users, account)
	}
	identityYAML, err := yaml.Marshal(identities)
	if err != nil {
		return nil, err
	}
	permissionYAML, err := yaml.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"identities.yaml": identityYAML, "permissions.yaml": permissionYAML}, nil
}
