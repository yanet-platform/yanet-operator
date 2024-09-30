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
			SecurityContext: &v1.SecurityContext{
				Privileged: &privileged,
			},
		},
	}
	return initContainers
}

// DeploymentForControlplane return dataplane Deployment object
func DeploymentForControlplane(ctx context.Context, m *yanetv1alpha1.Yanet, config yanetv1alpha1.YanetConfigSpec) *appsv1.Deployment {
	log := log.FromContext(ctx)
	ok, perTypeOpts := helpers.GetTypeOpts(config.EnabledOpts, m.Spec.Type)
	if !ok {
		log.Info(fmt.Sprintf("typeOpts is not specified for %s", m.Spec.Type))
	}
	// Filling in all init containers
	initContainers := newControlInitContainers(m)
	additionalInitContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers,    // all available initContainers in yanetConfig spec
		perTypeOpts.Controlplane.InitContainers, // initContainers enabled for specific type in global config
	)
	initContainers = append(initContainers, additionalInitContainers...)

	// start with default config, mount slb config and run reload for l3balancer
	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Controlplane.PostStart)

	depName := fmt.Sprintf("controlplane-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Controlplane.Image, m.Spec.Tag)
	if m.Spec.Controlplane.Tag != "" {
		image = fmt.Sprintf("%s:%s", m.Spec.Controlplane.Image, m.Spec.Controlplane.Tag)
	}
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	// Creating deployment based on previously created structures
	replicas := int32(0)
	if m.Spec.Controlplane.Enable {
		replicas = 1
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      depName,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: LabelsForYanet(nil, m, "controlplane"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        depName,
					Annotations: AnnotationsForYanet(config.AdditionalOpts.Annotations, perTypeOpts.Controlplane.Annotations),
					Labels:      LabelsForYanet(nil, m, "controlplane"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					HostIPC:        perTypeOpts.Controlplane.HostIpc,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "controlplane",
							Command:         []string{"/usr/bin/yanet-controlplane"},
							Args: []string{
								"-c",
								"/etc/yanet/controlplane.conf",
							},
							Lifecycle: &v1.Lifecycle{
								PostStart: &v1.LifecycleHandler{
									Exec: &v1.ExecAction{Command: poststart},
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "dev-hugepages", MountPath: "/dev/hugepages"},
								{Name: "etc-yanet", MountPath: "/etc/yanet"},
								{Name: "run-yanet", MountPath: "/run/yanet"},
								{Name: "run-bird", MountPath: "/run/bird"},
								{Name: "spool-yanet-agent", MountPath: "/var/spool/yanet-agent"},
							},
							TerminationMessagePath:   "/dev/stdout",
							TerminationMessagePolicy: "File",
							SecurityContext: &v1.SecurityContext{
								Privileged: &privileged,
								Capabilities: &v1.Capabilities{
									Add: []v1.Capability{
										"NET_ADMIN",
										"NET_RAW",
										"IPC_LOCK",
										"SYS_ADMIN",
										"SYS_RAWIO",
										"SYS_CHROOT",
									},
								},
							},
						},
					},
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": m.Spec.NodeName,
					},
					Tolerations: TolerationsForYanet(),
					Volumes:     GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet", "/run/bird", "/var/spool/yanet-agent"}),
				},
			},
		},
	}
	return dep
}
