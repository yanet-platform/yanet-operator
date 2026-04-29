package manifests

import (
	"fmt"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentBuilder provides a fluent interface for building Kubernetes Deployments
// for yanet components (dataplane, controlplane, announcer, bird).
// It encapsulates common deployment configuration logic and reduces code duplication.
type DeploymentBuilder struct {
	// Core fields
	name          string
	namespace     string
	componentName string
	replicas      int32

	// Yanet context
	yanet *yanetv1alpha1.Yanet

	// Image configuration
	registry string
	image    string
	tag      string

	// Pod configuration
	hostNetwork  bool
	hostIPC      bool
	nodeSelector map[string]string
	tolerations  []v1.Toleration
	annotations  map[string]string
	labels       map[string]string

	// Container configuration
	containerName string
	command       []string
	args          []string
	env           []v1.EnvVar
	volumeMounts  []v1.VolumeMount
	resources     v1.ResourceRequirements
	securityCtx   *v1.SecurityContext
	lifecycle     *v1.Lifecycle

	// Init containers
	initContainers []v1.Container

	// Volumes
	volumes []v1.Volume
}

// NewDeploymentBuilder creates a new builder instance with sensible defaults
func NewDeploymentBuilder() *DeploymentBuilder {
	return &DeploymentBuilder{
		hostNetwork: true, // Default for yanet components
		replicas:    1,    // Default replica count
	}
}

// WithName sets deployment name
func (b *DeploymentBuilder) WithName(name string) *DeploymentBuilder {
	b.name = name
	return b
}

// WithNamespace sets namespace
func (b *DeploymentBuilder) WithNamespace(namespace string) *DeploymentBuilder {
	b.namespace = namespace
	return b
}

// WithComponentName sets component name (dataplane, controlplane, announcer, bird)
func (b *DeploymentBuilder) WithComponentName(name string) *DeploymentBuilder {
	b.componentName = name
	return b
}

// WithYanet sets Yanet CR reference and automatically sets namespace
func (b *DeploymentBuilder) WithYanet(yanet *yanetv1alpha1.Yanet) *DeploymentBuilder {
	b.yanet = yanet
	b.namespace = yanet.Namespace
	return b
}

// WithReplicas sets replica count
func (b *DeploymentBuilder) WithReplicas(replicas int32) *DeploymentBuilder {
	b.replicas = replicas
	return b
}

// WithImage sets image configuration (registry, image name, tag)
func (b *DeploymentBuilder) WithImage(registry, image, tag string) *DeploymentBuilder {
	b.registry = registry
	b.image = image
	b.tag = tag
	return b
}

// WithHostNetwork enables/disables host network
func (b *DeploymentBuilder) WithHostNetwork(enabled bool) *DeploymentBuilder {
	b.hostNetwork = enabled
	return b
}

// WithHostIPC enables/disables host IPC
func (b *DeploymentBuilder) WithHostIPC(enabled bool) *DeploymentBuilder {
	b.hostIPC = enabled
	return b
}

// WithNodeSelector sets node selector
func (b *DeploymentBuilder) WithNodeSelector(selector map[string]string) *DeploymentBuilder {
	b.nodeSelector = selector
	return b
}

// WithTolerations sets tolerations
func (b *DeploymentBuilder) WithTolerations(tolerations []v1.Toleration) *DeploymentBuilder {
	b.tolerations = tolerations
	return b
}

// WithAnnotations sets pod annotations
func (b *DeploymentBuilder) WithAnnotations(annotations map[string]string) *DeploymentBuilder {
	b.annotations = annotations
	return b
}

// WithLabels sets pod labels
func (b *DeploymentBuilder) WithLabels(labels map[string]string) *DeploymentBuilder {
	b.labels = labels
	return b
}

// WithContainer sets main container configuration (name, command, args)
func (b *DeploymentBuilder) WithContainer(name string, command, args []string) *DeploymentBuilder {
	b.containerName = name
	b.command = command
	b.args = args
	return b
}

// WithEnv sets environment variables
func (b *DeploymentBuilder) WithEnv(env []v1.EnvVar) *DeploymentBuilder {
	b.env = env
	return b
}

// WithVolumeMounts sets volume mounts
func (b *DeploymentBuilder) WithVolumeMounts(mounts []v1.VolumeMount) *DeploymentBuilder {
	b.volumeMounts = mounts
	return b
}

// WithResources sets resource requirements
func (b *DeploymentBuilder) WithResources(resources v1.ResourceRequirements) *DeploymentBuilder {
	b.resources = resources
	return b
}

// WithSecurityContext sets security context
func (b *DeploymentBuilder) WithSecurityContext(ctx *v1.SecurityContext) *DeploymentBuilder {
	b.securityCtx = ctx
	return b
}

// WithLifecycle sets lifecycle hooks
func (b *DeploymentBuilder) WithLifecycle(lifecycle *v1.Lifecycle) *DeploymentBuilder {
	b.lifecycle = lifecycle
	return b
}

// WithInitContainers sets init containers
func (b *DeploymentBuilder) WithInitContainers(containers []v1.Container) *DeploymentBuilder {
	b.initContainers = containers
	return b
}

// AddInitContainer adds a single init container
func (b *DeploymentBuilder) AddInitContainer(container v1.Container) *DeploymentBuilder {
	b.initContainers = append(b.initContainers, container)
	return b
}

// WithVolumes sets volumes
func (b *DeploymentBuilder) WithVolumes(volumes []v1.Volume) *DeploymentBuilder {
	b.volumes = volumes
	return b
}

// AddVolume adds a single volume
func (b *DeploymentBuilder) AddVolume(volume v1.Volume) *DeploymentBuilder {
	b.volumes = append(b.volumes, volume)
	return b
}

// Build constructs the final Deployment object
func (b *DeploymentBuilder) Build() *appsv1.Deployment {
	// Build full image name
	fullImage := b.buildImageName()

	// Build labels
	labels := b.buildLabels()

	// Build deployment
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &b.replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        b.name,
					Annotations: b.annotations,
					Labels:      labels,
				},
				Spec: v1.PodSpec{
					HostNetwork:    b.hostNetwork,
					HostIPC:        b.hostIPC,
					InitContainers: b.initContainers,
					Containers: []v1.Container{
						{
							Name:                     b.containerName,
							Image:                    fullImage,
							ImagePullPolicy:          v1.PullIfNotPresent,
							Command:                  b.command,
							Args:                     b.args,
							Env:                      b.env,
							Resources:                b.resources,
							VolumeMounts:             b.volumeMounts,
							SecurityContext:          b.securityCtx,
							Lifecycle:                b.lifecycle,
							TerminationMessagePath:   "/dev/stdout",
							TerminationMessagePolicy: "File",
						},
					},
					Volumes:      b.volumes,
					NodeSelector: b.nodeSelector,
					Tolerations:  b.tolerations,
				},
			},
		},
	}

	return dep
}

// buildImageName constructs full image name with registry and tag
func (b *DeploymentBuilder) buildImageName() string {
	image := fmt.Sprintf("%s:%s", b.image, b.tag)
	if b.registry != "" {
		image = fmt.Sprintf("%s/%s", b.registry, image)
	}
	return image
}

// buildLabels constructs labels for deployment
// Uses provided labels or generates default labels from Yanet CR
func (b *DeploymentBuilder) buildLabels() map[string]string {
	if b.labels != nil {
		return b.labels
	}
	// Default labels if not provided
	if b.yanet != nil {
		return LabelsForYanet(nil, b.yanet, b.componentName)
	}
	return map[string]string{}
}
