package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func markCertificatesIssued(t *testing.T, r *YanetReconciler) {
	t.Helper()
	for _, c := range listCertificates(t, r) {
		c.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": c.GetGeneration()}}}
		if err := r.Status().Update(context.Background(), &c); err != nil {
			t.Fatal(err)
		}
	}
}

func certificateWorkload(t *testing.T, r *YanetReconciler, component string) *appsv1.Deployment {
	t.Helper()
	list := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), list, client.MatchingLabels{manifests.LabelComponent: component}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected one %s workload, got %d", component, len(list.Items))
	}
	return list.Items[0].DeepCopy()
}

func startCertificateWorkloads(t *testing.T, r *YanetReconciler, y *api.Yanet) {
	t.Helper()
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	markCertificatesIssued(t, r)
	reconcileCertificates(t, r, y)
	d := certificateWorkload(t, r, "credential-delivery")
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := r.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
	consumer := certificateWorkload(t, r, "controlplane")
	consumer.Status = appsv1.DeploymentStatus{ObservedGeneration: consumer.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := r.Status().Update(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(y), y); err != nil {
		t.Fatal(err)
	}
	y.Annotations[policyAckAnnotation] = string(y.UID) + "/1"
	if err := r.Update(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	reconcileCertificates(t, r, y)
}

func TestManagedCertificatesDrainDespiteCertificateFailure(t *testing.T) {
	for _, mode := range []string{"forbidden", "foreign-certificate", "foreign-delivery", "missing-delivery", "stop", "report-only", "healthy-certificates"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, y, _ := certificateEnvironment(t)
			startCertificateWorkloads(t, r, y)
			d := certificateWorkload(t, r, "credential-delivery")
			if mode == "foreign-delivery" {
				d.OwnerReferences = nil
				if err := r.Update(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-delivery" {
				if err := r.Delete(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			before := d.DeepCopy()
			consumerBefore := certificateWorkload(t, r, "controlplane")
			if err := r.Get(ctx, client.ObjectKeyFromObject(y), y); err != nil {
				t.Fatal(err)
			}
			y.Spec.Enabled = helpers.PtrFalse()
			if mode == "report-only" {
				y.Spec.AutoSync = helpers.PtrFalse()
			}
			if err := r.Update(ctx, y); err != nil {
				t.Fatal(err)
			}
			if mode == "stop" {
				r.GlobalConfig.Config.Stop = true
			}
			if mode == "foreign-certificate" {
				c := listCertificates(t, r)[0].DeepCopy()
				c.SetOwnerReferences(nil)
				if err := r.Update(ctx, c); err != nil {
					t.Fatal(err)
				}
			} else if mode != "healthy-certificates" {
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == "Certificate" {
						return apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, key.Name, errors.New("issuance access denied"))
					}
					return cl.Get(ctx, key, obj, opts...)
				}})
			}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			if (mode == "stop" || mode == "healthy-certificates") && err != nil {
				t.Fatal(err)
			}
			if mode != "stop" && mode != "healthy-certificates" && (err == nil || (mode != "foreign-certificate" && !strings.Contains(err.Error(), "issuance access denied"))) {
				t.Fatalf("lost certificate error: %v", err)
			}
			consumer := certificateWorkload(t, r, "controlplane")
			consumerReplicas := int32(0)
			if mode == "stop" || mode == "report-only" {
				consumerReplicas = 1
			}
			if *consumer.Spec.Replicas != consumerReplicas || !reflect.DeepEqual(consumer.Spec.Template, consumerBefore.Spec.Template) {
				t.Fatal("drain replaced a supported policy gate or violated replica controls")
			}
			got := &appsv1.Deployment{}
			err = r.Get(ctx, client.ObjectKeyFromObject(d), got)
			if mode == "missing-delivery" {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("created delivery during certificate failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if mode == "foreign-delivery" || mode == "stop" || mode == "report-only" {
				want = 1
			}
			if *got.Spec.Replicas != want {
				t.Fatalf("delivery replicas = %d, want %d", *got.Spec.Replicas, want)
			}
			before.Spec.Replicas = got.Spec.Replicas
			if !reflect.DeepEqual(before.Spec, got.Spec) || !reflect.DeepEqual(before.OwnerReferences, got.OwnerReferences) {
				t.Fatal("drain changed delivery sources/template/ownership")
			}
		})
	}
}

func TestManagedCertificatesFreezeWriterUntilDesiredIssuance(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "add-account", true: "remove-last-account"}[remove], func(t *testing.T) {
			ctx := context.Background()
			r, y, _ := certificateEnvironment(t)
			account := api.GatewayAccount{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}
			if remove {
				r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{account}
			}
			startCertificateWorkloads(t, r, y)
			writer := certificateWorkload(t, r, "credential-delivery")
			consumer := certificateWorkload(t, r, "controlplane")
			recoverGateway := gatewayRecoveryWithOldLeaf(t, writer)
			recoverGateway(writer, consumer)
			r.GlobalConfig.Config.Certificates.IssuerRef.Name = "replacement-issuer"
			if remove {
				r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = nil
			} else {
				r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{account}
			}
			publishCertificateConfig(t, r)
			// The API increments generation after spec updates; the fake does not.
			// Model it at that external boundary, retaining old Ready status.
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == "Certificate" {
					u.SetGeneration(u.GetGeneration() + 1)
				}
				if d, ok := obj.(*appsv1.Deployment); ok {
					options := &client.UpdateOptions{}
					for _, option := range opts {
						option.ApplyToUpdate(options)
					}
					if len(options.DryRun) == 0 {
						d.Generation++
					}
				}
				return cl.Update(ctx, obj, opts...)
			}})
			for range 3 {
				result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
				if err != nil || result.RequeueAfter != 5*time.Second {
					t.Fatalf("pending issuance: %+v %v", result, err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(y), y); err != nil {
					t.Fatal(err)
				}
				if apimeta.IsStatusConditionTrue(y.Status.Conditions, "Ready") || !apimeta.IsStatusConditionFalse(y.Status.Conditions, "CredentialsDelivered") {
					t.Fatalf("skipped delivery reported ready: %+v", y.Status.Conditions)
				}
				for _, before := range []*appsv1.Deployment{writer, consumer} {
					got := &appsv1.Deployment{}
					if err := r.Get(ctx, client.ObjectKeyFromObject(before), got); err != nil {
						t.Fatal(err)
					}
					if got.ResourceVersion != before.ResourceVersion {
						t.Fatalf("pending issuer changed working %s template/policy", before.Labels[manifests.LabelComponent])
					}
				}
				recoverGateway(certificateWorkload(t, r, "credential-delivery"), certificateWorkload(t, r, "controlplane"))
			}
			markCertificatesIssued(t, r)
			reconcileCertificates(t, r, y)
			newWriter := certificateWorkload(t, r, "credential-delivery")
			if reflect.DeepEqual(writer.Spec.Template, newWriter.Spec.Template) {
				t.Fatal("ready issuance did not activate writer policy")
			}
			if got := certificateWorkload(t, r, "controlplane"); got.ResourceVersion != consumer.ResourceVersion {
				t.Fatal("old delivery status unblocked a new consumer policy")
			}
			newWriter.Status.ObservedGeneration = newWriter.Generation
			if err := r.Status().Update(ctx, newWriter); err != nil {
				t.Fatal(err)
			}
			reconcileCertificates(t, r, y)
			newConsumer := certificateWorkload(t, r, "controlplane")
			if reflect.DeepEqual(consumer.Spec.Template, newConsumer.Spec.Template) {
				t.Fatal("ready delivery did not activate consumer policy")
			}
			for _, args := range [][]string{newWriter.Spec.Template.Spec.Containers[0].Args, newConsumer.Spec.Template.Spec.InitContainers[0].Args} {
				hasAlice := strings.Contains(strings.Join(args, " "), "alice")
				if hasAlice == remove {
					t.Fatalf("converged policy retained wrong account permissions: %v", args)
				}
			}
		})
	}
}

func TestManagedCertificatesReadyPolicyOnlyChange(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	startCertificateWorkloads(t, r, y)
	before := certificateWorkload(t, r, "controlplane")
	r.GlobalConfig.Config.Certificates.AdditionalGatewayAccounts = []api.GatewayAccount{{Username: "alice", Permissions: []string{"/example.v1.Service/Read"}}}
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	writer, consumer := certificateWorkload(t, r, "credential-delivery"), certificateWorkload(t, r, "controlplane")
	if consumer.ResourceVersion == before.ResourceVersion {
		t.Fatal("ready issuance blocked policy-only rollout")
	}
	for _, args := range [][]string{writer.Spec.Template.Spec.Containers[0].Args, consumer.Spec.Template.Spec.InitContainers[0].Args} {
		if !strings.Contains(strings.Join(args, " "), "alice") {
			t.Fatalf("policy-only rollout omitted new account: %v", args)
		}
	}
}

func TestManagedCertificatesFinalPodPreflightBeforeWrites(t *testing.T) {
	r, y, _ := certificateEnvironment(t)
	r.GlobalConfig.Config.Patches = []api.NamedPatch{{Name: "gate-name", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"yanet-credential-gateway-check","image":"example.test/sidecar:v1"}]}}}}`)}}}
	r.GlobalConfig.Config.BoxTypes[0].Components.Controlplane.Patches = []string{"gate-name"}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
	if err == nil || (!strings.Contains(err.Error(), "yanet-credential-gateway-check") && !strings.Contains(err.Error(), "startup gate")) {
		t.Fatalf("expected final Pod name conflict: %v", err)
	}
	if len(listCertificates(t, r)) != 0 {
		t.Fatal("preflight issued certificates before rejecting final consumer")
	}
	for _, list := range []client.ObjectList{&appsv1.DeploymentList{}, &corev1.ConfigMapList{}} {
		if err := r.List(context.Background(), list); err != nil {
			t.Fatal(err)
		}
		objects, err := apimeta.ExtractList(list)
		if err != nil {
			t.Fatal(err)
		}
		if len(objects) != 0 {
			t.Fatalf("preflight wrote %T before rejecting final consumer", list)
		}
	}
}
