package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
)

func TestOnlinePolicyWriterWaitsForOwnedProjection(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	r.GlobalConfig.Config.Certificates.OnlineGatewayPolicy = true
	publishCertificateConfig(t, r)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: y.Namespace, Name: manifests.PolicyConfigMapName(string(y.UID), string(node.UID)),
	}}
	if err := r.Create(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}
	if _, err := r.Reconcile(t.Context(), req); err == nil || !strings.Contains(err.Error(), "not an active object owned") {
		t.Fatalf("expected projection ownership rejection, got %v", err)
	}
	writers := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), writers, client.InNamespace(y.Namespace),
		client.MatchingLabels{manifests.LabelComponent: manifests.PolicyDeliveryComponent}); err != nil {
		t.Fatal(err)
	}
	if len(writers.Items) != 0 {
		t.Fatalf("started a writer mounting a ConfigMap whose ownership was rejected: %s", writers.Items[0].Name)
	}
}

func TestOnlinePolicyDisabledDrainPreservesConsumerTemplate(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	cfg := &r.GlobalConfig.Config
	cfg.Certificates.OnlineGatewayPolicy = true
	cfg.Components.Operators = []api.OperatorSpec{{Name: "peer", Containers: []api.OperatorContainer{
		{Name: "peer", Image: api.ImageRef{Name: "peer", Tag: "v1"}},
	}}}
	cfg.BoxTypes[0].Operators = map[string]api.BoxOperator{"peer": {}}
	cfg.Certificates.Profiles = append(cfg.Certificates.Profiles, api.CertificateProfile{
		Name: "peer", Component: "peer", Container: "peer", MountPath: "/etc/yanet2/pki/peer",
		Usage: "server", DNSNames: []string{"{node}"},
	})
	publishCertificateConfig(t, r)
	convergeOnlinePolicy(t, r, y)
	before := certificateWorkload(t, r, "controlplane")
	if len(manifests.OnlinePolicyTargets(before)) != 1 {
		t.Fatal("expected the running gateway to have a managed policy mount")
	}
	cfg.Certificates.Profiles = cfg.Certificates.Profiles[1:]
	publishCertificateConfig(t, r)
	y.Spec.Enabled = helpers.PtrFalse()
	y.Generation++
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}
	_, drainErr := r.Reconcile(t.Context(), req)
	drained := certificateWorkload(t, r, "controlplane")
	if !deploymentReplicasAreZero(drained) {
		t.Fatalf("gateway was not drained: %v", drainErr)
	}
	if err := r.Get(t.Context(), req.NamespacedName, y); err != nil {
		t.Fatal(err)
	}
	y.Spec.Enabled = helpers.PtrTrue()
	y.Generation++
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	_, enableErr := r.Reconcile(t.Context(), req)
	after := certificateWorkload(t, r, "controlplane")
	if !reflect.DeepEqual(before.Spec.Template, drained.Spec.Template) ||
		(!deploymentReplicasAreZero(after) && len(manifests.OnlinePolicyTargets(after)) == 0) {
		t.Fatalf("drain stripped the gateway policy (targets after drain=%d, re-enabled replicas=%d targets=%d, enable error=%v)",
			len(manifests.OnlinePolicyTargets(drained)), *after.Spec.Replicas, len(manifests.OnlinePolicyTargets(after)), enableErr)
	}
}

func TestOnlinePolicyUnavailableSSHDoesNotBlockNodeRelease(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run(fmt.Sprintf("online=%t", online), func(t *testing.T) {
			r, y, node := certificateEnvironment(t)
			node.Labels = map[string]string{"selected": "true"}
			if err := r.Update(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			y.Spec.NodeSelector = map[string]string{"selected": "true"}
			y.Generation++
			if err := r.Update(t.Context(), y); err != nil {
				t.Fatal(err)
			}
			sshTrust := createReviewSSHTrust(t, r, y)
			certs := r.GlobalConfig.Config.Certificates
			certs.OnlineGatewayPolicy = online
			if online {
				publishCertificateConfig(t, r)
				convergeOnlinePolicy(t, r, y)
			} else {
				startCertificateWorkloads(t, r, y)
			}
			gw := certificateWorkload(t, r, "controlplane")
			if err := r.Delete(t.Context(), sshTrust); err != nil {
				t.Fatal(err)
			}
			node.Labels = nil
			if err := r.Update(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			_, reconcileErr := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			got := &appsv1.Deployment{}
			err := r.Get(t.Context(), client.ObjectKeyFromObject(gw), got)
			if !apierrors.IsNotFound(err) && (err != nil || got.DeletionTimestamp.IsZero()) {
				t.Fatalf("no foreground cleanup of released gateway (reconcile=%v, get=%v)", reconcileErr, err)
			}
		})
	}
}

func createReviewSSHTrust(t *testing.T, r *YanetReconciler, y *api.Yanet) *corev1.ConfigMap {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "ssh-trust", Namespace: y.Namespace},
		Data:       map[string]string{"ssh-ca.pub": string(ssh.MarshalAuthorizedKey(pub))},
	}
	if err := r.Create(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Key: "ssh-ca.pub",
	}
	return cm
}

func TestOnlinePolicyDeliveryNameReservedForSidecars(t *testing.T) {
	r, _, _ := certificateEnvironment(t)
	cfg := &r.GlobalConfig.Config
	cfg.Patches = nil
	cfg.Certificates.OnlineGatewayPolicy = true
	cfg.Components.Dataplane.Sidecars = []api.SidecarSpec{{Name: "policy-delivery", Image: api.ImageRef{Name: "demo", Tag: "v1"}}}
	cfg.BoxTypes[0].Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{"policy-delivery": {}}
	validator := &api.YanetConfigCustomValidator{}
	_, err := validator.ValidateCreate(t.Context(), &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: *cfg})
	if err == nil || !strings.Contains(err.Error(), "policy-delivery") || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved name rejection: %v", err)
	}
}

func TestOnlinePolicyCredentialMountCannotShadowPolicy(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	certs := r.GlobalConfig.Config.Certificates
	certs.OnlineGatewayPolicy = true
	certs.Profiles = append(certs.Profiles, api.CertificateProfile{
		Name: "client", Component: "controlplane", Container: "controlplane",
		MountPath: "/var/lib/yanet2/gateway-policy/current", Usage: "client",
	})
	for i := 0; i < 2; i++ {
		publishCertificateConfig(t, r)
		if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil ||
			!strings.Contains(err.Error(), "policy mount") {
			t.Errorf("profile order %q did not consistently reject policy mount overlap: %v", certs.Profiles[0].Name, err)
		}
		slices.Reverse(certs.Profiles)
	}
}

func TestOnlinePolicyExclusivityUsesLiveNodeLabels(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run(fmt.Sprintf("online=%t", online), func(t *testing.T) {
			r, y, node := certificateEnvironment(t)
			r.GlobalConfig.Config.Certificates.OnlineGatewayPolicy = online
			publishCertificateConfig(t, r)
			y.Spec.NodeSelector = map[string]string{"rack": "selected"}
			y.Generation++
			if err := r.Update(t.Context(), y); err != nil {
				t.Fatal(err)
			}
			// With no incumbent producer, equal timestamps are ordered by name.
			older := &api.Yanet{ObjectMeta: metav1.ObjectMeta{
				Name: "aaa", Namespace: y.Namespace, UID: "other-uid", Generation: 1,
			}, Spec: api.YanetSpec{
				BoxType: y.Spec.BoxType, AutoSync: helpers.PtrTrue(), NodeSelector: map[string]string{"zone": "selected"},
			}}
			if err := r.Create(t.Context(), older); err != nil {
				t.Fatal(err)
			}
			node.Labels = map[string]string{"rack": "selected", "zone": "selected"}
			if err := r.Update(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			stale := node.DeepCopy()
			delete(stale.Labels, "zone")
			base := r.Client.(client.WithWatch)
			r.APIReader = base
			r.Client = interceptor.NewClient(base, interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if nodes, ok := list.(*corev1.NodeList); ok {
						nodes.Items = []corev1.Node{*stale.DeepCopy()}
						return nil
					}
					return c.List(ctx, list, opts...)
				},
			})
			_, reconcileErr := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			deps := &appsv1.DeploymentList{}
			if err := base.List(t.Context(), deps); err != nil {
				t.Fatal(err)
			}
			for i := range deps.Items {
				d := &deps.Items[i]
				if controlledByYanet(d, y) {
					t.Fatalf("stale labels let a losing installation acquire the live Node: deployment=%s err=%v", d.Name, reconcileErr)
				}
			}
		})
	}
}

func TestOnlinePolicyServerToggleKeepsExistingGatewaySpec(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	cfg := &r.GlobalConfig.Config
	cfg.Certificates.OnlineGatewayPolicy = true
	cfg.Components.Operators = []api.OperatorSpec{{Name: "peer", Containers: []api.OperatorContainer{
		{Name: "peer", Image: api.ImageRef{Name: "peer", Tag: "v1"}},
	}}}
	cfg.BoxTypes[0].Operators = map[string]api.BoxOperator{"peer": {}}
	cfg.Certificates.Profiles = append(cfg.Certificates.Profiles,
		api.CertificateProfile{Name: "peer-server", Component: "peer", Container: "peer",
			MountPath: "/etc/yanet2/pki/peer", Usage: "server", DNSNames: []string{"{node}"}},
		api.CertificateProfile{Name: "peer-client", Component: "peer", Container: "peer",
			MountPath: "/etc/yanet2/pki/peer-client", Usage: "client", GatewayPermissions: []string{"/example.v1.Service/Read"}},
	)
	publishCertificateConfig(t, r)
	convergeOnlinePolicy(t, r, y)
	before := certificateWorkload(t, r, "controlplane")
	writer := certificateWorkload(t, r, manifests.PolicyDeliveryComponent)
	beforeMap, _ := readPolicyConfigMap(t, r, y, node)
	for _, enabled := range []bool{false, true} {
		y.Spec.Components = &api.YanetComponentsOverride{Operators: map[string]api.YanetComponentOverride{
			"peer": {Enabled: &enabled},
		}}
		y.Generation++
		if err := r.Update(t.Context(), y); err != nil {
			t.Fatal(err)
		}
		reconcileCertificates(t, r, y)
		markCertificatesIssued(t, r)
		reconcileCertificates(t, r, y)
		readyCertificateDeployments(t, r)
		reconcileCertificates(t, r, y)
		peer := certificateWorkload(t, r, "peer")
		if deploymentReplicasAreZero(peer) == enabled {
			t.Fatalf("peer did not reach enabled=%t", enabled)
		}
		got := certificateWorkload(t, r, "controlplane")
		gotWriter := certificateWorkload(t, r, manifests.PolicyDeliveryComponent)
		cm, _ := readPolicyConfigMap(t, r, y, node)
		if !reflect.DeepEqual(got.Spec, before.Spec) || !reflect.DeepEqual(gotWriter.Spec, writer.Spec) ||
			!reflect.DeepEqual(cm.Data, beforeMap.Data) {
			t.Fatalf("toggle of separate dual-role component changed existing controlplane or policy")
		}
		if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOnlinePolicyPauseKeepsStateAndProjection(t *testing.T) {
	for _, mode := range []string{"stop", "report-only", "disabled", "stale-config"} {
		t.Run(mode, func(t *testing.T) {
			r, y, node := certificateEnvironment(t)
			cfg := &r.GlobalConfig.Config
			cfg.Certificates.OnlineGatewayPolicy = true
			publishCertificateConfig(t, r)
			state := convergeOnlinePolicy(t, r, y)
			projection, _ := readPolicyConfigMap(t, r, y, node)
			writer := certificateWorkload(t, r, manifests.PolicyDeliveryComponent)
			cfg.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{
				{Username: "demo", Permissions: []string{"/example.v1.Service/Read"}},
			}
			switch mode {
			case "stop":
				cfg.Stop = true
			case "report-only":
				y.Spec.AutoSync = helpers.PtrFalse()
			case "disabled":
				y.Spec.Enabled = helpers.PtrFalse()
			}
			if mode == "report-only" || mode == "disabled" {
				y.Generation++
				if err := r.Update(t.Context(), y); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "stale-config" {
				publishCertificateConfig(t, r)
			}
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			if mode == "stale-config" {
				if err == nil || !strings.Contains(err.Error(), "online policy source changed") {
					t.Fatalf("expected stale source rejection: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			latest := readOnlineState(t, r, y)
			latestProjection, _ := readPolicyConfigMap(t, r, y, node)
			latestWriter := certificateWorkload(t, r, manifests.PolicyDeliveryComponent)
			if latest != state || latestProjection.ResourceVersion != projection.ResourceVersion {
				t.Fatalf("policy state or projection changed while %s", mode)
			}
			if mode == "disabled" {
				if !deploymentReplicasAreZero(latestWriter) ||
					!reflect.DeepEqual(latestWriter.Spec.Template, writer.Spec.Template) {
					t.Fatal("expected replica-only drain for the writer")
				}
			} else if latestWriter.ResourceVersion != writer.ResourceVersion {
				t.Fatalf("writer changed while %s", mode)
			}
		})
	}
}

func TestOnlinePolicyProjectsExactSSHBinaryDataAndRemoval(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	source := createReviewSSHTrust(t, r, y)
	base := bytes.TrimSpace([]byte(source.Data["ssh-ca.pub"]))
	expected := append(bytes.Clone(base), ' ', 0xff, '\r', '\n')
	source.Data = nil
	source.BinaryData = map[string][]byte{"ssh-ca.pub": expected}
	if err := r.Update(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	cfg := &r.GlobalConfig.Config
	cfg.Certificates.OnlineGatewayPolicy = true
	publishCertificateConfig(t, r)
	convergeOnlinePolicy(t, r, y)
	_, initial := readPolicyConfigMap(t, r, y, node)
	if !bytes.Equal(initial.SSHTrust, expected) {
		t.Fatal("the projection changed the CA's bytes")
	}
	rotated := append(bytes.Clone(base), ' ', 0xfe, '\r', '\n')
	source.BinaryData["ssh-ca.pub"] = rotated
	if err := r.Update(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	_, updated := readPolicyConfigMap(t, r, y, node)
	if updated.Sequence != initial.Sequence+1 || !bytes.Equal(updated.SSHTrust, rotated) {
		t.Fatal("exact bytes did not contribute to a fresh revision")
	}
	cfg.Certificates.SSHTrustBundle = nil
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	_, removed := readPolicyConfigMap(t, r, y, node)
	if removed.Sequence != updated.Sequence+1 || len(removed.SSHTrust) != 0 {
		t.Fatal("removing SSH trust did not replace the projection")
	}
}
