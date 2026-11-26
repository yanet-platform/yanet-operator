package credentials_test

import (
	"bytes"
	"crypto/ecdsa"
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
	return newAuthorityAt(t, clock, nil)
}

func newAuthorityAt(t *testing.T, now time.Time, issuer *authority) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	if issuer == nil {
		issuer = &authority{certificate: certificate, key: key}
	} else {
		certificate.SerialNumber = big.NewInt(2)
		certificate.Subject.CommonName = "Test intermediate CA"
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, issuer.certificate, &key.PublicKey, issuer.key)
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
	first, err := credentials.Publish(source, destination, trust, identity, clock)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := credentials.Publish(source, destination, trust, identity, clock)
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
	second, err := credentials.Publish(source, destination, trust, identity, clock)
	if err != nil || second == first {
		t.Fatalf("renewal did not switch generation: %q %v", second, err)
	}
	// Loss of the Secret projection/API cannot affect the persisted generation.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock); err != nil {
		t.Fatalf("offline validation: %v", err)
	}
	if err := credentials.Check(destination, trust, identity, clock.Add(25*time.Hour)); err == nil {
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
			good, err := credentials.Publish(source, destination, trust, identity, clock)
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
			if _, publishErr := credentials.Publish(source, destination, trust, identity, clock); publishErr == nil {
				t.Fatal("invalid replacement accepted")
			}
			current, err := os.Readlink(filepath.Join(destination, "current"))
			if err != nil || current != good {
				t.Fatalf("last good generation changed: %s %v", current, err)
			}
			if err := credentials.Check(destination, filepath.Join(destination, "current", "ca.crt"), identity, clock); err != nil {
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

func TestCheckRequiresDeliveredTrustOverlap(t *testing.T) {
	a, b := newAuthority(t), newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	persistedTrust := filepath.Join(destination, "current", "ca.crt")
	identity := credentials.Identity{URI: identityURI, Usage: "client"}
	cert, key := a.leaf(t, 21, nil)
	write(t, trust, a.pem)
	project(t, source, "first", cert, key)
	first, err := credentials.Publish(source, destination, trust, identity, clock)
	if err != nil {
		t.Fatal(err)
	}
	overlap := append(bytes.Clone(a.pem), b.pem...)
	write(t, trust, overlap)
	if checkErr := credentials.Check(destination, trust, identity, clock); checkErr == nil {
		t.Error("readiness accepted X.509 trust overlap that was not delivered")
	}
	if checkErr := credentials.Check(destination, persistedTrust, identity, clock); checkErr != nil {
		t.Fatalf("last-good offline trust must remain usable: %v", checkErr)
	}
	// A failed delivery can keep the overlap pending indefinitely.
	project(t, source, "invalid", cert, []byte("invalid private key"))
	if _, publishErr := credentials.Publish(source, destination, trust, identity, clock); publishErr == nil {
		t.Fatal("invalid replacement accepted")
	}
	current, err := os.Readlink(filepath.Join(destination, "current"))
	if err != nil || current != first {
		t.Fatalf("failed delivery changed current: %s %v", current, err)
	}
	if checkErr := credentials.Check(destination, trust, identity, clock); checkErr == nil {
		t.Error("failed delivery became ready without installing overlap trust")
	}
	project(t, source, "valid", cert, key)
	second, err := credentials.Publish(source, destination, trust, identity, clock)
	if err != nil || second == first {
		t.Fatalf("trust-only update did not replace generation: %s %v", second, err)
	}
	stored, err := os.ReadFile(persistedTrust)
	if err != nil || !bytes.Equal(stored, overlap) {
		t.Fatalf("overlap trust was not delivered exactly: %q %v", stored, err)
	}
	if err := credentials.Check(destination, trust, identity, clock); err != nil {
		t.Fatalf("delivered overlap is not ready: %v", err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(trust); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, persistedTrust, identity, clock); err != nil {
		t.Fatalf("offline overlap requires neither source nor external trust: %v", err)
	}
}

func TestCheckRejectsDamagedInstalledTrust(t *testing.T) {
	for _, damage := range []string{"corrupt", "missing", "unrelated issuer"} {
		t.Run(damage, func(t *testing.T) {
			a := newAuthority(t)
			source, destination := t.TempDir(), t.TempDir()
			trust := filepath.Join(t.TempDir(), "ca.crt")
			identity := credentials.Identity{URI: identityURI, Usage: "client"}
			cert, key := a.leaf(t, 22, nil)
			write(t, trust, a.pem)
			project(t, source, "first", cert, key)
			if _, err := credentials.Publish(source, destination, trust, identity, clock); err != nil {
				t.Fatal(err)
			}
			persistedTrust := filepath.Join(destination, "current", "ca.crt")
			switch damage {
			case "corrupt":
				write(t, persistedTrust, []byte("invalid certificate"))
			case "missing":
				if err := os.Remove(persistedTrust); err != nil {
					t.Fatal(err)
				}
			case "unrelated issuer":
				write(t, persistedTrust, newAuthority(t).pem)
			}
			for _, checkTrust := range []string{trust, persistedTrust} {
				if err := credentials.Check(destination, checkTrust, identity, clock); err == nil {
					t.Errorf("%s installed trust passed Check with %s", damage, checkTrust)
				}
			}
		})
	}
}
