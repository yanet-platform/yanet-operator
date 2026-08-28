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
	"strings"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// ServicePlan describes one Service the v2 reconciler should
// reconcile. The reconciler converts each plan to a corev1.Service
// via ToService() and runs CreateOrUpdate.
type ServicePlan struct {
	Name string
	// Selector is the final label selector for the installation, component,
	// and optional NUMA index.
	Selector map[string]string
	Ports    []ServicePortPlan
	// Local sets internalTrafficPolicy=Local. Used for per-node
	// controlplane-numa{N} and per-operator Services to keep
	// in-node calls in-node.
	Local bool
}

// ServicePortPlan describes one named Service port.
type ServicePortPlan struct {
	Name           string
	Port           int32
	TargetPort     int32
	TargetPortName string
}

// Validate checks the fields that would otherwise be rejected by the
// Kubernetes API after Deployments had already been updated.
func (p ServicePlan) Validate() error {
	if errs := k8svalidation.IsDNS1035Label(p.Name); len(errs) > 0 {
		return fmt.Errorf("invalid Service name %q: %s", p.Name, strings.Join(errs, "; "))
	}
	if len(p.Selector) == 0 {
		return fmt.Errorf("Service %q has an empty selector", p.Name)
	}
	if len(p.Ports) == 0 {
		return fmt.Errorf("Service %q has no ports", p.Name)
	}
	portNames := make(map[string]struct{}, len(p.Ports))
	portNumbers := make(map[int32]struct{}, len(p.Ports))
	for i := range p.Ports {
		port := &p.Ports[i]
		if port.Name == "" {
			return fmt.Errorf("Service %q port[%d] has an empty name", p.Name, i)
		}
		if _, duplicate := portNames[port.Name]; duplicate {
			return fmt.Errorf("Service %q port name %q is duplicated", p.Name, port.Name)
		}
		portNames[port.Name] = struct{}{}
		if port.Port <= 0 || port.Port > 65535 {
			return fmt.Errorf("Service %q port %d must be in 1..65535", p.Name, port.Port)
		}
		if _, duplicate := portNumbers[port.Port]; duplicate {
			return fmt.Errorf("Service %q port %d is duplicated", p.Name, port.Port)
		}
		portNumbers[port.Port] = struct{}{}
		if port.TargetPort < 0 || port.TargetPort > 65535 {
			return fmt.Errorf("Service %q target port %d must be in 0..65535", p.Name, port.TargetPort)
		}
	}
	return nil
}

// BuildServices returns the full set of ServicePlan objects for one
// resolved component, given the build context.
//
// The reconciler aggregates plans across all components and nodes,
// de-duplicates identical stable plans by Name, and creates/updates each one.
func BuildServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c == nil || !c.ServiceEnabled {
		return nil
	}
	switch c.Kind {
	case helpers.KindControlplane:
		return buildControlplaneServices(ctx, c)
	case helpers.KindOperator:
		return buildOperatorServices(ctx, c)
	default:
		return buildSimpleServices(ctx, c)
	}
}

// buildControlplaneServices renders one stable Local Service per enabled NUMA
// index. Every Service exposes the same gRPC and HTTP ports because the
// controlplanes run in separate Pod network namespaces.
func buildControlplaneServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c.GRPCPort == 0 || c.HTTPPort == 0 {
		return nil
	}
	numa := effectiveNuma(ctx, c)
	disabled := disabledNumaSet(c)
	plans := make([]ServicePlan, 0, int(numa))

	for i := int32(0); i < numa; i++ {
		if _, skip := disabled[i]; skip {
			continue
		}
		plans = append(plans, ServicePlan{
			Name: controlplaneServiceName(ctx, c, i),
			Selector: map[string]string{
				labelYanet:     ctx.YanetName,
				labelComponent: c.Name,
				"app":          c.Name,
				labelNuma:      fmt.Sprintf("%d", i),
			},
			Ports: []ServicePortPlan{
				{Name: "grpc", Port: c.GRPCPort, TargetPortName: "grpc"},
				{Name: "http", Port: c.HTTPPort, TargetPortName: "http"},
			},
			Local: true,
		})
	}
	return plans
}

// buildSimpleServices returns one Local ClusterIP Service for an explicitly
// enabled single-port component.
func buildSimpleServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c.Port == 0 {
		return nil
	}
	return []ServicePlan{{
		Name: componentServiceName(ctx, c),
		Selector: map[string]string{
			labelYanet:     ctx.YanetName,
			labelComponent: c.Name,
			"app":          c.Name,
		},
		Ports: []ServicePortPlan{{
			Name: defaultPortName(c.Kind), Port: c.Port, TargetPortName: defaultPortName(c.Kind),
		}},
		Local: true,
	}}
}

// buildOperatorServices renders one Local ClusterIP Service for an explicitly
// enabled operator.
func buildOperatorServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c.Port == 0 {
		return nil
	}
	return []ServicePlan{{
		Name: componentServiceName(ctx, c),
		Selector: map[string]string{
			labelYanet:     ctx.YanetName,
			labelComponent: c.Name,
			"app":          c.Name,
		},
		Ports: []ServicePortPlan{{Name: "grpc", Port: c.Port, TargetPortName: "grpc"}},
		Local: true,
	}}
}

func componentServiceName(ctx BuildContextV2, c *helpers.ResolvedComponent) string {
	if c.ServiceName != "" {
		return c.ServiceName
	}
	return fmt.Sprintf("%s-%s", ctx.YanetName, toLowerKebab(c.Name))
}

func controlplaneServiceName(ctx BuildContextV2, c *helpers.ResolvedComponent, numa int32) string {
	return fmt.Sprintf("%s-numa%d", componentServiceName(ctx, c), numa)
}

func serviceEndpoint(ctx BuildContextV2, c *helpers.ResolvedComponent, numa *int32, port int32) string {
	name := componentServiceName(ctx, c)
	if c.Kind == helpers.KindControlplane && numa != nil {
		name = controlplaneServiceName(ctx, c, *numa)
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", name, ctx.Namespace, port)
}

// ToService materialises a ServicePlan into a corev1.Service ready
// for CreateOrUpdate. Owner references are filled by the caller.
func (p ServicePlan) ToService(namespace string, owner metav1.OwnerReference) *corev1.Service {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            p.Name,
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: p.Selector},
	}
	for i := range p.Ports {
		port := &p.Ports[i]
		targetPort := intstr.FromInt32(port.TargetPort)
		if port.TargetPortName != "" {
			targetPort = intstr.FromString(port.TargetPortName)
		} else if port.TargetPort == 0 {
			targetPort = intstr.FromInt32(port.Port)
		}
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name: port.Name, Port: port.Port, Protocol: corev1.ProtocolTCP, TargetPort: targetPort,
		})
	}
	if p.Local {
		policy := corev1.ServiceInternalTrafficPolicyLocal
		svc.Spec.InternalTrafficPolicy = &policy
	}
	return svc
}
