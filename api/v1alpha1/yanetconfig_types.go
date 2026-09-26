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

package v1alpha1

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
// A Yanet CR references a boxType by name; everything else is derived
// from this YanetConfig.
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
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=9223372036
	// +optional
	UpdateWindow int `json:"updateWindow,omitempty"`

	// Images defines global image settings shared by all generated
	// Deployments.
	// +optional
	Images ImagesSpec `json:"images,omitempty"`

	// Components is the palette of available workload components plus a
	// dynamic operators[] array. The dataplane slot describes one Pod with
	// explicitly declared native sidecars.
	// +kubebuilder:validation:Required
	Components ComponentsSpec `json:"components"`

	// Patches is the named registry of strategic-merge Deployment
	// fragments. Each patch is a slice of an appsv1.Deployment.
	// +optional
	Patches []NamedPatch `json:"patches,omitempty"`

	// BoxTypes are box presets, each wiring components to lists of
	// patches by name. A Yanet CR references one entry by name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	BoxTypes []BoxType `json:"boxTypes"`
}

// ImagesSpec describes global image settings.
type ImagesSpec struct {
	// Registry is the default registry for palette images without an override.
	// +optional
	Registry string `json:"registry,omitempty"`

	// Prefix is the default path segment between registry and image
	// name: {registry}/{prefix}/{image}:{tag}. Palette images may override it.
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
// Controlplane, dataplane and birdAdapter map to Deployments. The dataplane
// Deployment may also contain declared single-container native sidecars.
// The Operators array is a dynamic list keyed by Name; each entry is
// rendered as one Deployment with one or more containers in a single Pod.
type ComponentsSpec struct {
	// +kubebuilder:validation:Required
	Controlplane ControlplaneSpec `json:"controlplane"`

	// +kubebuilder:validation:Required
	Dataplane DataplaneSpec `json:"dataplane"`

	// BirdAdapter is a SEPARATE Deployment (not a sidecar to bird),
	// so the adapter can be updated without restarting bird.
	// bird ↔ birdAdapter share the bird unix socket via a hostPath.
	// Its socket mounts are configured explicitly through patches.
	// +optional
	BirdAdapter *BirdAdapterComp `json:"birdAdapter,omitempty"`

	// Operators are dynamic, keyed by Name. Each is rendered as one
	// Deployment and one Service.
	// +optional
	Operators []OperatorSpec `json:"operators,omitempty"`
}

// MaxControlplaneNuma bounds the supported physical NUMA fan-out per node.
const MaxControlplaneNuma int32 = 4

// ValidateControlplaneNuma checks an explicit (non-defaulted) domain count.
// Admission and rendering share this bound, including for persisted configs.
func ValidateControlplaneNuma(count int32) error {
	if count < 1 || count > MaxControlplaneNuma {
		return fmt.Errorf("spec.components.controlplane.numa must be between 1 and %d, got %d", MaxControlplaneNuma, count)
	}
	return nil
}

// ControlplaneSpec describes the controlplane component. Multi-NUMA nodes get
// one Deployment and one stable Service per NUMA domain.
type ControlplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// Config is the configuration source (inline | hostPath).
	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Numa is the configured controlplane NUMA fan-out per node (1 through 4).
	// Defaults to 1 when omitted. Set it explicitly for multi-NUMA hosts.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +optional
	Numa *int32 `json:"numa,omitempty"`

	// DisabledNuma lists NUMA indices that must NOT get a
	// controlplane instance within the configured fan-out.
	// The usual reason is a NUMA domain without any NIC: the
	// dataplane runs no instance there, so a controlplane for it
	// would have no dataplane peer to attach to.
	//
	// Indices are zero-based and refer to the same numbering as the
	// configured NUMA fan-out (`numa`, default 1). Out-of-range indices
	// are ignored, duplicates are collapsed. Disabling every index
	// is rejected by the webhook.
	//
	// This is the cluster-wide default; a single installation can
	// override it via
	// Yanet.spec.components.controlplane.disabledNuma.
	// +optional
	DisabledNuma []int32 `json:"disabledNuma,omitempty"`
}

const (
	// DataplaneContainerName is the primary container in the dataplane Pod.
	DataplaneContainerName = "dataplane"
	// BirdAdapterContainerName is the rendered bird-adapter container name.
	BirdAdapterContainerName = "bird-adapter"
)

// DataplaneSpec describes one dataplane Pod: the DPDK process, hugepages and
// declared native sidecars that share its private network namespace.
type DataplaneSpec struct {
	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Hugepages requested by the Pod.
	// +optional
	Hugepages *Hugepages `json:"hugepages,omitempty"`

	// Networks declares ordered device-backed Multus attachments. Each entry
	// reserves one device from ResourceName in the primary dataplane container.
	// Yanet can replace this list per installation. NADs are managed externally.
	// +optional
	// +listType=atomic
	Networks []NetworkAttachment `json:"networks,omitempty"`

	// Sidecars is the ordered palette of single-container native sidecars.
	// Each declaration reserves a gRPC/HTTP pair at 8080+2*i / 8081+2*i,
	// including unselected and disabled entries. The box type selects names;
	// it never changes their declared order or port indices.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=28728
	Sidecars []SidecarSpec `json:"sidecars,omitempty"`
}

// NetworkAttachment couples one Multus attachment with one extended resource.
type NetworkAttachment struct {
	// Name references an existing NetworkAttachmentDefinition.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace defaults to the Yanet installation namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`

	// Interface is the interface name inside the dataplane Pod.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	Interface string `json:"interface"`

	// ResourceName must match the NAD's k8s.v1.cni.cncf.io/resourceName annotation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ResourceName string `json:"resourceName"`
}

// SidecarSpec describes exactly one native sidecar container in the dataplane.
// Names are unique across sidecars and standalone operators.
type SidecarSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// +optional
	Config *ConfigSource `json:"config,omitempty"`

	// Listeners declares Service exposure, defaulting to grpc. An explicit
	// empty list creates no Service but retains the slot and host-config env.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=2
	Listeners *[]OperatorListener `json:"listeners,omitempty"`
}

// BirdAdapterComp describes the bird-adapter Deployment.
type BirdAdapterComp struct {
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
	if pageBytes <= 0 || resource.NewQuantity(pageBytes, pageQty.Format).Cmp(pageQty) != 0 {
		return resource.Quantity{}, fmt.Errorf("size %q must be a whole number of bytes that fits in int64", h.Size)
	}
	if pageBytes > math.MaxInt64/int64(h.Count) {
		return resource.Quantity{}, fmt.Errorf("size %q multiplied by count %d overflows int64", h.Size, h.Count)
	}
	return *resource.NewQuantity(pageBytes*int64(h.Count), pageQty.Format), nil
}

// OperatorSpec describes one independently deployed operator container group.
type OperatorSpec struct {
	// Name is unique within the Operators array. It is used as the component
	// label and default container name. Built-in component names are reserved.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Containers lists the containers of the Pod. At least one is
	// required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Containers []OperatorContainer `json:"containers"`

	// Listeners are owned by the first container. Omitted defaults to grpc.
	// An empty list means
	// no listener and no Service. Explicit lists are rendered in grpc/http order.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=2
	Listeners *[]OperatorListener `json:"listeners,omitempty"`
}

// OperatorListener is a supported application listener.
// +kubebuilder:validation:Enum=grpc;http
type OperatorListener string

// OperatorContainer describes one container of an operator Pod.
type OperatorContainer struct {
	// Name of the container. Must be unique within the operator and
	// is the key used by Yanet.spec.components.operators[].containers
	// for per-container image overrides.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	Image ImageRef `json:"image"`

	// Config is the configuration source for this container.
	// +optional
	Config *ConfigSource `json:"config,omitempty"`
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
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
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
}

// BoxComponent is the per-component slot in a boxType.
type BoxComponent struct {
	// Patches lists patch names from YanetConfig.spec.patches[].
	// Patches are applied in declared order.
	// +optional
	Patches []string `json:"patches,omitempty"`
}

// BoxDataplane is the per-box slot for the dataplane Deployment and its declared
// native sidecars. Patches apply to the whole Deployment, including sidecars.
type BoxDataplane struct {
	// Patches lists patch names from YanetConfig.spec.patches[]. Patches are
	// applied to the dataplane Deployment in declared order.
	// +optional
	Patches []string `json:"patches,omitempty"`

	// Sidecars selects native sidecars declared in
	// YanetConfig.spec.components.dataplane.sidecars.
	// +optional
	Sidecars map[string]BoxDataplaneSidecar `json:"sidecars,omitempty"`
}

// BoxDataplaneSidecar selects a sidecar for a box type. A present slot defaults
// to enabled; enabled=false keeps the declaration explicit while omitting the
// sidecar from the rendered Pod.
type BoxDataplaneSidecar struct {
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Patches address this sidecar's logical container and volume names.
	// +optional
	Patches []string `json:"patches,omitempty"`
}

// BoxOperator is the per-operator slot in a boxType.
type BoxOperator struct {
	// +optional
	Patches []string `json:"patches,omitempty"`
}

// MutexYanetConfigSpec wraps YanetConfigSpec for safe concurrent
// access from the reconciler.
// +kubebuilder:object:generate=false
type MutexYanetConfigSpec struct {
	Config YanetConfigSpec `json:"config,omitempty"`
	Lock   sync.Mutex      `json:"-"`
}

// YanetConfigStatus defines the observed state of YanetConfig.
type YanetConfigStatus struct {
	// Conditions hold latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// YanetConfigName is the fixed name of the cluster-wide YanetConfig
// singleton. A fixed cluster-scoped object key lets the API server enforce
// uniqueness atomically.
const YanetConfigName = "config"

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=yanetconfigs,scope=Cluster,shortName=yntcfg,categories=yanet
//+kubebuilder:validation:XValidation:rule="self.metadata.name == 'config'",message="metadata.name must be config"
//+kubebuilder:printcolumn:name="UpdateWindow",type=integer,JSONPath=`.spec.updateWindow`
//+kubebuilder:printcolumn:name="Stop",type=boolean,JSONPath=`.spec.stop`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// YanetConfig is the Schema for the yanetconfigs API.
type YanetConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   YanetConfigSpec   `json:"spec,omitempty"`
	Status YanetConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// YanetConfigList contains a list of YanetConfig.
type YanetConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []YanetConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&YanetConfig{}, &YanetConfigList{})
}
