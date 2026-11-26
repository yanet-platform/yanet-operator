package credentials_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

func TestOnlinePolicyDeliveryAndOfflineStartup(t *testing.T) {
	source := filepath.Join(t.TempDir(), credentials.PolicyBundleKey)
	destination := t.TempDir()
	run := func(mode string) error {
		return credentials.RunPolicy(t.Context(), []string{
			"--source=" + source, "--destination=" + destination, mode,
		})
	}
	project := func(bundle credentials.OnlinePolicyBundle) {
		t.Helper()
		value, err := credentials.EncodeOnlinePolicyBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
		write(t, source, value)
	}
	read := func(name string) []byte {
		t.Helper()
		value, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	checkAccounts := func(wantPermission string) {
		t.Helper()
		var identities struct {
			Identities []struct{ Username string } `yaml:"identities"`
		}
		var permissions struct {
			Permissions struct {
				Users []credentials.GatewayAccount `yaml:"users"`
			} `yaml:"permissions"`
		}
		if err := yaml.Unmarshal(read("identities.yaml"), &identities); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(read("permissions.yaml"), &permissions); err != nil {
			t.Fatal(err)
		}
		if wantPermission == "" {
			if identities.Identities == nil || len(identities.Identities) != 0 ||
				permissions.Permissions.Users == nil || len(permissions.Permissions.Users) != 0 {
				t.Fatal("revocation must leave explicit empty documents")
			}
			return
		}
		users := permissions.Permissions.Users
		if len(identities.Identities) != 1 || identities.Identities[0].Username != "alice" ||
			len(users) != 1 || users[0].Username != "alice" ||
			len(users[0].Permissions) != 1 || users[0].Permissions[0] != wantPermission {
			t.Fatalf("unexpected policy: identities=%+v permissions=%+v", identities, users)
		}
	}

	if err := run("--check"); err == nil {
		t.Fatal("empty persistent directory passed startup")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trust := append(bytes.TrimSpace(ssh.MarshalAuthorizedKey(public)), ' ', 0xff, '\n')
	bundle := credentials.OnlinePolicyBundle{
		Accounts: []credentials.GatewayAccount{{Username: "alice", Permissions: []string{"/example.Service/Read"}}},
		SSHTrust: trust,
	}
	project(bundle)
	if err := run("--once"); err != nil {
		t.Fatal(err)
	}
	checkAccounts("/example.Service/Read")
	if !bytes.Equal(read("ssh-ca.pub"), trust) {
		t.Fatal("SSH trust bytes changed")
	}
	if err := run("--check-projection"); err != nil {
		t.Fatal(err)
	}
	beforeIdentities, beforePermissions := read("identities.yaml"), read("permissions.yaml")
	for _, invalid := range []string{
		`{`, `{}`, `{"accounts":null}`, `{"accounts":[],"ssh_trust":"YmFk"}`,
		`{"accounts":[],"unknown":true}`, `{"accounts":[]} {}`,
		strings.Repeat(" ", credentials.MaxPolicyBundleSize+1),
	} {
		write(t, source, []byte(invalid))
		if err := run("--once"); err == nil {
			t.Fatal("invalid projection was accepted")
		}
		if !bytes.Equal(read("identities.yaml"), beforeIdentities) ||
			!bytes.Equal(read("permissions.yaml"), beforePermissions) ||
			!bytes.Equal(read("ssh-ca.pub"), trust) {
			t.Fatal("invalid projection changed the local policy")
		}
	}
	bundle.Accounts[0].Permissions = []string{"/example.Service/Write"}
	project(bundle)
	if err := run("--check-projection"); err == nil {
		t.Fatal("undelivered projection passed readiness")
	}
	if err := run("--once"); err != nil {
		t.Fatal(err)
	}
	checkAccounts("/example.Service/Write")

	project(credentials.OnlinePolicyBundle{Accounts: []credentials.GatewayAccount{}})
	if err := run("--once"); err != nil {
		t.Fatal(err)
	}
	checkAccounts("")
	if _, err := os.Stat(filepath.Join(destination, "ssh-ca.pub")); !os.IsNotExist(err) {
		t.Fatalf("removed SSH trust is still on disk: %v", err)
	}
	if err := run("--check-projection"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := run("--check"); err != nil {
		t.Fatalf("offline startup requires the projection: %v", err)
	}
	if err := run("--once"); err == nil {
		t.Fatal("missing projection must report a delivery error")
	}
	checkAccounts("")
}
