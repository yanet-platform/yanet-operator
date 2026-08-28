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
	"fmt"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildServices_DisabledByDefault(t *testing.T) {
	components := []*helpers.ResolvedComponent{
		{Kind: helpers.KindControlplane, Name: "controlplane", GRPCPort: 8080, HTTPPort: 8081},
		{Kind: helpers.KindAnnouncer, Name: "announcer", Port: 9090},
		{Kind: helpers.KindOperator, Name: "route", Port: 9000},
	}
	for _, component := range components {
		if plans := BuildServices(ctxV2(), component); len(plans) != 0 {
			t.Errorf("%s: service.enabled=false must produce no plans: %+v", component.Name, plans)
		}
	}
}

func TestBuildServices_Controlplane_PerNuma(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane",
		GRPCPort: 8080, HTTPPort: 8081, Numa: 2, ServiceEnabled: true,
	}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want one per NUMA: %+v", len(plans), plans)
	}
	for i := range plans {
		plan := &plans[i]
		wantName := fmt.Sprintf("edge-controlplane-numa%d", i)
		if plan.Name != wantName {
			t.Errorf("plan[%d].Name = %q, want %q", i, plan.Name, wantName)
		}
		if !plan.Local {
			t.Errorf("plan[%d] must use internalTrafficPolicy=Local", i)
		}
		if plan.Selector[labelYanet] != "edge" || plan.Selector[labelComponent] != "controlplane" {
			t.Errorf("plan[%d] selector does not isolate the installation: %v", i, plan.Selector)
		}
		if plan.Selector[labelNuma] != fmt.Sprintf("%d", i) {
			t.Errorf("plan[%d] NUMA selector = %q", i, plan.Selector[labelNuma])
		}
		if _, hasNode := plan.Selector[labelNode]; hasNode {
			t.Errorf("stable per-NUMA Service must not include a node hash selector: %v", plan.Selector)
		}
		if len(plan.Ports) != 2 ||
			plan.Ports[0].Name != "grpc" || plan.Ports[0].Port != 8080 ||
			plan.Ports[1].Name != "http" || plan.Ports[1].Port != 8081 {
			t.Errorf("plan[%d] ports = %+v", i, plan.Ports)
		}
	}
}

func TestBuildServices_Controlplane_CustomNameAndDisabledNuma(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane",
		GRPCPort: 8080, HTTPPort: 8081, Numa: 2, DisabledNuma: []int32{1},
		ServiceEnabled: true, ServiceName: "gateway",
	}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 {
		t.Fatalf("plans = %d, want only NUMA 0: %+v", len(plans), plans)
	}
	if plans[0].Name != "gateway-numa0" || plans[0].Selector[labelNuma] != "0" {
		t.Fatalf("unexpected surviving plan: %+v", plans[0])
	}
}

func TestBuildServices_Controlplane_MissingPortNoPlans(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane",
		GRPCPort: 8080, ServiceEnabled: true,
	}
	if plans := BuildServices(ctxV2(), c); len(plans) != 0 {
		t.Errorf("incomplete controlplane ports must produce no plans: %+v", plans)
	}
}

func TestBuildServices_Operator_DefaultAndCustomName(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "route", Port: 9000, ServiceEnabled: true,
	}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 || plans[0].Name != "edge-route" || !plans[0].Local {
		t.Fatalf("default operator Service plan: %+v", plans)
	}
	if plans[0].Selector[labelYanet] != "edge" {
		t.Errorf("operator selector must include Yanet identity: %v", plans[0].Selector)
	}

	c.ServiceName = "route-api"
	plans = BuildServices(ctxV2(), c)
	if len(plans) != 1 || plans[0].Name != "route-api" {
		t.Fatalf("custom operator Service name ignored: %+v", plans)
	}
}

func TestBuildServices_AnnouncerIsLocal(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindAnnouncer, Name: "announcer", Port: 9090, ServiceEnabled: true,
	}
	plans := BuildServices(ctxV2(), c)
	if len(plans) != 1 || plans[0].Name != "edge-announcer" || !plans[0].Local {
		t.Fatalf("announcer Service plan: %+v", plans)
	}
}

func TestBuildServices_NilComponent(t *testing.T) {
	if got := BuildServices(ctxV2(), nil); got != nil {
		t.Errorf("nil component must return nil, got %v", got)
	}
}

func TestServicePlan_ToService_MultipleNamedPorts(t *testing.T) {
	plan := ServicePlan{
		Name:     "gateway-numa0",
		Selector: map[string]string{"a": "b"},
		Ports: []ServicePortPlan{
			{Name: "grpc", Port: 8080, TargetPortName: "grpc"},
			{Name: "http", Port: 8081, TargetPortName: "http"},
		},
		Local: true,
	}
	svc := plan.ToService("yanet", metav1.OwnerReference{Name: "edge"})
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("Type = %q, want ClusterIP", svc.Spec.Type)
	}
	if svc.Spec.InternalTrafficPolicy == nil ||
		*svc.Spec.InternalTrafficPolicy != corev1.ServiceInternalTrafficPolicyLocal {
		t.Errorf("Local plan must set internalTrafficPolicy=Local: %+v", svc.Spec.InternalTrafficPolicy)
	}
	if len(svc.Spec.Ports) != 2 ||
		svc.Spec.Ports[0].TargetPort.StrVal != "grpc" ||
		svc.Spec.Ports[1].TargetPort.StrVal != "http" {
		t.Errorf("rendered ports = %+v", svc.Spec.Ports)
	}
}

func TestServicePlan_ToService_TargetPortDefaultsToPort(t *testing.T) {
	plan := ServicePlan{
		Name: "x", Ports: []ServicePortPlan{{Name: "grpc", Port: 80}},
	}
	svc := plan.ToService("yanet", metav1.OwnerReference{Name: "edge"})
	if svc.Spec.Ports[0].TargetPort.IntVal != 80 {
		t.Errorf("TargetPort = %+v, want 80", svc.Spec.Ports[0].TargetPort)
	}
	if svc.Spec.InternalTrafficPolicy != nil {
		t.Errorf("non-Local plan must omit internalTrafficPolicy")
	}
}

func TestServicePlan_Validate(t *testing.T) {
	tests := []struct {
		name string
		plan ServicePlan
	}{
		{
			name: "invalid name",
			plan: ServicePlan{
				Name: "edge.prod", Selector: map[string]string{"app": "x"},
				Ports: []ServicePortPlan{{Name: "grpc", Port: 80}},
			},
		},
		{
			name: "duplicate ports",
			plan: ServicePlan{
				Name: "edge", Selector: map[string]string{"app": "x"},
				Ports: []ServicePortPlan{{Name: "grpc", Port: 80}, {Name: "http", Port: 80}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.plan.Validate(); err == nil {
				t.Fatalf("invalid Service plan accepted: %+v", tt.plan)
			}
		})
	}
}
