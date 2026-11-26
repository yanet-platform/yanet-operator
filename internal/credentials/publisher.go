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
	if err := scanCertificatePEM(certPEM, true, nil); err != nil {
		return fmt.Errorf("certificate chain: %w", err)
	}
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
	roots, trustErr := trustRoots(trustPEM)
	if trustErr != nil {
		return trustErr
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
// immutable files and atomically switches current. An error after the switch
// means durability is unconfirmed, not that the switch was undone. Consumers
// must resolve current once per pair load;
// separate path lookups across a switch can otherwise observe different pairs.
// Paths are trusted administrative inputs from the node-bound delivery Pod;
// certificate contents cannot select paths outside its configured mounts.
func Publish(source, destination, trustFile string, identity Identity, now time.Time) (string, error) {
	return publish(source, destination, trustFile, identity, now, nil)
}

func publish(source, destination, trustFile string, identity Identity, now time.Time, durable *durableCurrent) (string, error) {
	// Pin the kubelet generation before reading either file. Reading tls.crt and
	// tls.key through separate projection symlinks races Secret renewal.
	generation, projectionErr := filepath.EvalSymlinks(filepath.Join(source, "..data"))
	if projectionErr != nil {
		return "", fmt.Errorf("resolve Secret generation: %w", projectionErr)
	}
	cert, certReadErr := os.ReadFile(filepath.Join(generation, "tls.crt"))
	if certReadErr != nil {
		return "", certReadErr
	}
	key, keyReadErr := os.ReadFile(filepath.Join(generation, "tls.key"))
	if keyReadErr != nil {
		return "", keyReadErr
	}
	trust, trustReadErr := os.ReadFile(trustFile) // #nosec G304 -- The independent trust path is selected by the trusted delivery configuration.
	if trustReadErr != nil {
		return "", trustReadErr
	}
	if err := Validate(cert, key, trust, identity, now); err != nil {
		return "", err
	}
	content := append(bytes.Clone(cert), trust...)
	digest := sha256.Sum256(content)
	name := hex.EncodeToString(digest[:])
	files := map[string][]byte{"tls.crt": cert, "tls.key": key, "ca.crt": trust}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(destination, 0o700); err != nil { // #nosec G302 -- This is an owner-only directory, which requires search permission.
		return "", err
	}
	// Recreate is the normal single-writer boundary. The lock also serializes
	// overlapping exporters and readiness probes.
	destinationRoot, rootErr := os.OpenRoot(destination)
	if rootErr != nil {
		return "", rootErr
	}
	defer func() { _ = destinationRoot.Close() }()
	lock, lockErr := lockPublication(destinationRoot, os.O_CREATE|os.O_RDWR)
	if lockErr != nil {
		return "", lockErr
	}
	// Closing the descriptor releases flock on every return path.
	defer func() { _ = lock.Close() }()
	previous, linkErr := destinationRoot.Readlink("current")
	if linkErr != nil && !os.IsNotExist(linkErr) {
		return "", linkErr
	}
	_, statErr := destinationRoot.Lstat(name)
	if os.IsNotExist(statErr) {
		if err := stageGeneration(destinationRoot, name, previous, files, durable); err != nil {
			return "", err
		}
	} else if statErr != nil {
		return "", statErr
	} else {
		if err := verifyGeneration(destinationRoot, name, files); err != nil {
			return "", err
		}
	}
	if err := switchCurrent(destinationRoot, previous, name, durable); err != nil {
		return "", err
	}
	return name, nil
}

// Check validates a single installed generation without the API or Secret cache.
// As in Publish, destination and trustFile are trusted administrative paths.
func Check(destination, trustFile string, identity Identity, now time.Time) error {
	return checkInstalled(destination, trustFile, identity, now)
}

// The writer syncs files and the generation directory before switching current.
// A readiness probe can complete a switch's durability if the writer's final
// sync failed. Hold the same per-role lock from before resolving current until
// after syncing its parent, so a concurrent writer cannot change the target.
func checkWriterReadiness(destination, trustFile string, identity Identity, now time.Time) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	lock, err := lockPublication(root, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if checkErr := checkInstalled(destination, trustFile, identity, now); checkErr != nil {
		return checkErr
	}
	if syncErr := syncRoot(root); syncErr != nil {
		return fmt.Errorf("confirm credential delivery durability: %w", syncErr)
	}
	return nil
}

func checkInstalled(destination, trustFile string, identity Identity, now time.Time) error {
	generation, resolveErr := filepath.EvalSymlinks(filepath.Join(destination, "current"))
	if resolveErr != nil {
		return resolveErr
	}
	root, rootErr := os.OpenRoot(generation)
	if rootErr != nil {
		return rootErr
	}
	defer func() { _ = root.Close() }()
	pinnedTrust, trustPathErr := pinInstalledPath(destination, generation, trustFile, "ca.crt")
	if trustPathErr != nil {
		return trustPathErr
	}
	trustFile = pinnedTrust
	cert, certReadErr := root.ReadFile("tls.crt")
	if certReadErr != nil {
		return certReadErr
	}
	key, keyReadErr := root.ReadFile("tls.key")
	if keyReadErr != nil {
		return keyReadErr
	}
	trust, trustReadErr := root.ReadFile("ca.crt")
	if trustReadErr != nil {
		return trustReadErr
	}
	if validateErr := Validate(cert, key, trust, identity, now); validateErr != nil {
		return validateErr
	}
	requiredTrust, requiredTrustErr := os.ReadFile(trustFile) // #nosec G304 -- Independent trust or the pinned offline path is selected by trusted delivery configuration.
	if requiredTrustErr != nil {
		return requiredTrustErr
	}
	if !bytes.Equal(trust, requiredTrust) {
		return fmt.Errorf("installed X.509 trust does not match the requested bundle")
	}
	return nil
}

// pinInstalledPath recognizes the configured offline path lexically: resolving
// its symlinks would perform a second current lookup and could mix generations.
// External independent trust paths are left unchanged.
func pinInstalledPath(destination, generation, path, filename string) (string, error) {
	installed, err := filepath.Abs(filepath.Join(destination, "current", filename))
	if err != nil {
		return "", err
	}
	requested, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if requested == installed {
		return filepath.Join(generation, filename), nil
	}
	return path, nil
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
