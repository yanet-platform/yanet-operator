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

// Package manifests builds Kubernetes resources for the v2alpha1 path.
//
// The v2 builder is intentionally minimal: it produces base
// Deployment skeletons (NUMA fan-out for controlplane, hugepages for
// dataplane, ConfigSource volumes for everything). Anything beyond
// that — annotations, postStart hooks, hostIPC/privileged, resource
// requests, init containers — lives in YanetConfigV2.spec.patches[]
// and is layered on top by ApplyPatches in patcher.go.
package manifests

import (
	"fmt"
	"strings"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildContextV2 carries everything the v2 builder needs to render a
// component into one or more Deployments.
type BuildContextV2 struct {
	// YanetName is the metadata.name of the YanetV2 CR.
	YanetName string
	// Namespace where Deployments will be created.
	Namespace string
	// NodeName the Deployment is pinned to (via nodeSelector).
	// May be empty for cluster-wide operator placement; when empty
	// the builder falls back to YanetSpec.NodeSelector and skips
	// the kubernetes.io/hostname constraint.
	NodeName string
	// NumaCount is the number of NUMA domains on the node, used by
	// the controlplane fan-out. Read from the NFD label by the
	// reconciler. <= 0 falls back to 1.
	NumaCount int32
	// PullPolicy is propagated from YanetConfigV2.spec.images.
	PullPolicy corev1.PullPolicy
	// PullSecrets are propagated from YanetConfigV2.spec.images.
	PullSecrets []corev1.LocalObjectReference
	// OwnerRef makes generated objects garbage-collected with the
	// YanetV2 CR.
	OwnerRef metav1.OwnerReference
}

// BuildDeployments produces the Deployment skeletons for one
// resolved component. The slice has length 1 except for
// controlplane fan-out, where len(out) == effective NUMA count.
//
// The resulting Deployments are NOT yet patched. The caller wires
// them through ApplyPatches(patcher.go) before calling Server-Side
// Apply / CreateOrUpdate.
func BuildDeployments(ctx BuildContextV2, c *helpers.ResolvedComponent) ([]*appsv1.Deployment, error) {
	if c == nil {
		return nil, fmt.Errorf("buildDeployments: nil ResolvedComponent")
	}

	switch c.Kind {
	case helpers.KindControlplane:
		return buildControlplaneFanout(ctx, c)
	case helpers.KindOperator:
		return []*appsv1.Deployment{buildOperator(ctx, c)}, nil
	default:
		return []*appsv1.Deployment{buildSingle(ctx, c)}, nil
	}
}

// buildControlplaneFanout renders one Deployment per NUMA domain.
// Each instance listens on Port + numa_index.
func buildControlplaneFanout(ctx BuildContextV2, c *helpers.ResolvedComponent) ([]*appsv1.Deployment, error) {
	numa := effectiveNuma(ctx, c)
	out := make([]*appsv1.Deployment, 0, numa)
	for i := int32(0); i < numa; i++ {
		d := buildSingle(ctx, c)
		// Decorate Deployment name & labels with the NUMA index.
		d.Name = numaDeploymentName(ctx, c, i)
		d.Labels[labelNuma] = fmt.Sprintf("%d", i)
		d.Spec.Selector.MatchLabels[labelNuma] = fmt.Sprintf("%d", i)
		d.Spec.Template.Labels[labelNuma] = fmt.Sprintf("%d", i)
		// Per-instance listen port (Port + i). The base Service
		// load-balances across all instances by Port (round-robin).
		if c.Port > 0 {
			port := c.Port + i
			cont := &d.Spec.Template.Spec.Containers[0]
			cont.Ports = []corev1.ContainerPort{{
				Name:          "grpc",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			}}
		}
		out = append(out, d)
	}
	return out, nil
}

// effectiveNuma resolves the per-component NUMA count. The component
// override (ResolvedComponent.Numa) wins; fallback is the node label
// (BuildContextV2.NumaCount); ultimate fallback is 1.
func effectiveNuma(ctx BuildContextV2, c *helpers.ResolvedComponent) int32 {
	if c.Numa > 0 {
		return c.Numa
	}
	if ctx.NumaCount > 0 {
		return ctx.NumaCount
	}
	return 1
}

// numaDeploymentName encodes the NUMA index into the Deployment
// name to keep all instances distinguishable in `kubectl get`.
func numaDeploymentName(ctx BuildContextV2, c *helpers.ResolvedComponent, numa int32) string {
	if ctx.NodeName != "" {
		return fmt.Sprintf("%s-%s-%s-numa%d", ctx.YanetName, shortHash(ctx.NodeName), toLowerKebab(c.Name), numa)
	}
	return fmt.Sprintf("%s-%s-numa%d", ctx.YanetName, toLowerKebab(c.Name), numa)
}

// buildSingle renders the base single-Deployment skeleton for the
// hardcoded components (dataplane, bird, birdAdapter, announcer)
// AND for one controlplane NUMA instance (the caller will rename it
// after this).
func buildSingle(ctx BuildContextV2, c *helpers.ResolvedComponent) *appsv1.Deployment {
	labels := baseLabels(ctx, c)
	volumes, volumeMounts, configMapName, configArg := buildConfigVolumes(ctx, c)

	container := corev1.Container{
		Name:            toLowerKebab(string(c.Kind)),
		Image:           c.Image.FullPath(),
		ImagePullPolicy: ctx.PullPolicy,
		VolumeMounts:    volumeMounts,
	}
	if configArg != "" {
		container.Args = []string{configArg}
	}
	if c.Port > 0 {
		container.Ports = []corev1.ContainerPort{{
			Name:          defaultPortName(c.Kind),
			ContainerPort: c.Port,
			Protocol:      corev1.ProtocolTCP,
		}}
	}
	// Hugepages on dataplane.
	if c.Hugepages != nil {
		applyHugepages(&container, &volumes, c.Hugepages)
	}

	pod := corev1.PodSpec{
		Containers:       []corev1.Container{container},
		Volumes:          volumes,
		ImagePullSecrets: ctx.PullSecrets,
		NodeSelector:     nodeSelector(ctx),
	}
	// hostNetwork is on by default for dataplane (DPDK).
	if c.Kind == helpers.KindDataplane {
		pod.HostNetwork = helpers.BoolValue(c.HostNetwork, true)
	}

	d := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            singleDeploymentName(ctx, c),
			Namespace:       ctx.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{ctx.OwnerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicasFor(c),
			Selector: &metav1.LabelSelector{MatchLabels: copyMap(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyMap(labels)},
				Spec:       pod,
			},
		},
	}
	// Annotate with the resolved ConfigMap name (if any) so the
	// reconciler can clean up orphaned ConfigMaps later.
	if configMapName != "" {
		if d.Annotations == nil {
			d.Annotations = map[string]string{}
		}
		d.Annotations[annotationConfigMap] = configMapName
	}
	return d
}

// buildOperator renders the multi-container operator Deployment. The
// first container is the primary (the one a Service targets). Any
// container with HostIPC=true escalates to Pod-level hostIPC.
func buildOperator(ctx BuildContextV2, c *helpers.ResolvedComponent) *appsv1.Deployment {
	labels := baseLabels(ctx, c)
	pod := corev1.PodSpec{
		ImagePullSecrets: ctx.PullSecrets,
		NodeSelector:     nodeSelector(ctx),
	}
	hostIPC := false
	for i, rc := range c.Containers {
		volumes, mounts, _, configArg := buildConfigVolumesForContainer(ctx, c, &rc, i)
		pod.Volumes = append(pod.Volumes, volumes...)
		container := corev1.Container{
			Name:            rc.Name,
			Image:           rc.Image.FullPath(),
			ImagePullPolicy: ctx.PullPolicy,
			VolumeMounts:    mounts,
		}
		if configArg != "" {
			container.Args = []string{configArg}
		}
		if i == 0 && c.Port > 0 {
			container.Ports = []corev1.ContainerPort{{
				Name:          "grpc",
				ContainerPort: c.Port,
				Protocol:      corev1.ProtocolTCP,
			}}
		}
		pod.Containers = append(pod.Containers, container)
		if rc.HostIPC {
			hostIPC = true
		}
	}
	pod.HostIPC = hostIPC

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            singleDeploymentName(ctx, c),
			Namespace:       ctx.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{ctx.OwnerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicasFor(c),
			Selector: &metav1.LabelSelector{MatchLabels: copyMap(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyMap(labels)},
				Spec:       pod,
			},
		},
	}
}

// -- naming -------------------------------------------------------------------

// singleDeploymentName builds a stable Deployment name for the
// non-NUMA components. Per-node uniqueness is achieved by mixing the
// node name (or its short hash) into the suffix.
func singleDeploymentName(ctx BuildContextV2, c *helpers.ResolvedComponent) string {
	if ctx.NodeName != "" {
		return fmt.Sprintf("%s-%s-%s", ctx.YanetName, shortHash(ctx.NodeName), toLowerKebab(c.Name))
	}
	return fmt.Sprintf("%s-%s", ctx.YanetName, toLowerKebab(c.Name))
}

func defaultPortName(kind helpers.ComponentKind) string {
	switch kind {
	case helpers.KindBird:
		return "bgp"
	case helpers.KindControlplane, helpers.KindBirdAdapter, helpers.KindAnnouncer:
		return "grpc"
	default:
		return "main"
	}
}

// -- labels -------------------------------------------------------------------

const (
	labelYanet     = "yanet.yanet-platform.io/yanet"
	labelComponent = "yanet.yanet-platform.io/component"
	labelNuma      = "yanet.yanet-platform.io/numa"
	labelNode      = "yanet.yanet-platform.io/node"

	annotationConfigMap = "yanet.yanet-platform.io/configmap"

	// Tracking annotations: comma-separated lists of label /
	// annotation keys the operator owns on a resource. Used by the
	// merge logic to drop keys retracted from the desired set while
	// leaving foreign keys (sidecars, webhooks) untouched.
	annotationManagedLabels      = "yanet.yanet-platform.io/managed-labels"
	annotationManagedAnnotations = "yanet.yanet-platform.io/managed-annotations"
)

func baseLabels(ctx BuildContextV2, c *helpers.ResolvedComponent) map[string]string {
	out := map[string]string{
		labelYanet:     ctx.YanetName,
		labelComponent: c.Name,
		"app":          c.Name,
	}
	if ctx.NodeName != "" {
		out[labelNode] = ctx.NodeName
	}
	return out
}

func nodeSelector(ctx BuildContextV2) map[string]string {
	if ctx.NodeName == "" {
		return nil
	}
	return map[string]string{
		"kubernetes.io/hostname": ctx.NodeName,
	}
}

func replicasFor(c *helpers.ResolvedComponent) *int32 {
	if !c.Enabled {
		zero := int32(0)
		return &zero
	}
	one := int32(1)
	return &one
}

// -- config volumes -----------------------------------------------------------

// buildConfigVolumes synthesises Pod-level Volumes and main-container
// VolumeMounts for the (singular) ConfigSource of the resolved
// component. The returned configMapName is non-empty only for inline
// configs — the reconciler must (re)create that ConfigMap.
// configArg is non-empty when ConfigSource.FileName is set; it carries
// the --config=<mountPath>/<fileName> argument to pass to the container.
func buildConfigVolumes(ctx BuildContextV2, c *helpers.ResolvedComponent) (
	volumes []corev1.Volume,
	mounts []corev1.VolumeMount,
	configMapName, configArg string,
) {
	cs := c.Config
	if cs.IsZero() {
		return nil, nil, "", ""
	}
	mountPath := defaultConfigMountPath(c.Kind)
	switch {
	case cs.HostPath != "":
		volumes = []corev1.Volume{{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: cs.HostPath},
			},
		}}
		mounts = []corev1.VolumeMount{{Name: "config", MountPath: mountPath, ReadOnly: true}}
	case cs.Inline != "":
		configMapName = inlineConfigMapName(ctx, c, cs.Inline)
		cmVol := corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
		}
		// When FileName is set, remap the "config" key inside the
		// ConfigMap to the requested file name so the component
		// binary finds it at <mountPath>/<FileName>.
		if cs.FileName != "" {
			cmVol.Items = []corev1.KeyToPath{{Key: "config", Path: cs.FileName}}
		}
		volumes = []corev1.Volume{{
			Name:         "config",
			VolumeSource: corev1.VolumeSource{ConfigMap: &cmVol},
		}}
		mounts = []corev1.VolumeMount{{Name: "config", MountPath: mountPath, ReadOnly: true}}
	case cs.URL != "":
		// URL-based config is downloaded by an initContainer
		// into an emptyDir; the patcher / future logic decides
		// the exact init image. For now we expose the empty
		// volume and let a patch attach the init container.
		volumes = []corev1.Volume{{
			Name:         "config",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		mounts = []corev1.VolumeMount{{Name: "config", MountPath: mountPath}}
	}
	if cs.FileName != "" {
		configArg = fmt.Sprintf("--config=%s/%s", mountPath, cs.FileName)
	}
	return volumes, mounts, configMapName, configArg
}

// buildConfigVolumesForContainer is the per-operator-container
// equivalent. It mounts the container-level Config when set; the
// volume name is derived from the container index to avoid clashes.
// configArg is non-empty when ConfigSource.FileName is set; it carries
// the --config=<mountPath>/<fileName> argument for the container.
func buildConfigVolumesForContainer(
	ctx BuildContextV2,
	c *helpers.ResolvedComponent,
	rc *helpers.ResolvedContainer,
	idx int,
) (volumes []corev1.Volume, mounts []corev1.VolumeMount, configMapName, configArg string) {
	if rc.Config.IsZero() {
		return nil, nil, "", ""
	}
	volName := fmt.Sprintf("config-%d", idx)
	mountPath := defaultConfigMountPath(c.Kind)
	switch {
	case rc.Config.HostPath != "":
		volumes = []corev1.Volume{{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: rc.Config.HostPath},
			},
		}}
		mounts = []corev1.VolumeMount{{Name: volName, MountPath: mountPath, ReadOnly: true}}
	case rc.Config.Inline != "":
		configMapName = inlineContainerConfigMapName(ctx, c, idx, rc.Config.Inline)
		cmVol := corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
		}
		if rc.Config.FileName != "" {
			cmVol.Items = []corev1.KeyToPath{{Key: "config", Path: rc.Config.FileName}}
		}
		volumes = []corev1.Volume{{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{ConfigMap: &cmVol},
		}}
		mounts = []corev1.VolumeMount{{Name: volName, MountPath: mountPath, ReadOnly: true}}
	case rc.Config.URL != "":
		volumes = []corev1.Volume{{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		mounts = []corev1.VolumeMount{{Name: volName, MountPath: mountPath}}
	}
	if rc.Config.FileName != "" {
		configArg = fmt.Sprintf("--config=%s/%s", mountPath, rc.Config.FileName)
	}
	return volumes, mounts, configMapName, configArg
}

// defaultConfigMountPath gives a sensible per-component mount
// directory. Patches can override the actual file path inside the
// container if needed.
func defaultConfigMountPath(kind helpers.ComponentKind) string {
	switch kind {
	case helpers.KindBird:
		return "/etc/bird"
	default:
		return "/etc/yanet2"
	}
}

// toLowerKebab converts a camelCase or mixed-case string to a lowercase
// kebab-case string safe for use in Kubernetes resource names (RFC 1123).
// For example "birdAdapter" → "bird-adapter".
func toLowerKebab(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				out = append(out, '-')
			}
			out = append(out, c+('a'-'A'))
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}

// inlineConfigMapName produces a stable ConfigMap name keyed by both
// the deployment identity and the inline content hash, so that any
// edit to the inline body produces a fresh ConfigMap (and a Pod
// rollout). Component names are normalised to lowercase-kebab to
// satisfy RFC 1123 (e.g. "birdAdapter" → "bird-adapter").
func inlineConfigMapName(ctx BuildContextV2, c *helpers.ResolvedComponent, content string) string {
	return fmt.Sprintf("%s-%s-cfg-%s",
		singleDeploymentName(ctx, c),
		toLowerKebab(c.Name),
		shortHashStr(content),
	)
}

func inlineContainerConfigMapName(ctx BuildContextV2, c *helpers.ResolvedComponent, idx int, content string) string {
	return fmt.Sprintf("%s-%s-c%d-cfg-%s",
		singleDeploymentName(ctx, c),
		toLowerKebab(c.Name),
		idx,
		shortHashStr(content),
	)
}

// InlineConfigMaps returns the {name → content} map of every inline
// ConfigMap that the resolved component requires. The reconciler
// iterates this map to CreateOrUpdate the corresponding objects
// before applying the Deployment.
//
// For non-inline configs the returned map is empty.
func InlineConfigMaps(ctx BuildContextV2, c *helpers.ResolvedComponent) map[string]string {
	out := map[string]string{}
	if c.Kind == helpers.KindOperator {
		for i, rc := range c.Containers {
			if !rc.Config.IsZero() && rc.Config.Inline != "" {
				out[inlineContainerConfigMapName(ctx, c, i, rc.Config.Inline)] = rc.Config.Inline
			}
		}
		return out
	}
	if !c.Config.IsZero() && c.Config.Inline != "" {
		out[inlineConfigMapName(ctx, c, c.Config.Inline)] = c.Config.Inline
	}
	return out
}

// -- hugepages ---------------------------------------------------------------

// applyHugepages adds a hugepages volume + mount + resource request
// to the dataplane main container. The exact size key is derived
// from Hugepages.Size: "1Gi" → hugepages-1Gi, "2Mi" → hugepages-2Mi.
func applyHugepages(c *corev1.Container, volumes *[]corev1.Volume, hp *yanetv2alpha1.Hugepages) {
	const volName = "hugepages"
	*volumes = append(*volumes, corev1.Volume{
		Name: volName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/dev/hugepages"},
		},
	})
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name:      volName,
		MountPath: "/dev/hugepages",
	})
	resourceName := corev1.ResourceName(fmt.Sprintf("hugepages-%s", hp.Size))
	totalQty := resource.MustParse(fmt.Sprintf("%d%s", hp.Count, trimUnitPrefix(hp.Size)))
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
	c.Resources.Requests[resourceName] = totalQty
	c.Resources.Limits[resourceName] = totalQty
}

// trimUnitPrefix returns the unit-suffix tail of a Kubernetes Quantity
// literal so a new integer count can be re-attached:
//
//	"1Gi"   -> "Gi"
//	"10Gi"  -> "Gi"
//	"1.5Gi" -> "Gi"
//	"+2Mi"  -> "Mi"
//	"Gi"    -> "Gi"
//	""      -> ""
//
// Anything in [0-9.+-] counts as part of the numeric prefix; the
// suffix starts at the first character that doesn't.
func trimUnitPrefix(s string) string {
	cut := strings.IndexFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r == '.' || r == '+' || r == '-')
	})
	if cut < 0 {
		return ""
	}
	return s[cut:]
}

// -- misc helpers ------------------------------------------------------------

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// shortHash and shortHashStr are aliases for helpers.ShortNodeKey to
// keep call-sites readable: shortHash(nodeName), shortHashStr(content).
func shortHash(in string) string    { return helpers.ShortNodeKey(in) }
func shortHashStr(in string) string { return helpers.ShortNodeKey(in) }
