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

package manifests

import (
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildServices_Controlplane_ThreeCategories(t *testing.T) {
	ctx := ctxV2()
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane",
		Port: 8080, Numa: 2,
	}
	plans := BuildServices(ctx, c)
	// per-NUMA: 2 nodeLocal + 2 cluster + 1 -all = 5 plans.
	if len(plans) != 5 {
		t.Fatalf("plans = %d: %+v", len(plans), plans)
	}
	var perNode, cluster, all int
	for _, p := range plans {
		switch {
		case strings.HasSuffix(p.Name, "-all"):
			all++
			if p.Local {
				t.Errorf("-all plan must NOT be Local")
			}
		case strings.HasSuffix(p.Name, "-cluster"):
			cluster++
			if p.Local {
				t.Errorf("-cluster plan must NOT be Local")
			}
		case p.PerNode:
			perNode++
			if !p.Local {
				t.Errorf("per-node plan must be Local")
			}
			if p.Selector[labelNode] != "node-1" {
				t.Errorf("per-node selector missing node label: %v", p.Selector)
			}
		}
	}
	if perNode != 2 || cluster != 2 || all != 1 {
		t.Errorf("category counts perNode=%d cluster=%d all=%d", perNode, cluster, all)
	}
}

func TestBuildServices_Controlplane_PortFanout(t *testing.T) {
	ctx := ctxV2()
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane",
		Port: 8080, Numa: 2,
	}
	plans := BuildServices(ctx, c)
	have := map[int32]bool{}
	for _, p := range plans {
		have[p.Port] = true
	}
	if !have[8080] || !have[8081] {
		t.Errorf("expected ports 8080 and 8081 across plans: %v", have)
	}
}

func TestBuildServices_Controlplane_NoPort_NoPlans(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane"}
	if plans := BuildServices(ctxV2(), c); len(plans) != 0 {
		t.Errorf("no port → no plans, got %v", plans)
	}
}

func TestBuildServices_Simple_BirdLocal(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindBird, Name: "bird", Port: 179}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 || !plans[0].Local {
		t.Fatalf("bird single plan with Local=true expected: %+v", plans)
	}
}

func TestBuildServices_Simple_BirdAdapterLocal(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindBirdAdapter, Name: "birdAdapter", Port: 9700}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 || !plans[0].Local {
		t.Fatalf("birdAdapter single plan with Local=true expected: %+v", plans)
	}
}

func TestBuildServices_Simple_AnnouncerNotLocal(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindAnnouncer, Name: "announcer", Port: 9090}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	if plans[0].Local {
		t.Errorf("announcer must NOT be Local")
	}
}

func TestBuildServices_Dataplane_NotLocal(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindDataplane, Name: "dataplane", Port: 8081}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 || plans[0].Local {
		t.Errorf("dataplane: 1 plan, not Local: %+v", plans)
	}
}

func TestBuildServices_Operator_LocalAndNamedAfterOperator(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "antiddos", Port: 9001,
	}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 {
		t.Fatalf("operator plans = %d", len(plans))
	}
	p := plans[0]
	if !p.Local {
		t.Errorf("operator Service must be Local")
	}
	if p.Name != "antiddos" {
		t.Errorf("operator Service name should equal operator name: %q", p.Name)
	}
}

func TestBuildServices_Operator_NoPort_NoService(t *testing.T) {
	c := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "x"}
	if plans := BuildServices(ctxV2(), c); len(plans) != 0 {
		t.Errorf("no port → no operator service, got %v", plans)
	}
}

func TestBuildServices_NilComponent(t *testing.T) {
	if got := BuildServices(ctxV2(), nil); got != nil {
		t.Errorf("nil → nil, got %v", got)
	}
}

// --- ToService -------------------------------------------------------------

func TestServicePlan_ToService_LocalSetsTrafficPolicy(t *testing.T) {
	p := ServicePlan{Name: "x", Selector: map[string]string{"a": "b"}, Port: 80, Local: true}
	svc := p.ToService("yanet", metav1.OwnerReference{Name: "yanet"})
	if svc.Spec.InternalTrafficPolicy == nil || *svc.Spec.InternalTrafficPolicy != corev1.ServiceInternalTrafficPolicyLocal {
		t.Errorf("Local=true: internalTrafficPolicy must be Local: %+v", svc.Spec.InternalTrafficPolicy)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("Type = %q, want ClusterIP", svc.Spec.Type)
	}
}

func TestServicePlan_ToService_NotLocalNoPolicy(t *testing.T) {
	p := ServicePlan{Name: "x", Selector: map[string]string{"a": "b"}, Port: 80}
	svc := p.ToService("yanet", metav1.OwnerReference{Name: "yanet"})
	if svc.Spec.InternalTrafficPolicy != nil {
		t.Errorf("non-Local plan must omit policy: %+v", svc.Spec.InternalTrafficPolicy)
	}
}

func TestServicePlan_ToService_TargetPortDefaultsToPort(t *testing.T) {
	p := ServicePlan{Name: "x", Selector: map[string]string{"a": "b"}, Port: 80}
	svc := p.ToService("yanet", metav1.OwnerReference{Name: "yanet"})
	if svc.Spec.Ports[0].TargetPort.IntVal != 80 {
		t.Errorf("TargetPort default to Port: %+v", svc.Spec.Ports[0].TargetPort)
	}
}

func TestServicePlan_ToService_TargetPortName(t *testing.T) {
	p := ServicePlan{Name: "x", Selector: map[string]string{"a": "b"}, Port: 80, TargetPortName: "grpc"}
	svc := p.ToService("yanet", metav1.OwnerReference{Name: "yanet"})
	if svc.Spec.Ports[0].TargetPort.StrVal != "grpc" {
		t.Errorf("TargetPort by name: %+v", svc.Spec.Ports[0].TargetPort)
	}
}
