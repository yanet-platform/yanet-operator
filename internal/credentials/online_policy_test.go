package credentials_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func onlineBundle() credentials.OnlinePolicyBundle {
	return credentials.OnlinePolicyBundle{
		Epoch: "installation-uid", NodeUID: "node-uid", Sequence: 1,
		Namespace: "default", Domain: "example.test", Roles: []string{"gateway"},
		Accounts: []credentials.GatewayAccount{{Username: "alice", Permissions: []string{"/example.Service/Read"}}},
	}
}

func runProjectedPolicy(t *testing.T, destination string, bundle credentials.OnlinePolicyBundle, witness string) {
	t.Helper()
	data, err := credentials.EncodeOnlinePolicyBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), credentials.PolicyBundleKey)
	write(t, source, data)
	scope := []string{
		"--epoch=" + bundle.Epoch, "--node-uid=" + bundle.NodeUID,
		"--namespace=" + bundle.Namespace, "--domain=" + bundle.Domain,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- credentials.RunPolicy(ctx, append(scope, "--source="+source, "--destination="+destination))
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("policy writer: %v", err)
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var checkErr error
	for {
		checkErr = credentials.RunPolicy(ctx, append(scope,
			"--destination="+filepath.Join(destination, witness), "--check-role="+witness))
		if checkErr == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("policy writer did not publish role %s: %v", witness, checkErr)
		case <-ticker.C:
		}
	}
}

func TestOnlinePolicyWriterFencesAndOfflineCheck(t *testing.T) {
	root := t.TempDir()
	bundle := onlineBundle()
	runProjectedPolicy(t, root, bundle, "gateway")

	current := filepath.Join(root, "gateway", "current")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(current, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if !bytes.Contains(read("permissions.yaml"), []byte("/example.Service/Read")) {
		t.Fatal("initial grant was not published")
	}
	bundle.Sequence = 2
	bundle.Accounts = []credentials.GatewayAccount{}
	bundle.Roles = []string{"gateway", "second"}
	runProjectedPolicy(t, root, bundle, "second")
	revision := read("policy.json")
	if !bytes.Contains(revision, []byte(`"sequence":2`)) ||
		strings.TrimSpace(string(read("identities.yaml"))) != "identities: []" ||
		strings.TrimSpace(string(read("permissions.yaml"))) != "permissions:\n    users: []" {
		t.Fatal("empty policy was not explicitly installed")
	}
	for _, tc := range []struct {
		role     string
		sequence uint64
	}{
		{"stale-witness", 1},
		{"conflict-witness", 2},
	} {
		previous := onlineBundle()
		previous.Sequence = tc.sequence
		previous.Roles = []string{"gateway", tc.role}
		// The untouched witness can publish despite the bad update on
		// gateway. The revoked role must retain its previous generation.
		runProjectedPolicy(t, root, previous, tc.role)
		if !bytes.Equal(revision, read("policy.json")) {
			t.Fatalf("%s changed the published revision", tc.role)
		}
	}
	err := credentials.RunPolicy(t.Context(), []string{
		"--epoch=installation-uid", "--node-uid=foreign-node", "--namespace=default",
		"--domain=example.test", "--destination=" + filepath.Join(root, "gateway"), "--check-role=gateway",
	})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected offline scope check to reject foreign Node: %v", err)
	}
}

func TestOnlinePolicyBundleBudgetRejectsWholeUpdate(t *testing.T) {
	bundle := onlineBundle()
	bundle.Accounts = nil
	for account := range 64 {
		var methods []string
		for method := range 128 {
			methods = append(methods, fmt.Sprintf("/%s/Method%d", strings.Repeat("x", 160), method))
		}
		bundle.Accounts = append(bundle.Accounts, credentials.GatewayAccount{
			Username: fmt.Sprintf("user%d", account), Permissions: methods,
		})
	}
	if data, err := credentials.EncodeOnlinePolicyBundle(bundle); err == nil || data != nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized policy not rejected in full: %d bytes, %v", len(data), err)
	}
}

func TestOnlinePolicyWriterPreservesSSHTrustBytes(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	bundle := onlineBundle()
	bundle.SSHTrust = append(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pub)), ' ', 0xff, '\r', '\n')
	root := t.TempDir()
	runProjectedPolicy(t, root, bundle, "gateway")
	path := filepath.Join(root, "gateway", "current", "ssh-ca.pub")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, bundle.SSHTrust) {
		t.Fatalf("SSH trust was not preserved byte-for-byte: %v", err)
	}
	bundle.Sequence++
	bundle.SSHTrust = nil
	bundle.Roles = append(bundle.Roles, "ready-witness")
	runProjectedPolicy(t, root, bundle, "ready-witness")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed trust is still present: %v", err)
	}
}

func TestOnlinePolicyOfflineGateSharesReaderLock(t *testing.T) {
	bundle := onlineBundle()
	root := t.TempDir()
	runProjectedPolicy(t, root, bundle, "gateway")
	role := filepath.Join(root, "gateway")
	lock, err := os.Open(filepath.Join(role, ".publish-lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if flockErr := unix.Flock(int(lock.Fd()), unix.LOCK_SH|unix.LOCK_NB); flockErr != nil {
		t.Fatal(flockErr)
	}
	args := []string{
		"--epoch=" + bundle.Epoch, "--node-uid=" + bundle.NodeUID, "--namespace=" + bundle.Namespace,
		"--domain=" + bundle.Domain, "--destination=" + role, "--check-role=gateway",
	}
	if checkErr := credentials.RunPolicy(t.Context(), args); checkErr != nil {
		t.Fatalf("another reader prevents a valid offline check: %v", checkErr)
	}
}
