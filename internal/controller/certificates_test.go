package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func certificateEnvironment(t *testing.T) (*YanetReconciler, *api.Yanet, *corev1.Node) {
	t.Helper()
	yanet := &api.Yanet{ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "default", UID: "installation-uid", Finalizers: []string{yanetFinalizer}}, Spec: api.YanetSpec{BoxType: "release", AutoSync: helpers.PtrTrue()}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker.example.test", UID: "node-uid"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{"hugepages-2Mi": resource.MustParse("1Gi")}}}
	scheme := testScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(yanet, node).WithStatusSubresource(&api.Yanet{}, &appsv1.Deployment{}, newCertificate()).Build()
	cfg := minimalConfig()
	cfg.Certificates = &api.CertificateManagementSpec{IssuerRef: api.CertificateIssuerRef{Name: "issuer", Kind: "Issuer"}, TrustDomain: "example.test", TrustBundle: corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "trust"}, Key: "ca.crt"}, HostPath: "/var/lib/yanet/pki", ExporterImage: "example.test/operator@sha256:" + strings.Repeat("a", 64), Duration: "720h", RenewBefore: "480h", Profiles: []api.CertificateProfile{{Name: "gateway", Component: "controlplane", Container: "controlplane", MountPath: "/etc/yanet2/pki/gateway", Usage: "server", DNSNames: []string{"{node}"}}}}
	return &YanetReconciler{Client: cl, Scheme: scheme, GlobalConfig: &api.MutexYanetConfigSpec{Config: cfg}, ManageCertificates: true}, yanet, node
}

func reconcileCertificates(t *testing.T, r *YanetReconciler, yanet *api.Yanet) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}); err != nil {
		t.Fatal(err)
	}
}

func listCertificates(t *testing.T, r *YanetReconciler) []unstructured.Unstructured {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(manifests.CertificateGVK.GroupVersion().WithKind("CertificateList"))
	if err := r.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestManagedCertificatesWaitForIssuanceAndDelivery(t *testing.T) {
	r, yanet, _ := certificateEnvironment(t)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}
	reconcileCertificates(t, r, yanet)
	certificates := listCertificates(t, r)
	if len(certificates) != 1 {
		t.Fatalf("expected one stable role certificate, got %d", len(certificates))
	}
	certificate := certificates[0].DeepCopy()
	version := certificate.GetResourceVersion()
	reconcileCertificates(t, r, yanet)
	if got := listCertificates(t, r)[0].GetResourceVersion(); got != version {
		t.Fatalf("reconcile rewrote the certificate: %s -> %s", version, got)
	}
	// A typed cert-manager webhook may canonicalize durations and omit false.
	certificate.Object["spec"].(map[string]any)["duration"] = "720h0m0s"
	certificate.Object["spec"].(map[string]any)["renewBefore"] = "480h0m0s"
	delete(certificate.Object["spec"].(map[string]any), "isCA")
	if err := r.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	version = certificate.GetResourceVersion()
	reconcileCertificates(t, r, yanet)
	if got := listCertificates(t, r)[0].GetResourceVersion(); got != version {
		t.Fatal("cert-manager defaulting caused an issuance loop")
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments); err != nil {
		t.Fatal(err)
	}
	var delivery *appsv1.Deployment
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if d.Labels[manifests.LabelComponent] == "controlplane" {
			t.Fatal("consumer started before issuance and delivery")
		}
		if d.Labels[manifests.LabelComponent] == "credential-delivery" {
			delivery = d.DeepCopy()
		}
	}
	if delivery == nil {
		t.Fatal("delivery workload missing")
	}
	// Issuance alone must not unblock the consumer.
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": certificate.GetGeneration()}}}
	if err := r.Status().Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, yanet)
	if err := r.List(context.Background(), deployments, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("Secret readiness unblocked consumers: %+v %v", deployments.Items, err)
	}
	if err := r.Get(context.Background(), request.NamespacedName, yanet); err != nil {
		t.Fatal(err)
	}
	if !apimeta.IsStatusConditionTrue(yanet.Status.Conditions, "CertificatesIssued") || !apimeta.IsStatusConditionFalse(yanet.Status.Conditions, "CredentialsDelivered") {
		t.Fatalf("issued and delivered status were conflated: %+v", yanet.Status.Conditions)
	}
	delivery.Status = appsv1.DeploymentStatus{ObservedGeneration: delivery.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := r.Status().Update(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, yanet)
	if err := r.List(context.Background(), deployments, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil || len(deployments.Items) != 1 {
		t.Fatalf("delivered credentials did not unblock consumer: %+v %v", deployments.Items, err)
	}
	if !manifests.RequiresCertificateDelivery(&deployments.Items[0]) {
		t.Fatal("consumer has no persistent credential mount")
	}
	consumerVersion := deployments.Items[0].ResourceVersion
	// A pending renewal keeps the existing working consumer, including its version.
	certificate = listCertificates(t, r)[0].DeepCopy()
	certificate.Object["status"] = map[string]any{}
	if err := r.Status().Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, yanet)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(&deployments.Items[0]), &deployments.Items[0]); err != nil {
		t.Fatal(err)
	}
	if deployments.Items[0].ResourceVersion != consumerVersion {
		t.Fatal("pending issuance restarted a working consumer")
	}
	// Explicit drain cannot be held hostage by the unavailable issuer/exporter.
	if err := r.Get(context.Background(), request.NamespacedName, yanet); err != nil {
		t.Fatal(err)
	}
	yanet.Spec.Enabled = helpers.PtrFalse()
	if err := r.Update(context.Background(), yanet); err != nil {
		t.Fatal(err)
	}
	certificate = listCertificates(t, r)[0].DeepCopy()
	certificate.SetOwnerReferences(nil)
	if err := r.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request); err == nil {
		t.Fatal("foreign certificate must still be reported during drain")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(&deployments.Items[0]), &deployments.Items[0]); err != nil {
		t.Fatal(err)
	}
	if *deployments.Items[0].Spec.Replicas != 0 {
		t.Fatal("credential wait blocked explicit drain")
	}
}

func TestManagedCertificatesReportOnlyStopAndOwnership(t *testing.T) {
	for _, mode := range []string{"report-only", "stop", "feature-disabled"} {
		t.Run(mode, func(t *testing.T) {
			r, y, _ := certificateEnvironment(t)
			switch mode {
			case "report-only":
				y.Spec.AutoSync = helpers.PtrFalse()
				if err := r.Update(context.Background(), y); err != nil {
					t.Fatal(err)
				}
			case "stop":
				r.GlobalConfig.Config.Stop = true
			case "feature-disabled":
				r.ManageCertificates = false
			}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			if mode == "feature-disabled" && err == nil {
				t.Fatal("missing manager opt-in was silently accepted")
			}
			if mode != "feature-disabled" && err != nil {
				t.Fatal(err)
			}
			if len(listCertificates(t, r)) != 0 {
				t.Fatal("read-only reconcile issued credentials")
			}
		})
	}
	r, y, _ := certificateEnvironment(t)
	reconcileCertificates(t, r, y)
	certificate := listCertificates(t, r)[0].DeepCopy()
	certificate.SetOwnerReferences(nil)
	if err := r.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments, client.MatchingLabels{manifests.LabelComponent: "credential-delivery"}); err != nil {
		t.Fatal(err)
	}
	for i := range deployments.Items {
		if err := r.Delete(context.Background(), &deployments.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil {
		t.Fatal("foreign certificate was adopted")
	}
	if err := r.List(context.Background(), deployments, client.MatchingLabels{manifests.LabelComponent: "credential-delivery"}); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("foreign Secret was mounted: %v %v", deployments.Items, err)
	}
}

func TestManagedCertificatesNodeReleaseCleansIssuance(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	reconcileCertificates(t, r, y)
	certificate := listCertificates(t, r)[0].DeepCopy()
	if err := r.Delete(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(certificate), newCertificate()); !apierrors.IsNotFound(err) {
		t.Fatalf("released node kept issuing new credentials: %v", err)
	}
}
