package manifests

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// GetVolumes generate Volumes for deployment
// TODO: add more type
func GetVolumes(hostpathOrCreate []string) []v1.Volume {
	Volumes := []v1.Volume{}
	hostPathDirectoryOrCreate := v1.HostPathDirectoryOrCreate
	for _, path := range hostpathOrCreate {
		if strings.Contains(path, "hugepages") {
			Volumes = append(Volumes, v1.Volume{
				Name: "hugepage",
				VolumeSource: v1.VolumeSource{
					EmptyDir: &v1.EmptyDirVolumeSource{
						Medium: v1.StorageMediumHugePages,
					},
				},
			})
		} else {
			segments := strings.Split(path, "/")
			var volumeName string
			if len(segments) >= 2 {
				volumeName = segments[len(segments)-2] + "-" + segments[len(segments)-1]
			} else {
				volumeName = segments[0]
			}
			Volumes = append(Volumes, v1.Volume{
				Name: volumeName,
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: path,
						Type: &hostPathDirectoryOrCreate,
					},
				},
			})
		}
	}
	return Volumes
}

func normalizeContainer(c v1.Container) v1.Container {
	// Inline defaults that Kubernetes API server would set on a Container,
	// so that our expected spec matches what the API returns for diff comparison.
	if c.TerminationMessagePath == "" {
		c.TerminationMessagePath = "/dev/termination-log"
	}
	if c.TerminationMessagePolicy == "" {
		c.TerminationMessagePolicy = v1.TerminationMessageReadFile
	}
	if c.ImagePullPolicy == "" {
		// Mimic Kubernetes defaulting: if tag is ":latest" or absent → Always, else IfNotPresent
		c.ImagePullPolicy = v1.PullIfNotPresent
	}
	for i := range c.Ports {
		if c.Ports[i].Protocol == "" {
			c.Ports[i].Protocol = v1.ProtocolTCP
		}
	}
	return c
}

// GetAdditionalInitContainers filters out initCointainers required for specific setup based on global configuration
func GetAdditionalInitContainers(initCs []v1.Container, names []string) []v1.Container {
	var resCs []v1.Container
	for _, ic := range initCs {
		if slices.Contains(names, ic.Name) {
			resCs = append(resCs, normalizeContainer(ic))
		}
	}
	return resCs
}

func GetPostStartExec(execs []yanetv1alpha1.NamedLifecycleHandler, names yanetv1alpha1.LifecycleHandler) []string {
	result := []string{
		"/bin/bash",
		"-c",
	}
	command := "echo starting..."
	for _, exec := range execs {
		if slices.Contains(names.Exec, exec.Name) {
			command += ";" + exec.Exec
		}
	}
	result = append(result, command)
	return result
}

// LabelsForYanet returns the labels for selecting the resources
func LabelsForYanet(addition map[string]string, m *yanetv1alpha1.Yanet, name string) map[string]string {
	labels := map[string]string{
		"app":                          name,
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/created-by": "yanet-operator",
		"topology-location-host":       m.Spec.NodeName,
	}
	maps.Copy(labels, addition)
	return labels
}

// AnnotationsForYanet returns annotations for pods
func AnnotationsForYanet(annotations []yanetv1alpha1.NamedAnnotations, names []string) map[string]string {
	resAnnotations := map[string]string{}
	for _, ann := range annotations {
		if slices.Contains(names, ann.Name) {
			maps.Copy(resAnnotations, ann.Annotations)
		}
	}
	if len(resAnnotations) == 0 {
		return nil
	}
	return resAnnotations
}

// TolerationsForYanet returns tolerations for pod template, do not restart pod on node problem
func TolerationsForYanet() []v1.Toleration {
	toleration := []v1.Toleration{
		{Key: "CriticalAddonsOnly", Effect: v1.TaintEffectNoSchedule},
		{Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule},
		{Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoExecute},
	}
	return toleration
}

// GetResources returns resources map with default values
func GetResources(
	ctx context.Context,
	nodename string,
	resources v1.ResourceRequirements,
	nodes v1.NodeList,
	enableHugepages bool) v1.ResourceRequirements {

	log := log.FromContext(ctx)

	res := resources.DeepCopy()

	if !enableHugepages {
		return resources
	}

	// Set defaults for dataplane
	// spec.template.spec.containers[0].resources: Forbidden: HugePages require cpu or memory
	hugepages := resource.MustParse("8Gi")
	memory := resource.MustParse("8Gi")
	for _, node := range nodes.Items {
		if node.Name == nodename {
			// Append hugepage limits to dataplane, use all of available hugepages on node
			if huge, ok := node.Status.Capacity["hugepages-1Gi"]; ok {
				hugepages = huge
			}
			log.Info(fmt.Sprintf(
				"Get %s hugepages capacity from node %s and use it for limits",
				hugepages.String(),
				node.Name),
			)
			break
		}
	}

	if res.Limits == nil {
		res.Limits = v1.ResourceList{}
	}
	if _, ok := res.Limits["memory"]; !ok {
		res.Limits["memory"] = memory
	}
	if _, ok := res.Limits["hugepages-1Gi"]; !ok {
		res.Limits["hugepages-1Gi"] = hugepages
	}

	return *res
}
