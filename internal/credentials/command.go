package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"
)

type dnsNames []string

func (d *dnsNames) String() string         { return fmt.Sprint([]string(*d)) }
func (d *dnsNames) Set(value string) error { *d = append(*d, value); return nil }

type gatewayAccounts []GatewayAccount

func (a *gatewayAccounts) String() string { return fmt.Sprint([]GatewayAccount(*a)) }
func (a *gatewayAccounts) Set(value string) error {
	var account GatewayAccount
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&account); err != nil {
		return fmt.Errorf("decode gateway account: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("gateway account must contain exactly one JSON object")
	}
	*a = append(*a, account)
	return nil
}

// Run starts the node-local exporter or checks the last published generation.
// The exporter has no API credentials; kubelet projects precisely its Secrets.
func Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("credential-sync", flag.ContinueOnError)
	source := flags.String("source", "/source", "Kubelet Secret projection directory")
	destination := flags.String("destination", "/persist", "Persistent generation directory")
	trust := flags.String("trust", "/trust/ca.crt", "Independent public trust bundle")
	uri := flags.String("uri", "", "Exact expected URI SAN")
	usage := flags.String("usage", "client", "Expected client or server EKU")
	check := flags.Bool("check", false, "Validate the installed generation and exit")
	var names dnsNames
	flags.Var(&names, "dns", "Exact expected DNS SAN, repeatable")
	var accounts gatewayAccounts
	gatewayPolicy := flags.Bool("gateway-policy", false, "Deliver gateway identity and permission files, including an empty policy")
	sshTrust := flags.String("ssh-trust", "", "OpenSSH CA public key bundle for gateway authentication")
	flags.Var(&accounts, "gateway-account", "Public gateway account JSON with username and permissions, repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *uri == "" || flags.NArg() != 0 {
		return fmt.Errorf("an expected URI and no positional arguments are required")
	}
	identity := Identity{URI: *uri, Usage: *usage, DNSNames: names}
	if (*gatewayPolicy || len(accounts) > 0 || *sshTrust != "") && *usage != "server" {
		return fmt.Errorf("gateway accounts can only accompany server credentials")
	}
	if *gatewayPolicy && accounts == nil {
		accounts = make(gatewayAccounts, 0)
	}
	if _, err := accountFiles(accounts); err != nil {
		return err
	}
	var policy *GatewayPolicy
	if *gatewayPolicy || accounts != nil || *sshTrust != "" {
		policy = &GatewayPolicy{Accounts: accounts, SSHTrustFile: *sshTrust}
	}
	if *check {
		return Check(*destination, *trust, identity, time.Now(), policy)
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastGeneration := ""
	for {
		generation, err := Publish(*source, *destination, *trust, identity, time.Now(), policy)
		if err != nil {
			slog.Error("credential delivery failed; keeping the previous generation", "error", err)
		} else if generation != lastGeneration {
			slog.Info("credential generation delivered", "generation", generation)
			lastGeneration = generation
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
