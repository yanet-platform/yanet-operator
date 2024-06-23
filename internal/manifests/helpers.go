package manifests

import (
	"fmt"
	"strings"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"golang.org/x/exp/maps"
	v1 "k8s.io/api/core/v1"
)

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

func duplicateElements(array []string) []string {
	mapUniq := make(map[string]bool)
	dups := []string{}
	for _, v := range array {
		if mapUniq[v] {
			dups = append(dups, v)
		} else {
			mapUniq[v] = true
		}

	}
	return dups
}

// GetInitContainers check if initContainers contains no errors (dup names and other possible issues)
func GetInitContainers(initCs []v1.Container) ([]v1.Container, error) {

	names := []string{}
	for _, c := range initCs {
		names = append(names, c.Name)
	}
	if dups := duplicateElements(names); len(dups) > 0 {
		return nil, fmt.Errorf("duplicate names found %s", names)
	}

	return initCs, nil
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
func AnnotationsForYanet(addition map[string]string) map[string]string {
	annotations := map[string]string{
		"checkpointer.ydb.tech/checkpoint":      "true",
		"checkpointer.ydb.tech/manual-recovery": "true",
	}
	maps.Copy(annotations, addition)
	return annotations
}
