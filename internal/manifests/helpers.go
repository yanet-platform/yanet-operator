package manifests

import (
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

// GetInitContainer generate init container spec from Yanet Container type.
func GetInitContainer(m *yanetv1alpha1.Container) v1.Container {
	c := v1.Container{
		Image:                    m.Image,
		Name:                     m.Name,
		Command:                  m.Command,
		Args:                     m.Args,
		TerminationMessagePath:   "/dev/stdout",
		TerminationMessagePolicy: "File",
	}
	if m.Privileged {
		privileged := true
		c.SecurityContext = &v1.SecurityContext{
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
		}
	}
	for _, v := range m.VolumeMounts {
		name := strings.Split(v, "/")
		c.VolumeMounts = append(
			c.VolumeMounts,
			v1.VolumeMount{Name: name[len(name)-1], MountPath: v},
		)
	}
	return c
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
