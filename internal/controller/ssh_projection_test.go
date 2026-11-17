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
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestManagedSSHBridgeRecoversWithNewNode(t *testing.T) {
	testCtx := context.Background()
	r, y, oldNode := certificateEnvironment(t)
	key := func() []byte {
		t.Helper()
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
	oldSSH, wrongSSH := key(), key()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-policy", Namespace: y.Namespace}, BinaryData: map[string][]byte{"ssh-ca.pub": oldSSH}}
	if createErr := r.Create(testCtx, cm); createErr != nil {
		t.Fatal(createErr)
	}
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Key: "ssh-ca.pub"}
	startCertificateWorkloads(t, r, y)

	legacyWriter := certificateWorkload(t, r, "credential-delivery")
	legacyGate := certificateWorkload(t, r, "controlplane")
	for _, d := range []*appsv1.Deployment{legacyWriter, legacyGate} {
		for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
			for i := range containers {
				containers[i].Args = slices.DeleteFunc(containers[i].Args, func(arg string) bool {
					return strings.HasPrefix(arg, "--policy-") || strings.HasPrefix(arg, "--ssh-trust-sha256=")
				})
			}
		}
		if updateErr := r.Update(testCtx, d); updateErr != nil {
			t.Fatal(updateErr)
		}
		// The fake API does not advance Deployment generations on spec updates.
		// Do not let bootstrap inherit readiness from the fixture's previous Pod.
		d.Status = appsv1.DeploymentStatus{}
		if statusErr := r.Status().Update(testCtx, d); statusErr != nil {
			t.Fatal(statusErr)
		}
	}
	readPolicyState(t, r, y)
	delete(y.Annotations, policyStateAnnotation)
	delete(y.Annotations, policyAckAnnotation)
	if updateErr := r.Update(testCtx, y); updateErr != nil {
		t.Fatal(updateErr)
	}

	type localRole struct {
		gatewayFileFixture
		projection string
	}
	role := func(writer *appsv1.Deployment) localRole {
		return localRole{newGatewayFileFixture(t, writer), filepath.Join(t.TempDir(), "ssh-ca.pub")}
	}
	publish := func(local localRole, writer *appsv1.Deployment, bundle []byte) string {
		t.Helper()
		if writeErr := os.WriteFile(local.projection, bundle, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		args := slices.Clone(writer.Spec.Template.Spec.Containers[0].Args[1:])
		destination := local.destination
		for _, arg := range args {
			if subdir, ok := strings.CutPrefix(arg, "--destination=/persist/"); ok {
				destination = filepath.Join(destination, subdir)
			}
		}
		one, cancel := context.WithCancel(testCtx)
		cancel()
		if runErr := credentials.Run(one, append(args, "--source="+local.source, "--destination="+destination, "--trust="+local.trust, "--ssh-trust="+local.projection)); runErr != nil {
			t.Fatal(runErr)
		}
		return destination
	}
	assertBundle := func(destination string, want []byte) {
		t.Helper()
		got, readErr := os.ReadFile(filepath.Join(destination, "current", "ssh-ca.pub"))
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s has the wrong durable SSH policy: %v", destination, readErr)
		}
	}
	probe := func(local localRole, writer *appsv1.Deployment) error {
		args := slices.Clone(writer.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec.Command[2:])
		return credentials.Run(testCtx, append(args, "--destination="+local.destination, "--trust="+local.trust, "--ssh-trust="+local.projection))
	}
	gate := func(local localRole, d *appsv1.Deployment) error {
		args := slices.Clone(d.Spec.Template.Spec.InitContainers[0].Args[1:])
		return credentials.Run(testCtx, append(args, "--destination="+local.destination, "--trust="+filepath.Join(local.destination, "current", "ca.crt"), "--ssh-trust="+filepath.Join(local.destination, "current", "ssh-ca.pub")))
	}
	deployment := func(node, component string) *appsv1.Deployment {
		t.Helper()
		list := &appsv1.DeploymentList{}
		if listErr := r.List(testCtx, list, client.MatchingLabels{manifests.LabelNode: node, manifests.LabelComponent: component}); listErr != nil {
			t.Fatal(listErr)
		}
		if len(list.Items) == 0 {
			return nil
		}
		return &list.Items[0]
	}
	exactReady := func(writer *appsv1.Deployment) {
		t.Helper()
		writer.Status = appsv1.DeploymentStatus{ObservedGeneration: writer.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
		if statusErr := r.Status().Update(testCtx, writer); statusErr != nil {
			t.Fatal(statusErr)
		}
	}

	oldRole := role(legacyWriter)
	publish(oldRole, legacyWriter, oldSSH)
	if gateErr := gate(oldRole, legacyGate); gateErr != nil {
		t.Fatal(gateErr)
	}
	newNode := oldNode.DeepCopy()
	newNode.Name, newNode.UID, newNode.ResourceVersion = "new.example.test", "new-node-uid", ""
	if createErr := r.Create(testCtx, newNode); createErr != nil {
		t.Fatal(createErr)
	}
	setBundle := func(bundle []byte) {
		t.Helper()
		cm.BinaryData["ssh-ca.pub"] = bundle
		if updateErr := r.Update(testCtx, cm); updateErr != nil {
			t.Fatal(updateErr)
		}
		reconcileCertificates(t, r, y)
	}
	setBundle(wrongSSH)
	markCertificatesIssued(t, r)
	reconcileCertificates(t, r, y)
	oldWriter := deployment(oldNode.Name, "credential-delivery")
	newWriter := deployment(newNode.Name, "credential-delivery")
	newRole := role(newWriter)
	publish(oldRole, oldWriter, wrongSSH)
	assertBundle(oldRole.destination, oldSSH)
	wrongPath := publish(newRole, newWriter, wrongSSH)
	assertBundle(wrongPath, wrongSSH)
	wrongProbe := probe(newRole, newWriter)
	acknowledgePolicyCheckpoints(t, r, y)
	if readPolicyState(t, r, y).Activated {
		t.Fatal("unready wrong policy was activated")
	}
	setBundle(oldSSH)
	oldWriter = deployment(oldNode.Name, "credential-delivery")
	newWriter = deployment(newNode.Name, "credential-delivery")
	publish(oldRole, oldWriter, oldSSH)
	assertBundle(publish(newRole, newWriter, oldSSH), oldSSH) // Red: the initial writer pins B here.
	if wrongProbe == nil {
		t.Fatal("unverified B could release a fresh gateway")
	}
	if newGateway := deployment(newNode.Name, "controlplane"); newGateway != nil {
		t.Fatal("fresh gateway was applied before legacy continuity")
	}
	if _, statErr := os.Stat(filepath.Join(newRole.destination, "current")); !os.IsNotExist(statErr) {
		t.Fatalf("unverified bytes reached the runtime path: %v", statErr)
	}
	if probeErr := probe(oldRole, oldWriter); probeErr != nil {
		t.Fatal(probeErr)
	}
	exactReady(oldWriter)
	reconcileCertificates(t, r, y)
	newWriter = deployment(newNode.Name, "credential-delivery")
	assertBundle(publish(newRole, newWriter, oldSSH), oldSSH)
	if probeErr := probe(newRole, newWriter); probeErr != nil {
		t.Fatalf("new writer did not pass exact readiness after continuity: %v", probeErr)
	}
	exactReady(newWriter)
	reconcileCertificates(t, r, y)
	for node, local := range map[string]localRole{oldNode.Name: oldRole, newNode.Name: newRole} {
		d := deployment(node, "controlplane")
		if d == nil {
			t.Fatalf("missing corrected gateway for %s", node)
		}
		if gateErr := gate(local, d); gateErr != nil {
			t.Fatalf("corrected gate for %s: %v", node, gateErr)
		}
		exactReady(d)
	}
	reconcileCertificates(t, r, y)
	if state := readPolicyState(t, r, y); state.Sequence != 2 || state.Activated {
		t.Fatalf("old ACK activated the corrected attempt: %+v", state)
	}
	acknowledgePolicyCheckpoints(t, r, y)
	if !readPolicyState(t, r, y).Activated {
		t.Fatal("corrected rollout and current ACK did not activate")
	}
}

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
				legacyGate := append(slices.Clone(oldConsumer.Spec.Template.Spec.InitContainers[0].Args[1:]), "--destination="+fixture.destination, "--trust="+filepath.Join(fixture.destination, "current", "ca.crt"), "--ssh-trust="+filepath.Join(fixture.destination, "current", "ssh-ca.pub"))
				cm.BinaryData["ssh-ca.pub"] = newSSH
				if err := r.Update(context.Background(), cm); err != nil {
					t.Fatal(err)
				}
				reconcileCertificates(t, r, y)
				wrong := certificateWorkload(t, r, "credential-delivery")
				writeProjection(newSSH)
				publish(wrong)
				if err := ready(wrong); err == nil {
					t.Fatal("wrong initial SSH policy passed exact readiness on legacy storage")
				}
				if got, err := os.ReadFile(filepath.Join(fixture.destination, "current", "ssh-ca.pub")); err != nil || !bytes.Equal(got, oldSSH) {
					t.Fatalf("wrong attempt changed durable SSH trust: %v", err)
				}
				cm.BinaryData["ssh-ca.pub"] = oldSSH
				if err := r.Update(context.Background(), cm); err != nil {
					t.Fatal(err)
				}
				writeProjection(oldSSH)
				reconcileCertificates(t, r, y)
				first = certificateWorkload(t, r, "credential-delivery")
				if readPolicyState(t, r, y).Sequence != 2 {
					t.Fatal("corrected SSH policy did not get a new provisional revision")
				}
				publish(first)
				if err := credentials.Run(context.Background(), legacyGate); err != nil {
					t.Fatalf("legacy checkpoint lost the unchanged policy during bridge: %v", err)
				}
			}
			publish(first)
			if readyErr := ready(first); readyErr != nil {
				t.Fatal(readyErr)
			}
			if mode == "unchanged-bootstrap" {
				readyCertificateDeployments(t, r)
				reconcileCertificates(t, r, y)
				oldConsumer = certificateWorkload(t, r, "controlplane")
				gate := append(slices.Clone(oldConsumer.Spec.Template.Spec.InitContainers[0].Args[1:]), "--destination="+fixture.destination, "--trust="+filepath.Join(fixture.destination, "current", "ca.crt"), "--ssh-trust="+filepath.Join(fixture.destination, "current", "ssh-ca.pub"))
				if err := credentials.Run(context.Background(), gate); err != nil {
					t.Fatalf("corrected bridge gate did not validate: %v", err)
				}
				readyCertificateDeployments(t, r)
				acknowledgePolicyCheckpoints(t, r, y)
			}
			bridgeRevision := readPolicyState(t, r, y).Sequence
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
				if readPolicyState(t, r, y).Sequence != bridgeRevision+1 {
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
			if readPolicyState(t, r, y).Sequence != bridgeRevision+1 {
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
			if readPolicyState(t, r, y).Sequence != bridgeRevision+2 {
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
