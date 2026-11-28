package manifests

import (
	"fmt"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildDiagnostics mounts the existing installation/node-specific diagnostics
// role. Keeping the DaemonSet node-scoped avoids exposing the credential tree.
func BuildDiagnostics(ctx BuildContext, image string) (*appsv1.DaemonSet, error) {
	const role = "diagnostics"
	if ctx.Certificates == nil || ctx.NodeUID == "" || ctx.OwnerRef.UID == "" {
		return nil, fmt.Errorf("diagnostics CLI requires managed certificates and installation and Node UIDs")
	}
	for _, profile := range ctx.Certificates.Profiles {
		if profile.Name != role || profile.Usage != "client" || profile.MountPath == "" {
			continue
		}
		c := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: api.DiagnosticsComponent, Enabled: true}
		d := buildSingle(ctx, c)
		directory := corev1.HostPathDirectory
		pod := corev1.PodSpec{
			AutomountServiceAccountToken: helpers.PtrFalse(),
			ImagePullSecrets:             ctx.PullSecrets,
			Tolerations:                  runtimeTolerations(),
			SecurityContext:              &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "cli", Image: image, ImagePullPolicy: ctx.PullPolicy,
				Command: []string{"/usr/bin/sleep", "infinity"},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: ptr[int64](0), AllowPrivilegeEscalation: helpers.PtrFalse(),
					ReadOnlyRootFilesystem: helpers.PtrTrue(), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: pemVolumePrefix + role, MountPath: profile.MountPath, ReadOnly: true}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "-ec", `cd "$1/current"; test -r ca.crt && test -r tls.crt && test -r tls.key`, "diagnostics-probe", profile.MountPath},
				}}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
				},
			}},
			Volumes: []corev1.Volume{{Name: pemVolumePrefix + role, VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: credentialHostPath(ctx, profile), Type: &directory},
			}}},
		}
		pinPodToNode(&pod, ctx.NodeName)
		d.Spec.Template.Spec = pod
		return &appsv1.DaemonSet{
			TypeMeta:   metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "DaemonSet"},
			ObjectMeta: d.ObjectMeta,
			Spec: appsv1.DaemonSetSpec{
				Selector: d.Spec.Selector, Template: d.Spec.Template,
				UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
			},
		}, nil
	}
	return nil, fmt.Errorf("diagnostics CLI requires a client certificate profile named %q in this installation", role)
}
