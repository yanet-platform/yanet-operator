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

// DeploymentForControlplane return dataplane Deployment object
func DeploymentForControlplane(ctx context.Context, m *yanetv1alpha1.Yanet, config yanetv1alpha1.YanetConfigSpec) *appsv1.Deployment {
	log := log.FromContext(ctx)
	replicas := int32(0)
	if m.Spec.Controlplane.Enable {
		replicas = 1
	}
	privileged := true
	poststart := []string{}
	// start with default config, mount slb config and run reload for l3balancer
	if m.Spec.Type == "l3balancer" {
		poststart = []string{
			"/bin/sh",
			"-c",
			`sleep 60;
			/bin/mountpoint -q /etc/yanet/controlplane.conf;
			/bin/mount -o ro,bind /etc/yanet/controlplane.slb.conf /etc/yanet/controlplane.conf;
			/usr/bin/yanet-cli reload`,
		}
	} else {
		poststart = []string{
			"/bin/sh",
			"-c",
			`echo "starting..."`,
		}
	}
	n := fmt.Sprintf("controlplane-%s", m.Spec.NodeName)
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
	configIcns, err := GetInitContainers(config.ControlPlainOpts.InitContainers)
	if err != nil {
		log.Error(err, "incorrect init containers in config spec for ControlPlain")
	} else {
		initContainers = append(initContainers, configIcns...)
	}
	yanetIcns, err := GetInitContainers(m.Spec.ControlPlainOpts.InitContainers)
	if err != nil {
		log.Error(err, "incorrect init containers in yanet spec for ControlPlain")
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
				MatchLabels: LabelsForYanet(nil, m, "controlplane"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        n,
					Annotations: AnnotationsForYanet(nil),
					Labels:      LabelsForYanet(nil, m, "controlplane"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "controlplane",
							Command:         []string{"/usr/bin/yanet-controlplane-" + m.Spec.Arch},
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
					Volumes: GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet", "/run/bird", "/var/spool/yanet-agent"}),
				},
			},
		},
	}
	return dep
}
