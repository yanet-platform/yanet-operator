package credentials_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/sys/unix"
)

func TestMonotonicPolicyHandoff(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust := filepath.Join(t.TempDir(), "ca.crt")
	write(t, trust, a.pem)
	cert, key := a.leaf(t, 42, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} })
	project(t, source, "issued", cert, key)
	identity := credentials.Identity{URI: identityURI, Usage: "server"}
	aPolicy := &credentials.GatewayPolicy{Revision: &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 1}, SSHTrustHash: "none", Accounts: []credentials.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}}
	bPolicy := &credentials.GatewayPolicy{Revision: &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 2}, SSHTrustHash: "none"}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, aPolicy); err != nil {
		t.Fatal(err)
	}
	lock, openErr := os.OpenFile(filepath.Join(destination, ".publish-lock"), os.O_RDWR, 0o600)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, bPolicy); err == nil {
		t.Fatal("concurrent writer bypassed publication lock")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// A failed filesystem switch must preserve the recoverable old policy and
	// release the writer lock so the next attempt can converge.
	blocker := filepath.Join(destination, ".current-next")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(blocker, "occupied"), []byte("block symlink replacement"))
	if _, err := credentials.Publish(source, destination, trust, identity, clock, bPolicy); err == nil {
		t.Fatal("filesystem failure unexpectedly committed a policy")
	}
	if err := credentials.Check(destination, trust, identity, clock, aPolicy); err != nil {
		t.Fatalf("failed switch lost old recovery: %v", err)
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, trust, identity, clock, bPolicy); err == nil {
		t.Fatal("new gate accepted an older policy")
	}
	second, publishErr := credentials.Publish(source, destination, trust, identity, clock, bPolicy)
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	priorBPolicy := *bPolicy
	wrongEpoch := *bPolicy
	wrongEpoch.Revision = &credentials.PolicyRevision{Epoch: "different-installation", Sequence: 2}
	if err := credentials.Check(destination, trust, identity, clock, &wrongEpoch); err == nil {
		t.Fatal("gate accepted copied epoch")
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, &wrongEpoch); err == nil {
		t.Fatal("writer overwrote different epoch")
	}
	if err := credentials.Check(destination, trust, identity, clock, &credentials.GatewayPolicy{Accounts: aPolicy.Accounts}); err == nil {
		t.Fatal("legacy exact gate unexpectedly accepted revoked permissions")
	}
	if err := credentials.Check(destination, trust, identity, clock, aPolicy); err != nil {
		t.Fatalf("old gate must load current revoked policy: %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(destination, "current", "permissions.yaml"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data), "alice") {
		t.Fatal("old grant survived revocation")
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, aPolicy); err == nil {
		t.Fatal("stale writer rolled back revocation")
	}
	conflict := *bPolicy
	conflict.Accounts = aPolicy.Accounts
	if _, err := credentials.Publish(source, destination, trust, identity, clock, &conflict); err == nil {
		t.Fatal("same revision acquired different permissions")
	}
	if err := credentials.Check(destination, trust, identity, clock, &conflict); err == nil {
		t.Fatal("equal-revision gate accepted different permissions")
	}
	// Reusing A's content is a new authorization, not a replay of revision 1.
	aPolicy.Revision = &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 3}
	if err := credentials.Check(destination, trust, identity, clock, aPolicy); err == nil {
		t.Fatal("ABA gate accepted revision 2")
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, aPolicy); err != nil {
		t.Fatal(err)
	}
	bPolicy.Revision = &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 4}
	replay := t.TempDir()
	if err := os.Symlink(filepath.Join(destination, second), filepath.Join(replay, "current")); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(replay, trust, identity, clock, &credentials.GatewayPolicy{}); err != nil {
		t.Fatalf("older B fixture is not a valid same-content publication: %v", err)
	}
	if err := credentials.Check(replay, trust, identity, clock, bPolicy); err == nil || !strings.Contains(err.Error(), "older than required") {
		t.Fatalf("new B conflated prior B's identical permissions with a new revision: %v", err)
	}
	if err := credentials.Check(destination, trust, identity, clock, bPolicy); err == nil {
		t.Fatal("repeated B gate accepted older B/A publication")
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, bPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, &priorBPolicy); err == nil {
		t.Fatal("same-content stale B writer reset the publication revision")
	}
	metadataPath := filepath.Join(destination, "current", "policy.json")
	permissionsPath := filepath.Join(destination, "current", "permissions.yaml")
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := os.ReadFile(permissionsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"malformed-metadata", "missing-metadata", "tampered-revision", "tampered-permissions"} {
		t.Run(fault, func(t *testing.T) {
			switch fault {
			case "malformed-metadata":
				write(t, metadataPath, []byte("{"))
			case "missing-metadata":
				if err := os.Remove(metadataPath); err != nil {
					t.Fatal(err)
				}
			case "tampered-revision":
				write(t, metadataPath, []byte(`{"revision":{"epoch":"installation-uid","sequence":100},"accounts":[]}`))
			case "tampered-permissions":
				write(t, permissionsPath, []byte("permissions: {users: [{username: alice, permissions: ['/*/*']}]}\n"))
			}
			if err := credentials.Check(destination, trust, identity, clock, aPolicy); err == nil {
				t.Fatal("old gate accepted damaged installed authorization")
			}
			if _, err := credentials.Publish(source, destination, trust, identity, clock, bPolicy); err == nil {
				t.Fatal("writer treated damaged managed policy as a legacy/bootstrap publication")
			}
			write(t, metadataPath, metadata)
			write(t, permissionsPath, permissions)
		})
	}
}

func TestPolicyCLIRejectsIncompleteRevision(t *testing.T) {
	for _, args := range [][]string{{"--policy-epoch=installation-uid"}, {"--policy-revision=1"}, {"--policy-revision=0"}, {"--exact-policy-revision"}} {
		args = append([]string{"--uri=" + identityURI, "--usage=server", "--gateway-policy", "--check"}, args...)
		if err := credentials.Run(context.Background(), args); err == nil {
			t.Fatalf("accepted incomplete revision flags: %v", args)
		}
	}
}

func TestMonotonicPolicySSHRevocation(t *testing.T) {
	a := newAuthority(t)
	source, destination := t.TempDir(), t.TempDir()
	trust, sshTrust := filepath.Join(t.TempDir(), "ca.crt"), filepath.Join(t.TempDir(), "ssh-ca.pub")
	write(t, trust, a.pem)
	sshBytes := sshAuthority(t)
	write(t, sshTrust, sshBytes)
	sshDigest := sha256.Sum256(sshBytes)
	cert, key := a.leaf(t, 43, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} })
	project(t, source, "issued", cert, key)
	identity := credentials.Identity{URI: identityURI, Usage: "server"}
	old := &credentials.GatewayPolicy{Revision: &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 1}, SSHTrustFile: sshTrust, SSHTrustHash: hex.EncodeToString(sshDigest[:])}
	current := &credentials.GatewayPolicy{Revision: &credentials.PolicyRevision{Epoch: "installation-uid", Sequence: 2}, SSHTrustHash: "none"}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, old); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, trust, identity, clock, current); err == nil {
		t.Fatal("new gate accepted old SSH authorization")
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, current); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Check(destination, trust, identity, clock, old); err != nil {
		t.Fatalf("retained gate refused current revoked SSH policy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "current", "ssh-ca.pub")); !os.IsNotExist(err) {
		t.Fatalf("old SSH CA trust survived revocation: %v", err)
	}
	if _, err := credentials.Publish(source, destination, trust, identity, clock, old); err == nil {
		t.Fatal("stale writer resurrected SSH trust")
	}
}
