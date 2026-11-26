package manifests

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

func TestCertificateDeliveryAllowsIndependentMissingLeafSources(t *testing.T) {
	ctx := certificateBuildContext()
	ctx.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ssh-trust"}, Key: "ca.pub"}
	ctx.Certificates.Profiles = append(ctx.Certificates.Profiles, api.CertificateProfile{Name: "acl", Component: "acl", Container: "acl", MountPath: "/etc/pki", Usage: "client"})
	var workloads []*appsv1.Deployment
	for _, component := range []*helpers.ResolvedComponent{
		{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true},
		{Kind: helpers.KindOperator, Name: "acl", Enabled: true, Containers: []helpers.ResolvedContainer{{Name: "acl"}}},
	} {
		rendered, err := RenderDeployments(ctx, component, nil)
		if err != nil {
			t.Fatal(err)
		}
		workloads = append(workloads, rendered...)
	}
	plan := BuildCertificatePlan(ctx, workloads, true)
	delivery := plan.Delivery
	if delivery == nil {
		t.Fatal("no credential delivery Deployment")
	}
	if delivery.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("delivery must retain its sole-writer strategy")
	}
	pod := delivery.Spec.Template.Spec
	sources := map[string]bool{}
	trust := 0
	for _, volume := range pod.Volumes {
		if source := volume.Secret; source != nil {
			sources[volume.Name] = true
			if source.Optional == nil || !*source.Optional {
				t.Errorf("missing leaf source %s blocks every role publisher", volume.Name)
			}
			if source.DefaultMode == nil || *source.DefaultMode != 0o400 || !reflect.DeepEqual(source.Items, []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}}) {
				t.Fatalf("leaf projection changed its restricted contract: %+v", source)
			}
		}
		if source := volume.ConfigMap; source != nil {
			trust++
			if source.Optional != nil && *source.Optional {
				t.Fatalf("public trust became optional: %+v", volume)
			}
		}
	}
	if len(sources) != 2 || trust != 1 || len(pod.Containers) != 2 {
		t.Fatalf("lost independent roles or required trust: %+v", pod)
	}
	for _, container := range pod.Containers {
		mounted := 0
		for _, mount := range container.VolumeMounts {
			if sources[mount.Name] {
				mounted++
				if mount.Name != "source-"+container.Name || !mount.ReadOnly || mount.MountPath != "/source" {
					t.Fatalf("publisher %s has an incorrect Secret mount: %+v", container.Name, mount)
				}
			}
		}
		if mounted != 1 {
			t.Fatalf("publisher %s has %d Secret mounts, want one at /source", container.Name, mounted)
		}
		if container.ReadinessProbe == nil || container.ReadinessProbe.Exec == nil || !slices.Contains(container.ReadinessProbe.Exec.Command, "--check") || !slices.Contains(container.ReadinessProbe.Exec.Command, "--writer-readiness") {
			t.Fatalf("publisher lost durable installed credential readiness: %+v", container)
		}
	}
	gates := 0
	for _, workload := range workloads {
		for _, gate := range workload.Spec.Template.Spec.InitContainers {
			if gate.Name != "yanet-credential-gateway-check" {
				continue
			}
			gates++
			if !slices.Contains(gate.Args, "--check") || slices.Contains(gate.Args, "--writer-readiness") {
				t.Fatalf("startup gate no longer uses the read-only check: %+v", gate)
			}
		}
	}
	if gates == 0 {
		t.Fatal("missing gateway startup gate")
	}
}

func TestCertificateReservedGateNamesFailRendering(t *testing.T) {
	for _, field := range []string{"containers", "initContainers"} {
		t.Run(field, func(t *testing.T) {
			ctx := certificateBuildContext()
			patch := api.NamedPatch{Name: "collision"}
			patch.Patch.Raw = []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{%q:[{"name":"yanet-credential-gateway-check","image":"example.test/setup:v1"}]}}}}`, field))
			config := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: api.YanetConfigSpec{
				Components: api.ComponentsSpec{
					Controlplane: api.ControlplaneSpec{Image: api.ImageRef{Name: "example.test/controlplane"}},
					Dataplane:    api.DataplaneSpec{Image: api.ImageRef{Name: "example.test/dataplane"}},
				},
				Patches: []api.NamedPatch{patch},
				BoxTypes: []api.BoxType{{Name: "firewall", Components: api.BoxComponents{
					Controlplane: &api.BoxComponent{Patches: []string{patch.Name}},
					Dataplane:    &api.BoxDataplane{},
				}}},
			}}
			if _, err := (&api.YanetConfigCustomValidator{}).ValidateCreate(context.Background(), config); err != nil {
				t.Fatalf("fixture must be an admission-accepted patch: %v", err)
			}
			component := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true, Patches: []string{patch.Name}}
			workloads, err := RenderDeployments(ctx, component, NewPatchRegistry([]api.NamedPatch{patch}))
			if err == nil || !strings.Contains(err.Error(), "startup gate") || workloads != nil {
				t.Fatalf("reserved %s gate must fail preflight, got workloads=%v err=%v", field, workloads, err)
			}
		})
	}
}

func TestCertificateGatePreservesNativeSidecar(t *testing.T) {
	ctx := certificateBuildContext()
	ctx.Certificates.Profiles[0].Component = "worker"
	ctx.Certificates.Profiles[0].Container = "worker"
	config, component := manifestPlacementConfig(t)
	workloads, err := RenderDeployments(ctx, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	BuildCertificatePlan(ctx, workloads, true)
	if err := ValidateComposedPod(workloads[0]); err != nil {
		t.Fatal(err)
	}
	containers := workloads[0].Spec.Template.Spec.InitContainers
	if len(containers) != 5 || containers[0].Name != "yanet-credential-gateway-check" || containers[0].RestartPolicy != nil ||
		containers[1].Name != "yanet-policy-gateway-check" || containers[1].RestartPolicy != nil ||
		containers[3].Name != scopedOperatorName("worker", "worker") || containers[3].RestartPolicy == nil || *containers[3].RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatalf("startup gate changed native sidecar composition: %+v", containers)
	}
}

func TestRenderDeploymentsRejectsUnsupportedNodeLabel(t *testing.T) {
	for _, node := range []string{strings.Repeat("a", 63), strings.Repeat("a", 62) + ".b", strings.Repeat("a", 63) + ".example.test.x"} {
		if problems := k8svalidation.IsDNS1123Subdomain(node); len(problems) != 0 {
			t.Fatalf("fixture must be a valid Node name: %v", problems)
		}
		for _, certificates := range []bool{false, true} {
			t.Run(fmt.Sprintf("length-%d/certificates-%t", len(node), certificates), func(t *testing.T) {
				ctx := certificateBuildContext()
				ctx.NodeName = node
				if !certificates {
					ctx.Certificates = nil
				}
				workloads, err := RenderDeployments(ctx, &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true}, nil)
				if len(node) > 63 {
					if err == nil || !strings.Contains(err.Error(), "unsupported Node name") || !strings.Contains(err.Error(), node) || workloads != nil {
						t.Fatalf("unsupported label must fail before returning manifests: workloads=%v err=%v", workloads, err)
					}
					return
				}
				if err != nil || len(workloads) != 1 {
					t.Fatalf("supported boundary Node failed: %v", err)
				}
				if workloads[0].Labels[LabelNode] != node {
					t.Fatal("supported Node lost its exact ownership label")
				}
				assertExactNodePin(t, &workloads[0].Spec.Template.Spec, node)
			})
		}
	}
}
