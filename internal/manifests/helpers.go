package manifests

import (
	"strings"

	"golang.org/x/exp/slices"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"golang.org/x/exp/maps"
	v1 "k8s.io/api/core/v1"
	kuberv1 "k8s.io/kubernetes/pkg/apis/core/v1"
)

var privileged = true

// GetVolumes generate Volumes for deployment
// TODO: add more type
func GetVolumes(HostpathOrCreate []string) []v1.Volume {
	Volumes := []v1.Volume{}
	hostPathDirectoryOrCreate := v1.HostPathDirectoryOrCreate
	for _, path := range HostpathOrCreate {
		name := strings.Split(path, "/")
		Volumes = append(Volumes, v1.Volume{
			Name: name[len(name)-2] + "-" + name[len(name)-1],
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: path,
					Type: &hostPathDirectoryOrCreate,
				},
			},
		})
	}
	return Volumes
}

func normalizeContainer(c v1.Container) v1.Container {
	// TODO: k8s.io/kubernetes/pkg/apis/core/v1@v1.26.1 is used for compatibility reasons
	// Upgrade with go itself after https://github.com/kubernetes-sigs/controller-tools/issues/880 is resolved
	kuberv1.SetDefaults_Container(&c)
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
