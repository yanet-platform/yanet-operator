package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func readOnlineState(t *testing.T, r *YanetReconciler, y *api.Yanet) onlinePolicyState {
	t.Helper()
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	var state onlinePolicyState
	if err := json.Unmarshal([]byte(y.Annotations[onlinePolicyStateAnnotation]), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func convergeOnlinePolicy(t *testing.T, r *YanetReconciler, y *api.Yanet) onlinePolicyState {
	t.Helper()
	reconcileCertificates(t, r, y)
	markCertificatesIssued(t, r)
	reconcileCertificates(t, r, y)
	readyCertificateDeployments(t, r)
	reconcileCertificates(t, r, y)
	return readOnlineState(t, r, y)
}

func readPolicyConfigMap(t *testing.T, r *YanetReconciler, y *api.Yanet, node *corev1.Node) (*corev1.ConfigMap, credentials.OnlinePolicyBundle) {
	t.Helper()
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{
		Namespace: y.Namespace, Name: manifests.PolicyConfigMapName(string(y.UID), string(node.UID)),
	}
	if err := r.Get(t.Context(), key, cm); err != nil {
		t.Fatal(err)
	}
	if !controlledByYanet(cm, y) {
		t.Fatal("policy projection is not owned by the installation")
	}
	bundle, err := credentials.DecodeOnlinePolicyBundle([]byte(cm.Data[credentials.PolicyBundleKey]))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Epoch != string(y.UID) || bundle.NodeUID != string(node.UID) {
		t.Fatalf("wrong policy scope: %s/%s", bundle.Epoch, bundle.NodeUID)
	}
	return cm, bundle
}

func TestOnlinePolicyUpdatesWithoutDeploymentWritesOrCertificateReadiness(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	other := node.DeepCopy()
	other.ObjectMeta = metav1.ObjectMeta{Name: "other.example.test", UID: "other-node-uid"}
	if err := r.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	cfg := &r.GlobalConfig.Config
	numa := int32(2)
	cfg.Components.Controlplane.Numa = &numa
	cfg.Certificates.OnlineGatewayPolicy = true
	cfg.UpdateWindow = 1800
	cfg.Components.Operators = []api.OperatorSpec{{Name: "dual",
		Containers: []api.OperatorContainer{{Name: "dual", Image: api.ImageRef{Name: "dual", Tag: "v1"}}}}}
	standby := api.NamedPatch{Name: "standby"}
	standby.Patch.Raw = []byte(`{"spec":{"replicas":0}}`)
	cfg.Patches = append(cfg.Patches, standby)
	cfg.BoxTypes[0].Operators = map[string]api.BoxOperator{"dual": {Patches: []string{"standby"}}}
	cfg.Certificates.Profiles = append(cfg.Certificates.Profiles,
		api.CertificateProfile{Name: "dual-server", Component: "dual", Container: "dual", MountPath: "/certs/dual-server",
			Usage: "server", DNSNames: []string{"{node}"}},
		api.CertificateProfile{Name: "dual-client", Component: "dual", Container: "dual", MountPath: "/certs/dual-client",
			Usage: "client", GatewayPermissions: []string{"/example.v1.Service/Read"}},
	)
	publishCertificateConfig(t, r)
	initial := convergeOnlinePolicy(t, r, y)
	onBothNodes := []*corev1.Node{node, other}
	for _, n := range onBothNodes {
		_, bundle := readPolicyConfigMap(t, r, y, n)
		want := api.CertificateURI(cfg.Certificates.TrustDomain, y.Namespace, string(y.UID), string(n.UID), "dual-client")
		if !slices.Equal(bundle.Roles, []string{"dual-server", "gateway"}) ||
			len(bundle.Accounts) != 1 || bundle.Accounts[0].Username != want {
			t.Fatalf("Node %s lost wired disabled client/server inventory: %+v", n.Name, bundle)
		}
	}

	before := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), before); err != nil {
		t.Fatal(err)
	}
	numGateways := 0
	for _, d := range before.Items {
		if d.Labels[manifests.LabelComponent] == "controlplane" {
			numGateways++
			if len(manifests.OnlinePolicyTargets(&d)) != 1 {
				t.Fatalf("%s has no managed runtime reader", d.Name)
			}
		}
	}
	if numGateways != 4 {
		t.Fatalf("expected two NUMA gateways per Node, got %d", numGateways)
	}
	var writes []string
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if d, ok := obj.(*appsv1.Deployment); ok {
				writes = append(writes, "create "+d.Name)
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if d, ok := obj.(*appsv1.Deployment); ok {
				writes = append(writes, "update "+d.Name)
			}
			return cl.Update(ctx, obj, opts...)
		},
	})

	r.lastUpdateTS = time.Now()
	r.lastUpdateHost = "unrelated.example.test"
	cfg.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Write"}}}
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	state := readOnlineState(t, r, y)
	if state.Sequence != initial.Sequence+1 {
		t.Fatalf("grant did not allocate a revision: %+v", state)
	}
	for _, n := range onBothNodes {
		_, bundle := readPolicyConfigMap(t, r, y, n)
		if bundle.Sequence != state.Sequence || len(bundle.Accounts) != 2 || bundle.Accounts[0].Username != "alice" {
			t.Fatalf("Node %s did not receive the grant: %+v", n.Name, bundle)
		}
	}

	// Changing the running server inventory must not allocate a policy
	// revision. Leave both of the new role's certificates unissued.
	y.Spec.Components = &api.YanetComponentsOverride{Operators: map[string]api.YanetComponentOverride{
		"dual": {Enabled: helpers.PtrTrue()},
	}}
	y.Generation++
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	if next := readOnlineState(t, r, y); next.Sequence != state.Sequence {
		t.Fatalf("pending dual-role server changed authorization: %+v", next)
	}
	if count := len(listCertificates(t, r)); count != 6 {
		t.Fatalf("test did not reach unrelated certificate issuance: got %d Certificates", count)
	}

	cfg.Certificates.AdditionalGatewayAccounts = nil
	cfg.Certificates.Profiles[2].GatewayPermissions = nil
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	for _, n := range onBothNodes {
		_, bundle := readPolicyConfigMap(t, r, y, n)
		if bundle.Sequence != state.Sequence+1 || bundle.Accounts == nil || len(bundle.Accounts) != 0 {
			t.Fatalf("Node %s did not receive an explicit empty policy: %+v", n.Name, bundle)
		}
	}
	if len(writes) != 0 {
		t.Fatalf("policy edits and pending issuance wrote Deployment objects: %v", writes)
	}
	for _, old := range before.Items {
		current := &appsv1.Deployment{}
		if err := r.Get(t.Context(), client.ObjectKeyFromObject(&old), current); err != nil {
			t.Fatal(err)
		}
		if current.ResourceVersion != old.ResourceVersion || !reflect.DeepEqual(current.Spec, old.Spec) {
			t.Fatalf("Deployment changed: %s", old.Name)
		}
	}
}

func TestOnlinePolicyRequiresFreshInstallation(t *testing.T) {
	for _, mode := range []string{"unmanaged", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			r, y, node := certificateEnvironment(t)
			cfg := &r.GlobalConfig.Config
			certificates := cfg.Certificates
			if mode == "legacy" {
				startCertificateWorkloads(t, r, y)
			} else {
				cfg.Certificates = nil
				publishCertificateConfig(t, r)
				reconcileCertificates(t, r, y)
			}
			before := certificateWorkload(t, r, "controlplane")

			cfg.Certificates = certificates
			certificates.OnlineGatewayPolicy = true
			publishCertificateConfig(t, r)
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			if err == nil || !strings.Contains(err.Error(), "requires a fresh installation") {
				t.Fatalf("expected existing installation to be rejected, got %v", err)
			}
			after := certificateWorkload(t, r, "controlplane")
			if after.ResourceVersion != before.ResourceVersion || !reflect.DeepEqual(after.Spec, before.Spec) {
				t.Fatal("rejecting online policy changed the existing gateway")
			}
			key := client.ObjectKey{Namespace: y.Namespace, Name: manifests.PolicyConfigMapName(string(y.UID), string(node.UID))}
			if err := r.Get(t.Context(), key, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
				t.Fatalf("expected no online projection, got %v", err)
			}
		})
	}
}
