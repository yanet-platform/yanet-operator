package controller

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func convergeOnlinePolicy(t *testing.T, r *YanetReconciler, y *api.Yanet) {
	t.Helper()
	reconcileCertificates(t, r, y)
	markCertificatesIssued(t, r)
	reconcileCertificates(t, r, y)
	readyCertificateDeployments(t, r)
	reconcileCertificates(t, r, y)
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
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
	convergeOnlinePolicy(t, r, y)
	onBothNodes := []*corev1.Node{node, other}
	for _, n := range onBothNodes {
		_, bundle := readPolicyConfigMap(t, r, y, n)
		want := api.CertificateURI(cfg.Certificates.TrustDomain, y.Namespace, string(y.UID), string(n.UID), "dual-client")
		if len(bundle.Accounts) != 1 || bundle.Accounts[0].Username != want {
			t.Fatalf("Node %s lost the wired disabled client's grants: %+v", n.Name, bundle)
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
			variables := d.Spec.Template.Spec.Containers[0].Env
			for _, expected := range []corev1.EnvVar{
				{Name: "YANET_GATEWAY_AUTH_MODE", Value: "enabled"},
				{Name: "YANET_GATEWAY_AUTH_IDENTITY_PROVIDERS_0_TYPE", Value: "file"},
				{Name: "YANET_GATEWAY_AUTH_IDENTITY_PROVIDERS_0_PATH", Value: "/var/lib/yanet2/gateway-policy/identities.yaml"},
				{Name: "YANET_GATEWAY_AUTH_IDENTITY_PROVIDERS_0_REFRESH_INTERVAL", Value: "5s"},
				{Name: "YANET_GATEWAY_AUTH_PERMISSIONS_PATH", Value: "/var/lib/yanet2/gateway-policy/permissions.yaml"},
				{Name: "YANET_GATEWAY_AUTH_PERMISSIONS_REFRESH_INTERVAL", Value: "5s"},
			} {
				if !slices.Contains(variables, expected) {
					t.Fatalf("%s does not configure upstream file polling: missing %+v", d.Name, expected)
				}
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
	projected := map[string]map[string]string{}
	for _, n := range onBothNodes {
		cm, bundle := readPolicyConfigMap(t, r, y, n)
		projected[n.Name] = cm.Data
		if len(bundle.Accounts) != 2 || bundle.Accounts[0].Username != "alice" {
			t.Fatalf("Node %s did not receive the grant: %+v", n.Name, bundle)
		}
	}

	// Changing the running server inventory preserves policy data. Leave both
	// of the new role's certificates unissued.
	y.Spec.Components = &api.YanetComponentsOverride{Operators: map[string]api.YanetComponentOverride{
		"dual": {Enabled: helpers.PtrTrue()},
	}}
	y.Generation++
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	for _, n := range onBothNodes {
		cm, _ := readPolicyConfigMap(t, r, y, n)
		if !reflect.DeepEqual(cm.Data, projected[n.Name]) {
			t.Fatalf("pending dual-role server changed authorization on %s", n.Name)
		}
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
		if bundle.Accounts == nil || len(bundle.Accounts) != 0 {
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
