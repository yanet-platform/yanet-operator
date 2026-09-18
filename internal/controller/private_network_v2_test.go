package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func transitionDeploymentV2(t *testing.T, r *YanetV2Reconciler, component string) *appsv1.Deployment {
	t.Helper()
	list := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), list, client.InNamespace("yanet"), client.MatchingLabels{manifests.LabelComponent: component}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected one %s Deployment, got %d", component, len(list.Items))
	}
	return list.Items[0].DeepCopy()
}

func TestReconcileV2PrivateNetworkRejectsPatchesBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ name, patch, want string }{
		{"host-network", `{"spec":{"template":{"spec":{"hostNetwork":true}}}}`, "private"},
		{"host-port", `{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","ports":[{"name":"external","containerPort":9000,"hostPort":20000}]}]}}}}`, "hostPort"},
		{"init-host-port", `{"spec":{"template":{"spec":{"initContainers":[{"name":"prepare","image":"prepare","ports":[{"containerPort":9000,"hostPort":20000}]}]}}}}`, "hostPort"},
		{"multiple-dataplane-replicas", `{"spec":{"replicas":2}}`, "dataplane"},
		{"same-pod-port", `{"spec":{"template":{"spec":{"containers":[{"name":"other","image":"other","ports":[{"name":"conflict","containerPort":8080}]}]}}}}`, "8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yanet := reviewYanetV2()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
			snapshot.Config = minimalConfigV2()
			snapshot.Config.Components.Dataplane.Config = &api.ConfigSource{Inline: "opaque configuration"}
			snapshot.Config.Patches = []api.NamedPatch{{Name: "invalid", Patch: runtime.RawExtension{Raw: []byte(tc.patch)}}}
			snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"invalid"}
			if tc.name == "same-pod-port" {
				snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = nil
				snapshot.Config.BoxTypes[0].Components.Controlplane.Patches = []string{"invalid"}
			}
			if _, err := reviewReconcileV2(context.Background(), r, yanet); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s rejection: %v", tc.want, err)
			}
			deployments, configMaps := &appsv1.DeploymentList{}, &corev1.ConfigMapList{}
			if err := r.List(context.Background(), deployments); err != nil {
				t.Fatal(err)
			}
			if err := r.List(context.Background(), configMaps); err != nil {
				t.Fatal(err)
			}
			if len(deployments.Items) != 0 || len(configMaps.Items) != 0 {
				t.Fatal("preflight failure caused partial writes")
			}
			current := &api.YanetV2{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(yanet), current); err != nil {
				t.Fatal(err)
			}
			degraded := false
			for _, condition := range current.Status.Conditions {
				degraded = degraded || condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue && condition.Reason == "ResourcePreflightFailed"
			}
			if !degraded {
				t.Fatalf("missing preflight degradation: %+v", current.Status.Conditions)
			}
		})
	}
}

func TestReconcileV2PrivateNetworkDoesNotReserveNodePorts(t *testing.T) {
	yanet := reviewYanetV2()
	foreign := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "other"}, Spec: corev1.PodSpec{NodeName: "test-node", HostNetwork: true,
		Containers: []corev1.Container{{Name: "foreign", Ports: []corev1.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 8081}}}}}}
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2(), foreign)
	snapshot.Config = minimalConfigV2()
	if _, err := reviewReconcileV2(context.Background(), r, yanet); err != nil {
		t.Fatal(err)
	}
	deployment := transitionDeploymentV2(t, r, "controlplane")
	if deployment.Spec.Template.Spec.HostNetwork || deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 8080 {
		t.Fatal("private listener depends on foreign node ports")
	}
	unchanged := &corev1.Pod{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(foreign), unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.ResourceVersion != foreign.ResourceVersion {
		t.Fatal("foreign workload was modified")
	}
}
