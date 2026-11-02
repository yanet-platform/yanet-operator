// Package credentials validates Secret projections and publishes immutable PEM
// generations on persistent local storage. It has no Kubernetes API client.
package credentials

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Identity is the exact issuer request a delivered leaf must satisfy.
type Identity struct {
	URI      string
	DNSNames []string
	Usage    string
}

// Validate checks the pair, the entire chain, validity, SANs and role EKU against
// independently supplied trust. It never trusts ca.crt from the leaf Secret.
func Validate(certPEM, keyPEM, trustPEM []byte, identity Identity, now time.Time) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("invalid certificate/key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse leaf: %w", err)
	}
	if leaf.IsCA || leaf.Subject.CommonName != "" || len(leaf.URIs) != 1 || leaf.URIs[0].String() != identity.URI || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 {
		return fmt.Errorf("leaf identity does not match requested node/role")
	}
	dns := slices.Clone(leaf.DNSNames)
	wantedDNS := slices.Clone(identity.DNSNames)
	slices.Sort(dns)
	slices.Sort(wantedDNS)
	if !slices.Equal(dns, wantedDNS) {
		return fmt.Errorf("leaf DNS SANs do not match the request")
	}
	usage := x509.ExtKeyUsageClientAuth
	if identity.Usage == "server" {
		usage = x509.ExtKeyUsageServerAuth
	} else if identity.Usage != "client" {
		return fmt.Errorf("invalid expected usage %q", identity.Usage)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != usage || len(leaf.UnknownExtKeyUsage) != 0 {
		return fmt.Errorf("leaf must have exactly the requested role EKU")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trustPEM) {
		return fmt.Errorf("trust bundle contains no PEM certificates")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return fmt.Errorf("parse intermediate: %w", parseErr)
		}
		intermediates.AddCert(cert)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}})
	if err != nil {
		return fmt.Errorf("verify leaf: %w", err)
	}
	return nil
}

// Publish reads one kubelet Secret projection generation, validates it, fsyncs
// immutable files and atomically switches current. A failed delivery leaves the
// last generation intact. Consumers must resolve current once per pair load;
// separate path lookups across a switch can otherwise observe different pairs.
// Paths are trusted administrative inputs from the node-bound delivery Pod;
// certificate contents cannot select paths outside its configured mounts.
func Publish(source, destination, trustFile string, identity Identity, now time.Time, accounts ...GatewayAccount) (string, error) {
	files, err := accountFiles(accounts)
	if err != nil {
		return "", err
	}
	if files == nil {
		files = make(map[string][]byte)
	}
	// Pin the kubelet generation before reading either file. Reading tls.crt and
	// tls.key through separate projection symlinks races Secret renewal.
	generation, err := filepath.EvalSymlinks(filepath.Join(source, "..data"))
	if err != nil {
		return "", fmt.Errorf("resolve Secret generation: %w", err)
	}
	cert, err := os.ReadFile(filepath.Join(generation, "tls.crt"))
	if err != nil {
		return "", err
	}
	key, err := os.ReadFile(filepath.Join(generation, "tls.key"))
	if err != nil {
		return "", err
	}
	trust, err := os.ReadFile(trustFile) // #nosec G304 -- The independent trust path is selected by the trusted delivery configuration.
	if err != nil {
		return "", err
	}
	if err := Validate(cert, key, trust, identity, now); err != nil {
		return "", err
	}
	content := append(bytes.Clone(cert), trust...)
	for _, filename := range []string{"identities.yaml", "permissions.yaml"} {
		content = append(content, files[filename]...)
	}
	digest := sha256.Sum256(content)
	name := hex.EncodeToString(digest[:])
	files["tls.crt"], files["tls.key"], files["ca.crt"] = cert, key, trust
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(destination, 0o700); err != nil { // #nosec G302 -- This is an owner-only directory, which requires search permission.
		return "", err
	}
	final := filepath.Join(destination, name)
	_, statErr := os.Stat(final) // #nosec G703 -- The only content-derived path component is a SHA-256 hex digest.
	if os.IsNotExist(statErr) {
		temp, err := os.MkdirTemp(destination, ".generation-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(temp) }()
		for filename, data := range files {
			if err := writeSynced(filepath.Join(temp, filename), data); err != nil {
				return "", err
			}
		}
		if err := syncDirectory(temp); err != nil {
			return "", err
		}
		if err := os.Rename(temp, final); err != nil { // #nosec G703 -- Both paths are under the trusted destination; name is a SHA-256 hex digest.
			return "", err
		}
	} else if statErr != nil {
		return "", statErr
	} else {
		for filename, expected := range files {
			stored, err := os.ReadFile(filepath.Join(final, filename)) // #nosec G304 G703 -- SHA-256 hex directory and fixed PEM basenames under the trusted destination.
			if err != nil {
				return "", err
			}
			if !bytes.Equal(stored, expected) {
				return "", fmt.Errorf("stored generation is damaged: %s", filename)
			}
		}
	}
	// A crashed writer can leave its temporary symlink, but not an incomplete
	// published generation. One node-bound Deployment is the sole writer here.
	link := filepath.Join(destination, ".current-next")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(name, link); err != nil {
		return "", err
	}
	if err := os.Rename(link, filepath.Join(destination, "current")); err != nil {
		return "", err
	}
	if err := syncDirectory(destination); err != nil {
		return "", err
	}
	return name, nil
}

// Check validates a single installed generation without the API or Secret cache.
// As in Publish, destination and trustFile are trusted administrative paths.
func Check(destination, trustFile string, identity Identity, now time.Time, accounts ...GatewayAccount) error {
	files, err := accountFiles(accounts)
	if err != nil {
		return err
	}
	generation, err := filepath.EvalSymlinks(filepath.Join(destination, "current"))
	if err != nil {
		return err
	}
	cert, err := os.ReadFile(filepath.Join(generation, "tls.crt"))
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(generation, "tls.key"))
	if err != nil {
		return err
	}
	trust, err := os.ReadFile(trustFile) // #nosec G304 -- The independent trust path is selected by the trusted delivery configuration.
	if err != nil {
		return err
	}
	if err := Validate(cert, key, trust, identity, now); err != nil {
		return err
	}
	for filename, expected := range files {
		stored, err := os.ReadFile(filepath.Join(generation, filename)) // #nosec G304 -- Fixed account basenames under the trusted, pinned generation.
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, expected) {
			return fmt.Errorf("installed gateway accounts do not match the requested policy: %s", filename)
		}
	}
	return nil
}

func writeSynced(name string, data []byte) (err error) {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- Fixed PEM basename under a private MkdirTemp directory; O_EXCL prevents replacement.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func syncDirectory(name string) (err error) {
	f, err := os.Open(name) // #nosec G304 -- Only the trusted destination or its private MkdirTemp directory is synced.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	return f.Sync()
}
