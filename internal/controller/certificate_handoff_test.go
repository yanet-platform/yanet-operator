package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestManagedCertificatesFinalPodPreflightBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, patch, want string }{
		{"gate-name", `{"spec":{"template":{"spec":{"containers":[{"name":"yanet-credential-gateway-check","image":"example.test/sidecar:v1"}]}}}}`, "startup gate"},
		{"mount-path", `{"spec":{"template":{"spec":{"containers":[{"name":"controlplane","volumeMounts":[{"name":"extra","mountPath":"/fixture"},{"name":"other","mountPath":"/fixture"},{"$patch":"replace"}]}],"volumes":[{"name":"extra","emptyDir":{}},{"name":"other","emptyDir":{}}]}}}}`, `duplicate volumeMount mountPath "/fixture"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, y, _ := certificateEnvironment(t)
			cfg := &r.GlobalConfig.Config
			cfg.Patches = []api.NamedPatch{{Name: tc.name, Patch: runtime.RawExtension{Raw: []byte(tc.patch)}}}
			cfg.BoxTypes[0].Components.Controlplane.Patches = []string{tc.name}
			if _, err := (&api.YanetConfigCustomValidator{}).ValidateCreate(ctx, &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: *cfg}); err != nil {
				t.Fatalf("palette must pass admission before final composition: %v", err)
			}
			publishCertificateConfig(t, r)
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected final Pod conflict containing %q: %v", tc.want, err)
			}
			if len(listCertificates(t, r)) != 0 {
				t.Fatal("preflight issued certificates before rejecting final consumer")
			}
			for _, list := range []client.ObjectList{&appsv1.DeploymentList{}, &corev1.ConfigMapList{}} {
				if err := r.List(ctx, list); err != nil {
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
			if err := r.Get(ctx, client.ObjectKeyFromObject(y), y); err != nil {
				t.Fatal(err)
			}
		})
	}
}
