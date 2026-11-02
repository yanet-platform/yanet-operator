package credentials

import (
	"fmt"
	"net/url"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

// GatewayAccount is a node/installation-bound client and its RPC permissions.
// Only administrative delivery configuration supplies these public policy data.
type GatewayAccount struct {
	Username    string   `json:"username" yaml:"username"`
	Permissions []string `json:"permissions" yaml:"permissions"`
}

func accountFiles(accounts []GatewayAccount) (map[string][]byte, error) {
	if accounts == nil {
		return nil, nil
	}
	if len(accounts) > 32 {
		return nil, fmt.Errorf("at most 32 gateway accounts are supported")
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
		if err != nil || uri.Scheme != "spiffe" || uri.Host == "" || uri.User != nil || uri.RawQuery != "" || uri.Fragment != "" || seen[account.Username] {
			return nil, fmt.Errorf("gateway accounts require unique SPIFFE usernames")
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
