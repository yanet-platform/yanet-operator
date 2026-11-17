package credentials_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

var clock = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

const identityURI = "spiffe://example.test/ns/default/installation/install-uid/node/node-uid/role/acl"

type authority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newAuthority(t *testing.T) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: clock.Add(-time.Hour), NotAfter: clock.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return authority{certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (a authority) leaf(t *testing.T, serial int64, mutate func(*x509.Certificate)) (certificatePEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(identityURI)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: clock.Add(-time.Minute), NotAfter: clock.Add(24 * time.Hour), URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if mutate != nil {
		mutate(certificate)
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func write(t *testing.T, name string, value []byte) {
	t.Helper()
	if err := os.WriteFile(name, value, 0o600); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T, root, name string, certificate, key []byte) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(directory, "tls.crt"), certificate)
	write(t, filepath.Join(directory, "tls.key"), key)
	link := filepath.Join(root, "..data")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(name, link); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRenewalAndOfflineValidation(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	write(t, trust, a.pem)
	identity := credentials.Identity{URI: identityURI, Usage: "client"}
	cert, key := a.leaf(t, 2, nil)
	project(t, source, "first", cert, key)
	first, err := credentials.Publish(source, destination, trust, identity, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := credentials.Publish(source, destination, trust, identity, clock, nil)
	if err != nil || first != repeated {
		t.Fatalf("unchanged material must be idempotent: %q %q %v", first, repeated, err)
	}
	for _, file := range []string{"tls.crt", "tls.key", "ca.crt"} {
		info, statErr := os.Stat(filepath.Join(destination, "current", file))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private generation permissions for %s: %v %v", file, info, statErr)
		}
	}
	cert2, key2 := a.leaf(t, 3, nil)
	project(t, source, "second", cert2, key2)
	second, err := credentials.Publish(source, destination, trust, identity, clock, nil)
	if err != nil || second == first {
		t.Fatalf("renewal did not switch generation: %q %v", second, err)
	}
	// Loss of the Secret projection/API cannot affect the persisted generation.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock, nil); err != nil {
		t.Fatalf("offline validation: %v", err)
	}
	if err := credentials.Check(destination, trust, identity, clock.Add(25*time.Hour), nil); err == nil {
		t.Fatal("expired offline credentials were accepted")
	}
}

func TestFailedDeliveryPreservesLastGoodGeneration(t *testing.T) {
	a, unrelated := newAuthority(t), newAuthority(t)
	for _, test := range []struct {
		name             string
		mutate           func(*x509.Certificate)
		badKey, badTrust bool
	}{
		{name: "different key", badKey: true},
		{name: "untrusted issuer", badTrust: true},
		{name: "wrong node", mutate: func(c *x509.Certificate) { c.URIs[0].Path = "/another-node" }},
		{name: "CN overrides URI", mutate: func(c *x509.Certificate) { c.Subject.CommonName = "admin" }},
		{name: "wrong EKU", mutate: func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} }},
		{name: "extra DNS", mutate: func(c *x509.Certificate) { c.DNSNames = []string{"unexpected.example.test"} }},
		{name: "expired", mutate: func(c *x509.Certificate) { c.NotAfter = clock.Add(-time.Second) }},
		{name: "not yet valid", mutate: func(c *x509.Certificate) { c.NotBefore = clock.Add(time.Minute) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			trust := filepath.Join(t.TempDir(), "ca.crt")
			write(t, trust, a.pem)
			identity := credentials.Identity{URI: identityURI, Usage: "client"}
			cert, key := a.leaf(t, 10, nil)
			project(t, source, "good", cert, key)
			good, err := credentials.Publish(source, destination, trust, identity, clock, nil)
			if err != nil {
				t.Fatal(err)
			}
			cert, key = a.leaf(t, 11, test.mutate)
			if test.badKey {
				_, key = a.leaf(t, 12, nil)
			}
			if test.badTrust {
				write(t, trust, unrelated.pem)
			}
			project(t, source, "bad", cert, key)
			if _, publishErr := credentials.Publish(source, destination, trust, identity, clock, nil); publishErr == nil {
				t.Fatal("invalid replacement accepted")
			}
			current, err := os.Readlink(filepath.Join(destination, "current"))
			if err != nil || current != good {
				t.Fatalf("last good generation changed: %s %v", current, err)
			}
			if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock, nil); err != nil {
				t.Fatalf("last good generation damaged: %v", err)
			}
		})
	}
}

func TestServerSANAndTrustOverlap(t *testing.T) {
	old, next := newAuthority(t), newAuthority(t)
	cert, key := next.leaf(t, 20, func(c *x509.Certificate) {
		c.DNSNames = []string{"gateway.example.test"}
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	identity := credentials.Identity{URI: identityURI, Usage: "server", DNSNames: []string{"gateway.example.test"}}
	if err := credentials.Validate(cert, key, old.pem, identity, clock); err == nil {
		t.Fatal("new issuer accepted before trust distribution")
	}
	if err := credentials.Validate(cert, key, append(old.pem, next.pem...), identity, clock); err != nil {
		t.Fatalf("overlap trust should accept new issuer: %v", err)
	}
	identity.DNSNames = []string{"other.example.test"}
	if err := credentials.Validate(cert, key, next.pem, identity, clock); err == nil {
		t.Fatal("wrong server DNS accepted")
	}
}

// A policy-only update must create a new durable generation, and a gateway
// startup check must reject old, missing or damaged account files without API access.
func TestPublishGatewayAccountsAndOfflinePolicyGate(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	write(t, trust, a.pem)
	identity := credentials.Identity{URI: identityURI, Usage: "server", DNSNames: []string{"gateway.example.test"}}
	cert, key := a.leaf(t, 30, func(c *x509.Certificate) {
		c.DNSNames = identity.DNSNames
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	project(t, source, "first", cert, key)
	account := credentials.GatewayAccount{Username: identityURI, Permissions: []string{"/example.v1.Service/Read"}}
	policy := &credentials.GatewayPolicy{Accounts: []credentials.GatewayAccount{account}}
	first, publishErr := credentials.Publish(source, destination, trust, identity, clock, policy)
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	for _, name := range []string{"identities.yaml", "permissions.yaml"} {
		data, err := os.ReadFile(filepath.Join(destination, "current", name))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if name == "identities.yaml" {
			entries := document["identities"].([]any)
			if len(entries) != 1 || entries[0].(map[string]any)["username"] != identityURI {
				t.Fatalf("wrong account identities: %s", data)
			}
		} else {
			users := document["permissions"].(map[string]any)["users"].([]any)
			user := users[0].(map[string]any)
			if len(users) != 1 || user["username"] != identityURI || user["permissions"].([]any)[0] != "/example.v1.Service/Read" {
				t.Fatalf("wrong least-privilege policy: %s", data)
			}
		}
	}
	account.Permissions = []string{"/example.v1.Service/Watch"}
	policy.Accounts[0] = account
	if err := credentials.Check(destination, trust, identity, clock, policy); err == nil {
		t.Fatal("new gateway accepted the old account policy")
	}
	second, publishErr := credentials.Publish(source, destination, trust, identity, clock, policy)
	if publishErr != nil || second == first {
		t.Fatalf("policy-only update reused its generation: %s %v", second, publishErr)
	}
	emptyPolicy := &credentials.GatewayPolicy{}
	if err := credentials.Check(destination, trust, identity, clock, emptyPolicy); err == nil {
		t.Fatal("revoking the last account accepted the old nonempty policy")
	}
	third, publishErr := credentials.Publish(source, destination, trust, identity, clock, emptyPolicy)
	if publishErr != nil || third == second {
		t.Fatalf("last account revocation reused its generation: %s %v", third, publishErr)
	}
	for name, expected := range map[string]string{"identities.yaml": "identities: []\n", "permissions.yaml": "permissions:\n    users: []\n"} {
		data, err := os.ReadFile(filepath.Join(destination, "current", name))
		if err != nil || string(data) != expected {
			t.Fatalf("empty policy %s: %q %v", name, data, err)
		}
	}
	if err := credentials.Check(destination, trust, identity, clock, emptyPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, policy); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock, policy); err != nil {
		t.Fatalf("offline policy gate: %v", err)
	}
	write(t, filepath.Join(destination, "current", "permissions.yaml"), []byte("permissions: {}\n"))
	if err := credentials.Check(destination, trust, identity, clock, policy); err == nil {
		t.Fatal("damaged account policy passed the startup gate")
	}
	if err := os.Remove(filepath.Join(destination, "current", "identities.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, trust, identity, clock, policy); err == nil {
		t.Fatal("missing identities passed the startup gate")
	}
}

func TestGatewayAccountCommandRejectsInvalidPolicyBeforeDelivery(t *testing.T) {
	for _, account := range []string{
		`{"username":"not/a/local/identity","permissions":["/example.v1.Service/Read"]}`,
		`{"username":"","permissions":["/example.v1.Service/Read"]}`,
		`{"username":"` + identityURI + `","permissions":["*"]}`,
		`{"username":"` + identityURI + `","permission":["/example.v1.Service/Read"]}`,
		`{"username":"` + identityURI + `","permissions":[]} {}`,
	} {
		t.Run(account, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "uncreated")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := credentials.Run(ctx, []string{"--uri=" + identityURI, "--usage=server", "--destination=" + destination, "--gateway-account=" + account})
			if err == nil {
				t.Fatal("invalid policy entered the delivery loop")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("invalid policy touched persistent state: %v", err)
			}
		})
	}
}

func TestPublishHumanGatewayAccountsAndRevocation(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	write(t, trust, a.pem)
	identity := credentials.Identity{URI: identityURI, Usage: "server", DNSNames: []string{"gateway.example.test"}}
	cert, key := a.leaf(t, 40, func(c *x509.Certificate) {
		c.DNSNames = identity.DNSNames
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	project(t, source, "first", cert, key)
	accounts := []credentials.GatewayAccount{
		{Username: identityURI, Permissions: []string{"/example.v1.Service/Update"}},
		{Username: "root", Permissions: []string{"/*/*"}},
		{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}},
	}
	gatewayPolicy := &credentials.GatewayPolicy{Accounts: accounts}
	first, err := credentials.Publish(source, destination, trust, identity, clock, gatewayPolicy)
	if err != nil {
		t.Fatalf("deliver human accounts alongside the workload account: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "current", "permissions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Permissions struct {
			Users []credentials.GatewayAccount `yaml:"users"`
		} `yaml:"permissions"`
	}
	if err = yaml.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Permissions.Users) != 3 || policy.Permissions.Users[0].Username != identityURI ||
		policy.Permissions.Users[1].Username != "root" || policy.Permissions.Users[1].Permissions[0] != "/*/*" ||
		policy.Permissions.Users[2].Username != "alice" || policy.Permissions.Users[2].Permissions[0] != "/example.v1.Service/Read" {
		t.Fatalf("human and workload grants were not preserved: %s", data)
	}
	gatewayPolicy.Accounts = accounts[:2]
	if err = credentials.Check(destination, trust, identity, clock, gatewayPolicy); err == nil {
		t.Fatal("the startup gate accepted a policy that still authorizes the removed human")
	}
	second, err := credentials.Publish(source, destination, trust, identity, clock, gatewayPolicy)
	if err != nil || second == first {
		t.Fatalf("human revocation did not replace the generation: %s %v", second, err)
	}
	if err := credentials.Check(destination, trust, identity, clock, gatewayPolicy); err != nil {
		t.Fatalf("the updated human policy did not pass the startup gate: %v", err)
	}
}

func sshAuthority(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(public)
}

func TestPublishSSHTrustRotationAndOfflineRecovery(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust, sshTrust := filepath.Join(t.TempDir(), "ca.crt"), filepath.Join(t.TempDir(), "ssh-ca.pub")
	write(t, trust, a.pem)
	firstCA, nextCA := sshAuthority(t), sshAuthority(t)
	write(t, sshTrust, firstCA)
	identity := credentials.Identity{URI: identityURI, Usage: "server", DNSNames: []string{"gateway.example.test"}}
	cert, key := a.leaf(t, 50, func(c *x509.Certificate) {
		c.DNSNames = identity.DNSNames
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	project(t, source, "first", cert, key)
	policy := &credentials.GatewayPolicy{Accounts: []credentials.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}, SSHTrustFile: sshTrust}
	first, err := credentials.Publish(source, destination, trust, identity, clock, policy)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(destination, "current", "ssh-ca.pub"))
	if err != nil || !bytes.Equal(stored, firstCA) {
		t.Fatalf("SSH trust was not delivered exactly: %q %v", stored, err)
	}
	unsupported, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unsupportedKey, err := ssh.NewPublicKey(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{nil, a.pem, []byte("invalid-key\n"), ssh.MarshalAuthorizedKey(unsupportedKey), append([]byte("cert-authority "), firstCA...), append(append([]byte{}, nextCA...), []byte("invalid-key\n")...)} {
		write(t, sshTrust, invalid)
		if _, publishErr := credentials.Publish(source, destination, trust, identity, clock, policy); publishErr == nil {
			t.Fatalf("invalid SSH trust replaced the persisted generation: %q", invalid)
		}
		current, readErr := os.Readlink(filepath.Join(destination, "current"))
		if readErr != nil || current != first {
			t.Fatalf("invalid SSH trust damaged last-good credentials: %s %v", current, readErr)
		}
	}
	overlap := append(append([]byte{}, firstCA...), nextCA...)
	write(t, sshTrust, overlap)
	if err = credentials.Check(destination, trust, identity, clock, policy); err == nil {
		t.Fatal("readiness accepted SSH trust that was not yet delivered")
	}
	second, err := credentials.Publish(source, destination, trust, identity, clock, policy)
	if err != nil || second == first {
		t.Fatalf("SSH CA-only rotation reused its generation: %s %v", second, err)
	}
	stored, err = os.ReadFile(filepath.Join(destination, "current", "ssh-ca.pub"))
	if err != nil || !bytes.Equal(stored, overlap) {
		t.Fatalf("SSH CA overlap was not retained: %q %v", stored, err)
	}
	withoutSSH := &credentials.GatewayPolicy{Accounts: policy.Accounts}
	if err = credentials.Check(destination, trust, identity, clock, withoutSSH); err == nil {
		t.Fatal("the startup gate accepted removed SSH trust from an older generation")
	}
	third, err := credentials.Publish(source, destination, trust, identity, clock, withoutSSH)
	if err != nil || third == second {
		t.Fatalf("SSH trust removal reused its generation: %s %v", third, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "current", "ssh-ca.pub")); !os.IsNotExist(err) {
		t.Fatalf("removed SSH trust remains in the current generation: %v", err)
	}
	if err := credentials.Check(destination, trust, identity, clock, withoutSSH); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, policy); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sshTrust); err != nil {
		t.Fatal(err)
	}
	policy.SSHTrustFile = filepath.Join(destination, "current", "ssh-ca.pub")
	if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock, policy); err != nil {
		t.Fatalf("offline SSH trust and account validation: %v", err)
	}
	write(t, policy.SSHTrustFile, []byte("invalid-key\n"))
	if err := credentials.Check(destination, trust, identity, clock, policy); err == nil {
		t.Fatal("offline startup accepted damaged SSH trust")
	}
}
