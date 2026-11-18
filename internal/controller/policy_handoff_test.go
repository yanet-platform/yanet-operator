package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func readyCertificateDeployments(t *testing.T, r *YanetReconciler) {
	t.Helper()
	list := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		d := &list.Items[i]
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: replicas, UpdatedReplicas: replicas, ReadyReplicas: replicas, AvailableReplicas: replicas}
		if err := r.Status().Update(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
}

func acknowledgePolicyCheckpoints(t *testing.T, r *YanetReconciler, y *api.Yanet) {
	t.Helper()
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	if y.Annotations == nil {
		y.Annotations = map[string]string{}
	}
	y.Annotations[policyAckAnnotation] = string(y.UID) + "/" + strconv.FormatUint(readPolicyState(t, r, y).Sequence, 10)
	if err := r.Update(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
}

func readPolicyState(t *testing.T, r *YanetReconciler, y *api.Yanet) policyState {
	t.Helper()
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	var state policyState
	if err := json.Unmarshal([]byte(y.Annotations[policyStateAnnotation]), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestManagedPolicyRecoveryAcrossPartialUpdates(t *testing.T) {
	for _, barrier := range []string{"delivery-pending", "consumer-forbidden"} {
		for _, remove := range []bool{false, true} {
			t.Run(barrier+map[bool]string{false: "/add", true: "/remove-last"}[remove], func(t *testing.T) {
				testCtx := context.Background()
				r, y, _ := certificateEnvironment(t)
				account := api.GatewayAccount{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}
				if remove {
					r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{account}
				}
				if barrier == "delivery-pending" {
					r.GlobalConfig.Config.Certificates.Profiles = append(r.GlobalConfig.Config.Certificates.Profiles, api.CertificateProfile{Name: "client", Component: "dataplane", Container: "dataplane", MountPath: "/etc/yanet2/pki/client", Usage: "client"})
				}
				numa := int32(2)
				r.GlobalConfig.Config.Components.Controlplane.Numa = &numa
				publishCertificateConfig(t, r)
				reconcileCertificates(t, r, y)
				markCertificatesIssued(t, r)
				reconcileCertificates(t, r, y)
				readyCertificateDeployments(t, r)
				reconcileCertificates(t, r, y)
				readyCertificateDeployments(t, r)
				acknowledgePolicyCheckpoints(t, r, y)
				if !readPolicyState(t, r, y).Activated {
					t.Fatal("ready acknowledged installation did not activate")
				}
				before := &appsv1.DeploymentList{}
				if err := r.List(testCtx, before, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil {
					t.Fatal(err)
				}
				if len(before.Items) != 2 {
					t.Fatalf("expected both NUMA gateways: %d", len(before.Items))
				}
				writer := certificateWorkload(t, r, "credential-delivery")
				if barrier == "delivery-pending" && len(writer.Spec.Template.Spec.Containers) != 2 {
					t.Fatal("independent exporter role is missing")
				}
				recoverGateway := gatewayRecoveryWithOldLeaf(t, writer)
				for i := range before.Items {
					recoverGateway(writer, &before.Items[i])
				}
				blocked := true
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					d, ok := obj.(*appsv1.Deployment)
					if !ok {
						return cl.Update(ctx, obj, opts...)
					}
					options := &client.UpdateOptions{}
					for _, o := range opts {
						o.ApplyToUpdate(options)
					}
					if len(options.DryRun) == 0 && blocked && barrier == "consumer-forbidden" && d.Labels[manifests.LabelComponent] == "controlplane" {
						return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, d.Name, errors.New("consumer update forbidden"))
					}
					old := &appsv1.Deployment{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(d), old); err != nil {
						return err
					}
					d.Generation = old.Generation
					if !reflect.DeepEqual(old.Spec, d.Spec) {
						d.Generation++
					}
					return cl.Update(ctx, obj, opts...)
				}})
				if remove {
					r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
				} else {
					r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{account}
				}
				publishCertificateConfig(t, r)
				reconcileCertificates(t, r, y)
				if barrier == "consumer-forbidden" {
					d := certificateWorkload(t, r, "credential-delivery")
					d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
					if err := r.Status().Update(testCtx, d); err != nil {
						t.Fatal(err)
					}
				}
				for range 3 {
					_, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
					if barrier == "consumer-forbidden" {
						if !apierrors.IsForbidden(err) {
							t.Fatalf("expected consumer API denial: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					writer = certificateWorkload(t, r, "credential-delivery")
					for i := range before.Items {
						got := &appsv1.Deployment{}
						if err := r.Get(testCtx, client.ObjectKeyFromObject(&before.Items[i]), got); err != nil {
							t.Fatal(err)
						}
						if got.ResourceVersion != before.Items[i].ResourceVersion {
							t.Fatal("barrier failed to retain gateway")
						}
						recoverGateway(writer, got)
					}
				}
				if readPolicyState(t, r, y).Sequence != 2 {
					t.Fatal("reconciliation retries allocated duplicate policy revisions")
				}
				blocked = false
				readyCertificateDeployments(t, r)
				reconcileCertificates(t, r, y)
				for i := range before.Items {
					got := &appsv1.Deployment{}
					if err := r.Get(testCtx, client.ObjectKeyFromObject(&before.Items[i]), got); err != nil {
						t.Fatal(err)
					}
					if got.ResourceVersion == before.Items[i].ResourceVersion {
						t.Fatal("unblocked gateway did not converge")
					}
					for _, args := range [][]string{writer.Spec.Template.Spec.Containers[0].Args, got.Spec.Template.Spec.InitContainers[0].Args} {
						var alice *api.GatewayAccount
						for _, arg := range args {
							if data, ok := strings.CutPrefix(arg, "--gateway-account="); ok {
								var decoded api.GatewayAccount
								if err := json.Unmarshal([]byte(data), &decoded); err != nil {
									t.Fatal(err)
								}
								if decoded.Username == account.Username {
									alice = &decoded
								}
							}
						}
						if (remove && alice != nil) || (!remove && !reflect.DeepEqual(alice, &account)) {
							t.Fatalf("wrong account after policy-only rollout (remove=%t): %+v", remove, alice)
						}
					}
					recoverGateway(writer, got)
				}
			})
		}
	}
}

func TestManagedPolicyBootstrapRequiresUnchangedPolicyAndCheckpointAck(t *testing.T) {
	testCtx := context.Background()
	r, y, _ := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	// Model deployed v3.1.0 objects: no persisted protocol state or revision flags.
	list := &appsv1.DeploymentList{}
	if err := r.List(testCtx, list); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		d := &list.Items[i]
		for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
			for j := range containers {
				containers[j].Args = slices.DeleteFunc(containers[j].Args, func(arg string) bool {
					return strings.HasPrefix(arg, "--policy-") || strings.HasPrefix(arg, "--ssh-trust-sha256=")
				})
				if probe := containers[j].ReadinessProbe; probe != nil && probe.Exec != nil {
					probe.Exec.Command = slices.DeleteFunc(probe.Exec.Command, func(arg string) bool {
						return strings.HasPrefix(arg, "--policy-") || arg == "--exact-policy-revision" || strings.HasPrefix(arg, "--ssh-trust-sha256=")
					})
				}
			}
		}
		if err := r.Update(testCtx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Get(testCtx, client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	delete(y.Annotations, policyStateAnnotation)
	delete(y.Annotations, policyAckAnnotation)
	if err := r.Update(testCtx, y); err != nil {
		t.Fatal(err)
	}
	legacyWriter := certificateWorkload(t, r, "credential-delivery")
	legacyConsumer := certificateWorkload(t, r, "controlplane")
	recoverGateway := gatewayRecoveryWithOldLeaf(t, legacyWriter)
	recoverGateway(legacyWriter, legacyConsumer)
	base := r.Client.(client.WithWatch)
	r.APIReader = base
	staleInventory := true
	r.Client = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if d, ok := list.(*appsv1.DeploymentList); ok && staleInventory {
			d.Items = nil
			return nil
		}
		return cl.List(ctx, list, opts...)
	}})
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
	publishCertificateConfig(t, r)
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "unchanged v3.1.0 policy") {
		t.Fatalf("upgrade changed legacy authorization: %v", err)
	}
	staleInventory = false
	if got := certificateWorkload(t, r, "credential-delivery"); got.ResourceVersion != legacyWriter.ResourceVersion {
		t.Fatal("failed bootstrap changed writer")
	}
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	bridge := certificateWorkload(t, r, "credential-delivery")
	recoverGateway(bridge, legacyConsumer)
	// Even a premature acknowledgment cannot replace rollout observation.
	consumer := certificateWorkload(t, r, "controlplane")
	consumer.Status = appsv1.DeploymentStatus{}
	if err := r.Status().Update(testCtx, consumer); err != nil {
		t.Fatal(err)
	}
	acknowledgePolicyCheckpoints(t, r, y)
	if readPolicyState(t, r, y).Activated {
		t.Fatal("ack bypassed unready revision-aware gate")
	}
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
	publishCertificateConfig(t, r)
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "blocked until") {
		t.Fatalf("bootstrap allowed early policy advancement: %v", err)
	}
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
	publishCertificateConfig(t, r)
	readyCertificateDeployments(t, r)
	reconcileCertificates(t, r, y)
	if !readPolicyState(t, r, y).Activated {
		t.Fatal("ack plus ready current gates failed to complete bootstrap")
	}
}

func TestManagedPolicyOrderingAndSourceFences(t *testing.T) {
	testCtx := context.Background()
	r, y, _ := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	account := api.GatewayAccount{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}
	for i, accounts := range [][]api.GatewayAccount{{account}, nil, {account}, nil} {
		r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = accounts
		publishCertificateConfig(t, r)
		reconcileCertificates(t, r, y)
		if got := readPolicyState(t, r, y).Sequence; got != uint64(i+2) {
			t.Fatalf("ABA transition %d reused revision %d", i, got)
		}
	}
	before := certificateWorkload(t, r, "controlplane")
	r.GlobalConfig.Config.Certificates.IssuerRef.Name = "replacement-issuer"
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	if readPolicyState(t, r, y).Sequence != 5 {
		t.Fatal("issuer rotation changed policy revision")
	}
	if got := certificateWorkload(t, r, "controlplane"); got.ResourceVersion != before.ResourceVersion {
		t.Fatal("pending issuer rotated gateway template")
	}
	// A stale snapshot must not mint an apparently newer authorization revision.
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{account}
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "policy source changed") {
		t.Fatalf("accepted unobserved source: %v", err)
	}
	if readPolicyState(t, r, y).Sequence != 5 {
		t.Fatal("stale source changed durable revision")
	}
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
	config := &api.YanetConfig{}
	if err := r.Get(testCtx, client.ObjectKey{Name: api.YanetConfigName}, config); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(testCtx, config); err != nil {
		t.Fatal(err)
	}
	config = &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName, UID: "recreated-config", Generation: 1}, Spec: *r.GlobalConfig.Config.DeepCopy()}
	if err := r.Create(testCtx, config); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	if state := readPolicyState(t, r, y); state.Sequence != 6 || state.ConfigUID != "recreated-config" {
		t.Fatalf("config recreation reset or conflated ordering: %+v", state)
	}
	if err := r.Get(testCtx, client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	var copied policyState
	if err := json.Unmarshal([]byte(y.Annotations[policyStateAnnotation]), &copied); err != nil {
		t.Fatal(err)
	}
	copied.UID = "different-installation"
	data, err := json.Marshal(copied)
	if err != nil {
		t.Fatal(err)
	}
	y.Annotations[policyStateAnnotation] = string(data)
	if err := r.Update(testCtx, y); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "different installation") {
		t.Fatalf("copied state/ack bypassed installation UID: %v", err)
	}
}

func TestManagedPolicyConfigRecreationBetweenSourceAndCASRead(t *testing.T) {
	testCtx := context.Background()
	r, y, _ := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	base := r.Client.(client.WithWatch)
	trigger := true
	r.APIReader = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		config, ok := obj.(*api.YanetConfig)
		if !ok || !trigger {
			return cl.Get(ctx, key, obj, opts...)
		}
		trigger = false
		if err := cl.Get(ctx, key, config, opts...); err != nil {
			return err
		}
		// Complete another allocation from the new UID after this request read
		// the old config, but before it reads the installation's CAS version.
		if err := cl.Delete(ctx, config); err != nil {
			return err
		}
		replacement := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName, UID: "new-config", Generation: 1}, Spec: *config.Spec.DeepCopy()}
		if err := cl.Create(ctx, replacement); err != nil {
			return err
		}
		other := &YanetReconciler{Client: base, Scheme: r.Scheme, GlobalConfig: r.GlobalConfig, ManageCertificates: true}
		_, err := other.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
		return err
	}})
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "policy source changed") {
		t.Fatalf("incomparable old UID minted a new revision: %v", err)
	}
	state := readPolicyState(t, r, y)
	if state.Sequence != 2 || state.ConfigUID != "new-config" {
		t.Fatalf("stale source rolled back the allocation: %+v", state)
	}
}

func TestManagedPolicyPauseDoesNotAllocateOrApply(t *testing.T) {
	for _, mode := range []string{"stop", "report-only"} {
		t.Run(mode, func(t *testing.T) {
			testCtx := context.Background()
			r, y, _ := certificateEnvironment(t)
			startCertificateWorkloads(t, r, y)
			before := readPolicyState(t, r, y)
			deployments := &appsv1.DeploymentList{}
			if err := r.List(testCtx, deployments); err != nil {
				t.Fatal(err)
			}
			certificates := listCertificates(t, r)
			if mode == "stop" {
				r.GlobalConfig.Config.Stop = true
			} else {
				y.Spec.AutoSync = helpers.PtrFalse()
				y.Generation++
				if err := r.Update(testCtx, y); err != nil {
					t.Fatal(err)
				}
			}
			r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
			publishCertificateConfig(t, r)
			reconcileCertificates(t, r, y)
			if after := readPolicyState(t, r, y); !reflect.DeepEqual(after, before) {
				t.Fatalf("paused reconcile mutated policy allocation: %+v -> %+v", before, after)
			}
			for i := range deployments.Items {
				got := &appsv1.Deployment{}
				if err := r.Get(testCtx, client.ObjectKeyFromObject(&deployments.Items[i]), got); err != nil {
					t.Fatal(err)
				}
				if got.ResourceVersion != deployments.Items[i].ResourceVersion {
					t.Fatal("paused reconcile persisted a workload change")
				}
			}
			observed := listCertificates(t, r)
			if len(observed) != len(certificates) {
				t.Fatal("paused reconcile changed the issuance inventory")
			}
			for i, got := range observed {
				if got.GetResourceVersion() != certificates[i].GetResourceVersion() {
					t.Fatal("paused reconcile changed issuance")
				}
			}
		})
	}
}

func TestManagedPolicyRejectsRetiredNodeIdentityFromCache(t *testing.T) {
	testCtx := context.Background()
	r, y, node := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	base := r.Client.(client.WithWatch)
	r.APIReader = base
	if err := base.Delete(testCtx, node); err != nil {
		t.Fatal(err)
	}
	replacement := node.DeepCopy()
	replacement.UID = "replacement-node-uid"
	replacement.ResourceVersion = ""
	if err := base.Create(testCtx, replacement); err != nil {
		t.Fatal(err)
	}
	r.Client = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if nodes, ok := list.(*corev1.NodeList); ok {
			nodes.Items = []corev1.Node{*node.DeepCopy()}
			return nil
		}
		return cl.List(ctx, list, opts...)
	}})
	writer := certificateWorkload(t, r, "credential-delivery")
	if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "policy Node source changed") {
		t.Fatalf("retired Node UID minted a policy revision: %v", err)
	}
	if readPolicyState(t, r, y).Sequence != 1 {
		t.Fatal("stale Node allocated a policy revision")
	}
	if got := certificateWorkload(t, r, "credential-delivery"); got.ResourceVersion != writer.ResourceVersion {
		t.Fatal("stale Node changed the writer identity")
	}
}

func TestManagedPolicyVersionsProjectedSSHBinaryData(t *testing.T) {
	testCtx := context.Background()
	r, y, _ := certificateEnvironment(t)
	key := func() []byte {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		public, err := ssh.NewPublicKey(&private.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return ssh.MarshalAuthorizedKey(public)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-trust", Namespace: y.Namespace}, BinaryData: map[string][]byte{"ssh-ca.pub": key()}}
	if err := r.Create(testCtx, cm); err != nil {
		t.Fatal(err)
	}
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Key: "ssh-ca.pub"}
	startCertificateWorkloads(t, r, y)
	cm.BinaryData["ssh-ca.pub"] = key()
	if err := r.Update(testCtx, cm); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	if readPolicyState(t, r, y).Sequence != 2 {
		t.Fatal("projected binary SSH policy changed without a new revision")
	}
	// Data/BinaryData are projection representations, not distinct policies.
	cm.Data = map[string]string{"ssh-ca.pub": string(cm.BinaryData["ssh-ca.pub"])}
	cm.BinaryData = nil
	if err := r.Update(testCtx, cm); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	if readPolicyState(t, r, y).Sequence != 2 {
		t.Fatal("equivalent ConfigMap representation restarted policy rollout")
	}
}

func TestManagedPolicyRejectsInvalidInitialSSHBundle(t *testing.T) {
	testCtx := context.Background()
	r, y, _ := certificateEnvironment(t)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "initial-ssh", Namespace: y.Namespace}, Data: map[string]string{"key": ""}}
	if err := r.Create(testCtx, cm); err != nil {
		t.Fatal(err)
	}
	r.GlobalConfig.Config.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Key: "key"}
	publishCertificateConfig(t, r)
	_, reconcileErr := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
	if reconcileErr == nil || !strings.Contains(reconcileErr.Error(), "SSH CA bundle contains no public keys") {
		t.Fatalf("empty initial SSH bundle accepted: %v", reconcileErr)
	}
	if err := r.Get(testCtx, client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	if y.Annotations[policyStateAnnotation] != "" {
		t.Fatal("invalid input committed an initial policy")
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["key"] = string(ssh.MarshalAuthorizedKey(public))
	if err := r.Update(testCtx, cm); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	markCertificatesIssued(t, r)
	reconcileCertificates(t, r, y)
	writer := certificateWorkload(t, r, "credential-delivery")
	fixture := newGatewayFileFixture(t, writer)
	projection := filepath.Join(t.TempDir(), "ssh-ca.pub")
	if err := os.WriteFile(projection, []byte(cm.Data["key"]), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"--source=" + fixture.source, "--destination=" + fixture.destination, "--trust=" + fixture.trust, "--ssh-trust=" + projection}
	one, cancel := context.WithCancel(testCtx)
	cancel()
	container := writer.Spec.Template.Spec.Containers[0]
	if err := credentials.Run(one, append(slices.Clone(container.Args[1:]), paths...)); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Run(testCtx, append(slices.Clone(container.ReadinessProbe.Exec.Command[2:]), paths...)); err != nil {
		t.Fatalf("corrected initial writer was not exact-ready: %v", err)
	}
	readyCertificateDeployments(t, r)
	reconcileCertificates(t, r, y)
	gate := certificateWorkload(t, r, "controlplane").Spec.Template.Spec.InitContainers[0]
	if err := credentials.Run(testCtx, append(slices.Clone(gate.Args[1:]), "--destination="+fixture.destination, "--trust="+filepath.Join(fixture.destination, "current", "ca.crt"), "--ssh-trust="+filepath.Join(fixture.destination, "current", "ssh-ca.pub"))); err != nil {
		t.Fatalf("corrected initial gate did not validate: %v", err)
	}
	readyCertificateDeployments(t, r)
	acknowledgePolicyCheckpoints(t, r, y)
	if state := readPolicyState(t, r, y); state.Sequence != 1 || !state.Activated {
		t.Fatalf("corrected first configuration did not complete bootstrap: %+v", state)
	}
}

func TestManagedPolicyRemovalRequiresActivation(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "pending"}[legacy], func(t *testing.T) {
			testCtx := context.Background()
			r, y, _ := certificateEnvironment(t)
			startCertificateWorkloads(t, r, y)
			readPolicyState(t, r, y)
			before := []*appsv1.Deployment{certificateWorkload(t, r, "credential-delivery"), certificateWorkload(t, r, "controlplane")}
			if legacy {
				for _, d := range before {
					for _, containers := range [][]corev1.Container{d.Spec.Template.Spec.Containers, d.Spec.Template.Spec.InitContainers} {
						for i := range containers {
							containers[i].Args = slices.DeleteFunc(containers[i].Args, func(arg string) bool { return strings.HasPrefix(arg, "--policy-") })
						}
					}
					if err := r.Update(testCtx, d); err != nil {
						t.Fatal(err)
					}
				}
				delete(y.Annotations, policyStateAnnotation)
			} else {
				state := readPolicyState(t, r, y)
				state.Activated = false
				encoded, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				y.Annotations[policyStateAnnotation] = string(encoded)
			}
			delete(y.Annotations, policyAckAnnotation)
			if err := r.Update(testCtx, y); err != nil {
				t.Fatal(err)
			}
			managed := r.GlobalConfig.Config.Certificates
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}
			for _, replacement := range []*api.CertificateManagementSpec{nil, managed.DeepCopy()} {
				if replacement != nil {
					replacement.Profiles = []api.CertificateProfile{{Name: "client", Component: "dataplane", Container: "dataplane", MountPath: "/etc/yanet2/pki/client", Usage: "client"}}
				}
				r.GlobalConfig.Config.Certificates = replacement
				publishCertificateConfig(t, r)
				if _, err := r.Reconcile(testCtx, request); err == nil || !strings.Contains(err.Error(), "removal is blocked") {
					t.Fatalf("policy removal bypassed bridge: %v", err)
				}
				for _, d := range before {
					got := &appsv1.Deployment{}
					if err := r.Get(testCtx, client.ObjectKeyFromObject(d), got); err != nil || !reflect.DeepEqual(d.Spec, got.Spec) {
						t.Fatalf("blocked removal changed %s: %v", d.Name, err)
					}
				}
			}
			r.GlobalConfig.Config.Certificates = managed
			publishCertificateConfig(t, r)
			reconcileCertificates(t, r, y)
			readyCertificateDeployments(t, r)
			reconcileCertificates(t, r, y)
			readyCertificateDeployments(t, r)
			acknowledgePolicyCheckpoints(t, r, y)
			if !readPolicyState(t, r, y).Activated {
				t.Fatal("restored managed gateway could not complete handoff")
			}
			r.GlobalConfig.Config.Certificates = nil
			publishCertificateConfig(t, r)
			reconcileCertificates(t, r, y)
			if got := gatewayContainers(certificateWorkload(t, r, "controlplane")); len(got) != 0 {
				t.Fatal("post-activation opt-out retained the gateway gate")
			}
		})
	}
}

func TestManagedPolicyNodeTurnoverBeforeAck(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{true: "same-name-new-UID", false: "departure"}[replace], func(t *testing.T) {
			testCtx := context.Background()
			r, y, node := certificateEnvironment(t)
			other := node.DeepCopy()
			other.Name, other.UID, other.ResourceVersion = "remaining.example.test", "remaining-uid", ""
			if err := r.Create(testCtx, other); err != nil {
				t.Fatal(err)
			}
			rollout := func() {
				t.Helper()
				reconcileCertificates(t, r, y)
				markCertificatesIssued(t, r)
				reconcileCertificates(t, r, y)
				readyCertificateDeployments(t, r)
				reconcileCertificates(t, r, y)
				readyCertificateDeployments(t, r)
			}
			rollout()
			// Keep an ACK for the old inventory without letting reconcile see it
			// until membership changes.
			readPolicyState(t, r, y)
			y.Annotations[policyAckAnnotation] = string(y.UID) + "/1"
			if err := r.Update(testCtx, y); err != nil {
				t.Fatal(err)
			}
			if err := r.Delete(testCtx, node); err != nil {
				t.Fatal(err)
			}
			if replace {
				replacement := node.DeepCopy()
				replacement.UID, replacement.ResourceVersion = "replacement-uid", ""
				if err := r.Create(testCtx, replacement); err != nil {
					t.Fatal(err)
				}
			}
			rollout()
			if state := readPolicyState(t, r, y); state.Sequence != 2 || state.Activated {
				t.Fatalf("turnover reused the old attempt/ack: %+v", state)
			}
			r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
			publishCertificateConfig(t, r)
			if _, err := r.Reconcile(testCtx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)}); err == nil || !strings.Contains(err.Error(), "blocked until") {
				t.Fatalf("turnover allowed new grants for a remaining scope: %v", err)
			}
			r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
			publishCertificateConfig(t, r)
			acknowledgePolicyCheckpoints(t, r, y)
			if !readPolicyState(t, r, y).Activated {
				t.Fatal("current rollout plus refreshed ACK did not activate")
			}
		})
	}
}
