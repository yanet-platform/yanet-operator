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

// ServicePlan describes one box-type-wide Service reconciled by the
// YanetConfigV2 controller.
type ServicePlan struct {
	Name      string
	BoxType   string
	Component string
	Selector  map[string]string
	Ports     []ServicePortPlan
	Local     bool
}

// ServicePortPlan describes one named Service port.
type ServicePortPlan struct {
	Name           string
	Port           int32
	TargetPort     int32
	TargetPortName string
}

// Validate checks the fields that would otherwise be rejected by the
// Kubernetes API after workloads had already been updated.
func (p ServicePlan) Validate() error {
	if errs := k8svalidation.IsDNS1035Label(p.Name); len(errs) > 0 {
		return fmt.Errorf("invalid Service name %q: %s", p.Name, strings.Join(errs, "; "))
	}
	if p.BoxType == "" {
		return fmt.Errorf("Service %q has an empty box type", p.Name)
	}
	if p.Component == "" {
		return fmt.Errorf("Service %q has an empty component", p.Name)
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

// BuildServices returns the stable Service plans for one component. Services
// are unconditional for every service-backed box slot, even when a particular
// YanetV2 or NUMA workload is scaled to zero. Dataplane and BIRD intentionally
// have no application Service.
func BuildServices(ctx BuildContextV2, component *helpers.ResolvedComponent) []ServicePlan {
	listeners := ListenerPorts(component)
	if len(listeners) == 0 {
		return nil
	}
	if component.Kind != helpers.KindControlplane {
		return []ServicePlan{buildServicePlan(ctx, component, nil, listeners)}
	}

	numa := effectiveNuma(ctx, component)
	plans := make([]ServicePlan, 0, numa)
	for index := int32(0); index < numa; index++ {
		index := index
		plans = append(plans, buildServicePlan(ctx, component, &index, listeners))
	}
	return plans
}

func buildServicePlan(
	ctx BuildContextV2,
	component *helpers.ResolvedComponent,
	numa *int32,
	listeners []ListenerPort,
) ServicePlan {
	selector := map[string]string{
		labelBoxType:   ctx.BoxType,
		labelComponent: component.Name,
	}
	if numa != nil {
		selector[labelNuma] = fmt.Sprintf("%d", *numa)
	}
	ports := make([]ServicePortPlan, 0, len(listeners))
	for _, listener := range listeners {
		ports = append(ports, ServicePortPlan{
			Name:           listener.Name,
			Port:           listener.ServicePort,
			TargetPortName: listener.Name,
		})
	}
	return ServicePlan{
		Name:      SharedServiceName(ctx.BoxType, component.Name, numa),
		BoxType:   ctx.BoxType,
		Component: component.Name,
		Selector:  selector,
		Ports:     ports,
		Local:     true,
	}
}

// SharedServiceName returns the deterministic Service name for one box-type
// component role. Names are readable until the Kubernetes limit requires a
// stable hash suffix.
func SharedServiceName(boxType, component string, numa *int32) string {
	name := fmt.Sprintf("yanet-%s-%s", toLowerKebab(boxType), toLowerKebab(component))
	if numa != nil {
		name = fmt.Sprintf("%s-numa%d", name, *numa)
	}
	if len(name) <= 63 {
		return name
	}
	const hashLength = 8
	prefixLength := 63 - hashLength - 1
	prefix := strings.TrimRight(name[:prefixLength], "-")
	return fmt.Sprintf("%s-%s", prefix, shortHashStr(name))
}

// ToService materialises a ServicePlan into a corev1.Service. The shared
// Service is owned by the cluster-scoped YanetConfigV2 singleton rather than a
// particular YanetV2 installation.
func (p ServicePlan) ToService(namespace string, owner metav1.OwnerReference) *corev1.Service {
	labels := map[string]string{
		labelSharedService: "true",
		labelBoxType:       p.BoxType,
		labelComponent:     p.Component,
	}
	if numa, ok := p.Selector[labelNuma]; ok {
		labels[labelNuma] = numa
	}
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: p.Selector,
		},
	}
	if owner.Name != "" {
		service.OwnerReferences = []metav1.OwnerReference{owner}
	}
	for i := range p.Ports {
		port := &p.Ports[i]
		targetPort := intstr.FromInt32(port.TargetPort)
		if port.TargetPortName != "" {
			targetPort = intstr.FromString(port.TargetPortName)
		} else if port.TargetPort == 0 {
			targetPort = intstr.FromInt32(port.Port)
		}
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
			Name:       port.Name,
			Port:       port.Port,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: targetPort,
		})
	}
	if p.Local {
		policy := corev1.ServiceInternalTrafficPolicyLocal
		service.Spec.InternalTrafficPolicy = &policy
	}
	return service
}
