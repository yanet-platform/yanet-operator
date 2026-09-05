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

	"github.com/google/go-cmp/cmp"
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
			component: &helpers.ResolvedComponent{Kind: helpers.KindBirdAdapter, Name: "birdAdapter"},
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
			ctx := ctxV2()
			ctx.NumaCount = 2
			tt.component.Image = helpers.ResolvedImage{Name: "component", Tag: "v2"}
			if tt.component.Kind == helpers.KindOperator {
				tt.component.Containers = []helpers.ResolvedContainer{{
					Name: "operator", Image: tt.component.Image,
				}}
			}
			deployments, err := BuildDeployments(ctx, tt.component)
			if err != nil {
				t.Fatalf("BuildDeployments: %v", err)
			}
			plans := BuildServices(ctx, tt.component)
			if len(plans) != tt.wantCount {
				t.Fatalf("plans = %+v, want count %d", plans, tt.wantCount)
			}
			for index := range plans {
				plan := plans[index]
				if err := plan.Validate(); err != nil {
					t.Fatalf("generated Service plan is invalid: %v", err)
				}
				if diff := cmp.Diff(tt.wantPorts, plan.Ports); diff != "" {
					t.Fatalf("ports mismatch (-want +got):\n%s", diff)
				}
				if index >= len(deployments) {
					t.Fatalf("Service %s has no matching Deployment", plan.Name)
				}
				pod := &deployments[index].Spec.Template
				for key, value := range plan.Selector {
					if key == labelYanet || key == labelNode {
						t.Errorf("shared Service selector contains installation identity: %v", plan.Selector)
					}
					if pod.Labels[key] != value {
						t.Errorf("Service %s selector %s=%s does not match Pod labels: %v", plan.Name, key, value, pod.Labels)
					}
				}
				for _, target := range plan.Ports {
					found := false
					for _, containers := range [][]corev1.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
						for _, container := range containers {
							for _, port := range container.Ports {
								if port.Name == target.TargetPortName && port.ContainerPort == target.Port &&
									port.Protocol == corev1.ProtocolTCP {
									found = true
								}
							}
						}
					}
					if !found {
						t.Errorf("Service %s target %q:%d has no Pod listener", plan.Name, target.TargetPortName, target.Port)
					}
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
			Name:      "invalid.name",
			BoxType:   "firewall",
			Component: "route",
			Selector:  map[string]string{"app": "x"},
			Ports:     []ServicePortPlan{{Name: ListenerGRPC, Port: ServiceGRPCPort}},
		},
		{
			Name:      "valid-name",
			BoxType:   "firewall",
			Component: "route",
			Selector:  map[string]string{"app": "x"},
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
