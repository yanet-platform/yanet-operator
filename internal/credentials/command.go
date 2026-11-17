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
	check := flags.Bool("check", false, "Validate the installed generation and exit; read-only unless --writer-readiness is set")
	writerReadiness := flags.Bool("writer-readiness", false, "With --check, also confirm the installed generation is durable under the publication lock")
	exactRevision := flags.Bool("exact-policy-revision", false, "Require the writer's exact managed revision for readiness")
	deferReadiness := flags.Bool("defer-policy-readiness", false, "Keep a provisional writer unready until the original policy is verified")
	var names dnsNames
	flags.Var(&names, "dns", "Exact expected DNS SAN, repeatable")
	var accounts gatewayAccounts
	gatewayPolicy := flags.Bool("gateway-policy", false, "Deliver gateway identity and permission files, including an empty policy")
	epoch := flags.String("policy-epoch", "", "Installation UID for monotonic managed policy")
	revision := flags.Uint64("policy-revision", 0, "Minimum managed policy revision")
	sshTrust := flags.String("ssh-trust", "", "OpenSSH CA public key bundle for gateway authentication")
	sshHash := flags.String("ssh-trust-sha256", "", "Allocated SSH bundle SHA-256, or none for removed trust")
	bridge := flags.String("policy-bridge", "", "Writer-only bootstrap mode: initial or legacy")
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
	versioned := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "policy-epoch" || f.Name == "policy-revision" {
			versioned = true
		}
	})
	if versioned {
		if policy == nil {
			return fmt.Errorf("policy revision requires gateway policy")
		}
		policy.Revision = &PolicyRevision{Epoch: *epoch, Sequence: *revision}
		policy.SSHTrustHash = *sshHash
		if policy.SSHTrustHash == "" {
			return fmt.Errorf("managed policy requires --ssh-trust-sha256")
		}
		if err := validateRevision(policy.Revision); err != nil {
			return err
		}
	}
	if !versioned && *sshHash != "" {
		return fmt.Errorf("SSH trust commitment requires a versioned policy")
	}
	if *exactRevision && (!*check || !versioned) {
		return fmt.Errorf("exact policy revision requires a versioned readiness check")
	}
	if *deferReadiness {
		if !*exactRevision {
			return fmt.Errorf("deferred policy readiness requires an exact policy readiness check")
		}
		return fmt.Errorf("policy readiness deferred until legacy policy continuity is verified")
	}
	if *writerReadiness && !*check {
		return fmt.Errorf("writer readiness requires --check")
	}
	if *bridge != "" {
		if *check || !versioned || (*bridge != "initial" && *bridge != "legacy") {
			return fmt.Errorf("policy bridge requires a versioned writer and initial or legacy mode")
		}
		policy.bridge = true
		policy.requireCurrent = *bridge == "legacy"
	}
	if *check {
		if *writerReadiness {
			return checkWriterReadiness(*destination, *trust, identity, time.Now(), policy, *exactRevision)
		}
		return checkInstalled(*destination, *trust, identity, time.Now(), policy, *exactRevision)
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastGeneration := ""
	var durable durableCurrent
	defer durable.forget()
	for {
		generation, err := publish(*source, *destination, *trust, identity, time.Now(), policy, &durable)
		if err != nil {
			slog.Error("credential delivery failed", "error", err)
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
