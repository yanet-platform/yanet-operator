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
	"fmt"
	"math"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

	// HostNetworkPortRange is the inclusive range used for application
	// listeners in workloads whose final patched PodSpec has hostNetwork=true.
	// Service ports stay fixed; only their per-Pod target ports are allocated
	// from this range. The field may be omitted when no service-backed workload
	// uses the host network.
	// +optional
	HostNetworkPortRange *HostNetworkPortRange `json:"hostNetworkPortRange,omitempty"`

	// AutoDiscovery configures the optional new-worker initializer
	// (carried over from v1alpha1 verbatim, untyped here).
	// +optional
	AutoDiscovery AutoDiscovery `json:"autoDiscovery,omitempty"`

	// Images defines global image settings shared by all generated
	// Deployments.
	// +optional
	Images ImagesSpec `json:"images,omitempty"`

	// Components is the palette of available workload components plus a
	// dynamic operators[] array. The dataplane slot describes one Pod with
	// fixed optional native sidecars.
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

// HostNetworkPortRange bounds deterministic per-node listener allocation for
// service-backed host-network workloads.
type HostNetworkPortRange struct {
	// Start is the first port in the inclusive range.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Start int32 `json:"start"`

	// End is the last port in the inclusive range.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	End int32 `json:"end"`
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
// Controlplane, dataplane, birdAdapter and announcer map to Deployments. The
// dataplane Deployment may also contain fixed BIRD and netlink native
// sidecars. The Operators array is a dynamic list keyed by Name; each entry is
// rendered as one Deployment with one or more containers in a single Pod.
type ComponentsSpec struct {
	// +kubebuilder:validation:Required
	Controlplane ControlplaneSpec `json:"controlplane"`

	// +kubebuilder:validation:Required
	Dataplane DataplaneSpec `json:"dataplane"`

	// BirdAdapter is a SEPARATE Deployment (not a sidecar to bird),
	// so the adapter can be updated without restarting bird.
	// bird ↔ birdAdapter share the bird unix socket via a hostPath.
	// +optional
	BirdAdapter *BirdAdapterComp `json:"birdAdapter,omitempty"`

	// +optional
	Announcer *AnnouncerComp `json:"announcer,omitempty"`

	// Operators are dynamic, keyed by Name. Each is rendered as one
	// Deployment and one Service.
	// +optional
	Operators []OperatorSpec `json:"operators,omitempty"`
}

// ControlplaneSpec describes the controlplane component. Multi-NUMA nodes get
// one Deployment and one stable Service per NUMA domain.
type ControlplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// Config is the configuration source (inline | hostPath | url).
	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Numa overrides automatic NUMA detection. When nil, the
	// operator reads `feature.node.kubernetes.io/cpu-numa_nodes_count`
	// from the Node and falls back to 1.
	// +optional
	Numa *int32 `json:"numa,omitempty"`

	// DisabledNuma lists NUMA indices that must NOT get a
	// controlplane instance, even though NUMA detection reports
	// them. The usual reason is a NUMA domain without any NIC: the
	// dataplane runs no instance there, so a controlplane for it
	// would have no dataplane peer to attach to.
	//
	// Indices are zero-based and refer to the same numbering as the
	// NUMA fan-out (`numa` / the NFD label). Out-of-range indices
	// are ignored, duplicates are collapsed. Disabling every index
	// is rejected by the webhook.
	//
	// This is the cluster-wide default; a single installation can
	// override it via
	// YanetV2.spec.components.controlplane.disabledNuma.
	// +optional
	DisabledNuma []int32 `json:"disabledNuma,omitempty"`
}

const (
	// DataplaneContainerName is the primary container in the dataplane Pod.
	DataplaneContainerName = "dataplane"
	// BirdSidecarContainerName is the BIRD native-sidecar container name.
	BirdSidecarContainerName = "bird"
	// BirdAdapterContainerName is the rendered bird-adapter container name.
	BirdAdapterContainerName = "bird-adapter"
	// NetlinkDataplaneSidecarContainerName is the netlink native-sidecar
	// container name.
	NetlinkDataplaneSidecarContainerName = "netlink-dataplane-sidecar"
)

// DataplaneSpec describes one dataplane Pod: the DPDK process, hugepages and
// fixed optional native sidecars that share its network namespace.
type DataplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Hugepages requested by the Pod.
	// +optional
	Hugepages *Hugepages `json:"hugepages,omitempty"`

	// HostNetwork defaults to false. Set it explicitly only for legacy
	// deployments that intentionally run the dataplane in the host network.
	// +kubebuilder:default=false
	// +optional
	HostNetwork *bool `json:"hostNetwork,omitempty"`

	// Sidecars is the palette of native sidecars available to box types. A
	// sidecar runs only when the selected box type wires its corresponding slot.
	// +optional
	Sidecars *DataplaneSidecarsSpec `json:"sidecars,omitempty"`
}

// DataplaneSidecarsSpec contains the fixed native-sidecar slots supported by
// the dataplane Pod.
type DataplaneSidecarsSpec struct {
	// Bird runs the BIRD2 daemon in the dataplane network namespace.
	// +optional
	Bird *DataplaneSidecarSpec `json:"bird,omitempty"`

	// NetlinkDataplaneSidecar owns KNI/VLAN/address/route reconciliation in the
	// dataplane network namespace.
	// +optional
	NetlinkDataplaneSidecar *DataplaneSidecarSpec `json:"netlinkDataplaneSidecar,omitempty"`
}

// DataplaneSidecarSpec describes an image and configuration source for one
// fixed dataplane native sidecar.
type DataplaneSidecarSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// +optional
	Config *ConfigSource `json:"config,omitempty"`
}

// BirdAdapterComp describes the bird-adapter Deployment.
type BirdAdapterComp struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`
	// +optional
	Config *ConfigSource `json:"config,omitempty"`
}

// AnnouncerComp describes the announcer Deployment.
type AnnouncerComp struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`
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

// TotalQuantity validates the Hugepages spec and returns the total memory
// reservation (single page size multiplied by Count). It is the single
// source of truth for both the admission webhook and the manifest builder,
// so validation rules cannot drift between them.
func (h *Hugepages) TotalQuantity() (resource.Quantity, error) {
	pageQty, err := resource.ParseQuantity(h.Size)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("size %q is not a valid Kubernetes quantity: %w", h.Size, err)
	}
	if pageQty.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("size must be greater than zero, got %q", h.Size)
	}
	if h.Count <= 0 {
		return resource.Quantity{}, fmt.Errorf("count must be greater than zero, got %d", h.Count)
	}
	pageBytes := pageQty.Value()
	if pageBytes > math.MaxInt64/int64(h.Count) {
		return resource.Quantity{}, fmt.Errorf("size %q multiplied by count %d overflows int64", h.Size, h.Count)
	}
	return *resource.NewQuantity(pageBytes*int64(h.Count), pageQty.Format), nil
}

// OperatorSpec describes one dynamic operator. The whole Pod is
// rendered as a single Deployment.
type OperatorSpec struct {
	// Name is unique within the Operators array. It is used as the component
	// label and default container name. Built-in component names are reserved.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

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

	// Components defines which fixed workload components are
	// enabled and which patches each receives.
	// +kubebuilder:validation:Required
	Components BoxComponents `json:"components"`

	// Operators is keyed by OperatorSpec.Name.
	// +optional
	Operators map[string]BoxOperator `json:"operators,omitempty"`
}

// BoxComponents lists per-workload patch wiring. A nil section means the
// workload is disabled for this boxType. Dataplane native sidecars are selected
// inside the dataplane slot because they share its Deployment.
type BoxComponents struct {
	// +optional
	Controlplane *BoxComponent `json:"controlplane,omitempty"`
	// +optional
	Dataplane *BoxDataplane `json:"dataplane,omitempty"`
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

// BoxDataplane is the per-box slot for the dataplane Deployment and its fixed
// native sidecars. Patches apply to the whole Deployment, including sidecars.
type BoxDataplane struct {
	// Patches lists patch names from YanetConfigV2.spec.patches[]. Patches are
	// applied to the dataplane Deployment in declared order.
	// +optional
	Patches []string `json:"patches,omitempty"`

	// Sidecars selects native sidecars declared in
	// YanetConfigV2.spec.components.dataplane.sidecars.
	// +optional
	Sidecars *BoxDataplaneSidecars `json:"sidecars,omitempty"`
}

// BoxDataplaneSidecars contains per-box enablement for fixed native sidecars.
type BoxDataplaneSidecars struct {
	// +optional
	Bird *BoxDataplaneSidecar `json:"bird,omitempty"`

	// +optional
	NetlinkDataplaneSidecar *BoxDataplaneSidecar `json:"netlinkDataplaneSidecar,omitempty"`
}

// BoxDataplaneSidecar selects a sidecar for a box type. A present slot defaults
// to enabled; enabled=false keeps the declaration explicit while omitting the
// sidecar from the rendered Pod.
type BoxDataplaneSidecar struct {
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
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

// YanetConfigName is the fixed name of the cluster-wide YanetConfigV2
// singleton. A fixed cluster-scoped object key lets the API server enforce
// uniqueness atomically.
const YanetConfigName = "config"

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=yanetconfigsv2,scope=Cluster,shortName=yntcfgv2,categories=yanetv2
//+kubebuilder:validation:XValidation:rule="self.metadata.name == 'config'",message="metadata.name must be config"
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
