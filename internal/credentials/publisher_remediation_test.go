package credentials_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
)

func TestPublishValidatesCompleteCertificateInputs(t *testing.T) {
	root := newAuthority(t)
	intermediate := newAuthorityAt(t, clock, &root)
	overlap := newAuthority(t)
	leaf, key := intermediate.leaf(t, 11, nil)
	chain := bytes.Join([][]byte{leaf, intermediate.pem}, nil)
	broken := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	other := pem.EncodeToMemory(&pem.Block{Type: "OTHER", Bytes: []byte("not a certificate")})
	truncated := []byte("-----BEGIN CERTIFICATE-----\nYWJj\n")
	note := []byte("# root and intermediates\n")
	identity := credentials.Identity{URI: identityURI, Usage: "client"}
	for _, tc := range []struct {
		name string
		cert []byte
		ca   []byte
		want string
	}{
		{"root only with separate chain", chain, root.pem, ""},
		{"overlap, comments and trusted intermediate", bytes.Join([][]byte{note, leaf, note, intermediate.pem, note}, nil), bytes.Join([][]byte{note, root.pem, intermediate.pem, overlap.pem, note}, nil), ""},
		{"unrelated PEM is ignored by both loaders", bytes.Join([][]byte{chain, other, note}, nil), bytes.Join([][]byte{other, root.pem, note}, nil), ""},
		{"missing intermediate", leaf, root.pem, "verify leaf"},
		{"non-CA in trust bundle", chain, bytes.Join([][]byte{root.pem, leaf}, nil), "not a CA"},
		{"invalid trust DER", chain, bytes.Join([][]byte{root.pem, broken}, nil), "parse trust certificate"},
		{"truncated trust tail", chain, bytes.Join([][]byte{root.pem, truncated}, nil), "malformed PEM"},
		{"invalid chain DER", bytes.Join([][]byte{chain, broken}, nil), root.pem, "parse intermediate"},
		{"truncated chain tail", bytes.Join([][]byte{chain, truncated}, nil), root.pem, "malformed PEM"},
		{"malformed chain before valid intermediate", bytes.Join([][]byte{leaf, truncated, intermediate.pem}, nil), root.pem, "malformed PEM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertResult := func(err error) {
				t.Helper()
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("wanted %q, got %v", tc.want, err)
				}
			}
			assertResult(credentials.Validate(tc.cert, key, tc.ca, identity, clock))
			source, destination := t.TempDir(), t.TempDir()
			trust := filepath.Join(t.TempDir(), "ca.crt")
			write(t, trust, root.pem)
			project(t, source, "first", chain, key)
			first, err := credentials.Publish(source, destination, trust, identity, clock, nil)
			if err != nil {
				t.Fatal(err)
			}
			write(t, trust, tc.ca)
			project(t, source, "update", tc.cert, key)
			_, err = credentials.Publish(source, destination, trust, identity, clock, nil)
			assertResult(err)
			if tc.want != "" {
				if target, err := os.Readlink(filepath.Join(destination, "current")); err != nil || target != first {
					t.Fatalf("invalid inputs changed current: %q, %v", target, err)
				}
			}
		})
	}
}

func TestPublishPolicyABAReusesImmutablePEM(t *testing.T) {
	f := newPublicationFixture(t)
	data := map[string][]byte{"tls.crt": f.cert, "tls.key": f.key, "ca.crt": f.authority.pem}
	policy := recoveryPolicy(1, true)
	first, err := f.publish(policy)
	if err != nil {
		t.Fatal(err)
	}
	previous := first
	for _, next := range []*credentials.GatewayPolicy{recoveryPolicy(2, false), recoveryPolicy(3, true)} {
		got, err := f.publish(next)
		if err != nil || got == previous || got == first {
			t.Fatalf("expected new immutable revision: %q, %v", got, err)
		}
		for name, want := range data {
			before := stat(t, filepath.Join(f.destination, first, name))
			path := filepath.Join(f.destination, got, name)
			after := stat(t, path)
			if after.Mode() != 0o600 || !os.SameFile(before, after) {
				t.Fatalf("%s is not the same regular 0600 PEM", name)
			}
			if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, want) {
				t.Fatalf("unexpected published bytes for %s: %v", name, err)
			}
		}
		previous = got
	}
}

func TestPublishIdenticalDoesNotReplaceCurrent(t *testing.T) {
	f := newPublicationFixture(t)
	policy := recoveryPolicy(1, false)
	first, err := f.publish(policy)
	if err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(f.destination, "current")
	before := stat(t, current)
	// Keep the original inode alive even if a buggy writer replaces current.
	if err := os.Link(current, filepath.Join(f.destination, "pinned-link")); err != nil {
		t.Fatal(err)
	}
	if got, err := f.publish(policy); err != nil || got != first {
		t.Fatalf("no-op publication: %q, %v", got, err)
	}
	if !os.SameFile(before, stat(t, current)) {
		t.Fatal("no-op publication replaced current")
	}
}

func TestPublishRetainsValidAlternateKeyEncoding(t *testing.T) {
	f := newPublicationFixture(t)
	policy := recoveryPolicy(1, false)
	previous, err := f.publish(policy)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(f.cert, f.key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("expected an ECDSA test key")
	}
	der, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	project(t, f.source, "alternate", f.cert, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	if got, err := f.publish(policy); err != nil || got != previous {
		t.Fatalf("equivalent key re-encoding replaced the generation or blocked delivery: %q, %v", got, err)
	}
	if body, err := os.ReadFile(filepath.Join(f.destination, previous, "tls.key")); err != nil || !bytes.Equal(body, f.key) {
		t.Fatalf("valid immutable key changed: %v", err)
	}
}

func TestPublishRecoversPrivateKeyLossWithReissuanceAtSameFloor(t *testing.T) {
	for _, damage := range []string{"missing", "damaged"} {
		t.Run(damage, func(t *testing.T) {
			f := newPublicationFixture(t)
			old, floor := recoveryPolicy(1, true), recoveryPolicy(2, false)
			if _, err := f.publish(old); err != nil {
				t.Fatal(err)
			}
			current, publishErr := f.publish(floor)
			if publishErr != nil {
				t.Fatal(publishErr)
			}
			keyPath := filepath.Join(f.destination, current, "tls.key")
			// Model loss of this directory entry, not an in-place edit of a
			// potentially shared PEM inode.
			if err := os.Remove(keyPath); err != nil {
				t.Fatal(err)
			}
			if damage == "damaged" {
				write(t, keyPath, []byte("damaged key"))
			}
			if _, err := f.publish(floor); err == nil || !strings.Contains(err.Error(), "reissue") {
				t.Fatalf("same-cert delivery must recommend reissuance; got %v", err)
			}
			cert, key := f.authority.leaf(t, 51, serverEKU)
			project(t, f.source, "reissued", cert, key)
			if _, err := f.publish(old); err == nil || !strings.Contains(err.Error(), "stale policy writer") {
				t.Fatalf("repair accepted a stale writer: %v", err)
			}
			if got, err := f.publish(floor); err != nil || got == current {
				t.Fatalf("reissuance failed to recover at the same floor: %q, %v", got, err)
			}
			if err := credentials.Check(f.destination, f.trust, f.identity, clock, floor); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(keyPath)
			if damage == "missing" && !os.IsNotExist(err) || damage == "damaged" && (err != nil || string(data) != "damaged key") {
				t.Fatalf("changed immutable reader path: %v", err)
			}
		})
	}
}

type publicationFixture struct {
	authority                  authority
	source, destination, trust string
	identity                   credentials.Identity
	cert, key                  []byte
}

func newPublicationFixture(t *testing.T) publicationFixture {
	t.Helper()
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	write(t, trust, a.pem)
	cert, key := a.leaf(t, 50, serverEKU)
	project(t, source, "issued", cert, key)
	return publicationFixture{a, source, destination, trust, credentials.Identity{URI: identityURI, Usage: "server"}, cert, key}
}

func (f publicationFixture) publish(policy *credentials.GatewayPolicy) (string, error) {
	return credentials.Publish(f.source, f.destination, f.trust, f.identity, clock, policy)
}

func serverEKU(cert *x509.Certificate) {
	cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
}

func recoveryPolicy(sequence uint64, grant bool) *credentials.GatewayPolicy {
	policy := &credentials.GatewayPolicy{Revision: &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: sequence}, SSHTrustHash: "none"}
	if grant {
		policy.Accounts = []credentials.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
	}
	return policy
}

func stat(t *testing.T, name string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
