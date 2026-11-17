package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestDeploymentClaimWithDuplicateHostnames(t *testing.T) {
	for _, mode := range []string{"exact", "legacy", "legacy-with-old-owner-uid"} {
		t.Run(mode, func(t *testing.T) {
			testContext := t.Context()
			left, right := reviewYanet(), reviewYanet()
			left.Namespace, right.Namespace, right.UID = "left", "right", "right-owner"
			a, b := reviewNode(), reviewNode()
			a.Name, b.Name = "node-a", "node-b"
			a.Labels[corev1.LabelHostname], b.Labels[corev1.LabelHostname] = a.Name, a.Name
			left.Spec.NodeSelector = map[string]string{"pool": "left"}
			right.Spec.NodeSelector = map[string]string{"pool": "right"}
			a.Labels["pool"], b.Labels["pool"] = "left", "right"
			r, snapshot := makeReconcilerEnv(t, left, right, a, b)
			snapshot.Config = minimalConfig()
			snapshot.Config.Patches = []api.NamedPatch{{Name: "hostname", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":"node-a"}}}}}`)}}}
			snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"hostname"}
			if _, err := reviewReconcile(testContext, r, left); err != nil {
				t.Fatal(err)
			}
			old := certificateWorkload(t, r, "dataplane")
			pod := &old.Spec.Template.Spec
			if pod.NodeSelector[corev1.LabelHostname] != a.Name || !manifests.HasExactNodePin(pod, a.Name) {
				t.Fatal("rendered Deployment lost the user hostname selector or exact node pin")
			}
			if mode != "exact" {
				pod.Affinity = nil // A persisted Deployment from before exact-name pinning.
				if mode == "legacy-with-old-owner-uid" {
					old.OwnerReferences[0].UID = "old-left-owner"
				}
				if err := r.Update(testContext, old); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reviewReconcile(testContext, r, right); err != nil {
				t.Fatal(err)
			}
			dep := &appsv1.DeploymentList{}
			if err := r.List(testContext, dep, client.InNamespace(right.Namespace), client.MatchingLabels{manifests.LabelComponent: "dataplane"}); err != nil {
				t.Fatal(err)
			}
			if mode == "legacy" {
				if len(dep.Items) != 0 {
					t.Fatal("legacy hostname overlap did not block acquisition")
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(right), right); err != nil {
					t.Fatal(err)
				}
				if c := apimeta.FindStatusCondition(right.Status.Conditions, "Degraded"); c == nil || c.Reason != "NodeSelectionConflict" {
					t.Fatalf("expected node selection conflict, got %+v", right.Status.Conditions)
				}
			} else if len(dep.Items) != 1 || !manifests.HasExactNodePin(&dep.Items[0].Spec.Template.Spec, b.Name) {
				t.Fatal("independent Node was not acquired with its own exact pin")
			}
			version := old.ResourceVersion
			if err := r.Get(testContext, client.ObjectKeyFromObject(old), old); err != nil || old.ResourceVersion != version {
				t.Fatalf("cross-namespace reconcile touched incumbent Deployment: %v", err)
			}
		})
	}
}

func TestCertificateRoleFailureDoesNotStopOtherIssuance(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	cfg := r.GlobalConfig.Config.Certificates
	second := cfg.Profiles[0]
	second.Name, second.MountPath = "client", "/etc/yanet2/pki/client"
	cfg.Profiles = append(cfg.Profiles, second)
	publishCertificateConfig(t, r)
	failure := errors.New("issuer temporarily unavailable for first role")
	failed := false
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == "Certificate" && !failed {
				failed = true
				return failure
			}
			return cl.Create(ctx, obj, opts...)
		},
	})
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); !errors.Is(err, failure) {
		t.Fatalf("expected issuance error, got %v", err)
	}
	if got := len(listCertificates(t, r)); got != 1 {
		t.Fatalf("independent Certificates created = %d, want 1", got)
	}
	deps := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), deps); err != nil {
		t.Fatal(err)
	}
	for _, d := range deps.Items {
		if manifests.RequiresCertificateDelivery(&d) || d.Labels[manifests.LabelComponent] == "credential-delivery" {
			t.Fatalf("failed issuance unblocked %s", d.Name)
		}
	}
}

func TestMissingBoxExplicitDrain(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	foreign := certificateWorkload(t, r, "dataplane")
	foreign.Name += "-foreign"
	foreign.ResourceVersion, foreign.UID = "", ""
	foreign.OwnerReferences[0].UID = "previous-installation"
	if err := r.Create(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	r.GlobalConfig.Config.BoxTypes[0].Name = "replacement"
	publishCertificateConfig(t, r)
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	old := y.DeepCopy()
	y.Spec.Enabled = helpers.PtrFalse()
	v := &api.YanetCustomValidator{Client: r.Client}
	if _, err := v.ValidateUpdate(t.Context(), old, y); err != nil {
		t.Errorf("admission denied a drain: %v", err)
	}
	changed := y.DeepCopy()
	changed.Spec.NodeSelector = map[string]string{"pool": "different"}
	if _, err := v.ValidateUpdate(t.Context(), old, changed); err == nil {
		t.Error("missing-box exception admitted a selector change")
	}
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	deps := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), deps); err != nil {
		t.Fatal(err)
	}
	if len(deps.Items) != 4 {
		t.Fatalf("drain changed Deployment inventory: %d", len(deps.Items))
	}
	for _, d := range deps.Items {
		if d.Name == foreign.Name {
			if d.ResourceVersion != foreign.ResourceVersion {
				t.Error("foreign Deployment was modified")
			}
			continue
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			t.Errorf("%s was not drained", d.Name)
		}
	}
}

func TestDeploymentNameCollisionAbortsAllNodeApply(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	cfg := &r.GlobalConfig.Config
	listeners := []api.OperatorListener{}
	cfg.Components.Operators = append(cfg.Components.Operators, api.OperatorSpec{Name: "controlplane-numa0", Listeners: &listeners, Containers: []api.OperatorContainer{{Name: "operator", Image: api.ImageRef{Name: "operator", Tag: "v1"}}}})
	cfg.BoxTypes[0].Operators = map[string]api.BoxOperator{"controlplane-numa0": {}}
	publishCertificateConfig(t, r)
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
	if err == nil || !strings.Contains(err.Error(), "duplicate Deployment") {
		t.Errorf("expected duplicate Deployment error, got %v", err)
	}
	deps := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), deps); err != nil {
		t.Fatal(err)
	}
	if len(deps.Items) != 0 || len(listCertificates(t, r)) != 0 {
		t.Fatal("wrote managed resources before detecting a duplicate Deployment")
	}
}

func TestDeploymentClaimSurvivesMissingMetadataLabel(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{true: "legacy", false: "affinity"}[legacy], func(t *testing.T) {
			testContext := t.Context()
			r, y, node := certificateEnvironment(t)
			node.Labels = map[string]string{"pool": "new", corev1.LabelHostname: node.Name}
			if err := r.Update(testContext, node); err != nil {
				t.Fatal(err)
			}
			incumbent := &api.Yanet{ObjectMeta: metav1.ObjectMeta{Name: "incumbent", Namespace: "other", UID: "incumbent-uid"}, Spec: api.YanetSpec{NodeSelector: map[string]string{"pool": "old"}}}
			if err := r.Create(testContext, incumbent); err != nil {
				t.Fatal(err)
			}
			d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "incumbent", Namespace: incumbent.Namespace, Finalizers: []string{"test.example/foreground"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(incumbent, api.GroupVersion.WithKind("Yanet"))}}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{manifests.LabelNode: node.Name}}}}
			pod := &d.Spec.Template.Spec
			if legacy {
				pod.NodeSelector = map[string]string{corev1.LabelHostname: node.Name}
			} else {
				pod.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node.Name}}}}}}}}
			}
			if err := r.Create(testContext, d); err != nil {
				t.Fatal(err)
			}
			if err := r.Delete(testContext, d, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
				t.Fatal(err)
			}
			reconcileCertificates(t, r, y)
			deps := &appsv1.DeploymentList{}
			if err := r.List(testContext, deps, client.InNamespace(y.Namespace)); err != nil {
				t.Fatal(err)
			}
			if len(deps.Items) != 0 {
				t.Fatal("replacement started before foreground deletion completed")
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			d.Finalizers = nil
			if err := r.Update(testContext, d); err != nil {
				t.Fatal(err)
			}
			reconcileCertificates(t, r, y)
			_ = certificateWorkload(t, r, "dataplane")
		})
	}
}

func TestMissingSSHBundleAllowsIndependentReconcileAndRelease(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	trust := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-trust", Namespace: y.Namespace}}
	if err := r.Create(t.Context(), trust); err != nil {
		t.Fatal(err)
	}
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: trust.Name}, Key: "ssh-ca.pub"}
	publishCertificateConfig(t, r)
	node.Labels = map[string]string{"pool": "current"}
	if err := r.Update(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	y.Spec.NodeSelector = node.Labels
	if err := r.Update(t.Context(), y); err != nil {
		t.Fatal(err)
	}
	stale := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "released-workload", Namespace: y.Namespace,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(y, api.GroupVersion.WithKind("Yanet"))}},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{manifests.LabelNode: "released.example.test"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{corev1.LabelHostname: "released.example.test"}}}}}
	if err := r.Create(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	var unavailable *sshPolicyUnavailableError
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); !errors.As(err, &unavailable) {
		t.Fatalf("expected SSH source error, got %v", err)
	}
	_ = certificateWorkload(t, r, "dataplane")
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(stale), stale); !apierrors.IsNotFound(err) {
		t.Fatalf("former Node was not released: %v", err)
	}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	if !apimeta.IsStatusConditionPresentAndEqual(y.Status.Conditions, "CertificatesIssued", metav1.ConditionUnknown) {
		t.Fatalf("unevaluated issuance status: %+v", y.Status.Conditions)
	}
}

func TestMissingSSHBundleDoesNotPermitStalePaletteWrites(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ssh-trust"}, Key: "ssh-ca.pub"}
	// The informer snapshot still advertises this box, but the authoritative
	// palette no longer does. The unrelated dataplane must not be created.
	cfg := &api.YanetConfig{}
	if err := r.Get(t.Context(), client.ObjectKey{Name: api.YanetConfigName}, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Spec.BoxTypes[0].Name = "replacement"
	cfg.Generation++
	if err := r.Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("expected stale source error, got %v", err)
	}
	deps := &appsv1.DeploymentList{}
	if err := r.List(t.Context(), deps); err != nil {
		t.Fatal(err)
	}
	if len(deps.Items) != 0 || len(listCertificates(t, r)) != 0 {
		t.Fatal("missing SSH source masked stale palette")
	}
}

func TestUnavailableSSHBundleKeepsLiveCredentialWorkloads(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trust := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-trust", Namespace: y.Namespace}, Data: map[string]string{"ssh-ca.pub": string(ssh.MarshalAuthorizedKey(pub))}}
	if err := r.Create(t.Context(), trust); err != nil {
		t.Fatal(err)
	}
	cfg := &r.GlobalConfig.Config
	cfg.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: trust.Name}, Key: "ssh-ca.pub"}
	startCertificateWorkloads(t, r, y)
	dep := certificateWorkload(t, r, "dataplane")
	writer := certificateWorkload(t, r, "credential-delivery")
	consumer := certificateWorkload(t, r, "controlplane")
	cert := listCertificates(t, r)[0]
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	policy := y.Annotations[policyStateAnnotation]
	if !apimeta.IsStatusConditionTrue(y.Status.Conditions, "CredentialsDelivered") {
		t.Fatal("test setup did not reach delivered status")
	}
	cfg.Components.Dataplane.Image.Tag = "next"
	cfg.Certificates.Duration = "87601h"
	publishCertificateConfig(t, r)
	trust.Data["ssh-ca.pub"] = "invalid test bundle"
	if err := r.Update(t.Context(), trust); err != nil {
		t.Fatal(err)
	}
	var unavailable *sshPolicyUnavailableError
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); !errors.As(err, &unavailable) {
		t.Fatalf("expected SSH error, got %v", err)
	}
	if d := certificateWorkload(t, r, "dataplane"); d.ResourceVersion == dep.ResourceVersion || !strings.HasSuffix(d.Spec.Template.Spec.Containers[0].Image, ":next") {
		t.Fatal("unrelated dataplane image was not updated")
	}
	for component, version := range map[string]string{"credential-delivery": writer.ResourceVersion, "controlplane": consumer.ResourceVersion} {
		if got := certificateWorkload(t, r, component).ResourceVersion; got != version {
			t.Errorf("%s was modified during policy outage", component)
		}
	}
	if got := listCertificates(t, r)[0].GetResourceVersion(); got != cert.GetResourceVersion() {
		t.Error("affected Certificate was modified")
	}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	if y.Annotations[policyStateAnnotation] != policy || !apimeta.IsStatusConditionPresentAndEqual(y.Status.Conditions, "CredentialsDelivered", metav1.ConditionUnknown) {
		t.Fatalf("invalid policy advanced allocation or left delivery status falsely green: %+v", y.Status.Conditions)
	}
}
