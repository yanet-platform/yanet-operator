package manifests

import (
	"context"
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DeploymentForDataplane return dataplane Deployment object
func DeploymentForDataplane(
	ctx context.Context,
	m *yanetv1alpha1.Yanet,
	config yanetv1alpha1.YanetConfigSpec,
	nodes v1.NodeList) *appsv1.Deployment {
	log := log.FromContext(ctx)
	ok, perTypeOpts := helpers.GetTypeOpts(config.EnabledOpts, m.Spec.Type)
	if !ok {
		log.Info("typeOpts is not specified", "type", m.Spec.Type)
	}

	// Prepare init containers
	initContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers,
		perTypeOpts.Dataplain.InitContainers,
	)

	// Prepare poststart hook
	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Dataplain.PostStart)

	// Determine image and tag
	image := m.Spec.Dataplane.Image
	tag := m.Spec.Tag
	if m.Spec.Dataplane.Tag != "" {
		tag = m.Spec.Dataplane.Tag
	}

	// Calculate replicas
	replicas := int32(0)
	if m.Spec.Dataplane.Enable {
		replicas = 1
	}

	// Build security context
	privileged := perTypeOpts.Dataplain.Privileged
	securityCtx := &v1.SecurityContext{
		Privileged: &privileged,
		Capabilities: &v1.Capabilities{
			Add: []v1.Capability{
				"NET_ADMIN",
				"NET_BIND_SERVICE",
				"IPC_LOCK",
				"SYS_MODULE",
				"SYS_NICE",
			},
		},
	}

	// Build deployment using builder pattern
	return NewDeploymentBuilder().
		WithYanet(m).
		WithName(fmt.Sprintf("dataplane-%s", m.Spec.NodeName)).
		WithComponentName("dataplane").
		WithReplicas(replicas).
		WithImage(m.Spec.Registry, image, tag).
		WithHostNetwork(true).
		WithHostIPC(perTypeOpts.Dataplain.HostIpc).
		WithAnnotations(AnnotationsForYanet(
			config.AdditionalOpts.Annotations,
			perTypeOpts.Dataplain.Annotations,
		)).
		WithNodeSelector(map[string]string{
			"kubernetes.io/hostname": m.Spec.NodeName,
		}).
		WithTolerations(TolerationsForYanet()).
		WithContainer("dataplane",
			[]string{"/usr/bin/yanet-dataplane"},
			[]string{"-c", "/etc/yanet/dataplane.conf"},
		).
		WithVolumeMounts([]v1.VolumeMount{
			{Name: "hugepage", MountPath: "/dev/hugepages"},
			{Name: "etc-yanet", MountPath: "/etc/yanet"},
			{Name: "run-yanet", MountPath: "/run/yanet"},
		}).
		WithResources(GetResources(
			ctx,
			m.Spec.NodeName,
			perTypeOpts.Dataplain.Resources,
			nodes,
			true,
		)).
		WithSecurityContext(securityCtx).
		WithLifecycle(&v1.Lifecycle{
			PostStart: &v1.LifecycleHandler{
				Exec: &v1.ExecAction{Command: poststart},
			},
		}).
		WithInitContainers(initContainers).
		WithVolumes(GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet"})).
		Build()
}
