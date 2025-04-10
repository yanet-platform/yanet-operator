package manifests

import (
	"context"
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		log.Info(fmt.Sprintf("typeOpts is not specified for %s", m.Spec.Type))
	}
	// Filling in all init containers
	initContainers := newAnnouncerInitContainers()
	additionalInitContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers, // all available initContainers in yanetConfig spec
		perTypeOpts.Announcer.InitContainers, // initContainers enabled for specific type in global config
	)
	initContainers = append(initContainers, additionalInitContainers...)

	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Announcer.PostStart)

	// Creating deployment based on previously created structures
	replicas := int32(0)
	if m.Spec.Announcer.Enable {
		replicas = 1
	}
	depName := fmt.Sprintf("announcer-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Announcer.Image, m.Spec.Tag)
	if m.Spec.Announcer.Tag != "" {
		image = fmt.Sprintf("%s:%s", m.Spec.Announcer.Image, m.Spec.Announcer.Tag)
	}
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      depName,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: LabelsForYanet(nil, m, "announcer"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        depName,
					Annotations: AnnotationsForYanet(config.AdditionalOpts.Annotations, perTypeOpts.Announcer.Annotations),
					Labels:      LabelsForYanet(nil, m, "announcer"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					HostIPC:        perTypeOpts.Announcer.HostIpc,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "announcer",
							Command:         []string{"/usr/bin/yanet-announcer"},
							Args:            []string{"--run"},
							Resources: GetResources(
								ctx,
								m.Spec.NodeName,
								perTypeOpts.Announcer.Resources,
								nodes,
								false,
							),
							Lifecycle: &v1.Lifecycle{
								PostStart: &v1.LifecycleHandler{
									Exec: &v1.ExecAction{Command: poststart},
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "etc-yanet", MountPath: "/etc/yanet"},
								{Name: "run-yanet", MountPath: "/run/yanet"},
								{Name: "run-bird", MountPath: "/run/bird"},
							},
							TerminationMessagePath:   "/dev/stdout",
							TerminationMessagePolicy: "File",
							SecurityContext: &v1.SecurityContext{
								Privileged: &perTypeOpts.Announcer.Privileged,
								Capabilities: &v1.Capabilities{
									Add: []v1.Capability{
										"NET_ADMIN",
										"NET_BIND_SERVICE",
										"IPC_LOCK",
										"SYS_MODULE",
										"SYS_NICE",
									},
								},
							},
						},
					},
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": m.Spec.NodeName,
					},
					Tolerations: TolerationsForYanet(),
					Volumes:     GetVolumes([]string{"/etc/yanet", "/run/yanet", "/run/bird"}),
				},
			},
		},
	}
	return dep
}
