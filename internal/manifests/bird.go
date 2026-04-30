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

func newBirdInitContainers(m *yanetv1alpha1.Yanet) []v1.Container {
	image := fmt.Sprintf("%s:%s", m.Spec.Controlplane.Image, m.Spec.Tag)
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	initContainers := []v1.Container{
		{
			// TODO: make tools docker image
			Image:           image,
			Name:            "wait-controlplane",
			Command:         []string{"/bin/sh"},
			ImagePullPolicy: "IfNotPresent",
			Args: []string{
				"-c",
				`while [ $(/usr/bin/yanet-cli version | grep controlplane | wc -l) -ne 1 ]; do
					echo "controlplane waiting...";
					sleep 5;
				done;
				until [ -e /run/yanet/controlplane.sock ]; do
					echo "controlplane.sock waiting...";
					sleep 5;
				done;
				/usr/bin/yanet-cli version;
				sleep 5;`,
			},
			VolumeMounts: []v1.VolumeMount{
				{Name: "run-yanet", MountPath: "/run/yanet"},
			},
			TerminationMessagePath:   "/dev/stdout",
			TerminationMessagePolicy: "File",
		},
	}
	return initContainers
}

// DeploymentForBird return bird Deployment object
func DeploymentForBird(
	ctx context.Context,
	m *yanetv1alpha1.Yanet,
	config yanetv1alpha1.YanetConfigSpec,
	nodes v1.NodeList) *appsv1.Deployment {
	log := log.FromContext(ctx)
	ok, perTypeOpts := helpers.GetTypeOpts(config.EnabledOpts, m.Spec.Type)
	if !ok {
		log.Info("typeOpts is not specified", "type", m.Spec.Type)
	}

	// Build init containers (wait-controlplane + additional)
	initContainers := newBirdInitContainers(m)
	additionalInitContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers,
		perTypeOpts.Bird.InitContainers,
	)
	initContainers = append(initContainers, additionalInitContainers...)

	// Prepare poststart hook
	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Bird.PostStart)

	// Determine image and tag
	image := m.Spec.Bird.Image
	tag := m.Spec.Tag
	if m.Spec.Bird.Tag != "" {
		tag = m.Spec.Bird.Tag
	}

	// Calculate replicas
	replicas := int32(0)
	if m.Spec.Bird.Enable {
		replicas = 1
	}

	// Build security context
	privileged := perTypeOpts.Bird.Privileged
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
		WithName(fmt.Sprintf("bird-%s", m.Spec.NodeName)).
		WithComponentName("bird").
		WithReplicas(replicas).
		WithImage(m.Spec.Registry, image, tag).
		WithHostNetwork(true).
		WithHostIPC(perTypeOpts.Bird.HostIpc).
		WithAnnotations(AnnotationsForYanet(
			config.AdditionalOpts.Annotations,
			perTypeOpts.Bird.Annotations,
		)).
		WithNodeSelector(map[string]string{
			"kubernetes.io/hostname": m.Spec.NodeName,
		}).
		WithTolerations(TolerationsForYanet()).
		WithContainer("bird",
			[]string{"/usr/sbin/bird"},
			[]string{"-f"},
		).
		WithVolumeMounts([]v1.VolumeMount{
			{Name: "etc-bird", MountPath: "/etc/bird"},
			{Name: "run-yanet", MountPath: "/run/yanet"},
			{Name: "run-bird", MountPath: "/run/bird"},
		}).
		WithResources(GetResources(
			ctx,
			m.Spec.NodeName,
			perTypeOpts.Bird.Resources,
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
		WithVolumes(GetVolumes([]string{"/etc/bird", "/run/yanet", "/run/bird"})).
		Build()
}
