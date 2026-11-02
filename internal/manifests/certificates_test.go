package manifests

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func certificateBuildContext() BuildContext {
	return BuildContext{YanetName: "edge", Namespace: "default", BoxType: "firewall", NodeName: "worker.example.test", NodeUID: "node-uid", OwnerRef: metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "Yanet", Name: "edge", UID: "installation-uid", Controller: helpers.PtrTrue()}, Certificates: &api.CertificateManagementSpec{
		IssuerRef: api.CertificateIssuerRef{Name: "issuer", Kind: "Issuer"}, TrustDomain: "example.test", TrustBundle: corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "trust"}, Key: "ca.crt"},
		HostPath: "/var/lib/yanet/credentials", ExporterImage: "example.test/operator@sha256:" + strings.Repeat("a", 64), Duration: "87600h", RenewBefore: "480h",
		Profiles: []api.CertificateProfile{{Name: "gateway", Component: "controlplane", Container: "controlplane", MountPath: "/etc/yanet2/pki/gateway", Usage: "server", DNSNames: []string{"{node}", "yanet-{boxType}-controlplane-numa0.{namespace}.svc.cluster.local"}}},
	}}
}

func TestCertificatePlanIsNodeBoundAndSharedAcrossNUMA(t *testing.T) {
	build := certificateBuildContext()
	component := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true, Numa: 2}
	workloads, err := RenderDeployments(build, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildCertificatePlan(build, workloads, true)
	if len(workloads) != 2 || len(plan.Certificates) != 1 || plan.Delivery == nil {
		t.Fatalf("NUMA must share one role identity: %+v", plan)
	}
	for _, workload := range workloads {
		found := false
		for _, volume := range workload.Spec.Template.Spec.Volumes {
			if volume.Secret != nil || volume.ConfigMap != nil {
				t.Fatalf("runtime recovery depends on API volume: %+v", volume)
			}
			if volume.Name == "yanet-credential-gateway" {
				found = volume.HostPath != nil && volume.HostPath.Path == "/var/lib/yanet/credentials/default/edge/installation-uid/node-uid/gateway"
			}
		}
		if !found {
			t.Fatal("persistent node/installation/role mount missing")
		}
		for _, mount := range workload.Spec.Template.Spec.Containers[0].VolumeMounts {
			if mount.Name == "yanet-credential-gateway" && !mount.ReadOnly {
				t.Fatal("consumer can write credentials")
			}
		}
	}
	certificate := plan.Certificates[0]
	uris, _, err := unstructured.NestedStringSlice(certificate.Object, "spec", "uris")
	if err != nil || len(uris) != 1 || uris[0] != "spiffe://example.test/ns/default/installation/installation-uid/node/node-uid/role/gateway" {
		t.Fatalf("URI: %v %v", uris, err)
	}
	if certificate.Object["spec"].(map[string]any)["commonName"] != nil {
		t.Fatal("CN would override the URI account")
	}
	if *plan.Delivery.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("exporter must not get API credentials")
	}
	if got := plan.Delivery.Spec.Template.Annotations["checkpointer.ydb.tech/checkpoint"]; got == "true" {
		t.Fatal("delivery must not be selected for static recovery")
	}
	if len(plan.Delivery.Spec.Template.Spec.Containers) != 1 || plan.Delivery.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec == nil {
		t.Fatal("delivery must validate persisted credentials before readiness")
	}
	repeated := BuildCertificatePlan(build, workloads, true)
	if repeated.Certificates[0].GetName() != certificate.GetName() {
		t.Fatal("reconcile changed certificate identity")
	}
	build.NodeUID = "replacement-node"
	newWorkloads, err := RenderDeployments(build, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := BuildCertificatePlan(build, newWorkloads, true)
	if replacement.Certificates[0].GetName() == certificate.GetName() {
		t.Fatal("replacement node reused old credentials")
	}
}

func TestCertificateMountConflictsFailRendering(t *testing.T) {
	build := certificateBuildContext()
	component := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true, Patches: []string{"conflict"}}
	patch := api.NamedPatch{Name: "conflict"}
	patch.Patch.Raw = []byte(`{"spec":{"template":{"spec":{"volumes":[{"name":"yanet-credential-gateway","emptyDir":{}}]}}}}`)
	if _, err := RenderDeployments(build, component, NewPatchRegistry([]api.NamedPatch{patch})); err == nil {
		t.Fatal("patch replaced the managed credential volume")
	}
	// Without opt-in, no cert-manager resource or delivery workload is produced.
	build.Certificates = nil
	component.Patches = nil
	workloads, err := RenderDeployments(build, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildCertificatePlan(build, workloads, true)
	if len(plan.Certificates) != 0 || plan.Delivery != nil || RequiresCertificateDelivery(workloads[0]) {
		t.Fatal("default workload acquired credential dependencies")
	}
}

func TestCertificateRoleIsolation(t *testing.T) {
	build := certificateBuildContext()
	build.Certificates.Profiles = append(build.Certificates.Profiles, api.CertificateProfile{Name: "acl", Component: "acl", Container: "acl", MountPath: "/etc/yanet2/pki/acl", Usage: "client"})
	cp, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "acl", Enabled: true, Containers: []helpers.ResolvedContainer{{Name: "acl"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		workloads []*appsv1.Deployment
		forbidden string
	}{{cp, "yanet-credential-acl"}, {acl, "yanet-credential-gateway"}} {
		for _, volume := range test.workloads[0].Spec.Template.Spec.Volumes {
			if volume.Name == test.forbidden {
				t.Fatal("consumer gained another role's identity")
			}
		}
	}
	plan := BuildCertificatePlan(build, append(cp, acl...), true)
	if len(plan.Certificates) != 2 || len(plan.Delivery.Spec.Template.Spec.Containers) != 2 {
		t.Fatal("effective roles did not get separate delivery/key pairs")
	}
}

func TestCertificateGatewayAccountsFollowEffectiveNodeRoles(t *testing.T) {
	build := certificateBuildContext()
	build.Certificates.Profiles = append(build.Certificates.Profiles,
		api.CertificateProfile{Name: "acl", Component: "acl", Container: "acl", MountPath: "/etc/yanet2/pki/acl", Usage: "client", GatewayPermissions: []string{"/example.v1.Service/Update"}},
		api.CertificateProfile{Name: "unused", Component: "unused", Container: "unused", MountPath: "/etc/yanet2/pki/unused", Usage: "client", GatewayPermissions: []string{"/example.v1.Service/*"}},
	)
	cp, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "acl", Enabled: true, Containers: []helpers.ResolvedContainer{{Name: "acl"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildCertificatePlan(build, append(cp, acl...), true)
	if plan.Delivery.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("credential publishers can overlap during rollout")
	}
	var accounts []map[string]any
	for _, arg := range plan.Delivery.Spec.Template.Spec.Containers[0].Args {
		if value, ok := strings.CutPrefix(arg, "--gateway-account="); ok {
			var account map[string]any
			if err := json.Unmarshal([]byte(value), &account); err != nil {
				t.Fatal(err)
			}
			accounts = append(accounts, account)
		}
	}
	if len(accounts) != 1 || accounts[0]["username"] != "spiffe://example.test/ns/default/installation/installation-uid/node/node-uid/role/acl" || accounts[0]["permissions"].([]any)[0] != "/example.v1.Service/Update" {
		t.Fatalf("wrong effective node policy: %+v", accounts)
	}
	for _, arg := range plan.Delivery.Spec.Template.Spec.Containers[1].Args {
		if strings.HasPrefix(arg, "--gateway-account=") {
			t.Fatal("client role received another role's account policy")
		}
	}
	initialHash := cp[0].Spec.Template.Annotations["yanet.yanet-platform.io/accounts-gateway"]
	if initialHash == "" || len(cp[0].Spec.Template.Spec.InitContainers) != 1 {
		t.Fatal("gateway lacks a policy-bound startup gate")
	}
	gate := cp[0].Spec.Template.Spec.InitContainers[0]
	if gate.Image != build.Certificates.ExporterImage || len(gate.VolumeMounts) != 1 || gate.VolumeMounts[0].Name != "yanet-credential-gateway" || !gate.VolumeMounts[0].ReadOnly {
		t.Fatalf("gateway gate escaped its read-only role: %+v", gate)
	}
	if len(acl[0].Spec.Template.Spec.InitContainers) != 0 {
		t.Fatal("client unnecessarily acquired gateway account initialization")
	}
	build.Certificates.Profiles[1].GatewayPermissions = []string{"/example.v1.Service/Read"}
	cp2, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	BuildCertificatePlan(build, append(cp2, acl...), true)
	if cp2[0].Spec.Template.Annotations["yanet.yanet-platform.io/accounts-gateway"] == initialHash {
		t.Fatal("gateway will not restart after an account policy update")
	}
	build.Certificates.Profiles[1].GatewayPermissions = nil
	cp3, err := RenderDeployments(build, &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := BuildCertificatePlan(build, append(cp3, acl...), true)
	if len(cp3[0].Spec.Template.Spec.InitContainers) != 1 || cp3[0].Spec.Template.Annotations["yanet.yanet-platform.io/accounts-gateway"] == cp2[0].Spec.Template.Annotations["yanet.yanet-platform.io/accounts-gateway"] {
		t.Fatal("removing the last granted role lost the policy gate or rollout")
	}
	policyEnabled := false
	for _, arg := range empty.Delivery.Spec.Template.Spec.Containers[0].Args {
		if strings.HasPrefix(arg, "--gateway-account=") {
			t.Fatalf("removed role retained account %s", arg)
		}
		if arg == "--gateway-policy" {
			policyEnabled = true
		}
	}
	if !policyEnabled {
		t.Fatal("last account revocation must deliver an explicit empty policy")
	}
}
