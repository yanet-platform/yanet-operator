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

func newControlInitContainers(m *yanetv1alpha1.Yanet) []v1.Container {
	image := fmt.Sprintf("%s:%s", m.Spec.Controlplane.Image, m.Spec.Tag)
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	initContainers := []v1.Container{
		{
			// TODO: make tools docker image
			Image:           image,
			Name:            "wait-dataplane",
			Command:         []string{"/bin/sh"},
			ImagePullPolicy: "IfNotPresent",
			Args: []string{
				"-c",
				`while [ $(/usr/bin/yanet-cli version | grep controlplane | wc -l) -ne 0 ]; do
					echo "controlplane already running...";
					sleep 1;
				done;
				until [ -e /run/yanet/dataplane.sock ]; do
					echo "dataplane.sock waiting...";
					sleep 1;
				done;
				/usr/bin/yanet-cli version;
				sleep 20;`,
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

// DeploymentForControlplane return controlplane Deployment object
func DeploymentForControlplane(
	ctx context.Context, m *yanetv1alpha1.Yanet,
	config yanetv1alpha1.YanetConfigSpec,
	nodes v1.NodeList) *appsv1.Deployment {
	log := log.FromContext(ctx)
	ok, perTypeOpts := helpers.GetTypeOpts(config.EnabledOpts, m.Spec.Type)
	if !ok {
		log.Info("typeOpts is not specified", "type", m.Spec.Type)
	}

	// Build init containers (wait-dataplane + additional)
	initContainers := newControlInitContainers(m)
	additionalInitContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers,
		perTypeOpts.Controlplane.InitContainers,
	)
	initContainers = append(initContainers, additionalInitContainers...)

	// Prepare poststart hook
	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Controlplane.PostStart)

	// Determine image and tag
	image := m.Spec.Controlplane.Image
	tag := m.Spec.Tag
	if m.Spec.Controlplane.Tag != "" {
		tag = m.Spec.Controlplane.Tag
	}

	// Calculate replicas
	replicas := int32(0)
	if m.Spec.Controlplane.Enable {
		replicas = 1
	}

	// Build security context
	privileged := perTypeOpts.Controlplane.Privileged
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
		WithName(fmt.Sprintf("controlplane-%s", m.Spec.NodeName)).
		WithComponentName("controlplane").
		WithReplicas(replicas).
		WithImage(m.Spec.Registry, image, tag).
		WithHostNetwork(true).
		WithHostIPC(perTypeOpts.Controlplane.HostIpc).
		WithAnnotations(AnnotationsForYanet(
			config.AdditionalOpts.Annotations,
			perTypeOpts.Controlplane.Annotations,
		)).
		WithNodeSelector(map[string]string{
			"kubernetes.io/hostname": m.Spec.NodeName,
		}).
		WithTolerations(TolerationsForYanet()).
		WithContainer("controlplane",
			[]string{"/usr/bin/yanet-controlplane"},
			[]string{"-c", "/etc/yanet/controlplane.conf"},
		).
		WithVolumeMounts([]v1.VolumeMount{
			{Name: "etc-yanet", MountPath: "/etc/yanet"},
			{Name: "run-yanet", MountPath: "/run/yanet"},
			{Name: "run-bird", MountPath: "/run/bird"},
			{Name: "spool-yanet-agent", MountPath: "/var/spool/yanet-agent"},
		}).
		WithResources(GetResources(
			ctx,
			m.Spec.NodeName,
			perTypeOpts.Controlplane.Resources,
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
		WithVolumes(GetVolumes([]string{"/etc/yanet", "/run/yanet", "/run/bird", "/var/spool/yanet-agent"})).
		Build()
}
