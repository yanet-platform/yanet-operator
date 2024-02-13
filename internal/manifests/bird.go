package manifests

import (
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentForBird return bird Deployment object
func DeploymentForBird(m *yanetv1alpha1.Yanet) *appsv1.Deployment {
	replicas := int32(0)
	if m.Spec.Bird.Enable {
		replicas = 1
	}
	privileged := true
	n := fmt.Sprintf("bird-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Bird.Image, m.Spec.Bird.Tag)
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	initContainers := []v1.Container{
		{
			// TODO: make tools docker image
			Image:           fmt.Sprintf("%s/%s:%s", m.Spec.Registry, m.Spec.Controlplane.Image, m.Spec.Tag),
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
			SecurityContext: &v1.SecurityContext{
				Privileged: &privileged,
			},
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
				MatchLabels: LabelsForYanet(nil, m, "bird"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        n,
					Annotations: AnnotationsForYanet(nil),
					Labels:      LabelsForYanet(nil, m, "bird"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "bird",
							Command:         []string{"/usr/sbin/bird"},
							Args:            []string{"-f"},
							VolumeMounts: []v1.VolumeMount{
								{Name: "etc-bird", MountPath: "/etc/bird"},
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
					Volumes: GetVolumes([]string{"/etc/bird", "/run/yanet", "/run/bird"}),
				},
			},
		},
	}
	return dep
}
