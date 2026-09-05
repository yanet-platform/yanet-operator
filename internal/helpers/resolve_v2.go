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

package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

// ShortNodeKey returns a stable short hex key derived from the node
// name. The reconciler uses it to build deterministic per-node
// resource names that fit within the 63-character DNS label limit.
func ShortNodeKey(nodeName string) string {
	h := sha256.Sum256([]byte(nodeName))
	return hex.EncodeToString(h[:4])
}

// ComponentKind identifies which workload component is being resolved. BIRD
// and the netlink dataplane helper are native sidecars of KindDataplane rather
// than independent component kinds. KindOperator covers any element of the
// dynamic operators[] array.
type ComponentKind string

const (
	KindControlplane ComponentKind = "controlplane"
	KindDataplane    ComponentKind = "dataplane"
	KindBirdAdapter  ComponentKind = "birdAdapter"
	KindAnnouncer    ComponentKind = "announcer"
	KindOperator     ComponentKind = "operator"
)

// ResolvedImage is the image reference after merging the component
// definition from YanetConfigV2.spec.components with the optional
// per-installation override from YanetV2.spec.components.
//
// Registry and Prefix come straight from YanetConfigV2.spec.images and
// are kept here so the builder can render the full path without
// touching the global config again.
type ResolvedImage struct {
	Registry string
	Prefix   string
	Name     string
	Tag      string
}

// ResolvedComponent is the merged view of a single Deployment slot
// requested by a YanetV2 CR. It feeds the builder (Партия R3): the
// builder turns this struct into one or more Deployment skeletons and
// then ApplyPatches (Партия R3.5) layers strategic-merge patches on
// top.
//
// Numa is only populated for KindControlplane. Containers is only populated
// for KindOperator. NativeSidecars is only populated for KindDataplane.
type ResolvedComponent struct {
	Kind ComponentKind
	// Name is the canonical workload name. For fixed workload kinds it equals
	// the kind ("controlplane", "dataplane", ...).
	// For operators it is OperatorSpec.Name.
	Name string

	// Enabled is the effective replicas gate. true → 1, false → 0.
	// Defaults to true unless the per-installation override sets it
	// explicitly to false.
	Enabled bool

	// Image is the merged image reference (registry/prefix/name:tag).
	// For operators this is the image of the first container; the
	// rest are exposed via Containers.
	Image ResolvedImage

	// Config carries the resolved config source (inline | hostPath |
	// URL). nil means the component does not need a config volume.
	Config *yanetv2alpha1.ConfigSource

	// Hugepages is only set for KindDataplane.
	Hugepages *yanetv2alpha1.Hugepages

	// HostNetwork applies to KindDataplane (default false) and is nil
	// for the other kinds.
	HostNetwork *bool

	// Numa is the NUMA fan-out count for KindControlplane.
	// Zero means "use the NFD label / fall back to 1".
	Numa int32

	// DisabledNuma is the resolved set of NUMA indices that must not
	// get a controlplane instance. Only populated for
	// KindControlplane. The per-installation override in
	// YanetV2 replaces the cluster-wide YanetConfigV2 list rather
	// than merging with it, so what lands here is already final.
	DisabledNuma []int32

	// Containers is the resolved per-container view of an operator
	// Pod. The first element is the primary container and backs the
	// per-operator Service.
	Containers []ResolvedContainer

	// NativeSidecars is the ordered set of enabled native sidecars for a
	// dataplane Pod. The netlink sidecar precedes BIRD so it also terminates
	// after BIRD when kubelet stops sidecars in reverse order.
	NativeSidecars []ResolvedContainer

	// Patches is the ordered list of patch NAMES that the box wires
	// to this component. Resolution into actual NamedPatch objects
	// happens in the patcher package, where dry-run is also done.
	Patches []string
}

// ResolvedContainer carries the operator-container view after image
// override resolution.
type ResolvedContainer struct {
	Name    string
	Image   ResolvedImage
	Config  *yanetv2alpha1.ConfigSource
	HostIPC bool
}

// FindBoxType returns the BoxType with the given name, or an error if
// it does not exist. Webhook validation guarantees existence at admit
// time, but the reconciler can be invoked between admission and the
// next config refresh, so we double-check.
func FindBoxType(config *yanetv2alpha1.YanetConfigSpec, name string) (*yanetv2alpha1.BoxType, error) {
	if config == nil {
		return nil, fmt.Errorf("yanetConfig is nil")
	}
	for i := range config.BoxTypes {
		if config.BoxTypes[i].Name == name {
			return &config.BoxTypes[i], nil
		}
	}
	return nil, fmt.Errorf("boxType %q not found in YanetConfigV2", name)
}

// FindOperator returns the OperatorSpec with the given name, or an
// error if the operator is not declared in the components palette.
func FindOperator(config *yanetv2alpha1.YanetConfigSpec, name string) (*yanetv2alpha1.OperatorSpec, error) {
	if config == nil {
		return nil, fmt.Errorf("yanetConfig is nil")
	}
	for i := range config.Components.Operators {
		if config.Components.Operators[i].Name == name {
			return &config.Components.Operators[i], nil
		}
	}
	return nil, fmt.Errorf("operator %q not found in YanetConfigV2.spec.components.operators", name)
}

// ResolveBoxComponent merges three layers for one component slot in
// the requested boxType:
//
//  1. YanetConfigV2.spec.components.<kind|operator-name> (palette).
//  2. The boxType slot in YanetConfigV2.spec.boxTypes[name] (the list
//     of patch names — copied verbatim into ResolvedComponent.Patches;
//     actual patch fetching/dry-run is done by the patcher).
//  3. YanetV2.spec.components.<kind|operators[name]> (typed
//     per-installation overrides: enabled, image.name, image.tag).
//
// kind is one of the constants above. For KindOperator the operator
// name is taken from the boxType.operators map and the overrides come
// from YanetV2.spec.components.operators[name]. For fixed workload kinds the
// operatorName argument is ignored.
//
// A nil result with a nil error means the component is disabled in
// the boxType (no slot at all, or operator key absent in
// boxType.operators). Callers must skip the component in this case.
func ResolveBoxComponent(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	kind ComponentKind,
	operatorName string,
) (*ResolvedComponent, error) {
	if config == nil {
		return nil, fmt.Errorf("yanetConfig is nil")
	}
	if yanet == nil {
		return nil, fmt.Errorf("yanet is nil")
	}
	box, err := FindBoxType(config, yanet.BoxType)
	if err != nil {
		return nil, err
	}

	switch kind {
	case KindControlplane:
		return resolveControlplane(config, yanet, box)
	case KindDataplane:
		return resolveDataplane(config, yanet, box)
	case KindBirdAdapter:
		return resolveBirdAdapter(config, yanet, box)
	case KindAnnouncer:
		return resolveAnnouncer(config, yanet, box)
	case KindOperator:
		return resolveOperator(config, yanet, box, operatorName)
	default:
		return nil, fmt.Errorf("unknown component kind %q", kind)
	}
}

// EnabledComponentsForBox returns the set of (kind, operatorName)
// pairs that the boxType actually wires up. The reconciler calls this
// to know which ResolveBoxComponent invocations to make.
//
// For hardcoded kinds operatorName is empty.
type ComponentRef struct {
	Kind         ComponentKind
	OperatorName string
}

// EnabledComponentsForBox lists every component slot wired by the
// boxType (in stable order: hardcoded first, then operators sorted by
// declaration order in YanetConfigV2.spec.components.operators).
func EnabledComponentsForBox(config *yanetv2alpha1.YanetConfigSpec, boxName string) ([]ComponentRef, error) {
	box, err := FindBoxType(config, boxName)
	if err != nil {
		return nil, err
	}
	var refs []ComponentRef
	if box.Components.Controlplane != nil {
		refs = append(refs, ComponentRef{Kind: KindControlplane})
	}
	if box.Components.Dataplane != nil {
		refs = append(refs, ComponentRef{Kind: KindDataplane})
	}
	if box.Components.BirdAdapter != nil {
		refs = append(refs, ComponentRef{Kind: KindBirdAdapter})
	}
	if box.Components.Announcer != nil {
		refs = append(refs, ComponentRef{Kind: KindAnnouncer})
	}
	// Walk operators in declaration order (stable rendering).
	for i := range config.Components.Operators {
		op := &config.Components.Operators[i]
		if _, ok := box.Operators[op.Name]; ok {
			refs = append(refs, ComponentRef{Kind: KindOperator, OperatorName: op.Name})
		}
	}
	return refs, nil
}

// -- internal resolvers -------------------------------------------------------

func resolveControlplane(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) (*ResolvedComponent, error) {
	slot := box.Components.Controlplane
	if slot == nil {
		return nil, nil
	}
	cp := config.Components.Controlplane
	override := componentOverride(yanet, KindControlplane, "")
	return &ResolvedComponent{
		Kind:         KindControlplane,
		Name:         string(KindControlplane),
		Enabled:      resolveEnabled(override),
		Image:        mergeImage(config.Images, cp.Image, containerOverride(override, string(KindControlplane))),
		Config:       cp.Config,
		Numa:         Int32Value(cp.Numa, 0),
		DisabledNuma: resolveDisabledNuma(cp.DisabledNuma, yanet),
		Patches:      slot.Patches,
	}, nil
}

// resolveDisabledNuma picks the effective disabled-NUMA list for the
// controlplane. The per-installation list in
// YanetV2.spec.components.controlplane.disabledNuma REPLACES the
// cluster-wide default from YanetConfigV2 when it is non-nil — an empty
// but non-nil list therefore clears the default and re-enables every
// NUMA index. A nil list inherits the cluster-wide value.
func resolveDisabledNuma(clusterWide []int32, yanet *yanetv2alpha1.YanetSpec) []int32 {
	if yanet.Components != nil &&
		yanet.Components.Controlplane != nil &&
		yanet.Components.Controlplane.DisabledNuma != nil {
		return append([]int32(nil), yanet.Components.Controlplane.DisabledNuma...)
	}
	return append([]int32(nil), clusterWide...)
}

func resolveDataplane(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) (*ResolvedComponent, error) {
	slot := box.Components.Dataplane
	if slot == nil {
		return nil, nil
	}
	dp := config.Components.Dataplane
	override := componentOverride(yanet, KindDataplane, "")
	nativeSidecars, err := resolveDataplaneSidecars(config, yanet, box)
	if err != nil {
		return nil, err
	}
	return &ResolvedComponent{
		Kind:           KindDataplane,
		Name:           string(KindDataplane),
		Enabled:        resolveEnabled(override),
		Image:          mergeImage(config.Images, dp.Image, containerOverride(override, yanetv2alpha1.DataplaneContainerName)),
		Config:         dp.Config,
		Hugepages:      dp.Hugepages,
		HostNetwork:    dp.HostNetwork,
		NativeSidecars: nativeSidecars,
		Patches:        slot.Patches,
	}, nil
}

func resolveDataplaneSidecars(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) ([]ResolvedContainer, error) {
	var palette *yanetv2alpha1.DataplaneSidecarsSpec
	if config.Components.Dataplane.Sidecars != nil {
		palette = config.Components.Dataplane.Sidecars
	}
	var selection *yanetv2alpha1.BoxDataplaneSidecars
	if box.Components.Dataplane != nil {
		selection = box.Components.Dataplane.Sidecars
	}
	var override *yanetv2alpha1.YanetComponentOverride
	if yanet.Components != nil {
		override = yanet.Components.Dataplane
	}

	type sidecarSlot struct {
		name      string
		palette   *yanetv2alpha1.DataplaneSidecarSpec
		selection *yanetv2alpha1.BoxDataplaneSidecar
	}
	slots := []sidecarSlot{
		{name: yanetv2alpha1.NetlinkDataplaneSidecarContainerName},
		{name: yanetv2alpha1.BirdSidecarContainerName},
	}
	if palette != nil {
		slots[0].palette = palette.NetlinkDataplaneSidecar
		slots[1].palette = palette.Bird
	}
	if selection != nil {
		slots[0].selection = selection.NetlinkDataplaneSidecar
		slots[1].selection = selection.Bird
	}

	resolved := make([]ResolvedContainer, 0, len(slots))
	for _, slot := range slots {
		sidecarOverride := containerOverride(override, slot.name)
		if slot.selection == nil {
			// Admission rejects new unwired overrides. Ignore a stale persisted
			// override here so removing a sidecar from the box can roll it out.
			continue
		}
		if slot.palette == nil {
			return nil, fmt.Errorf(
				"boxType %q wires dataplane sidecar %q but YanetConfigV2.spec.components.dataplane.sidecars does not define it",
				box.Name,
				slot.name,
			)
		}
		enabled := BoolValue(slot.selection.Enabled, true)
		if sidecarOverride != nil && sidecarOverride.Enabled != nil {
			enabled = *sidecarOverride.Enabled
		}
		if !enabled {
			continue
		}
		resolved = append(resolved, ResolvedContainer{
			Name:   slot.name,
			Image:  mergeImage(config.Images, slot.palette.Image, sidecarOverride),
			Config: slot.palette.Config,
		})
	}
	return resolved, nil
}

func resolveBirdAdapter(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) (*ResolvedComponent, error) {
	slot := box.Components.BirdAdapter
	if slot == nil {
		return nil, nil
	}
	if config.Components.BirdAdapter == nil {
		return nil, fmt.Errorf("boxType %q wires birdAdapter but YanetConfigV2.spec.components.birdAdapter is not defined", box.Name)
	}
	ad := config.Components.BirdAdapter
	override := componentOverride(yanet, KindBirdAdapter, "")
	return &ResolvedComponent{
		Kind:    KindBirdAdapter,
		Name:    string(KindBirdAdapter),
		Enabled: resolveEnabled(override),
		Image:   mergeImage(config.Images, ad.Image, containerOverride(override, yanetv2alpha1.BirdAdapterContainerName)),
		Config:  ad.Config,
		Patches: slot.Patches,
	}, nil
}

func resolveAnnouncer(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) (*ResolvedComponent, error) {
	slot := box.Components.Announcer
	if slot == nil {
		return nil, nil
	}
	if config.Components.Announcer == nil {
		return nil, fmt.Errorf("boxType %q wires announcer but YanetConfigV2.spec.components.announcer is not defined", box.Name)
	}
	an := config.Components.Announcer
	override := componentOverride(yanet, KindAnnouncer, "")
	return &ResolvedComponent{
		Kind:    KindAnnouncer,
		Name:    string(KindAnnouncer),
		Enabled: resolveEnabled(override),
		Image:   mergeImage(config.Images, an.Image, containerOverride(override, string(KindAnnouncer))),
		Config:  an.Config,
		Patches: slot.Patches,
	}, nil
}

func resolveOperator(
	config *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
	operatorName string,
) (*ResolvedComponent, error) {
	slot, ok := box.Operators[operatorName]
	if !ok {
		return nil, nil
	}
	op, err := FindOperator(config, operatorName)
	if err != nil {
		return nil, err
	}
	if len(op.Containers) == 0 {
		return nil, fmt.Errorf("operator %q has no containers", operatorName)
	}
	override := componentOverride(yanet, KindOperator, operatorName)
	containers := make([]ResolvedContainer, 0, len(op.Containers))
	for i := range op.Containers {
		c := &op.Containers[i]
		containerOvr := containerOverride(override, c.Name)
		img := mergeImage(config.Images, c.Image, containerOvr)
		containers = append(containers, ResolvedContainer{
			Name:    c.Name,
			Image:   img,
			Config:  c.Config,
			HostIPC: BoolValue(c.HostIPC, false),
		})
	}

	return &ResolvedComponent{
		Kind:       KindOperator,
		Name:       op.Name,
		Enabled:    resolveEnabled(override),
		Image:      containers[0].Image,
		Containers: containers,
		Patches:    slot.Patches,
	}, nil
}

// componentOverride returns the per-installation override block that
// matches the requested kind/operator. The result is always safe to
// dereference for nil-safe field reads.
func componentOverride(
	yanet *yanetv2alpha1.YanetSpec,
	kind ComponentKind,
	operatorName string,
) *yanetv2alpha1.YanetComponentOverride {
	if yanet.Components == nil {
		return nil
	}
	switch kind {
	case KindControlplane:
		if yanet.Components.Controlplane == nil {
			return nil
		}
		return &yanet.Components.Controlplane.YanetComponentOverride
	case KindDataplane:
		return yanet.Components.Dataplane
	case KindBirdAdapter:
		return yanet.Components.BirdAdapter
	case KindAnnouncer:
		return yanet.Components.Announcer
	case KindOperator:
		if v, ok := yanet.Components.Operators[operatorName]; ok {
			return &v
		}
	}
	return nil
}

// resolveEnabled defaults to true and honours the override when set.
func resolveEnabled(override *yanetv2alpha1.YanetComponentOverride) bool {
	if override == nil {
		return true
	}
	return BoolValue(override.Enabled, true)
}

// mergeImage builds the final image reference. When the per-container
// override carries Name or Tag, those win over the palette values.
// Registry / Prefix always come from YanetConfigV2.spec.images.
func mergeImage(
	images yanetv2alpha1.ImagesSpec,
	base yanetv2alpha1.ImageRef,
	override *yanetv2alpha1.YanetContainerOverride,
) ResolvedImage {
	out := ResolvedImage{
		Registry: images.Registry,
		Prefix:   images.Prefix,
		Name:     base.Name,
		Tag:      base.Tag,
	}
	if override != nil {
		if override.Name != "" {
			out.Name = override.Name
		}
		if override.Tag != "" {
			out.Tag = override.Tag
		}
	}
	return out
}

// containerOverride looks up the per-container image override for the
// given container name in the component-level override block. Returns
// nil when no override is set, which lets mergeImage skip the merge.
func containerOverride(
	override *yanetv2alpha1.YanetComponentOverride,
	containerName string,
) *yanetv2alpha1.YanetContainerOverride {
	if override == nil || len(override.Containers) == 0 {
		return nil
	}
	if v, ok := override.Containers[containerName]; ok {
		return &v
	}
	return nil
}

// FullPath assembles the full image reference (registry/prefix/name:tag).
// Empty registry/prefix segments are skipped.
func (i ResolvedImage) FullPath() string {
	path := i.Name
	if i.Prefix != "" {
		path = i.Prefix + "/" + path
	}
	if i.Registry != "" {
		path = i.Registry + "/" + path
	}
	if i.Tag != "" {
		path = path + ":" + i.Tag
	}
	return path
}
