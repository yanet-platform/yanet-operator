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
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// YanetConfigSpec is the cluster-wide knowledge base. It defines:
//   - which components exist (spec.components)
//   - the named registry of strategic-merge patches (spec.patches)
//   - box presets that wire components and patches together
//     (spec.boxTypes).
//
// A YanetV2 CR references a boxType by name; everything else is derived
// from this YanetConfigV2.
type YanetConfigSpec struct {
	// Stop is a global kill switch. When true, the reconcile loop
	// does nothing across the whole cluster.
	// +kubebuilder:default=false
	// +optional
	Stop bool `json:"stop,omitempty"`

	// UpdateWindow is a global per-cluster throttling between any
	// two node restarts. Expressed in seconds. After a restart on
	// any node, the reconciler delays the next restart (anywhere)
	// by this many seconds.
	// +kubebuilder:default=0
	// +optional
	UpdateWindow int `json:"updateWindow,omitempty"`

	// AutoDiscovery configures the optional new-worker initializer
	// (carried over from v1alpha1 verbatim, untyped here).
	// +optional
	AutoDiscovery AutoDiscovery `json:"autoDiscovery,omitempty"`

	// Images defines global image settings shared by all generated
	// Deployments.
	// +optional
	Images ImagesSpec `json:"images,omitempty"`

	// Components is the palette of available components: 5 hardcoded
	// names plus a dynamic operators[] array.
	// +kubebuilder:validation:Required
	Components ComponentsSpec `json:"components"`

	// Patches is the named registry of strategic-merge Deployment
	// fragments. Each patch is a slice of an appsv1.Deployment.
	// +optional
	Patches []NamedPatch `json:"patches,omitempty"`

	// BoxTypes are box presets, each wiring components to lists of
	// patches by name. A YanetV2 CR references one entry by name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	BoxTypes []BoxType `json:"boxTypes"`
}

// ImagesSpec describes global image settings.
type ImagesSpec struct {
	// Registry is the base registry shared by all components.
	// +optional
	Registry string `json:"registry,omitempty"`

	// Prefix is an optional path segment between registry and image
	// name: {registry}/{prefix}/{image}:{tag}.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// PullPolicy applies to every container the operator generates.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`

	// PullSecrets are propagated to every Pod created by the
	// operator.
	// +optional
	PullSecrets []corev1.LocalObjectReference `json:"pullSecrets,omitempty"`
}

// ComponentsSpec is the palette of components the operator can render.
//
// The 5 hardcoded names map 1:1 to Deployments. The Operators array is
// a dynamic list keyed by Name; each entry is rendered as one
// Deployment with one or more containers in a single Pod.
type ComponentsSpec struct {
	// +kubebuilder:validation:Required
	Controlplane ControlplaneSpec `json:"controlplane"`

	// +kubebuilder:validation:Required
	Dataplane DataplaneSpec `json:"dataplane"`

	// +optional
	Bird *BirdComponent `json:"bird,omitempty"`

	// BirdAdapter is a SEPARATE Deployment (not a sidecar to bird),
	// so the adapter can be updated without restarting bird.
	// bird ↔ birdAdapter share the bird unix socket via a hostPath.
	// +optional
	BirdAdapter *BirdAdapterComp `json:"birdAdapter,omitempty"`

	// +optional
	Announcer *AnnouncerComp `json:"announcer,omitempty"`

	// Operators are dynamic, keyed by Name. Each is rendered as one
	// Deployment + (optional) Service.
	// +optional
	Operators []OperatorSpec `json:"operators,omitempty"`
}

// ControlplaneSpec describes the controlplane component. Multi-NUMA
// nodes get one Deployment per NUMA domain, each listening on
// `Port + numa_index`. The NUMA-agnostic Service uses Port and load
// balances across all instances.
type ControlplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// Port is the round-robin Service port over all CP instances.
	// When numa>1 each instance also listens on Port+i.
	// +optional
	Port int32 `json:"port,omitempty"`

	// PortRange is the upper bound on per-NUMA listen ports
	// (informational, the operator will validate that
	// Port..Port+PortRange-1 does not overlap other component ports).
	// +optional
	PortRange int32 `json:"portRange,omitempty"`

	// Config is the configuration source (inline | hostPath | url).
	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Numa overrides automatic NUMA detection. When nil, the
	// operator reads `feature.node.kubernetes.io/cpu-numa_nodes_count`
	// from the Node and falls back to 1.
	// +optional
	Numa *int32 `json:"numa,omitempty"`
}

// DataplaneSpec describes the dataplane component (DPDK + hugepages).
type DataplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// +optional
	Port int32 `json:"port,omitempty"`

	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Hugepages requested by the Pod.
	// +optional
	Hugepages *Hugepages `json:"hugepages,omitempty"`

	// HostNetwork defaults to true (DPDK requirement).
	// +optional
	HostNetwork *bool `json:"hostNetwork,omitempty"`
}

// BirdComponent describes the BIRD2 daemon Deployment.
type BirdComponent struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`
	// Port is the BGP port. Default 179.
	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Config *ConfigSource `json:"config,omitempty"`
}

// BirdAdapterComp describes the bird-adapter Deployment.
type BirdAdapterComp struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`
	// Port is the gRPC listen port of the adapter.
	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Config *ConfigSource `json:"config,omitempty"`
}

// AnnouncerComp describes the announcer Deployment.
type AnnouncerComp struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`
	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Config *ConfigSource `json:"config,omitempty"`
}

// Hugepages defines the hugepage resource request for the dataplane.
type Hugepages struct {
	// Size of a single hugepage (e.g. "1Gi", "2Mi").
	// +kubebuilder:validation:Required
	Size string `json:"size"`

	// Count is the number of hugepages requested.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
}

// OperatorSpec describes one dynamic operator. The whole Pod is
// rendered as a single Deployment.
type OperatorSpec struct {
	// Name is unique within the Operators array. It is used as the
	// component label, default container name, and (when Port is
	// set) as the Service name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Port, when non-zero, asks the operator to render a single
	// cluster-wide ClusterIP Service `<Name>` with
	// internalTrafficPolicy=Local (so callers on the same node hit
	// the local pod). targetContainer = first container.
	// +optional
	Port int32 `json:"port,omitempty"`

	// Containers lists the containers of the Pod. At least one is
	// required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Containers []OperatorContainer `json:"containers"`
}

// OperatorContainer describes one container of an operator Pod.
type OperatorContainer struct {
	// Name of the container. Must be unique within the operator and
	// is the key used by YanetV2.spec.components.operators[].containers
	// for per-container image overrides.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// Config is the configuration source for this container.
	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// HostIPC, when true, requests host IPC namespace for the whole
	// Pod. Pod-level hostIPC=true is set if any container in the
	// list requests it.
	// +optional
	HostIPC *bool `json:"hostIPC,omitempty"`
}

// NamedPatch is a strategic-merge patch fragment of an appsv1.Deployment
// stored in the cluster-wide patch registry.
type NamedPatch struct {
	// Name uniquely identifies the patch within the registry.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Patch carries the strategic-merge fragment as raw JSON/YAML.
	// +kubebuilder:validation:Required
	// +kubebuilder:pruning:PreserveUnknownFields
	Patch runtime.RawExtension `json:"patch"`
}

// BoxType is a named preset wiring components to patch lists.
type BoxType struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Components defines which of the 5 hardcoded components are
	// enabled and which patches each receives.
	// +kubebuilder:validation:Required
	Components BoxComponents `json:"components"`

	// Operators is keyed by OperatorSpec.Name.
	// +optional
	Operators map[string]BoxOperator `json:"operators,omitempty"`
}

// BoxComponents lists per-hardcoded-component patch wiring. A nil
// section means the component is disabled for this boxType.
type BoxComponents struct {
	// +optional
	Controlplane *BoxComponent `json:"controlplane,omitempty"`
	// +optional
	Dataplane *BoxComponent `json:"dataplane,omitempty"`
	// +optional
	Bird *BoxComponent `json:"bird,omitempty"`
	// +optional
	BirdAdapter *BoxComponent `json:"birdAdapter,omitempty"`
	// +optional
	Announcer *BoxComponent `json:"announcer,omitempty"`
}

// BoxComponent is the per-component slot in a boxType.
type BoxComponent struct {
	// Patches lists patch names from YanetConfigV2.spec.patches[].
	// Patches are applied in declared order.
	// +optional
	Patches []string `json:"patches,omitempty"`
}

// BoxOperator is the per-operator slot in a boxType.
type BoxOperator struct {
	// +optional
	Patches []string `json:"patches,omitempty"`
}

// AutoDiscovery configures the optional new-worker initializer.
//
// Untouched from v1alpha1 to keep helm-chart shape stable. Not part of
// the components/patches/boxTypes pipeline.
type AutoDiscovery struct {
	// +kubebuilder:default=false
	// +optional
	Enable bool `json:"enable,omitempty"`

	// +optional
	TypeURI string `json:"typeUri,omitempty"`

	// +kubebuilder:default=default
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// +kubebuilder:default=dockerhub.io
	// +optional
	Registry string `json:"registry,omitempty"`

	// +optional
	VersionURI string `json:"versionUri,omitempty"`

	// +optional
	ArchURI string `json:"archUri,omitempty"`

	// +optional
	ConfigsURI string `json:"configsUri,omitempty"`
}

// MutexYanetConfigSpec wraps YanetConfigSpec for safe concurrent
// access from the reconciler. Mirrors the v1alpha1 helper.
// +kubebuilder:object:generate=false
type MutexYanetConfigSpec struct {
	Config YanetConfigSpec `json:"config,omitempty"`
	Lock   sync.Mutex      `json:"-"`
}

// YanetConfigStatus defines the observed state of YanetConfigV2.
type YanetConfigStatus struct {
	// Conditions hold latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=yanetconfigsv2,shortName=yntcfgv2,categories=yanet
//+kubebuilder:printcolumn:name="UpdateWindow",type=integer,JSONPath=`.spec.updateWindow`
//+kubebuilder:printcolumn:name="Stop",type=boolean,JSONPath=`.spec.stop`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// YanetConfigV2 is the Schema for the yanetconfigs API.
type YanetConfigV2 struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   YanetConfigSpec   `json:"spec,omitempty"`
	Status YanetConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// YanetConfigV2List contains a list of YanetConfigV2.
type YanetConfigV2List struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []YanetConfigV2 `json:"items"`
}

func init() {
	SchemeBuilder.Register(&YanetConfigV2{}, &YanetConfigV2List{})
}
