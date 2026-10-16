package controller

import (
	"context"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestReconcileInstallationOverridesAfterPatches(t *testing.T) {
	y := reviewInstallation("overrides", "node-1")
	y.Spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{
		YanetComponentOverride: api.YanetComponentOverride{Enabled: helpers.PtrTrue(), Containers: map[string]api.YanetContainerOverride{"dataplane": {Tag: "candidate"}}},
		Hugepages:              &api.Hugepages{Size: "2Mi", Count: 1024},
	}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: y.Spec.NodeSelector}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{"hugepages-2Mi": resource.MustParse("8Gi")},
	}}
	r, snapshot := makeReconcilerEnv(t, y, node)
	snapshot.Config = minimalConfig()
	snapshot.Config.Components.Dataplane.Hugepages = &api.Hugepages{Size: "1Gi", Count: 4}
	snapshot.Config.Patches = []api.NamedPatch{{Name: "standby", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"replicas":0,"template":{"spec":{"containers":[{"name":"dataplane","image":"patched:old","resources":{"requests":{"hugepages-1Gi":"4Gi"},"limits":{"hugepages-1Gi":"4Gi"}}}]}}}}`)}}}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"standby"}
	reconcileAndRead(t, r, y)
	deployment := transitionDeployment(t, r, string(helpers.KindDataplane))
	if *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Spec.Containers[0].Image != "dp:candidate" {
		t.Fatalf("typed overrides lost: %+v", deployment.Spec)
	}
	requests := deployment.Spec.Template.Spec.Containers[0].Resources.Requests
	got := requests["hugepages-2Mi"]
	if len(requests) != 1 || got.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Fatalf("effective reservation = %v", requests)
	}
	// Global disable is stronger than the explicit component-level enablement.
	y.Spec.Enabled = helpers.PtrFalse()
	if err := r.Update(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	reconcileAndRead(t, r, y)
	for _, d := range reviewDeployments(t, r) {
		if *d.Spec.Replicas != 0 {
			t.Fatalf("installation disable lost for %s", d.Name)
		}
	}
	if snapshot.Config.Components.Dataplane.Hugepages.Count != 4 {
		t.Fatal("reconcile mutated shared hugepages")
	}
	if deployment.Labels[manifests.LabelNode] != node.Name {
		t.Fatal("node ownership lost")
	}
}
