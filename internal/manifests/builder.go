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

// Package manifests builds Kubernetes resources for the v1alpha1 path.
//
// The builder is intentionally minimal: it produces base
// Deployment skeletons (NUMA fan-out for controlplane, hugepages and native
// sidecars for dataplane, ConfigSource volumes for everything). It also emits
// the intrinsic security/mount baseline a component cannot run without — the
// dataplane's privileged + hostIPC + minimal host devices
// (applyDataplaneSecurity) and the controlplane's hostIPC + shmem-arena mount
// (applyControlplaneShmem). Everything optional
// beyond that and typed dataplane network attachments — general annotations,
// postStart hooks, resource requests, init
// containers, extra hostIPC/privileged for operators — lives in
// YanetConfig.spec.patches[] and is layered on top by ApplyPatches
// in patcher.go.
package manifests

import (
	"fmt"
	"strings"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildContext carries everything the builder needs to render a
// component into one or more Deployments.
type BuildContext struct {
	// YanetName is the metadata.name of the Yanet CR.
	YanetName string
	// Namespace where Deployments will be created.
	Namespace string
	// BoxType is the shared service and endpoint identity selected by Yanet.
	BoxType string
	// NodeName the Deployment is pinned to (via nodeSelector).
	// May be empty for cluster-wide operator placement; when empty
	// the builder falls back to YanetSpec.NodeSelector and skips
	// the kubernetes.io/hostname constraint.
	NodeName string
	// PullPolicy is propagated from YanetConfig.spec.images.
	PullPolicy corev1.PullPolicy
	// PullSecrets are propagated from YanetConfig.spec.images.
	PullSecrets []corev1.LocalObjectReference
	// OwnerRef makes generated objects garbage-collected with the
	// Yanet CR.
	OwnerRef metav1.OwnerReference
	// Gateways is the complete active physical-NUMA gateway selection.
	// Nil means no deployment-specific gateway override was requested.
	Gateways []GatewayEndpointOverride
}

// BuildDeployments produces the Deployment skeletons for one
// resolved component. The slice has length 1 except for
// controlplane fan-out, where len(out) == effective NUMA count.
//
// The resulting Deployments are NOT yet patched. The caller wires
// them through ApplyPatches(patcher.go) before calling Server-Side
// Apply / CreateOrUpdate.
func BuildDeployments(ctx BuildContext, c *helpers.ResolvedComponent) ([]*appsv1.Deployment, error) {
	if c == nil {
		return nil, fmt.Errorf("buildDeployments: nil ResolvedComponent")
	}
	if c.Hugepages != nil {
		if _, err := c.Hugepages.TotalQuantity(); err != nil {
			return nil, fmt.Errorf("buildDeployments: invalid hugepages for component %q: %w", c.Name, err)
		}
	}

	var deployments []*appsv1.Deployment
	switch c.Kind {
	case helpers.KindControlplane:
		deployments = buildControlplaneFanout(ctx, c)
	case helpers.KindOperator:
		deployments = []*appsv1.Deployment{buildOperator(ctx, c)}
	default:
		deployments = []*appsv1.Deployment{buildSingle(ctx, c)}
	}
	for _, deployment := range deployments {
		// Sidecars are composed after their own logical patches are applied.
		if err := configureComponentListeners(deployment, c, false); err != nil {
			return nil, fmt.Errorf("buildDeployments: component %q: %w", c.Name, err)
		}
	}
	return deployments, nil
}

// buildControlplaneFanout renders one Deployment per NUMA domain.
//
// NUMA indices listed in DisabledNuma are skipped for Deployments. Shared
// Services remain unconditional so their DNS names do not appear and disappear
// as installations are scaled or temporarily disabled.
func buildControlplaneFanout(ctx BuildContext, c *helpers.ResolvedComponent) []*appsv1.Deployment {
	numa := effectiveNuma(c)
	disabled := disabledNumaSet(c)
	out := make([]*appsv1.Deployment, 0, numa)
	for i := int32(0); i < numa; i++ {
		if _, skip := disabled[i]; skip {
			continue
		}
		d := buildSingle(ctx, c)
		// Decorate Deployment name & labels with the NUMA index.
		d.Name = numaDeploymentName(ctx, c, i)
		d.Labels[labelNuma] = fmt.Sprintf("%d", i)
		d.Spec.Selector.MatchLabels[labelNuma] = fmt.Sprintf("%d", i)
		d.Spec.Template.Labels[labelNuma] = fmt.Sprintf("%d", i)
		// Each instance gets its own config file: the controlplane
		// reads gateway.instance_id and all endpoints from the file
		// and accepts only `-c <path>`, so a shared file would make
		// every instance serve dataplane instance 0.
		cont := &d.Spec.Template.Spec.Containers[0]
		cont.Args = numaConfigArgs(cont.Args, i)
		out = append(out, d)
	}
	return out
}

// disabledNumaSet indexes ResolvedComponent.DisabledNuma for O(1)
// lookups. Negative and duplicate entries are harmless: they simply
// never match a fan-out index.
func disabledNumaSet(c *helpers.ResolvedComponent) map[int32]struct{} {
	if len(c.DisabledNuma) == 0 {
		return nil
	}
	out := make(map[int32]struct{}, len(c.DisabledNuma))
	for _, n := range c.DisabledNuma {
		out[n] = struct{}{}
	}
	return out
}

// numaConfigArgs substitutes {numa} in explicit per-NUMA arguments, for example
// /etc/yanet2/controlplane.d/numa{numa}.yaml. All other arguments stay literal.
// The index is the physical fan-out index, not the position among enabled domains.
func numaConfigArgs(args []string, numa int32) []string {
	if len(args) == 0 {
		return args
	}
	out := append([]string(nil), args...)
	for i, a := range out {
		out[i] = strings.ReplaceAll(a, "{numa}", fmt.Sprint(numa))
	}
	return out
}

// effectiveNuma resolves the configured NUMA count, defaulting to one domain.
func effectiveNuma(c *helpers.ResolvedComponent) int32 {
	if c.Numa > 0 {
		return c.Numa
	}
	return 1
}

// numaDeploymentName encodes the NUMA index into the Deployment
// name to keep all instances distinguishable in `kubectl get`.
func numaDeploymentName(ctx BuildContext, c *helpers.ResolvedComponent, numa int32) string {
	if ctx.NodeName != "" {
		return fmt.Sprintf("%s-%s-%s-numa%d", ctx.YanetName, shortHash(ctx.NodeName), toLowerKebab(c.Name), numa)
	}
	return fmt.Sprintf("%s-%s-numa%d", ctx.YanetName, toLowerKebab(c.Name), numa)
}

// buildSingle renders the base single-Deployment skeleton for the fixed
// workload components and for one controlplane NUMA instance (the caller
// renames it afterwards).
func buildSingle(ctx BuildContext, c *helpers.ResolvedComponent) *appsv1.Deployment {
	selectorLabels := baseLabels(ctx, c)
	labels := workloadLabels(ctx, selectorLabels)
	volumes, volumeMounts, configMapName, configArgs := buildConfigVolumes(ctx, c)

	container := corev1.Container{
		Name:            toLowerKebab(string(c.Kind)),
		Image:           c.Image.FullPath(),
		ImagePullPolicy: ctx.PullPolicy,
		VolumeMounts:    volumeMounts,
	}
	if c.Kind == helpers.KindSidecar {
		container.Name = c.Name
	}
	container.Args = configArgs
	// Hugepages on dataplane.
	if c.Hugepages != nil {
		applyHugepages(&container, &volumes, c.Hugepages)
	}
	// Per-component security/mount baseline. This is intrinsic to how
	// each component runs (DPDK device access and shmem arena), so it lives in the builder rather than in a
	// YanetConfig patch.
	switch c.Kind {
	case helpers.KindDataplane:
		applyDataplaneSecurity(&container, &volumes)
	case helpers.KindControlplane:
		applyControlplaneShmem(&container, &volumes)
	}

	pod := corev1.PodSpec{
		Containers:       []corev1.Container{container},
		Volumes:          volumes,
		ImagePullSecrets: ctx.PullSecrets,
		NodeSelector:     nodeSelector(ctx),
	}
	switch c.Kind {
	case helpers.KindDataplane:
		pod.HostIPC = true
	case helpers.KindControlplane:
		// Modules in the controlplane attach to the dataplane shmem
		// arena (/dev/hugepages/yanet) over the host IPC namespace.
		pod.HostIPC = true
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
			Selector: &metav1.LabelSelector{MatchLabels: copyMap(selectorLabels)},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
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
// first container is the primary (the one a Service targets). Pod IPC settings
// and shared-memory mounts are explicit Deployment patches.
func buildOperator(ctx BuildContext, c *helpers.ResolvedComponent) *appsv1.Deployment {
	selectorLabels := baseLabels(ctx, c)
	labels := workloadLabels(ctx, selectorLabels)
	pod := corev1.PodSpec{
		ImagePullSecrets: ctx.PullSecrets,
		NodeSelector:     nodeSelector(ctx),
	}
	for i, rc := range c.Containers {
		volumes, mounts, _, configArgs := buildConfigVolumesForContainer(ctx, c, &rc, i)
		pod.Volumes = append(pod.Volumes, volumes...)
		container := corev1.Container{
			Name:            rc.Name,
			Image:           rc.Image.FullPath(),
			ImagePullPolicy: ctx.PullPolicy,
			VolumeMounts:    mounts,
		}
		container.Args = configArgs
		pod.Containers = append(pod.Containers, container)
	}

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
			Selector: &metav1.LabelSelector{MatchLabels: copyMap(selectorLabels)},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
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
func singleDeploymentName(ctx BuildContext, c *helpers.ResolvedComponent) string {
	if ctx.NodeName != "" {
		return fmt.Sprintf("%s-%s-%s", ctx.YanetName, shortHash(ctx.NodeName), toLowerKebab(c.Name))
	}
	return fmt.Sprintf("%s-%s", ctx.YanetName, toLowerKebab(c.Name))
}

// -- labels -------------------------------------------------------------------

const (
	labelYanet         = "yanet.yanet-platform.io/yanet"
	labelBoxType       = "yanet.yanet-platform.io/box-type"
	labelComponent     = "yanet.yanet-platform.io/component"
	labelNuma          = "yanet.yanet-platform.io/numa"
	labelNode          = "yanet.yanet-platform.io/node"
	labelSharedService = "yanet.yanet-platform.io/shared-service"

	annotationConfigMap = "yanet.yanet-platform.io/configmap"

	// Tracking annotations: comma-separated lists of label /
	// annotation keys the operator owns on a resource. Used by the
	// merge logic to drop keys retracted from the desired set while
	// leaving foreign keys (sidecars, webhooks) untouched.
	annotationManagedLabels      = "yanet.yanet-platform.io/managed-labels"
	annotationManagedAnnotations = "yanet.yanet-platform.io/managed-annotations"
)

func baseLabels(ctx BuildContext, c *helpers.ResolvedComponent) map[string]string {
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

func workloadLabels(ctx BuildContext, selectorLabels map[string]string) map[string]string {
	out := copyMap(selectorLabels)
	if ctx.BoxType != "" {
		out[labelBoxType] = ctx.BoxType
	}
	return out
}

func nodeSelector(ctx BuildContext) map[string]string {
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
// configArgs are copied verbatim from ConfigSource.Args.
func buildConfigVolumes(ctx BuildContext, c *helpers.ResolvedComponent) (
	volumes []corev1.Volume,
	mounts []corev1.VolumeMount,
	configMapName string,
	configArgs []string,
) {
	cs := c.Config
	if cs.IsZero() {
		return nil, nil, "", nil
	}
	mountPath := defaultConfigMountPath
	if cs.MountPath != "" {
		mountPath = cs.MountPath
	}
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
		volumes = []corev1.Volume{{
			Name:         "config",
			VolumeSource: corev1.VolumeSource{ConfigMap: &cmVol},
		}}
		mounts = []corev1.VolumeMount{{Name: "config", MountPath: mountPath, ReadOnly: true}}
	}
	return volumes, mounts, configMapName, append([]string(nil), cs.Args...)
}

// buildConfigVolumesForContainer is the per-operator-container
// equivalent. It mounts the container-level Config when set; the
// volume name is derived from the container index to avoid clashes.
// configArgs are copied verbatim from ConfigSource.Args.
func buildConfigVolumesForContainer(
	ctx BuildContext,
	c *helpers.ResolvedComponent,
	rc *helpers.ResolvedContainer,
	idx int,
) (volumes []corev1.Volume, mounts []corev1.VolumeMount, configMapName string, configArgs []string) {
	if rc.Config.IsZero() {
		return nil, nil, "", nil
	}
	volName := fmt.Sprintf("config-%d", idx)
	mountPath := defaultConfigMountPath
	if rc.Config.MountPath != "" {
		mountPath = rc.Config.MountPath
	}
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
		volumes = []corev1.Volume{{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{ConfigMap: &cmVol},
		}}
		mounts = []corev1.VolumeMount{{Name: volName, MountPath: mountPath, ReadOnly: true}}
	}
	return volumes, mounts, configMapName, append([]string(nil), rc.Config.Args...)
}

// defaultConfigMountPath is shared by all roles unless ConfigSource overrides it.
const defaultConfigMountPath = "/etc/yanet2"

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
func inlineConfigMapName(ctx BuildContext, c *helpers.ResolvedComponent, content string) string {
	return fmt.Sprintf("%s-%s-cfg-%s",
		singleDeploymentName(ctx, c),
		toLowerKebab(c.Name),
		shortHashStr(content),
	)
}

func inlineContainerConfigMapName(ctx BuildContext, c *helpers.ResolvedComponent, idx int, content string) string {
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
func InlineConfigMaps(ctx BuildContext, c *helpers.ResolvedComponent) map[string]string {
	out := map[string]string{}
	for _, sidecar := range c.Sidecars {
		if sidecar.Enabled {
			for name, content := range InlineConfigMaps(ctx, sidecar) {
				out[name] = content
			}
		}
	}
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

// -- dataplane / controlplane security baseline ------------------------------

// hostDevVolume builds a hostPath Volume for a /dev (or /sys) node. A nil
// hostPathType (typeless) skips kubelet path-type validation, which is
// required for single char-device nodes such as /dev/vhost-net.
func hostDevVolume(name, path string, t *corev1.HostPathType) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: path, Type: t},
		},
	}
}

// applyDataplaneSecurity gives the DPDK dataplane its mandatory privileged
// baseline plus the minimal set of host devices it needs.
//
// privileged: true is REQUIRED and cannot be replaced by a capability set:
// opening /dev/vfio/vfio and /dev/vhost-net is blocked by the device cgroup
// (containerd/runc) for any non-privileged container, even root with
// CAP_SYS_ADMIN/CAP_SYS_RAWIO. The only alternative is an SR-IOV device
// plugin (out of scope). Host exposure is otherwise minimized: only the
// specific device nodes DPDK uses are mounted, and /sys is read-only. The
// read-only config and hugepages mounts are added elsewhere in buildSingle.
func applyDataplaneSecurity(c *corev1.Container, volumes *[]corev1.Volume) {
	priv := true
	c.SecurityContext = &corev1.SecurityContext{Privileged: &priv}

	dir := corev1.HostPathDirectory
	c.VolumeMounts = append(c.VolumeMounts,
		corev1.VolumeMount{Name: "host-vfio", MountPath: "/dev/vfio"},
		corev1.VolumeMount{Name: "host-vhost-net", MountPath: "/dev/vhost-net"},
		corev1.VolumeMount{Name: "host-net", MountPath: "/dev/net"},
		corev1.VolumeMount{Name: "host-sys", MountPath: "/sys", ReadOnly: true},
	)
	*volumes = append(*volumes,
		// /dev/vfio: VFIO group + container nodes for DPDK PMD binding.
		hostDevVolume("host-vfio", "/dev/vfio", &dir),
		// /dev/vhost-net: single char device → typeless (no validation).
		hostDevVolume("host-vhost-net", "/dev/vhost-net", nil),
		// /dev/net: holds /dev/net/tun for KNI / virtio-user.
		hostDevVolume("host-net", "/dev/net", &dir),
		// /sys read-only: PCI/NUMA/hugepage topology that DPDK probes.
		hostDevVolume("host-sys", "/sys", &dir),
	)
}

// shmemVolName / shmemDir identify the hugepages-backed shmem arena that
// the dataplane publishes (files under /dev/hugepages/yanet) and that
// the controlplane mmaps. Optional agent mounts are declared in patches.
const (
	shmemVolName = "hugepages"
	shmemDir     = "/dev/hugepages"
)

func shmemMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: shmemVolName, MountPath: shmemDir}
}

func shmemVolume() corev1.Volume {
	return corev1.Volume{
		Name: shmemVolName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: shmemDir},
		},
	}
}

// applyControlplaneShmem mounts the hugepages-backed shmem arena into the
// controlplane. Controlplane modules mmap the dataplane arena to compile
// config into the binary format the dataplane reads. Unlike the dataplane
// this needs no privileged/device access — hugetlbfs files are not
// device-cgroup gated — only the mount plus pod-level hostIPC (set in
// buildSingle).
func applyControlplaneShmem(c *corev1.Container, volumes *[]corev1.Volume) {
	c.VolumeMounts = append(c.VolumeMounts, shmemMount())
	*volumes = append(*volumes, shmemVolume())
}

// -- hugepages ---------------------------------------------------------------

// applyHugepages adds a hugepages volume + mount + resource request
// to the dataplane main container. The exact size key is derived
// from Hugepages.Size: "1Gi" → hugepages-1Gi, "2Mi" → hugepages-2Mi.
func applyHugepages(c *corev1.Container, volumes *[]corev1.Volume, hp *yanetv1alpha1.Hugepages) {
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
	// hp is validated by TotalQuantity in BuildDeployments before we get
	// here, so this call cannot fail; guard defensively rather than ignore it.
	totalQty, err := hp.TotalQuantity()
	if err != nil {
		return
	}
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
	c.Resources.Requests[resourceName] = totalQty
	c.Resources.Limits[resourceName] = totalQty
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
