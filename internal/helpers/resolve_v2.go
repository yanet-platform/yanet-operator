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
	"slices"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

// ShortNodeKey returns the stable short identity used in generated names.
func ShortNodeKey(nodeName string) string {
	h := sha256.Sum256([]byte(nodeName))
	return hex.EncodeToString(h[:4])
}

// ComponentKind distinguishes independently deployed roles from native sidecars.
type ComponentKind string

const (
	KindControlplane ComponentKind = "controlplane"
	KindDataplane    ComponentKind = "dataplane"
	KindBirdAdapter  ComponentKind = "birdAdapter"
	KindOperator     ComponentKind = "operator"
	KindSidecar      ComponentKind = "sidecar"
)

// ResolvedImage contains the palette image with installation overrides applied.
type ResolvedImage struct {
	Registry string
	Prefix   string
	Name     string
	Tag      string
}

// ResolvedComponent is the immutable rendering input for one workload or sidecar.
type ResolvedComponent struct {
	Kind         ComponentKind
	Name         string
	Enabled      bool
	Image        ResolvedImage
	Config       *yanetv2alpha1.ConfigSource
	Hugepages    *yanetv2alpha1.Hugepages
	Numa         int32
	DisabledNuma []int32
	Networks     []yanetv2alpha1.NetworkAttachment
	// NetworkResources includes inherited and overridden names, so patches
	// cannot retain stale device reservations when an override replaces a pool.
	NetworkResources []string

	// Containers belongs only to standalone operators. The first owns listeners.
	Containers []ResolvedContainer
	// Sidecars retains the entire ordered palette, including unselected and
	// disabled entries, so selection and enablement never compact port slots.
	Sidecars []*ResolvedComponent
	// Nil listeners defaults to grpc; an explicit empty slice exposes no Service.
	ListenerNames []string
	PortIndex     int
	Patches       []string
}

// ResolvedContainer carries a standalone operator container's effective inputs.
type ResolvedContainer struct {
	Name   string
	Image  ResolvedImage
	Config *yanetv2alpha1.ConfigSource
}

// FindBoxType returns the requested preset from the current configuration.
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

// FindOperator returns a declared standalone operator.
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

// ResolveBoxComponent combines palette definitions, box wiring and typed overrides.
// An unwired independent component has no rendering input.
func ResolveBoxComponent(config *yanetv2alpha1.YanetConfigSpec, yanet *yanetv2alpha1.YanetSpec,
	kind ComponentKind, operatorName string,
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
		if box.Components.Controlplane == nil {
			return nil, nil
		}
		cp := config.Components.Controlplane
		override := componentOverride(yanet, kind, "")
		return &ResolvedComponent{
			Kind: kind, Name: string(kind), Enabled: resolveEnabled(override),
			Image:  mergeImage(config.Images, cp.Image, containerOverride(override, "controlplane")),
			Config: cp.Config, Numa: Int32Value(cp.Numa, 0),
			DisabledNuma: resolveDisabledNuma(cp.DisabledNuma, yanet), Patches: box.Components.Controlplane.Patches,
		}, nil
	case KindDataplane:
		if box.Components.Dataplane == nil {
			return nil, nil
		}
		sidecars, err := resolveDataplaneSidecars(config, yanet, box)
		if err != nil {
			return nil, err
		}
		dp := config.Components.Dataplane
		networks, resources, err := resolveDataplaneNetworks(dp.Networks, yanet)
		if err != nil {
			return nil, err
		}
		override := componentOverride(yanet, kind, "")
		return &ResolvedComponent{
			Kind: kind, Name: string(kind), Enabled: resolveEnabled(override),
			Image:  mergeImage(config.Images, dp.Image, containerOverride(override, "dataplane")),
			Config: dp.Config, Hugepages: dp.Hugepages, Sidecars: sidecars, Patches: box.Components.Dataplane.Patches,
			Networks: networks, NetworkResources: resources,
		}, nil
	case KindSidecar:
		if box.Components.Dataplane == nil {
			return nil, nil
		}
		if _, wired := box.Components.Dataplane.Sidecars[operatorName]; !wired {
			return nil, nil
		}
		sidecars, err := resolveDataplaneSidecars(config, yanet, box)
		if err != nil {
			return nil, err
		}
		for _, sidecar := range sidecars {
			if sidecar.Name == operatorName {
				return sidecar, nil
			}
		}
		return nil, fmt.Errorf("sidecar %q is not declared in the dataplane palette", operatorName)
	case KindBirdAdapter:
		if box.Components.BirdAdapter == nil {
			return nil, nil
		}
		if config.Components.BirdAdapter == nil {
			return nil, fmt.Errorf("boxType %q wires birdAdapter but YanetConfigV2.spec.components.birdAdapter is not defined", box.Name)
		}
		adapter := config.Components.BirdAdapter
		override := componentOverride(yanet, kind, "")
		return &ResolvedComponent{
			Kind: kind, Name: string(kind), Enabled: resolveEnabled(override),
			Image:  mergeImage(config.Images, adapter.Image, containerOverride(override, "bird-adapter")),
			Config: adapter.Config, Patches: box.Components.BirdAdapter.Patches,
		}, nil
	case KindOperator:
		return resolveOperator(config, yanet, box, operatorName)
	default:
		return nil, fmt.Errorf("unknown component kind %q", kind)
	}
}

func resolveDataplaneNetworks(defaults []yanetv2alpha1.NetworkAttachment, yanet *yanetv2alpha1.YanetSpec) ([]yanetv2alpha1.NetworkAttachment, []string, error) {
	if err := yanetv2alpha1.ValidateNetworkAttachments(defaults); err != nil {
		return nil, nil, err
	}
	selected := defaults
	if yanet.Components != nil && yanet.Components.Dataplane != nil && yanet.Components.Dataplane.Networks != nil {
		selected = yanet.Components.Dataplane.Networks
	}
	if err := yanetv2alpha1.ValidateNetworkAttachments(selected); err != nil {
		return nil, nil, err
	}
	resources := make(map[string]bool)
	for _, group := range [][]yanetv2alpha1.NetworkAttachment{defaults, selected} {
		for _, attachment := range group {
			resources[attachment.ResourceName] = true
		}
	}
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Clone(selected), names, nil
}

// ResolveBoxServiceComponent ignores installation enablement and NUMA opt-outs.
// Shared role Services outlive temporarily disabled workloads.
func ResolveBoxServiceComponent(config *yanetv2alpha1.YanetConfigSpec, boxName string,
	kind ComponentKind, operatorName string,
) (*ResolvedComponent, error) {
	return ResolveBoxComponent(config, &yanetv2alpha1.YanetSpec{BoxType: boxName}, kind, operatorName)
}

// ComponentRef identifies a selected workload or sidecar Service role.
type ComponentRef struct {
	Kind         ComponentKind
	OperatorName string
}

// EnabledComponentsForBox enumerates wired roles in declaration order.
func EnabledComponentsForBox(config *yanetv2alpha1.YanetConfigSpec, boxName string) ([]ComponentRef, error) {
	box, err := FindBoxType(config, boxName)
	if err != nil {
		return nil, err
	}
	for name := range box.Operators {
		if _, err := FindOperator(config, name); err != nil {
			return nil, err
		}
	}
	var refs []ComponentRef
	if box.Components.Controlplane != nil {
		refs = append(refs, ComponentRef{Kind: KindControlplane})
	}
	if box.Components.Dataplane != nil {
		refs = append(refs, ComponentRef{Kind: KindDataplane})
		sidecars, err := resolveDataplaneSidecars(config, &yanetv2alpha1.YanetSpec{BoxType: boxName}, box)
		if err != nil {
			return nil, err
		}
		for _, sidecar := range sidecars {
			if _, wired := box.Components.Dataplane.Sidecars[sidecar.Name]; wired {
				refs = append(refs, ComponentRef{Kind: KindSidecar, OperatorName: sidecar.Name})
			}
		}
	}
	if box.Components.BirdAdapter != nil {
		refs = append(refs, ComponentRef{Kind: KindBirdAdapter})
	}
	for _, operator := range config.Components.Operators {
		if _, wired := box.Operators[operator.Name]; wired {
			refs = append(refs, ComponentRef{Kind: KindOperator, OperatorName: operator.Name})
		}
	}
	return refs, nil
}

func resolveDisabledNuma(clusterWide []int32, yanet *yanetv2alpha1.YanetSpec) []int32 {
	if yanet.Components != nil && yanet.Components.Controlplane != nil && yanet.Components.Controlplane.DisabledNuma != nil {
		return append([]int32(nil), yanet.Components.Controlplane.DisabledNuma...)
	}
	return append([]int32(nil), clusterWide...)
}

func resolveDataplaneSidecars(config *yanetv2alpha1.YanetConfigSpec, yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType,
) ([]*ResolvedComponent, error) {
	palette := config.Components.Dataplane.Sidecars
	if len(palette) > yanetv2alpha1.MaxDataplaneSidecars {
		return nil, fmt.Errorf("dataplane sidecars exhaust the listener port range")
	}
	declared := map[string]bool{}
	identities := map[string]string{}
	var resolved []*ResolvedComponent
	for index, sidecar := range palette {
		if declared[sidecar.Name] {
			return nil, fmt.Errorf("duplicate dataplane sidecar %q", sidecar.Name)
		}
		declared[sidecar.Name] = true
		identity := ShortNodeKey(sidecar.Name)
		if previous, collision := identities[identity]; collision {
			return nil, fmt.Errorf("sidecars %q and %q have colliding role identities", previous, sidecar.Name)
		}
		identities[identity] = sidecar.Name
		if err := yanetv2alpha1.ValidateListeners(sidecar.Name, sidecar.Listeners); err != nil {
			return nil, err
		}
		slot, wired := box.Components.Dataplane.Sidecars[sidecar.Name]
		var override *yanetv2alpha1.YanetContainerOverride
		if wired && yanet.Components != nil && yanet.Components.Dataplane != nil {
			if value, present := yanet.Components.Dataplane.Sidecars[sidecar.Name]; present {
				override = &value
			}
		}
		enabled := wired && BoolValue(slot.Enabled, true)
		if override != nil && override.Enabled != nil {
			enabled = *override.Enabled
		}
		resolved = append(resolved, &ResolvedComponent{
			Kind: KindSidecar, Name: sidecar.Name, Enabled: enabled,
			Image: mergeImage(config.Images, sidecar.Image, override), Config: sidecar.Config,
			ListenerNames: resolveListeners(sidecar.Listeners), PortIndex: index, Patches: slot.Patches,
		})
	}
	for name := range box.Components.Dataplane.Sidecars {
		if !declared[name] {
			return nil, fmt.Errorf("boxType %q wires dataplane sidecar %q but the palette does not define it", box.Name, name)
		}
	}
	return resolved, nil
}

func resolveOperator(config *yanetv2alpha1.YanetConfigSpec, yanet *yanetv2alpha1.YanetSpec,
	box *yanetv2alpha1.BoxType, name string,
) (*ResolvedComponent, error) {
	slot, wired := box.Operators[name]
	if !wired {
		return nil, nil
	}
	operator, err := FindOperator(config, name)
	if err != nil {
		return nil, err
	}
	if len(operator.Containers) == 0 {
		return nil, fmt.Errorf("operator %q has no containers", name)
	}
	if err := yanetv2alpha1.ValidateListeners(name, operator.Listeners); err != nil {
		return nil, err
	}
	override := componentOverride(yanet, KindOperator, name)
	containers := make([]ResolvedContainer, 0, len(operator.Containers))
	for _, container := range operator.Containers {
		containers = append(containers, ResolvedContainer{
			Name: container.Name, Image: mergeImage(config.Images, container.Image, containerOverride(override, container.Name)),
			Config: container.Config,
		})
	}
	return &ResolvedComponent{
		Kind: KindOperator, Name: name, Enabled: resolveEnabled(override),
		Image: containers[0].Image, Containers: containers, Patches: slot.Patches,
		ListenerNames: resolveListeners(operator.Listeners),
	}, nil
}

func resolveListeners(listeners *[]yanetv2alpha1.OperatorListener) []string {
	if listeners == nil {
		return nil
	}
	result := make([]string, 0, len(*listeners))
	for _, protocol := range []yanetv2alpha1.OperatorListener{"grpc", "http"} {
		for _, listener := range *listeners {
			if listener == protocol {
				result = append(result, string(listener))
			}
		}
	}
	return result
}

// IsColocated identifies a role composed into the dataplane workload.
func (c *ResolvedComponent) IsColocated() bool {
	return c != nil && c.Kind == KindSidecar
}

func componentOverride(yanet *yanetv2alpha1.YanetSpec, kind ComponentKind, operatorName string) *yanetv2alpha1.YanetComponentOverride {
	if yanet.Components == nil {
		return nil
	}
	switch kind {
	case KindControlplane:
		if yanet.Components.Controlplane != nil {
			return &yanet.Components.Controlplane.YanetComponentOverride
		}
	case KindDataplane:
		if yanet.Components.Dataplane != nil {
			return &yanet.Components.Dataplane.YanetComponentOverride
		}
	case KindBirdAdapter:
		return yanet.Components.BirdAdapter
	case KindOperator:
		if override, present := yanet.Components.Operators[operatorName]; present {
			return &override
		}
	}
	return nil
}

func resolveEnabled(override *yanetv2alpha1.YanetComponentOverride) bool {
	return override == nil || BoolValue(override.Enabled, true)
}

func mergeImage(images yanetv2alpha1.ImagesSpec, base yanetv2alpha1.ImageRef,
	override *yanetv2alpha1.YanetContainerOverride,
) ResolvedImage {
	result := ResolvedImage{Registry: images.Registry, Prefix: images.Prefix, Name: base.Name, Tag: base.Tag}
	if base.Registry != nil {
		result.Registry = *base.Registry
	}
	if base.Prefix != nil {
		result.Prefix = *base.Prefix
	}
	if override != nil {
		if override.Name != "" {
			result.Name = override.Name
		}
		if override.Tag != "" {
			result.Tag = override.Tag
		}
	}
	return result
}

func containerOverride(override *yanetv2alpha1.YanetComponentOverride, name string) *yanetv2alpha1.YanetContainerOverride {
	if override != nil {
		if container, present := override.Containers[name]; present {
			return &container
		}
	}
	return nil
}

// FullPath assembles an image reference, including digest-qualified tags.
func (i ResolvedImage) FullPath() string {
	path := i.Name
	if i.Prefix != "" {
		path = i.Prefix + "/" + path
	}
	if i.Registry != "" {
		path = i.Registry + "/" + path
	}
	if i.Tag != "" {
		path += ":" + i.Tag
	}
	return path
}
