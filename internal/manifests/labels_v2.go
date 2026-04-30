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

// Exported aliases for the v2-builder labels so callers outside this
// package (e.g. the reconciler's prune logic) can build label
// selectors without re-declaring the strings.
const (
	// LabelYanet identifies the owning YanetV2 CR by name. Every
	// Deployment, Service and ConfigMap rendered for a YanetV2
	// installation carries this label.
	LabelYanet = labelYanet
	// LabelComponent identifies the component (controlplane,
	// dataplane, bird, birdAdapter, announcer, or operator name).
	LabelComponent = labelComponent
	// LabelNuma identifies the NUMA index for a controlplane
	// instance (0..numa-1).
	LabelNuma = labelNuma
	// LabelNode identifies the node a Deployment is pinned to.
	LabelNode = labelNode

	// AnnotationManagedLabels lists label keys owned by the operator.
	// Operator-internal — do not edit by hand.
	AnnotationManagedLabels = annotationManagedLabels

	// AnnotationManagedAnnotations lists annotation keys owned by the
	// operator. Operator-internal — do not edit by hand.
	AnnotationManagedAnnotations = annotationManagedAnnotations
)
