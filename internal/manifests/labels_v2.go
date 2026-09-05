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

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Exported aliases for the v2-builder labels so callers outside this
// package (e.g. the reconciler's prune logic) can build label
// selectors without re-declaring the strings.
const (
	// LabelYanet identifies the owning YanetV2 CR by name. Per-installation
	// Deployments and ConfigMaps carry this label; shared Services do not.
	LabelYanet = labelYanet
	// LabelBoxType groups Pods and Services across all YanetV2 installations of
	// one box type in a namespace.
	LabelBoxType = labelBoxType
	// LabelComponent identifies the component (controlplane,
	// dataplane, bird, birdAdapter, announcer, or operator name).
	LabelComponent = labelComponent
	// LabelNuma identifies the NUMA index for a controlplane
	// instance (0..numa-1).
	LabelNuma = labelNuma
	// LabelNode identifies the node a Deployment is pinned to.
	LabelNode = labelNode
	// LabelSharedService identifies Services managed by the aggregate
	// YanetConfigV2 reconciler.
	LabelSharedService = labelSharedService

	// AnnotationManagedLabels lists label keys owned by the operator.
	// Operator-internal — do not edit by hand.
	AnnotationManagedLabels = annotationManagedLabels

	// AnnotationManagedAnnotations lists annotation keys owned by the
	// operator. Operator-internal — do not edit by hand.
	AnnotationManagedAnnotations = annotationManagedAnnotations
)

// WorkloadIdentity captures operator-owned identity and placement before
// strategic patches are applied. Restoring it afterwards prevents a generic
// patch from disconnecting a Pod from its shared Service, moving it to another
// node, changing its controller owner, or enabling an unsafe rolling update.
type WorkloadIdentity struct {
	Name                 string
	Namespace            string
	OwnerReferences      []metav1.OwnerReference
	Deployment           map[string]string
	Selector             *metav1.LabelSelector
	Pod                  map[string]string
	NodeName             string
	NodeSelector         map[string]string
	Strategy             appsv1.DeploymentStrategy
	ManageNativeSidecars bool
	NativeSidecars       []corev1.Container
}

var workloadIdentityLabels = map[string]struct{}{
	labelYanet:     {},
	labelBoxType:   {},
	labelComponent: {},
	labelNuma:      {},
	labelNode:      {},
	"app":          {},
}

// CaptureWorkloadIdentity returns the operator-owned invariants of deployment.
func CaptureWorkloadIdentity(deployment *appsv1.Deployment) WorkloadIdentity {
	identity := WorkloadIdentity{}
	if deployment == nil {
		return identity
	}
	identity.Name = deployment.Name
	identity.Namespace = deployment.Namespace
	identity.OwnerReferences = append([]metav1.OwnerReference(nil), deployment.OwnerReferences...)
	identity.Deployment = reservedLabels(deployment.Labels)
	if deployment.Spec.Selector != nil {
		identity.Selector = deployment.Spec.Selector.DeepCopy()
	}
	identity.Pod = reservedLabels(deployment.Spec.Template.Labels)
	identity.NodeName = deployment.Spec.Template.Spec.NodeName
	identity.NodeSelector = copyMap(deployment.Spec.Template.Spec.NodeSelector)
	identity.Strategy = *deployment.Spec.Strategy.DeepCopy()
	identity.ManageNativeSidecars = identity.Pod[labelComponent] == string(helpers.KindDataplane)
	if identity.ManageNativeSidecars {
		for i := range deployment.Spec.Template.Spec.InitContainers {
			container := &deployment.Spec.Template.Spec.InitContainers[i]
			if isFixedNativeSidecar(container.Name) {
				identity.NativeSidecars = append(identity.NativeSidecars, *container.DeepCopy())
			}
		}
	}
	return identity
}

// RestoreWorkloadIdentity restores operator-owned invariants while preserving
// non-reserved labels and other patchable Deployment and Pod fields.
func RestoreWorkloadIdentity(deployment *appsv1.Deployment, identity WorkloadIdentity) {
	if deployment == nil {
		return
	}
	deployment.Name = identity.Name
	deployment.Namespace = identity.Namespace
	deployment.OwnerReferences = append([]metav1.OwnerReference(nil), identity.OwnerReferences...)
	deployment.Labels = mergeIdentityLabels(deployment.Labels, identity.Deployment)
	if identity.Selector == nil {
		deployment.Spec.Selector = nil
	} else {
		deployment.Spec.Selector = identity.Selector.DeepCopy()
	}
	deployment.Spec.Template.Labels = mergeIdentityLabels(deployment.Spec.Template.Labels, identity.Pod)
	deployment.Spec.Template.Spec.NodeName = identity.NodeName
	deployment.Spec.Template.Spec.NodeSelector = copyMap(identity.NodeSelector)
	deployment.Spec.Strategy = *identity.Strategy.DeepCopy()
	if identity.ManageNativeSidecars {
		restoreNativeSidecars(&deployment.Spec.Template.Spec, identity.NativeSidecars)
	}
}

func restoreNativeSidecars(pod *corev1.PodSpec, expected []corev1.Container) {
	patched := make(map[string]corev1.Container, len(expected))
	lastFixed := -1
	for i := range pod.InitContainers {
		container := pod.InitContainers[i]
		if isFixedNativeSidecar(container.Name) {
			patched[container.Name] = container
			lastFixed = i
		}
	}
	restoredExpected := make([]corev1.Container, 0, len(expected))
	for i := range expected {
		baseline := &expected[i]
		container, present := patched[baseline.Name]
		if !present {
			container = *baseline.DeepCopy()
		}
		if baseline.RestartPolicy == nil {
			container.RestartPolicy = nil
		} else {
			restartPolicy := *baseline.RestartPolicy
			container.RestartPolicy = &restartPolicy
		}
		restoredExpected = append(restoredExpected, container)
	}

	restored := make([]corev1.Container, 0, len(pod.InitContainers)+len(expected))
	if lastFixed == -1 {
		restored = append(restored, restoredExpected...)
	}
	nextExpected := 0
	for i := range pod.InitContainers {
		container := pod.InitContainers[i]
		if isFixedNativeSidecar(container.Name) {
			if nextExpected < len(restoredExpected) {
				restored = append(restored, restoredExpected[nextExpected])
				nextExpected++
			}
			if i == lastFixed {
				restored = append(restored, restoredExpected[nextExpected:]...)
				nextExpected = len(restoredExpected)
			}
			continue
		}
		restored = append(restored, container)
	}
	pod.InitContainers = restored
}

// ValidatePodContainerNames rejects patch results that Kubernetes would reject
// because regular and init containers share one name namespace.
func ValidatePodContainerNames(deployment *appsv1.Deployment) error {
	if deployment == nil {
		return nil
	}
	seen := make(map[string]string)
	check := func(containers []corev1.Container, kind string) error {
		for i := range containers {
			name := containers[i].Name
			if previous, duplicate := seen[name]; duplicate {
				return fmt.Errorf(
					"deployment %s uses container name %q in both %s and %s containers",
					deployment.Name,
					name,
					previous,
					kind,
				)
			}
			seen[name] = kind
		}
		return nil
	}
	if err := check(deployment.Spec.Template.Spec.Containers, "regular"); err != nil {
		return err
	}
	return check(deployment.Spec.Template.Spec.InitContainers, "init")
}

func isFixedNativeSidecar(name string) bool {
	return name == yanetv2alpha1.BirdSidecarContainerName ||
		name == yanetv2alpha1.NetlinkDataplaneSidecarContainerName
}

func reservedLabels(labels map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range labels {
		if _, reserved := workloadIdentityLabels[key]; reserved {
			out[key] = value
		}
	}
	return out
}

func mergeIdentityLabels(labels, identity map[string]string) map[string]string {
	if labels == nil {
		labels = make(map[string]string, len(identity))
	}
	for key := range workloadIdentityLabels {
		delete(labels, key)
	}
	for key, value := range identity {
		labels[key] = value
	}
	return labels
}
