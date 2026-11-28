package controller

import (
	"context"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDiagnosticsWaitsForCredentialsAndControlplaneRollout(t *testing.T) {
	r, y, node := certificateEnvironment(t)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	readSets := func() []appsv1.DaemonSet {
		t.Helper()
		var list appsv1.DaemonSetList
		check(r.List(ctx, &list, client.InNamespace(y.Namespace)))
		return list.Items
	}
	wantImage := func(image string) appsv1.DaemonSet {
		t.Helper()
		sets := readSets()
		if len(sets) != 1 || sets[0].Spec.Template.Spec.Containers[0].Image != image {
			t.Fatalf("expected one DaemonSet with image %s, got %v", image, sets)
		}
		return sets[0]
	}
	markReady := func(d *appsv1.Deployment) {
		t.Helper()
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
		check(r.Status().Update(ctx, d))
	}
	cfg := &r.GlobalConfig.Config
	cfg.Certificates.Profiles = append(cfg.Certificates.Profiles, api.CertificateProfile{
		Name: "diagnostics", Component: "dataplane", Container: "dataplane", MountPath: "/example/diagnostics", Usage: "client",
	})
	y.Spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{
		YanetComponentOverride: api.YanetComponentOverride{Enabled: helpers.PtrFalse()},
	}}
	check(r.Update(ctx, y))
	cfg.Images.Diagnostics = map[string]string{"cp:v1": "cli:v1", "cp:v2": "cli:v2"}
	publishCertificateConfig(t, r)

	reconcileCertificates(t, r, y)
	certs := listCertificates(t, r)
	if len(certs) != 2 || len(readSets()) != 0 {
		t.Fatalf("expected issuance for the gateway and the CLI-only consumer, but no DaemonSet yet; certificates: %d", len(certs))
	}
	for i := range certs {
		cert := &certs[i]
		cert.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": cert.GetGeneration()}}}
		check(r.Status().Update(ctx, cert))
	}
	reconcileCertificates(t, r, y)
	delivery := certificateWorkload(t, r, "credential-delivery")
	markReady(delivery)
	reconcileCertificates(t, r, y)
	if len(readSets()) != 0 {
		t.Fatal("diagnostics started before the controlplane was available")
	}
	cp := certificateWorkload(t, r, "controlplane")
	markReady(cp)
	reconcileCertificates(t, r, y)
	first := wantImage("cli:v1")
	reconcileCertificates(t, r, y)
	if wantImage("cli:v1").ResourceVersion != first.ResourceVersion {
		t.Fatal("an unchanged diagnostics DaemonSet was rewritten")
	}

	// Simulate an update that has not finished rolling out, then observe the
	// new generation become ready. Keep the old CLI until that observation.
	cp.Status = appsv1.DeploymentStatus{ObservedGeneration: cp.Generation, Replicas: 2, UpdatedReplicas: 1, ReadyReplicas: 1}
	check(r.Status().Update(ctx, cp))
	cfg.Components.Controlplane.Image.Tag = "v2"
	publishCertificateConfig(t, r)
	reconcileCertificates(t, r, y)
	wantImage("cli:v1")
	cp = certificateWorkload(t, r, "controlplane")
	if cp.Spec.Template.Spec.Containers[0].Image != "cp:v2" {
		t.Fatal("test did not update the controlplane deployment")
	}
	markReady(cp)
	reconcileCertificates(t, r, y)
	wantImage("cli:v2")

	check(r.Get(ctx, client.ObjectKeyFromObject(y), y))
	y.Spec.AutoSync = helpers.PtrFalse()
	check(r.Update(ctx, y))
	check(r.Delete(ctx, node))
	reconcileCertificates(t, r, y)
	retained := wantImage("cli:v2")
	if retained.Labels[manifests.LabelNode] != node.Name {
		t.Fatalf("unexpected retained DaemonSet: %s", retained.Name)
	}
	check(r.Get(ctx, client.ObjectKeyFromObject(y), y))
	y.Spec.AutoSync = helpers.PtrTrue()
	check(r.Update(ctx, y))
	reconcileCertificates(t, r, y)
	if len(readSets()) != 0 {
		t.Fatal("node release did not prune the diagnostics DaemonSet")
	}
}
