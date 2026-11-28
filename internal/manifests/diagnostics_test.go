package manifests

import (
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDiagnosticsMountsOnlyTheLocalCredential(t *testing.T) {
	build := certificateBuildContext()
	build.Certificates.Profiles = append(build.Certificates.Profiles, api.CertificateProfile{
		Name: "diagnostics", Usage: "client", MountPath: "/example/credentials",
	})
	const image = "registry.example.test/runtime/cli:1.2.3"
	ds, err := BuildDiagnostics(build, image)
	if err != nil {
		t.Fatal(err)
	}
	pod := ds.Spec.Template.Spec
	if len(pod.Containers) != 1 || pod.Containers[0].Image != image || len(pod.Volumes) != 1 ||
		pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.HostNetwork || pod.HostPID {
		t.Fatalf("unexpected diagnostics Pod: %+v", pod)
	}
	owner := metav1.GetControllerOf(ds)
	if owner == nil || owner.UID != build.OwnerRef.UID {
		t.Fatalf("wrong DaemonSet owner: %+v", owner)
	}
	if v := pod.Volumes[0].HostPath; v == nil || v.Path != build.Certificates.HostPath+"/default/edge/installation-uid/node-uid/diagnostics" ||
		v.Type == nil || *v.Type != corev1.HostPathDirectory || !pod.Containers[0].VolumeMounts[0].ReadOnly {
		t.Fatalf("diagnostics credential mount is not isolated: %+v", pod.Volumes)
	}
	fields := pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields
	if len(fields) != 1 || fields[0].Key != "metadata.name" || len(fields[0].Values) != 1 || fields[0].Values[0] != "worker.example.test" {
		t.Fatalf("diagnostics is not pinned to the credential's node: %+v", fields)
	}
}
