package credentials_test

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/sys/unix"
)

func TestWriterReadinessSharesRolePublicationLock(t *testing.T) {
	for _, usage := range []string{"client", "server"} {
		t.Run(usage, func(t *testing.T) {
			now := time.Now()
			a := newAuthorityAt(t, now, nil)
			source, destination := t.TempDir(), t.TempDir()
			trust := filepath.Join(t.TempDir(), "ca.crt")
			write(t, trust, a.pem)
			cert, key := a.leaf(t, 61, func(c *x509.Certificate) {
				c.NotBefore, c.NotAfter = now.Add(-time.Hour), now.Add(time.Hour)
				if usage == "server" {
					serverEKU(c)
				}
			})
			project(t, source, "issued", cert, key)
			identity := credentials.Identity{URI: identityURI, Usage: usage}
			args := []string{"--uri=" + identityURI, "--usage=" + usage, "--destination=" + destination, "--trust=" + trust, "--check"}
			if _, err := credentials.Publish(source, destination, trust, identity, now); err != nil {
				t.Fatal(err)
			}
			probe := append(slices.Clone(args), "--writer-readiness")
			lockPath := filepath.Join(destination, ".publish-lock")
			lock, openErr := os.Open(lockPath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer func() { _ = lock.Close() }()
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			if err := credentials.Run(t.Context(), probe); !errors.Is(err, unix.EWOULDBLOCK) {
				t.Fatalf("writer readiness did not refuse a busy publication lock: %v", err)
			}
			if err := credentials.Run(t.Context(), args); err != nil {
				t.Fatalf("offline check depends on the writer lock: %v", err)
			}
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
				t.Fatal(err)
			}
			if err := credentials.Run(t.Context(), probe); err != nil {
				t.Fatalf("writer readiness did not recover after release: %v", err)
			}
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
			if err := credentials.Run(t.Context(), probe); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("writer readiness should not create a lock: %v", err)
			}
			if err := credentials.Run(t.Context(), args); err != nil {
				t.Fatalf("offline check requires a lock file: %v", err)
			}
		})
	}
}

func TestWriterReadinessRequiresCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := credentials.Run(ctx, []string{"--writer-readiness", "--uri=" + identityURI}); err == nil || !strings.Contains(err.Error(), "writer readiness requires --check") {
		t.Fatalf("wanted a check-only flag diagnostic, got %v", err)
	}
}
