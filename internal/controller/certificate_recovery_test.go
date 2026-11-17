package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	appsv1 "k8s.io/api/apps/v1"
)

// Model the node-local writer and a restarted gateway using the templates the
// controller actually retained. The source remains a valid old-issuer leaf,
// and the independent trust bundle already trusts both issuers.
type gatewayFileFixture struct {
	source, destination, trust string
	identity                   credentials.Identity
	now                        time.Time
}

func newGatewayFileFixture(t *testing.T, writer *appsv1.Deployment) gatewayFileFixture {
	t.Helper()
	now := time.Now()
	args := writer.Spec.Template.Spec.Containers[0].Args
	identity := credentials.Identity{Usage: "server"}
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--uri="); ok {
			identity.URI = value
		}
		if value, ok := strings.CutPrefix(arg, "--dns="); ok {
			identity.DNSNames = append(identity.DNSNames, value)
		}
	}
	uri, err := url.Parse(identity.URI)
	if err != nil {
		t.Fatal(err)
	}
	makeCA := func(serial int64) ([]byte, *x509.Certificate, ed25519.PrivateKey) {
		pub, key, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
		der, certErr := x509.CreateCertificate(rand.Reader, template, template, pub, key)
		if certErr != nil {
			t.Fatal(certErr)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), template, key
	}
	oldPEM, oldCA, oldKey := makeCA(1)
	newPEM, _, _ := makeCA(2)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), URIs: []*url.URL{uri}, DNSNames: identity.DNSNames, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, oldCA, pub, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	source, destination, trust := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "ca.crt")
	if err := os.Mkdir(filepath.Join(source, "issued"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("issued", filepath.Join(source, "..data")); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		filepath.Join(source, "issued", "tls.crt"): pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		filepath.Join(source, "issued", "tls.key"): pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		trust: append(oldPEM, newPEM...),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return gatewayFileFixture{source: source, destination: destination, trust: trust, identity: identity, now: now}
}

func gatewayRecoveryWithOldLeaf(t *testing.T, writer *appsv1.Deployment) func(*appsv1.Deployment, *appsv1.Deployment) {
	t.Helper()
	fixture := newGatewayFileFixture(t, writer)
	source, destination, trust, identity, now := fixture.source, fixture.destination, fixture.trust, fixture.identity, fixture.now
	return func(writer, consumer *appsv1.Deployment) {
		t.Helper()
		policy := &credentials.GatewayPolicy{}
		for _, arg := range writer.Spec.Template.Spec.Containers[0].Args {
			if value, ok := strings.CutPrefix(arg, "--ssh-trust-sha256="); ok {
				policy.SSHTrustHash = value
			}
			if value, ok := strings.CutPrefix(arg, "--policy-epoch="); ok {
				if policy.Revision == nil {
					policy.Revision = &credentials.PolicyRevision{}
				}
				policy.Revision.Epoch = value
			}
			if value, ok := strings.CutPrefix(arg, "--policy-revision="); ok {
				if policy.Revision == nil {
					policy.Revision = &credentials.PolicyRevision{}
				}
				sequence, err := strconv.ParseUint(value, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				policy.Revision.Sequence = sequence
			}
			if value, ok := strings.CutPrefix(arg, "--gateway-account="); ok {
				var account credentials.GatewayAccount
				if err := json.Unmarshal([]byte(value), &account); err != nil {
					t.Fatal(err)
				}
				policy.Accounts = append(policy.Accounts, account)
			}
		}
		if _, err := credentials.Publish(source, destination, trust, identity, now, policy); err != nil {
			t.Fatal(err)
		}
		probe := writer.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec.Command
		if policy.Revision != nil && !slices.Contains(probe, "--exact-policy-revision") {
			t.Fatal("managed writer readiness omitted exact revision fence")
		}
		probeArgs := append(slices.Clone(probe[2:]), "--destination="+destination, "--trust="+trust)
		if err := credentials.Run(context.Background(), probeArgs); err != nil {
			t.Fatalf("published writer failed its actual readiness command: %v", err)
		}
		var check []string
		for _, arg := range consumer.Spec.Template.Spec.InitContainers[0].Args[1:] {
			if strings.HasPrefix(arg, "--destination=") {
				arg = "--destination=" + destination
			}
			if strings.HasPrefix(arg, "--trust=") {
				arg = "--trust=" + filepath.Join(destination, "current", "ca.crt")
			}
			check = append(check, arg)
		}
		if err := credentials.Run(context.Background(), check); err != nil {
			t.Fatalf("retained gateway recovery rejected writer's valid old leaf/policy: %v", err)
		}
		for _, arg := range check {
			if value, ok := strings.CutPrefix(arg, "--policy-revision="); ok && policy.Revision != nil {
				floor, err := strconv.ParseUint(value, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				if floor < policy.Revision.Sequence {
					err := credentials.Run(context.Background(), append(slices.Clone(check), "--exact-policy-revision"))
					if err == nil || !strings.Contains(err.Error(), "differs from desired writer revision") {
						t.Fatalf("writer readiness accepted another revision as its own: %v", err)
					}
				}
			}
		}
	}
}
