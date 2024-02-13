package manifests

import (
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentForAnnouncer returns a yanet announcer Deployment object
func DeploymentForAnnouncer(m *yanetv1alpha1.Yanet) *appsv1.Deployment {
	replicas := int32(0)
	if m.Spec.Announcer.Enable {
		replicas = 1
	}
	privileged := true
	n := fmt.Sprintf("announcer-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Announcer.Image, m.Spec.Tag)
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
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
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n,
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
					Name:        n,
					Annotations: AnnotationsForYanet(nil),
					Labels:      LabelsForYanet(nil, m, "announcer"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "announcer",
							Command:         []string{"/usr/bin/yanet-announcer"},
							Args:            []string{"--run"},
							VolumeMounts: []v1.VolumeMount{
								{Name: "dev-hugepages", MountPath: "/dev/hugepages"},
								{Name: "etc-yanet", MountPath: "/etc/yanet"},
								{Name: "run-yanet", MountPath: "/run/yanet"},
								{Name: "run-bird", MountPath: "/run/bird"},
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
					Volumes: GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet", "/run/bird"}),
				},
			},
		},
	}
	return dep
}
