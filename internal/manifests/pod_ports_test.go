package manifests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateComposedPod_PortConcurrency(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	container := func(name string, native bool, protocol corev1.Protocol) corev1.Container {
		result := corev1.Container{Name: name, Image: "test", Ports: []corev1.ContainerPort{{Name: name, ContainerPort: 9000, Protocol: protocol}}}
		if native {
			result.RestartPolicy = &always
		}
		return result
	}
	app := container("app", false, corev1.ProtocolTCP)
	native := container("sidecar", true, corev1.ProtocolTCP)
	init := container("prepare", false, corev1.ProtocolTCP)
	for _, tc := range []struct {
		name                       string
		applications, initializers []corev1.Container
		conflict                   bool
	}{
		{"application containers", []corev1.Container{app, container("second", false, corev1.ProtocolTCP)}, nil, true},
		{"application and native", []corev1.Container{app}, []corev1.Container{native}, true},
		{"native and later init", []corev1.Container{{Name: "app", Image: "test"}}, []corev1.Container{native, init}, true},
		{"earlier init and native", []corev1.Container{{Name: "app", Image: "test"}}, []corev1.Container{init, native}, false},
		{"sequential init and application", []corev1.Container{app}, []corev1.Container{init, container("prepare-two", false, corev1.ProtocolTCP)}, false},
		{"different protocols", []corev1.Container{app}, []corev1.Container{container("sidecar", true, corev1.ProtocolUDP)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: tc.applications, InitContainers: tc.initializers}}}}
			err := ValidateComposedPod(deployment)
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "same TCP port 9000") {
					t.Fatalf("expected concurrent port rejection: %v", err)
				}
			} else if err != nil {
				t.Fatalf("nonconcurrent/protocol-separated ports rejected: %v", err)
			}
		})
	}
}
