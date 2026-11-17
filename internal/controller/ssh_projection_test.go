package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestManagedSSHProjectionIsBoundToAllocatedRevision(t *testing.T) {
	for _, mode := range []string{"fresh", "unchanged-bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			r, y, _ := certificateEnvironment(t)
			key := func() []byte {
				private, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if keyErr != nil {
					t.Fatal(keyErr)
				}
				public, publicErr := ssh.NewPublicKey(&private.PublicKey)
				if publicErr != nil {
					t.Fatal(publicErr)
				}
				return ssh.MarshalAuthorizedKey(public)
			}
			oldSSH, newSSH := key(), key()
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-policy", Namespace: y.Namespace}, BinaryData: map[string][]byte{"ssh-ca.pub": oldSSH}}
			if createErr := r.Create(context.Background(), cm); createErr != nil {
				t.Fatal(createErr)
			}
			r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Key: "ssh-ca.pub"}
			startCertificateWorkloads(t, r, y)
			if mode == "unchanged-bootstrap" {
				for _, component := range []string{"credential-delivery", "controlplane"} {
					legacy := certificateWorkload(t, r, component)
					for _, containers := range [][]corev1.Container{legacy.Spec.Template.Spec.Containers, legacy.Spec.Template.Spec.InitContainers} {
						for i := range containers {
							strip := func(arg string) bool {
								return strings.HasPrefix(arg, "--policy-") || strings.HasPrefix(arg, "--ssh-trust-sha256=") || arg == "--exact-policy-revision"
							}
							containers[i].Args = slices.DeleteFunc(containers[i].Args, strip)
							if probe := containers[i].ReadinessProbe; probe != nil && probe.Exec != nil {
								probe.Exec.Command = slices.DeleteFunc(probe.Exec.Command, strip)
							}
						}
					}
					if updateErr := r.Update(context.Background(), legacy); updateErr != nil {
						t.Fatal(updateErr)
					}
				}
				if getErr := r.Get(context.Background(), client.ObjectKeyFromObject(y), y); getErr != nil {
					t.Fatal(getErr)
				}
				delete(y.Annotations, policyStateAnnotation)
				delete(y.Annotations, policyAckAnnotation)
				if updateErr := r.Update(context.Background(), y); updateErr != nil {
					t.Fatal(updateErr)
				}
				reconcileCertificates(t, r, y)
			}
			first := certificateWorkload(t, r, "credential-delivery")
			oldConsumer := certificateWorkload(t, r, "controlplane")
			fixture := newGatewayFileFixture(t, first)
			projection := filepath.Join(t.TempDir(), "ssh-ca.pub")
			writeProjection := func(data []byte) {
				t.Helper()
				if writeErr := os.WriteFile(projection, data, 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			paths := []string{"--source=" + fixture.source, "--destination=" + fixture.destination, "--trust=" + fixture.trust, "--ssh-trust=" + projection}
			publish := func(d *appsv1.Deployment) {
				t.Helper()
				one, cancel := context.WithCancel(context.Background())
				cancel()
				args := append(slices.Clone(d.Spec.Template.Spec.Containers[0].Args[1:]), paths...)
				if runErr := credentials.Run(one, args); runErr != nil {
					t.Fatal(runErr)
				}
			}
			ready := func(d *appsv1.Deployment) error {
				args := append(slices.Clone(d.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec.Command[2:]), paths...)
				return credentials.Run(context.Background(), args)
			}
			writeProjection(oldSSH)
			if mode == "unchanged-bootstrap" {
				legacyArgs := slices.DeleteFunc(slices.Clone(first.Spec.Template.Spec.Containers[0].Args[1:]), func(arg string) bool {
					return strings.HasPrefix(arg, "--policy-") || strings.HasPrefix(arg, "--ssh-trust-sha256=")
				})
				one, cancel := context.WithCancel(context.Background())
				cancel()
				if runErr := credentials.Run(one, append(legacyArgs, paths...)); runErr != nil {
					t.Fatal(runErr)
				}
				if _, statErr := os.Stat(filepath.Join(fixture.destination, "current", "ssh-ca.pub")); statErr != nil {
					t.Fatalf("legacy SSH policy was not published: %v", statErr)
				}
				if _, statErr := os.Stat(filepath.Join(fixture.destination, "current", "policy.json")); !os.IsNotExist(statErr) {
					t.Fatalf("legacy publication unexpectedly has managed metadata: %v", statErr)
				}
			}
			publish(first)
			if readyErr := ready(first); readyErr != nil {
				t.Fatal(readyErr)
			}
			if mode == "unchanged-bootstrap" {
				readyCertificateDeployments(t, r)
				reconcileCertificates(t, r, y)
				readyCertificateDeployments(t, r)
				acknowledgePolicyCheckpoints(t, r, y)
			}
			initial, linkErr := os.Readlink(filepath.Join(fixture.destination, "current"))
			if linkErr != nil {
				t.Fatal(linkErr)
			}
			cm.BinaryData["ssh-ca.pub"] = newSSH
			if updateErr := r.Update(context.Background(), cm); updateErr != nil {
				t.Fatal(updateErr)
			}
			reconcileCertificates(t, r, y)
			second := certificateWorkload(t, r, "credential-delivery")
			for range 3 {
				publish(second)
				if readyErr := ready(second); readyErr == nil {
					t.Fatal("allocated SSH policy B was reported ready with projected A")
				}
				current, currentErr := os.Readlink(filepath.Join(fixture.destination, "current"))
				if currentErr != nil {
					t.Fatal(currentErr)
				}
				if current != initial {
					t.Fatal("stale projection occupied the new revision")
				}
				reconcileCertificates(t, r, y)
				if readPolicyState(t, r, y).Sequence != 2 {
					t.Fatal("projection retry churned the allocated revision")
				}
			}
			writeProjection(newSSH)
			publish(second)
			if readyErr := ready(second); readyErr != nil {
				t.Fatalf("projection catch-up did not converge at the allocated revision: %v", readyErr)
			}
			stored, readErr := os.ReadFile(filepath.Join(fixture.destination, "current", "ssh-ca.pub"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(stored, newSSH) {
				t.Fatal("replaced SSH CA survived publication")
			}
			oldArgs := append(slices.Clone(oldConsumer.Spec.Template.Spec.InitContainers[0].Args[1:]), "--destination="+fixture.destination, "--trust="+filepath.Join(fixture.destination, "current", "ca.crt"), "--ssh-trust="+filepath.Join(fixture.destination, "current", "ssh-ca.pub"))
			if gateErr := credentials.Run(context.Background(), oldArgs); gateErr != nil {
				t.Fatalf("old supported gate retained historical SSH requirements: %v", gateErr)
			}
			// Projection may also lead the old allocation: it must not mutate revision 1.
			if readyErr := ready(first); readyErr == nil {
				t.Fatal("old writer claimed new SSH policy as its exact revision")
			}
			readyCertificateDeployments(t, r)
			reconcileCertificates(t, r, y)
			if readPolicyState(t, r, y).Sequence != 2 {
				t.Fatal("successful delivery allocated an unnecessary revision")
			}
			// Revocation removes the input projection from the rendered writer. A retained
			// supported checkpoint must follow that newer policy, not restore its old CA.
			r.GlobalConfig.Config.Certificates.SSHTrustBundle = nil
			publishCertificateConfig(t, r)
			reconcileCertificates(t, r, y)
			removed := certificateWorkload(t, r, "credential-delivery")
			paths = paths[:3]
			publish(removed)
			if readyErr := ready(removed); readyErr != nil {
				t.Fatalf("SSH removal did not become ready: %v", readyErr)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.destination, "current", "ssh-ca.pub")); !os.IsNotExist(statErr) {
				t.Fatalf("revoked SSH CA remains installed: %v", statErr)
			}
			if gateErr := credentials.Run(context.Background(), oldArgs); gateErr != nil {
				t.Fatalf("old checkpoint could not recover after SSH removal: %v", gateErr)
			}
			if readPolicyState(t, r, y).Sequence != 3 {
				t.Fatal("SSH removal did not allocate exactly one new revision")
			}
			// Even with its own historical bytes available again, a stale writer cannot
			// roll the installed revocation back.
			writeProjection(oldSSH)
			paths = append(paths, "--ssh-trust="+projection)
			publish(first)
			if _, statErr := os.Stat(filepath.Join(fixture.destination, "current", "ssh-ca.pub")); !os.IsNotExist(statErr) {
				t.Fatalf("stale writer restored revoked SSH trust: %v", statErr)
			}
		})
	}
}
