package manifests

import (
	"context"
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DeploymentForDataplane return dataplane Deployment object
func DeploymentForDataplane(ctx context.Context, m *yanetv1alpha1.Yanet, config yanetv1alpha1.YanetConfigSpec) *appsv1.Deployment {
	replicas := int32(0)
	log := log.FromContext(ctx)
	if m.Spec.Dataplane.Enable {
		replicas = 1
	}
	privileged := true
	n := fmt.Sprintf("dataplane-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Dataplane.Image, m.Spec.Tag)
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	initContainers := []v1.Container{
		{
			Image:           "busybox",
			Name:            "init-hugepages",
			ImagePullPolicy: "IfNotPresent",
			Command:         []string{"/bin/sh"},
			Args: []string{
				"-c",
				`cat /etc/yanet/hugepages |
				tee /sys/devices/system/node/node*/hugepages/hugepages-1048576kB/nr_hugepages`,
			},
			VolumeMounts: []v1.VolumeMount{
				{Name: "dev-hugepages", MountPath: "/dev/hugepages"},
				{Name: "etc-yanet", MountPath: "/etc/yanet"},
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
	}
	configIcns, err := GetInitContainers(config.DataPlainOpts.InitContainers)
	if err != nil {
		log.Error(err, "incorrect init containers in config spec for DataPlain")
	} else {
		initContainers = append(initContainers, configIcns...)
	}
	yanetIcns, err := GetInitContainers(m.Spec.DataPlainOpts.InitContainers)
	if err != nil {
		log.Error(err, "incorrect init containers in yanet spec for DataPlain")
	} else {
		// overriding initContainers with specified in yanet spec
		for _, j := range yanetIcns {
			flag := false
			for k, v := range initContainers {
				if j.Name == v.Name {
					initContainers[k] = j
					flag = true
					break
				}
			}
			if !flag {
				initContainers = append(initContainers, j)
			}
		}
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: LabelsForYanet(nil, m, "dataplane"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        n,
					Annotations: AnnotationsForYanet(nil),
					Labels:      LabelsForYanet(nil, m, "dataplane"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "dataplane",
							Command:         []string{"/usr/bin/yanet-wrapper"},
							Args: []string{
								"-a", m.Spec.Arch,
								"-t", m.Spec.Type,
								"-c", "/etc/yanet/dataplane.conf",
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "dev-hugepages", MountPath: "/dev/hugepages"},
								{Name: "etc-yanet", MountPath: "/etc/yanet"},
								{Name: "run-yanet", MountPath: "/run/yanet"},
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
					Volumes: GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet"}),
				},
			},
		},
	}
	return dep
}
