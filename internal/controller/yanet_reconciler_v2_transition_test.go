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
	"errors"
	"reflect"
	"strings"
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func hostPortTransitionConfigV2() yanetv2alpha1.YanetConfigSpec {
	config := minimalConfigV2()
	config.HostNetworkPortRange = &yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20020}
	config.Patches = []yanetv2alpha1.NamedPatch{{
		Name: "host-network", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"hostNetwork":true}}}}`)},
	}}
	config.BoxTypes[0].Components.Controlplane.Patches = []string{"host-network"}
	config.Components.Operators = []yanetv2alpha1.OperatorSpec{{
		Name: "route", Containers: []yanetv2alpha1.OperatorContainer{{
			Name: "route", Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/route", Tag: "v1"},
		}},
	}}
	config.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{
		"route": {Patches: []string{"host-network"}},
	}
	return config
}

func transitionDeploymentV2(t *testing.T, r *YanetV2Reconciler, component string) *appsv1.Deployment {
	t.Helper()
	list := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), list, client.InNamespace("yanet"),
		client.MatchingLabels{manifests.LabelComponent: component}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected one %s Deployment, got %d", component, len(list.Items))
	}
	return list.Items[0].DeepCopy()
}

func TestReconcileV2PortTransition_BlocksCrossDeploymentReuseBeforeApply(t *testing.T) {
	for _, change := range []string{"remove-listener", "insert-earlier-listener", "shift-range"} {
		t.Run(change, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
			snapshot.Config = hostPortTransitionConfigV2()
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			route := transitionDeploymentV2(t, r, "route")
			if port := route.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; port != 20002 {
				t.Fatalf("fixture must reserve port 20002 for route, got %d", port)
			}
			before := &appsv1.DeploymentList{}
			if err := r.List(testContext, before); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "remove-listener":
				snapshot.Config.BoxTypes[0].Components.Controlplane = nil
			case "insert-earlier-listener":
				// Allocation follows palette declaration order, not operator names.
				// Prepend alpha so it takes route's still-live port 20002.
				snapshot.Config.Components.Operators = append([]yanetv2alpha1.OperatorSpec{{
					Name: "alpha", Containers: []yanetv2alpha1.OperatorContainer{{
						Name: "alpha", Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/alpha", Tag: "v1"},
					}},
				}}, snapshot.Config.Components.Operators...)
				snapshot.Config.BoxTypes[0].Operators["alpha"] = yanetv2alpha1.BoxOperator{Patches: []string{"host-network"}}
			case "shift-range":
				snapshot.Config.HostNetworkPortRange.Start++
			}
			// This would create a ConfigMap before the Deployment loop unless the
			// entire live-port check happens in preflight.
			snapshot.Config.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "new config"}
			result, err := reviewReconcileV2(testContext, r, yanet)
			if err == nil || !strings.Contains(err.Error(), "host-port migration") || !strings.Contains(err.Error(), "stop") {
				t.Fatalf("expected actionable migration refusal, got %+v, %v", result, err)
			}
			if change == "insert-earlier-listener" && (!strings.Contains(err.Error(), "TCP port 20002") || !strings.Contains(err.Error(), route.Name)) {
				t.Fatalf("expected refusal to reuse route's live port 20002, got %v", err)
			}
			after := &appsv1.DeploymentList{}
			if err := r.List(testContext, after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Items, after.Items) {
				t.Fatal("unsafe migration partially updated or pruned Deployments")
			}
			cms := &corev1.ConfigMapList{}
			if err := r.List(testContext, cms); err != nil || len(cms.Items) != 0 {
				t.Fatalf("unsafe migration wrote inline ConfigMaps: %v, count=%d", err, len(cms.Items))
			}
			current := &yanetv2alpha1.YanetV2{}
			if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), current); err != nil {
				t.Fatal(err)
			}
			degraded := false
			for _, condition := range current.Status.Conditions {
				if condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue && condition.Reason == "ResourcePreflightFailed" {
					degraded = true
				}
			}
			if !degraded {
				t.Fatalf("missing preflight degradation: %+v", current.Status.Conditions)
			}
		})
	}
}

func TestReconcileV2PortTransition_AppendingListenerPreservesExistingPort(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = hostPortTransitionConfigV2()
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Components.Operators = append(snapshot.Config.Components.Operators, yanetv2alpha1.OperatorSpec{
		Name: "alpha", Containers: []yanetv2alpha1.OperatorContainer{{
			Name: "alpha", Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/alpha", Tag: "v1"},
		}},
	})
	snapshot.Config.BoxTypes[0].Operators["alpha"] = yanetv2alpha1.BoxOperator{Patches: []string{"host-network"}}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatalf("appending a listener must not be mistaken for cross-Deployment reuse: %v", err)
	}
	for component, wantPort := range map[string]int32{"route": 20002, "alpha": 20003} {
		deployment := transitionDeploymentV2(t, r, component)
		if port := deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; port != wantPort {
			t.Errorf("%s listener: got %d, want %d", component, port, wantPort)
		}
	}
}

func TestReconcileV2PortTransition_WaitsForOldPodAfterDeploymentDeletion(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = hostPortTransitionConfigV2()
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	old := transitionDeploymentV2(t, r, "controlplane")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old-controlplane-pod", Namespace: yanet.Namespace},
		Spec: *old.Spec.Template.Spec.DeepCopy()}
	pod.Spec.NodeName = "test-node"
	if err := r.Create(testContext, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(testContext, old); err != nil {
		t.Fatal(err)
	}
	snapshot.Config.BoxTypes[0].Components.Controlplane = nil
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), pod.Name) {
		t.Fatalf("old Pod still holds the reassigned port: %v", err)
	}
	if err := r.Delete(testContext, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
		t.Fatalf("migration should proceed after old Pods terminate: %v", err)
	}
	route := transitionDeploymentV2(t, r, "route")
	if got := route.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; got != 20000 {
		t.Fatalf("route must receive released port 20000, got %d", got)
	}
}

func TestReconcileV2PortTransition_RecreateExemptionRequiresOwnerChain(t *testing.T) {
	for _, sameDeployment := range []bool{true, false} {
		name := "same-deployment"
		if !sameDeployment {
			name = "same-labels-foreign-replicaset"
		}
		t.Run(name, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
			snapshot.Config = hostPortTransitionConfigV2()
			if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			deployment := transitionDeploymentV2(t, r, "controlplane")
			deployment.UID = "current-deployment"
			if err := r.Update(testContext, deployment); err != nil {
				t.Fatal(err)
			}
			controller := true
			ownerUID := deployment.UID
			if !sameDeployment {
				ownerUID = "previous-deployment-instance"
			}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
				Name: "previous-replicaset", Namespace: yanet.Namespace, UID: "replicaset-uid",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment",
					Name: deployment.Name, UID: ownerUID, Controller: &controller}},
			}}
			pod := &corev1.Pod{ObjectMeta: *deployment.Spec.Template.ObjectMeta.DeepCopy(), Spec: *deployment.Spec.Template.Spec.DeepCopy()}
			pod.Name, pod.Namespace, pod.Spec.NodeName = "old-pod", yanet.Namespace, "test-node"
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &controller}}
			for _, object := range []client.Object{rs, pod} {
				if err := r.Create(testContext, object); err != nil {
					t.Fatal(err)
				}
			}
			snapshot.Config.Components.Controlplane.Image.Tag = "v2"
			_, err := reviewReconcileV2(testContext, r, yanet)
			if sameDeployment && err != nil {
				t.Fatalf("Recreate already serializes Pods of this Deployment: %v", err)
			}
			if !sameDeployment && (err == nil || !strings.Contains(err.Error(), pod.Name)) {
				t.Fatalf("matching labels must not exempt an old resource instance: %v", err)
			}
		})
	}
}

func TestReconcileV2PortTransition_ForeignReservations(t *testing.T) {
	for _, test := range []struct {
		name        string
		deployment  bool
		hostNetwork bool
		hostPort    int32
		protocol    corev1.Protocol
		nodeName    string
		phase       corev1.PodPhase
		terminating bool
		conflict    bool
	}{
		{name: "foreign-deployment", deployment: true, hostNetwork: true, nodeName: "test-node", conflict: true},
		{name: "foreign-host-network-pod", hostNetwork: true, nodeName: "test-node", conflict: true},
		{name: "foreign-pod-host-port", hostPort: 20000, nodeName: "test-node", conflict: true},
		{name: "terminating-pod", hostNetwork: true, nodeName: "test-node", terminating: true, conflict: true},
		{name: "different-protocol", hostPort: 20000, protocol: corev1.ProtocolUDP, nodeName: "test-node"},
		{name: "different-node", hostNetwork: true, nodeName: "another-node"},
		{name: "terminal-pod", hostNetwork: true, nodeName: "test-node", phase: corev1.PodSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			podSpec := corev1.PodSpec{NodeName: test.nodeName, HostNetwork: test.hostNetwork,
				Containers: []corev1.Container{{Name: "foreign", Ports: []corev1.ContainerPort{{
					ContainerPort: 20000, HostPort: test.hostPort, Protocol: test.protocol,
				}}}}}
			var existing client.Object = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "other-namespace"},
				Spec: podSpec, Status: corev1.PodStatus{Phase: test.phase}}
			if test.deployment {
				existing = &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "other-namespace"},
					Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}}}
			}
			if test.terminating {
				now := metav1.Now()
				existing.SetDeletionTimestamp(&now)
				existing.SetFinalizers([]string{"example.com/wait"})
			}
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2(), existing)
			snapshot.Config = hostPortTransitionConfigV2()
			_, err := reviewReconcileV2(testContext, r, yanet)
			if test.conflict && (err == nil || !strings.Contains(err.Error(), "other-namespace/foreign")) {
				t.Fatalf("foreign reservation must prevent rollout: %v", err)
			}
			if !test.conflict && err != nil {
				t.Fatalf("nonconflicting reservation must not prevent rollout: %v", err)
			}
		})
	}
}

func TestReconcileV2PortTransition_OldReplicaSetReservesWithoutPods(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	one := int32(1)
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old-replicaset", Namespace: yanet.Namespace},
		Spec: appsv1.ReplicaSetSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			NodeName: "test-node", HostNetwork: true,
			Containers: []corev1.Container{{Name: "old", Ports: []corev1.ContainerPort{{ContainerPort: 20000}}}},
		}}}}
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2(), rs)
	snapshot.Config = hostPortTransitionConfigV2()
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), rs.Name) {
		t.Fatalf("a live old ReplicaSet may still create a conflicting Pod: %v", err)
	}
}

func TestReconcileV2PortTransition_DesiredPodHostPortChecksLiveHostNetwork(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "other-namespace"},
		Spec: corev1.PodSpec{NodeName: "test-node", HostNetwork: true,
			Containers: []corev1.Container{{Name: "test", Ports: []corev1.ContainerPort{{ContainerPort: 25000}}}},
		}}
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2(), pod)
	snapshot.Config = minimalConfigV2()
	snapshot.Config.Patches = []yanetv2alpha1.NamedPatch{{Name: "host-port", Patch: runtime.RawExtension{Raw: []byte(
		`{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","ports":[{"name":"external","containerPort":9000,"hostPort":25000}]}]}}}}`,
	)}}}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"host-port"}
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "25000") {
		t.Fatalf("Pod-network hostPort must respect the live host-network listener: %v", err)
	}
}

func TestReconcileV2PortTransition_LiveReadFailureFailsClosed(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanetV2()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
	snapshot.Config = hostPortTransitionConfigV2()
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*appsv1.ReplicaSetList); ok {
				return errors.New("ReplicaSet read forbidden")
			}
			return c.List(ctx, list, opts...)
		},
	})
	if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "read forbidden") {
		t.Fatalf("unverifiable live ports must fail preflight: %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(testContext, deployments); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("live-read failure must prevent workload creation: %v, count=%d", err, len(deployments.Items))
	}
}
