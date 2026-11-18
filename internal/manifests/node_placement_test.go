package manifests

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func requiredNodeTerms(t *testing.T, pod *corev1.PodSpec) []corev1.NodeSelectorTerm {
	t.Helper()
	if pod.NodeName != "" {
		t.Fatalf("spec.nodeName=%q bypasses scheduler filters", pod.NodeName)
	}
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil || pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatal("missing required node affinity")
	}
	return pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
}

func assertExactNodePin(t *testing.T, pod *corev1.PodSpec, node string) {
	t.Helper()
	want := []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}}}
	if diff := cmp.Diff(want, requiredNodeTerms(t, pod)); diff != "" || len(pod.NodeSelector) != 0 {
		t.Fatalf("expected only exact Node affinity, got nodeSelector=%v; terms diff (-want +got):\n%s", pod.NodeSelector, diff)
	}
}

func TestRenderNodePlacementAndManagedDelivery(t *testing.T) {
	build := certificateBuildContext()
	build.NodeName = "api-node"
	component := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true, Numa: 3, DisabledNuma: []int32{1}}
	workloads, err := RenderDeployments(build, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(workloads) != 2 || workloads[0].Labels[LabelNuma] != "0" || workloads[1].Labels[LabelNuma] != "2" {
		t.Fatalf("expected NUMA 0 and 2 on the same selected Node: %v", workloads)
	}
	plan := BuildCertificatePlan(build, workloads, true)
	if plan.Delivery == nil {
		t.Fatal("expected managed delivery")
	}
	for _, deployment := range append(workloads, plan.Delivery) {
		pod := &deployment.Spec.Template.Spec
		assertExactNodePin(t, pod, build.NodeName)
		if deployment.Spec.Template.Labels[LabelNode] != build.NodeName {
			t.Fatalf("%s lost the selected Node label", deployment.Name)
		}
		// A wildcard NoSchedule toleration includes the scheduler's cordon taint.
		if !slices.ContainsFunc(pod.Tolerations, func(tol corev1.Toleration) bool {
			return tol.Key == "" && tol.Operator == corev1.TolerationOpExists && tol.Effect == corev1.TaintEffectNoSchedule
		}) {
			t.Fatalf("%s lost the existing default cordon toleration", deployment.Name)
		}
	}
}

func renderPatchedNodePlacement() ([]*appsv1.Deployment, error) {
	patch := api.NamedPatch{Name: "scheduling", Patch: runtime.RawExtension{Raw: []byte(`{
  "spec": {"template": {"metadata": {"labels": {"yanet.yanet-platform.io/node": "wrong"}}, "spec": {
    "nodeName": "other-node",
    "nodeSelector": {"kubernetes.io/hostname": "custom-host"},
    "tolerations": null,
    "affinity": {"nodeAffinity": {
      "preferredDuringSchedulingIgnoredDuringExecution": [{"weight": 5, "preference": {"matchExpressions": [{"key": "fast", "operator": "Exists"}]}}],
      "requiredDuringSchedulingIgnoredDuringExecution": {"nodeSelectorTerms": [
        {"matchExpressions": [{"key": "zone", "operator": "In", "values": ["edge"]}]},
        {"matchFields": [{"key": "metadata.name", "operator": "In", "values": ["other-node"]}]},
        {}
      ]}
    }}
  }}}
}`)}}
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "fixture", Tag: "example"}, Patches: []string{patch.Name},
	}
	return RenderDeployments(ctx(), component, NewPatchRegistry([]api.NamedPatch{patch}))
}

func TestRenderNodePinIntersectsPatchConstraints(t *testing.T) {
	workloads, err := renderPatchedNodePlacement()
	if err != nil {
		t.Fatal(err)
	}
	pod := &workloads[0].Spec.Template.Spec
	pin := corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}}
	want := []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"edge"}}}, MatchFields: []corev1.NodeSelectorRequirement{pin}},
		{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"other-node"}}, pin}},
		{}, // Kubernetes empty terms match no nodes.
	}
	if diff := cmp.Diff(want, requiredNodeTerms(t, pod)); diff != "" {
		t.Fatalf("patch required affinity was weakened or the exact pin was lost (-want +got):\n%s", diff)
	}
	if !HasExactNodePin(pod, "node-1") || HasExactNodePin(pod, "other-node") {
		t.Fatal("ownership checks must recognize only the operator pin despite contradictory or empty terms")
	}
	if workloads[0].Spec.Template.Labels[LabelNode] != "node-1" || pod.NodeSelector[corev1.LabelHostname] != "custom-host" {
		t.Fatalf("placement identity or custom hostname selector was lost: %+v", workloads[0].Spec.Template)
	}
	if len(pod.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatal("preferred node affinity was lost")
	}
	// Removing the default tolerations lets the scheduler reject a cordoned
	// node. The operator must neither restore tolerations nor force-bind.
	if len(pod.Tolerations) != 0 {
		t.Fatalf("patch removed tolerations, got %v", pod.Tolerations)
	}
}
