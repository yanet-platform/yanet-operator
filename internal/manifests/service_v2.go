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

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ServicePlan describes one Service the v2 reconciler should
// reconcile. The reconciler converts each plan to a corev1.Service
// via ToService() and runs CreateOrUpdate.
type ServicePlan struct {
	Name string
	// PerNode is true for the per-node Local Service (selector
	// pinned by node label). False for cluster-wide Services.
	PerNode  bool
	NodeName string
	// Selector is the final label selector merged from labelComponent
	// + (optionally) labelNuma + (optionally) labelNode.
	Selector map[string]string
	// Port is the Service port; TargetPort defaults to it when
	// TargetPortName is empty.
	Port           int32
	TargetPort     int32
	TargetPortName string
	// Local sets internalTrafficPolicy=Local. Used for per-node
	// controlplane-numa{N} and per-operator Services to keep
	// in-node calls in-node.
	Local bool
}

// BuildServices returns the full set of ServicePlan objects for one
// resolved component, given the build context.
//
// The reconciler aggregates plans across all components on a node
// (and cluster-wide for the round-robin Services), de-duplicates by
// Name, and creates/updates each one.
func BuildServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c == nil {
		return nil
	}
	switch c.Kind {
	case helpers.KindControlplane:
		return buildControlplaneServices(ctx, c)
	case helpers.KindOperator:
		return buildOperatorServices(c)
	default:
		return buildSimpleServices(ctx, c)
	}
}

// buildControlplaneServices renders three Service categories:
//  1. controlplane-numa{N}: per-node Local Service for each NUMA index.
//     Selector: app=controlplane, numa=N, node=<host>.
//  2. controlplane-numa{N}-cluster: cluster-wide RR Service per NUMA.
//     Selector: app=controlplane, numa=N.
//  3. controlplane-all: cluster-wide RR Service across all instances.
//     Selector: app=controlplane.
//
// All listen on Port + numa_index (per instance), but the cluster-wide
// Services route to the unified Port for ease of client config.
func buildControlplaneServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c.Port == 0 {
		return nil
	}
	numa := effectiveNuma(ctx, c)
	plans := make([]ServicePlan, 0, int(numa)*2+1)

	// per-node Local + cluster-wide RR for each NUMA
	for i := int32(0); i < numa; i++ {
		port := c.Port + i
		base := map[string]string{
			labelComponent: c.Name,
			"app":          c.Name,
			labelNuma:      fmt.Sprintf("%d", i),
		}
		if ctx.NodeName != "" {
			perNode := copyMap(base)
			perNode[labelNode] = ctx.NodeName
			plans = append(plans, ServicePlan{
				Name:       fmt.Sprintf("%s-%s-numa%d", ctx.YanetName, helpers.ShortNodeKey(ctx.NodeName), i),
				PerNode:    true,
				NodeName:   ctx.NodeName,
				Selector:   perNode,
				Port:       port,
				TargetPort: port,
				Local:      true,
			})
		}
		plans = append(plans, ServicePlan{
			Name:       fmt.Sprintf("%s-%s-numa%d-cluster", ctx.YanetName, toLowerKebab(c.Name), i),
			Selector:   base,
			Port:       port,
			TargetPort: port,
		})
	}

	// cluster-wide RR across all NUMA instances on Port (round
	// robin entry point for unaware clients).
	plans = append(plans, ServicePlan{
		Name: fmt.Sprintf("%s-%s-all", ctx.YanetName, toLowerKebab(c.Name)),
		Selector: map[string]string{
			labelComponent: c.Name,
			"app":          c.Name,
		},
		Port:       c.Port,
		TargetPort: c.Port,
	})
	return plans
}

// buildSimpleServices returns a single cluster-wide ClusterIP Service
// for components that have a Port (bird, birdAdapter, announcer,
// dataplane). Local policy is enabled where in-node hopping is the
// expected callsite (bird/birdAdapter share /run/bird).
func buildSimpleServices(ctx BuildContextV2, c *helpers.ResolvedComponent) []ServicePlan {
	if c.Port == 0 {
		return nil
	}
	return []ServicePlan{{
		Name: fmt.Sprintf("%s-%s", ctx.YanetName, toLowerKebab(c.Name)),
		Selector: map[string]string{
			labelComponent: c.Name,
			"app":          c.Name,
		},
		Port:       c.Port,
		TargetPort: c.Port,
		Local:      c.Kind == helpers.KindBird || c.Kind == helpers.KindBirdAdapter,
	}}
}

// buildOperatorServices renders ONE cluster-wide ClusterIP Service per
// operator (when operator.port is set). internalTrafficPolicy=Local
// keeps in-node callers on the local pod.
//
// Service name == operator name (so the bird-adapter inline config can
// resolve `route-operator:9001` directly).
func buildOperatorServices(c *helpers.ResolvedComponent) []ServicePlan {
	if c.Port == 0 {
		return nil
	}
	return []ServicePlan{{
		Name: c.Name,
		Selector: map[string]string{
			labelComponent: c.Name,
			"app":          c.Name,
		},
		Port:       c.Port,
		TargetPort: c.Port,
		Local:      true,
	}}
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
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: p.Selector,
			Ports: []corev1.ServicePort{{
				Port:     p.Port,
				Protocol: corev1.ProtocolTCP,
				TargetPort: func() intstr.IntOrString {
					if p.TargetPortName != "" {
						return intstr.FromString(p.TargetPortName)
					}
					tp := p.TargetPort
					if tp == 0 {
						tp = p.Port
					}
					return intstr.FromInt32(tp)
				}(),
			}},
		},
	}
	if p.Local {
		policy := corev1.ServiceInternalTrafficPolicyLocal
		svc.Spec.InternalTrafficPolicy = &policy
	}
	return svc
}
