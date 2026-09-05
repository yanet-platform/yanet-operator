/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"strings"
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestYanetV2ReconcileReservesPatchedPodNetworkHostPorts(t *testing.T) {
	ctx := context.Background()
	config := minimalConfigV2()
	config.HostNetworkPortRange = &yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20002}
	config.Patches = []yanetv2alpha1.NamedPatch{
		{Name: "host-network", Patch: runtime.RawExtension{Raw: []byte(`{
			"spec":{"template":{"spec":{"hostNetwork":true}}}
		}`)}},
		{Name: "host-port", Patch: runtime.RawExtension{Raw: []byte(`{
			"spec":{"template":{"spec":{"containers":[{"name":"dataplane","ports":[
				{"name":"external","containerPort":9000,"hostPort":20000}
			]}]}}}
		}`)}},
	}
	config.BoxTypes[0].Components.Controlplane.Patches = []string{"host-network"}
	config.BoxTypes[0].Components.Dataplane.Patches = []string{"host-port"}
	r, yanet := reconcilerForHostPortReviewTest(t, config)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.MatchingLabels{manifests.LabelComponent: "controlplane"}); err != nil {
		t.Fatalf("list controlplane: %v", err)
	}
	if len(deployments.Items) != 1 {
		t.Fatalf("expected one controlplane, got %d", len(deployments.Items))
	}
	assertListenerPortAndEnv(t, &deployments.Items[0], "controlplane",
		manifests.ListenerGRPC, manifests.EnvKubernetesGRPCPort, 20001)
	assertListenerPortAndEnv(t, &deployments.Items[0], "controlplane",
		manifests.ListenerHTTP, manifests.EnvKubernetesHTTPPort, 20002)
}

func TestYanetV2ReconcileRejectsPatchedHostPortCollisions(t *testing.T) {
	for _, tt := range []struct {
		name       string
		patch      string
		otherPatch string
	}{
		{
			name: "different pod-network workloads",
			patch: `{"spec":{"template":{"spec":{"containers":[{"name":"controlplane","ports":[
				{"name":"external","containerPort":9001,"hostPort":20000}
			]}]}}}}`,
			otherPatch: `{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","ports":[
				{"name":"external","containerPort":9002,"hostPort":20000}
			]}]}}}}`,
		},
		{
			name: "concurrent containers in one pod",
			patch: `{"spec":{"template":{"spec":{"containers":[
				{"name":"controlplane","ports":[{"name":"external","containerPort":9001,"hostPort":20000}]},
				{"name":"proxy","image":"docker.io/test/proxy:v1","ports":[{"name":"proxy","containerPort":9002,"hostPort":20000}]}
			]}}}}`,
		},
		{
			name: "multiple pod-network replicas pinned to one node",
			patch: `{"spec":{"replicas":2,"template":{"spec":{"containers":[{"name":"controlplane","ports":[
				{"name":"external","containerPort":9001,"hostPort":20000}
			]}]}}}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			config := minimalConfigV2()
			config.Patches = []yanetv2alpha1.NamedPatch{{
				Name: "host-port", Patch: runtime.RawExtension{Raw: []byte(tt.patch)},
			}}
			config.BoxTypes[0].Components.Controlplane.Patches = []string{"host-port"}
			if tt.otherPatch != "" {
				config.Patches = append(config.Patches, yanetv2alpha1.NamedPatch{
					Name: "other-host-port", Patch: runtime.RawExtension{Raw: []byte(tt.otherPatch)},
				})
				config.BoxTypes[0].Components.Dataplane.Patches = []string{"other-host-port"}
			}
			r, yanet := reconcilerForHostPortReviewTest(t, config)
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(yanet)}); err == nil ||
				!strings.Contains(err.Error(), "20000") {
				t.Fatalf("expected host-port collision before apply, got %v", err)
			}
			deployments := &appsv1.DeploymentList{}
			if err := r.List(ctx, deployments); err != nil {
				t.Fatalf("list Deployments: %v", err)
			}
			if len(deployments.Items) != 0 {
				t.Fatalf("host-port collision must prevent all workload writes, got %d", len(deployments.Items))
			}
			current := &yanetv2alpha1.YanetV2{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(yanet), current); err != nil {
				t.Fatalf("get installation status: %v", err)
			}
			degraded := false
			for _, condition := range current.Status.Conditions {
				if condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue &&
					condition.Reason == "ResourcePreflightFailed" {
					degraded = true
				}
			}
			if !degraded {
				t.Fatalf("host-port collision was not reported in status: %+v", current.Status.Conditions)
			}
		})
	}
}

func reconcilerForHostPortReviewTest(t *testing.T, config yanetv2alpha1.YanetConfigSpec) (*YanetV2Reconciler, *yanetv2alpha1.YanetV2) {
	t.Helper()
	autoSync := true
	yanet := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "yanet", UID: "yanet-uid", Finalizers: []string{yanetFinalizer},
		},
		Spec: yanetv2alpha1.YanetSpec{BoxType: "release", AutoSync: &autoSync},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}
	r, snapshot := makeReconcilerEnv(t, yanet, node)
	snapshot.Config = config
	return r, yanet
}
