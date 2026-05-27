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

package v2alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NFDNumaCountLabel is the Node Feature Discovery label that exposes
// the number of NUMA domains on the host. The operator reads it to
// decide how many controlplane Deployments to generate per node.
const NFDNumaCountLabel = "feature.node.kubernetes.io/cpu-numa_nodes_count"

// YanetSpec is the per-installation CR. Everything a "box" looks like
// (which components are deployed and how they are patched) is defined
// in YanetConfigV2.spec.boxTypes[<boxType>]. This CR is intentionally
// tiny: it only selects the target nodes and references a boxType.
//
// No patches and no inline component specs are accepted here — the
// only per-installation customisation knobs are typed point-overrides
// in components.<name>.{enabled,image}.
type YanetSpec struct {
	// BoxType selects a boxType definition from
	// YanetConfigV2.spec.boxTypes[]. Required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	BoxType string `json:"boxType"`

	// NodeSelector restricts the installation to a subset of nodes.
	// Empty selector matches all nodes (use with care).
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Enabled is the "scale-to-zero" switch for the whole
	// installation. When false, the operator still renders every
	// Deployment/Service/ConfigMap (so generated specs can be
	// inspected and patches still apply), but forces replicas=0
	// on every Deployment regardless of per-component
	// overrides. Use this to verify the rendered spec without
	// actually running pods. Defaults to true.
	//
	// To freeze the operator's view of the CR (keep existing
	// Deployments untouched, including hand edits) use
	// AutoSync=false instead.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// AutoSync enables automatic synchronization. When false, the
	// reconciler reports drift via Status without touching the
	// generated Deployments — including not creating missing
	// Deployments and not pruning orphans. Hand edits to managed
	// Deployments are preserved. Defaults to false.
	// +optional
	AutoSync *bool `json:"autoSync,omitempty"`

	// Components offers narrow, per-installation overrides for
	// individual components. Only image and enabled flags are
	// allowed. Anything else (annotations, resources, ...) lives in
	// YanetConfigV2 patches.
	// +optional
	Components *YanetComponentsOverride `json:"components,omitempty"`
}

// YanetComponentsOverride holds typed per-installation overrides for
// the 5 hardcoded components and dynamic operators (by name).
type YanetComponentsOverride struct {
	// +optional
	Controlplane *YanetComponentOverride `json:"controlplane,omitempty"`
	// +optional
	Dataplane *YanetComponentOverride `json:"dataplane,omitempty"`
	// +optional
	Bird *YanetComponentOverride `json:"bird,omitempty"`
	// +optional
	BirdAdapter *YanetComponentOverride `json:"birdAdapter,omitempty"`
	// +optional
	Announcer *YanetComponentOverride `json:"announcer,omitempty"`
	// Operators keyed by OperatorSpec.Name.
	// +optional
	Operators map[string]YanetComponentOverride `json:"operators,omitempty"`
}

// YanetComponentOverride is the only per-installation customisation
// surface for a component. Anything broader belongs to a patch.
type YanetComponentOverride struct {
	// Enabled toggles this component for this installation only.
	// true → replicas=1, false → replicas=0 (component still
	// rendered, but with zero pods).
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Containers overrides image.name and/or image.tag per
	// container, keyed by container name. For the 5 hardcoded
	// components (single-container) the key is the kind:
	// "controlplane", "dataplane", "bird", "birdAdapter",
	// "announcer". For operators the key is the
	// YanetConfigV2.spec.components.operators[].containers[].name.
	// Registry/prefix come from YanetConfigV2.spec.images.
	// +optional
	Containers map[string]ImageRef `json:"containers,omitempty"`
}

// ImageRef identifies an image. Registry and prefix come from
// YanetConfigV2.spec.images.
type ImageRef struct {
	// Name is the image name without registry/prefix/tag.
	// +optional
	Name string `json:"name,omitempty"`

	// Tag is the image tag.
	// +optional
	Tag string `json:"tag,omitempty"`
}

// YanetStatus describes the observed state of a YanetV2 installation.
type YanetStatus struct {
	// Pods groups managed Pod names by phase.
	// +optional
	Pods map[corev1.PodPhase][]string `json:"pods,omitempty"`

	// Sync summarises Deployments by sync state.
	// +optional
	Sync SyncStatus `json:"sync,omitempty"`

	// Conditions hold latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// NodesStatus tracks status per node.
	// +optional
	NodesStatus map[string]NodeStatus `json:"nodesStatus,omitempty"`

	// Services lists managed Service names.
	// +optional
	Services []string `json:"services,omitempty"`
}

// SyncStatus summarises generated Deployments by sync state.
type SyncStatus struct {
	// +optional
	Synced []string `json:"synced,omitempty"`
	// +optional
	OutOfSync []string `json:"outofsync,omitempty"`
	// +optional
	SyncWaiting []string `json:"syncwaiting,omitempty"`
	// +optional
	Error []string `json:"error,omitempty"`
	// +optional
	Disabled []string `json:"disabled,omitempty"`
}

// NodeStatus carries per-node status.
type NodeStatus struct {
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`

	// Deployments maps Deployment name to a short status string.
	// +optional
	Deployments map[string]string `json:"deployments,omitempty"`

	// NumaCount records the number of controlplane Deployments
	// generated for this node.
	// +optional
	NumaCount int32 `json:"numaCount,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=yanetsv2,shortName=yntv2,categories=yanetv2
//+kubebuilder:printcolumn:name="BoxType",type=string,JSONPath=`.spec.boxType`
//+kubebuilder:printcolumn:name="AutoSync",type=boolean,JSONPath=`.spec.autoSync`
//+kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
//+kubebuilder:printcolumn:name="Progressing",type=string,JSONPath=`.status.conditions[?(@.type=="Progressing")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// YanetV2 is the Schema for the yanets API.
type YanetV2 struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   YanetSpec   `json:"spec,omitempty"`
	Status YanetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// YanetV2List contains a list of YanetV2.
type YanetV2List struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []YanetV2 `json:"items"`
}

func init() {
	SchemeBuilder.Register(&YanetV2{}, &YanetV2List{})
}
