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

func newAnnouncerInitContainers() []v1.Container {
	initContainers := []v1.Container{
		{
			Image:           "busybox",
			Name:            "wait-bird",
			ImagePullPolicy: "IfNotPresent",
			Command:         []string{"/bin/sh"},
			Args: []string{
				"-c",
				`until [ -e /run/bird/bird.ctl ]; do
					echo "bird.ctl waiting...";
					sleep 3;
				done;
				sleep 5;`,
			},
			VolumeMounts: []v1.VolumeMount{
				{Name: "run-bird", MountPath: "/run/bird"},
			},
			TerminationMessagePath:   "/dev/stdout",
			TerminationMessagePolicy: "File",
		},
	}
	return initContainers
}

// DeploymentForAnnouncer returns a yanet announcer Deployment object
func DeploymentForAnnouncer(
	ctx context.Context,
	m *yanetv1alpha1.Yanet,
	config yanetv1alpha1.YanetConfigSpec,
	nodes v1.NodeList) *appsv1.Deployment {
	log := log.FromContext(ctx)
	ok, perTypeOpts := helpers.GetTypeOpts(config.EnabledOpts, m.Spec.Type)
	if !ok {
		log.Info("typeOpts is not specified", "type", m.Spec.Type)
	}

	// Build init containers (wait-bird + additional)
	initContainers := newAnnouncerInitContainers()
	additionalInitContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers,
		perTypeOpts.Announcer.InitContainers,
	)
	initContainers = append(initContainers, additionalInitContainers...)

	// Prepare poststart hook
	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Announcer.PostStart)

	// Determine image and tag
	image := m.Spec.Announcer.Image
	tag := m.Spec.Tag
	if m.Spec.Announcer.Tag != "" {
		tag = m.Spec.Announcer.Tag
	}

	// Calculate replicas
	replicas := int32(0)
	if m.Spec.Announcer.Enable {
		replicas = 1
	}

	// Build security context
	privileged := perTypeOpts.Announcer.Privileged
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
		WithName(fmt.Sprintf("announcer-%s", m.Spec.NodeName)).
		WithComponentName("announcer").
		WithReplicas(replicas).
		WithImage(m.Spec.Registry, image, tag).
		WithHostNetwork(true).
		WithHostIPC(perTypeOpts.Announcer.HostIpc).
		WithAnnotations(AnnotationsForYanet(
			config.AdditionalOpts.Annotations,
			perTypeOpts.Announcer.Annotations,
		)).
		WithNodeSelector(map[string]string{
			"kubernetes.io/hostname": m.Spec.NodeName,
		}).
		WithTolerations(TolerationsForYanet()).
		WithContainer("announcer",
			[]string{"/usr/bin/yanet-announcer"},
			[]string{"--run"},
		).
		WithVolumeMounts([]v1.VolumeMount{
			{Name: "etc-yanet", MountPath: "/etc/yanet"},
			{Name: "run-yanet", MountPath: "/run/yanet"},
			{Name: "run-bird", MountPath: "/run/bird"},
		}).
		WithResources(GetResources(
			ctx,
			m.Spec.NodeName,
			perTypeOpts.Announcer.Resources,
			nodes,
			false,
		)).
		WithSecurityContext(securityCtx).
		WithLifecycle(&v1.Lifecycle{
			PostStart: &v1.LifecycleHandler{
				Exec: &v1.ExecAction{Command: poststart},
			},
		}).
		WithInitContainers(initContainers).
		WithVolumes(GetVolumes([]string{"/etc/yanet", "/run/yanet", "/run/bird"})).
		Build()
}
