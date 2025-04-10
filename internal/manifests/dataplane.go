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

// DeploymentForDataplane return dataplane Deployment object
func DeploymentForDataplane(
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
	initContainers := GetAdditionalInitContainers(
		config.AdditionalOpts.InitContainers, // all available initContainers in yanetConfig spec
		perTypeOpts.Dataplain.InitContainers, // initContainers enabled for specific type in global config
	)

	poststart := GetPostStartExec(config.AdditionalOpts.PostStart, perTypeOpts.Dataplain.PostStart)

	// Creating deployment based on previously created structures
	depName := fmt.Sprintf("dataplane-%s", m.Spec.NodeName)
	image := fmt.Sprintf("%s:%s", m.Spec.Dataplane.Image, m.Spec.Tag)
	if m.Spec.Controlplane.Tag != "" {
		image = fmt.Sprintf("%s:%s", m.Spec.Controlplane.Image, m.Spec.Controlplane.Tag)
	}
	if m.Spec.Registry != "" {
		image = fmt.Sprintf("%s/%s", m.Spec.Registry, image)
	}
	replicas := int32(0)
	if m.Spec.Dataplane.Enable {
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
				MatchLabels: LabelsForYanet(nil, m, "dataplane"),
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        depName,
					Annotations: AnnotationsForYanet(config.AdditionalOpts.Annotations, perTypeOpts.Dataplain.Annotations),
					Labels:      LabelsForYanet(nil, m, "dataplane"),
				},
				Spec: v1.PodSpec{
					HostNetwork:    true,
					HostIPC:        perTypeOpts.Dataplain.HostIpc,
					InitContainers: initContainers,
					Containers: []v1.Container{
						{
							Image:           image,
							ImagePullPolicy: v1.PullIfNotPresent,
							Name:            "dataplane",
							Command:         []string{"/usr/bin/yanet-dataplane"},
							Args: []string{
								"-c", "/etc/yanet/dataplane.conf",
							},
							Resources: GetResources(
								ctx,
								m.Spec.NodeName,
								perTypeOpts.Dataplain.Resources,
								nodes,
								true,
							),
							Lifecycle: &v1.Lifecycle{
								PostStart: &v1.LifecycleHandler{
									Exec: &v1.ExecAction{Command: poststart},
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "hugepage", MountPath: "/dev/hugepages"},
								{Name: "etc-yanet", MountPath: "/etc/yanet"},
								{Name: "run-yanet", MountPath: "/run/yanet"},
							},
							TerminationMessagePath:   "/dev/stdout",
							TerminationMessagePolicy: "File",
							SecurityContext: &v1.SecurityContext{
								Privileged: &perTypeOpts.Dataplain.Privileged,
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
					Volumes:     GetVolumes([]string{"/dev/hugepages", "/etc/yanet", "/run/yanet"}),
				},
			},
		},
	}
	return dep
}
