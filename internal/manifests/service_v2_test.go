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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func serviceContextV2() BuildContextV2 {
	return BuildContextV2{BoxType: "firewall", NumaCount: 2}
}

func TestBuildServices_ListenerMatrix(t *testing.T) {
	tests := []struct {
		name      string
		component *helpers.ResolvedComponent
		wantPorts []ServicePortPlan
		wantCount int
	}{
		{
			name: "dataplane netlink sidecar",
			component: &helpers.ResolvedComponent{
				Kind: helpers.KindDataplane,
				Name: "dataplane",
				NativeSidecars: []helpers.ResolvedContainer{{
					Name: yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
				}},
			},
			wantPorts: []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: NetlinkGRPCTargetPort}},
			wantCount: 1,
		},
		{
			name:      "controlplane",
			component: &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane"},
			wantPorts: []ServicePortPlan{
				{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: ListenerGRPC},
				{Name: ListenerHTTP, Port: ServiceHTTPPort, TargetPortName: ListenerHTTP},
			},
			wantCount: 2,
		},
		{
			name:      "bird adapter",
			component: &helpers.ResolvedComponent{Kind: helpers.KindBirdAdapter, Name: "bird-adapter"},
			wantPorts: []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: ListenerGRPC}},
			wantCount: 1,
		},
		{
			name:      "announcer",
			component: &helpers.ResolvedComponent{Kind: helpers.KindAnnouncer, Name: "announcer"},
			wantPorts: []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: ListenerGRPC}},
			wantCount: 1,
		},
		{
			name:      "generic operator",
			component: &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "mirror"},
			wantPorts: []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: ListenerGRPC}},
			wantCount: 1,
		},
		{
			name:      "metrics operator",
			component: &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "metrics"},
			wantPorts: []ServicePortPlan{{Name: ListenerHTTP, Port: ServiceHTTPPort, TargetPortName: ListenerHTTP}},
			wantCount: 1,
		},
		{name: "dataplane without sidecar", component: &helpers.ResolvedComponent{Kind: helpers.KindDataplane, Name: "dataplane"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plans := BuildServices(serviceContextV2(), tt.component)
			if len(plans) != tt.wantCount {
				t.Fatalf("plans = %+v, want count %d", plans, tt.wantCount)
			}
			for index := range plans {
				if fmt.Sprint(plans[index].Ports) != fmt.Sprint(tt.wantPorts) {
					t.Fatalf("ports = %+v, want %+v", plans[index].Ports, tt.wantPorts)
				}
			}
		})
	}
}

func TestBuildServices_DataplaneNetlinkSidecar(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane,
		Name: "dataplane",
		NativeSidecars: []helpers.ResolvedContainer{{
			Name: yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
		}},
	}
	plans := BuildServices(serviceContextV2(), component)
	if len(plans) != 1 {
		t.Fatalf("plans = %+v, want one netlink sidecar Service", plans)
	}
	plan := plans[0]
	if plan.Name != "yanet-firewall-netlink-dataplane-sidecar" ||
		plan.Component != yanetv2alpha1.NetlinkDataplaneSidecarContainerName {
		t.Fatalf("dataplane sidecar Service identity = %+v", plan)
	}
	if plan.Selector[LabelComponent] != "dataplane" || !plan.Local {
		t.Fatalf("dataplane sidecar Service selector/locality = %+v", plan)
	}
}

func TestBuildServices_ControlplaneSharedPerNuma(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind:         helpers.KindControlplane,
		Name:         "controlplane",
		Numa:         2,
		Enabled:      false,
		DisabledNuma: []int32{1},
	}
	plans := BuildServices(serviceContextV2(), component)
	if len(plans) != 2 {
		t.Fatalf("plans = %+v, want one for every NUMA role regardless of enablement", plans)
	}
	for index := range plans {
		plan := &plans[index]
		wantName := fmt.Sprintf("yanet-firewall-controlplane-numa%d", index)
		if plan.Name != wantName {
			t.Errorf("name = %q, want %q", plan.Name, wantName)
		}
		if plan.Selector[LabelBoxType] != "firewall" ||
			plan.Selector[LabelComponent] != "controlplane" ||
			plan.Selector[labelNuma] != fmt.Sprintf("%d", index) {
			t.Errorf("selector = %v", plan.Selector)
		}
		if _, present := plan.Selector[labelYanet]; present {
			t.Errorf("shared selector must not contain Yanet identity: %v", plan.Selector)
		}
		if _, present := plan.Selector[labelNode]; present {
			t.Errorf("shared selector must not contain node identity: %v", plan.Selector)
		}
	}
}

func TestBuildServices_OperatorIsUnconditional(t *testing.T) {
	component := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "route", Enabled: false}
	plans := BuildServices(serviceContextV2(), component)
	if len(plans) != 1 || plans[0].Name != "yanet-firewall-route" || !plans[0].Local {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestServicePlan_ToService(t *testing.T) {
	owner := metav1.OwnerReference{
		APIVersion: "yanet.yanet-platform.io/v2alpha1",
		Kind:       "YanetConfigV2",
		Name:       "config",
	}
	plan := ServicePlan{
		Name:     "yanet-firewall-controlplane-numa0",
		Selector: map[string]string{LabelBoxType: "firewall", labelNuma: "0"},
		Ports: []ServicePortPlan{
			{Name: ListenerGRPC, Port: ServiceGRPCPort, TargetPortName: ListenerGRPC},
			{Name: ListenerHTTP, Port: ServiceHTTPPort, TargetPortName: ListenerHTTP},
		},
		Local: true,
	}
	service := plan.ToService("yanet", owner)
	if service.Spec.Type != corev1.ServiceTypeClusterIP ||
		service.Spec.InternalTrafficPolicy == nil ||
		*service.Spec.InternalTrafficPolicy != corev1.ServiceInternalTrafficPolicyLocal {
		t.Fatalf("service policy = %+v", service.Spec)
	}
	if service.Spec.Ports[0].TargetPort.StrVal != ListenerGRPC ||
		service.Spec.Ports[1].TargetPort.StrVal != ListenerHTTP {
		t.Fatalf("target ports = %+v", service.Spec.Ports)
	}
	if service.Labels[LabelSharedService] != "true" || len(service.OwnerReferences) != 1 ||
		service.OwnerReferences[0].Name != "config" {
		t.Fatalf("metadata = %+v", service.ObjectMeta)
	}
}

func TestServicePlan_Validate(t *testing.T) {
	tests := []ServicePlan{
		{
			Name:     "invalid.name",
			Selector: map[string]string{"app": "x"},
			Ports:    []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort}},
		},
		{
			Name:     "valid-name",
			Selector: map[string]string{"app": "x"},
			Ports: []ServicePortPlan{
				{Name: ListenerGRPC, Port: ServiceGRPCPort},
				{Name: ListenerHTTP, Port: ServiceGRPCPort},
			},
		},
	}
	for _, plan := range tests {
		if err := plan.Validate(); err == nil {
			t.Fatalf("invalid plan accepted: %+v", plan)
		}
	}
}
