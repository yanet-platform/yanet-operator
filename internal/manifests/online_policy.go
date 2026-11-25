package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const PolicyDeliveryComponent = "policy-delivery"
const policyVolumePrefix = "yanet-policy-"
const policyMount = "/var/lib/yanet2/gateway-policy"

// PolicyConfigMapName is stable for the lifetime of an installation/Node pair.
func PolicyConfigMapName(epoch, nodeUID string) string {
	digest := sha256.Sum256([]byte(epoch + "/" + nodeUID))
	return "yanet-policy-" + hex.EncodeToString(digest[:16])
}

func policyHostPath(ctx BuildContext) string {
	return path.Join(ctx.Certificates.HostPath, ctx.Namespace, ctx.YanetName, string(ctx.OwnerRef.UID), ctx.NodeUID, ".gateway-policy")
}

func policyArgs(ctx BuildContext) []string {
	return []string{
		"policy-sync", "--epoch=" + string(ctx.OwnerRef.UID), "--node-uid=" + ctx.NodeUID,
		"--namespace=" + ctx.Namespace, "--domain=" + ctx.Certificates.TrustDomain,
	}
}

func policySecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser: ptr[int64](0), RunAsGroup: ptr[int64](0), RunAsNonRoot: helpers.PtrFalse(),
		AllowPrivilegeEscalation: helpers.PtrFalse(), ReadOnlyRootFilesystem: helpers.PtrTrue(),
		Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func buildPolicyDelivery(ctx BuildContext, enabled bool) *appsv1.Deployment {
	if !ctx.Certificates.OnlineGatewayPolicy {
		return nil
	}
	hasServer := false
	for _, profile := range ctx.Certificates.Profiles {
		hasServer = hasServer || profile.Usage == "server"
	}
	if !hasServer {
		return nil
	}
	delivery := buildSingle(ctx, &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: PolicyDeliveryComponent, Enabled: enabled})
	directory := corev1.HostPathDirectoryOrCreate
	args := policyArgs(ctx)
	projection := &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: PolicyConfigMapName(string(ctx.OwnerRef.UID), ctx.NodeUID)},
		Items:                []corev1.KeyToPath{{Key: credentials.PolicyBundleKey, Path: credentials.PolicyBundleKey}},
	}
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: helpers.PtrFalse(),
		Tolerations:                  runtimeTolerations(),
		ImagePullSecrets:             ctx.PullSecrets,
		Volumes: []corev1.Volume{
			{Name: "policy", VolumeSource: corev1.VolumeSource{ConfigMap: projection}},
			{Name: "output", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: policyHostPath(ctx), Type: &directory,
			}}},
		},
		Containers: []corev1.Container{{
			Name: PolicyDeliveryComponent, Image: ctx.Certificates.ExporterImage, ImagePullPolicy: ctx.PullPolicy,
			Command: []string{"/manager"}, Args: args,
			SecurityContext: policySecurityContext(),
			VolumeMounts: []corev1.VolumeMount{
				{Name: "policy", MountPath: "/policy", ReadOnly: true},
				{Name: "output", MountPath: "/persist"},
			},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: append(append([]string{"/manager"}, args...), "--check-projection"),
			}}, PeriodSeconds: 5, TimeoutSeconds: 4},
		}},
	}
	pinPodToNode(&pod, ctx.NodeName)
	delivery.Spec.Template.Spec = pod
	delivery.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	return delivery
}

func configureOnlinePolicyMount(ctx BuildContext, pod *corev1.PodSpec, target *corev1.Container, profile api.CertificateProfile) error {
	name := policyVolumePrefix + profile.Name
	gateName := name + "-check"
	for _, volume := range pod.Volumes {
		if volume.Name == name {
			return fmt.Errorf("patch conflicts with managed policy volume %q", name)
		}
	}
	if overlapsPolicyMount(profile.MountPath) {
		return fmt.Errorf("certificate profile %q mount overlaps the managed policy path", profile.Name)
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			if container.Name == gateName {
				return fmt.Errorf("patch conflicts with managed policy gate %q", gateName)
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == name || (container.Name == target.Name && overlapsPolicyMount(mount.MountPath)) {
					return fmt.Errorf("patch conflicts with managed policy mount for %q", profile.Name)
				}
			}
		}
	}
	scope := api.CertificateURI(ctx.Certificates.TrustDomain, ctx.Namespace, string(ctx.OwnerRef.UID), ctx.NodeUID, profile.Name)
	env := []corev1.EnvVar{
		{Name: "YANET_GATEWAY_AUTH_MODE", Value: "enabled"},
		{Name: "YANET_GATEWAY_AUTH_MANAGED_POLICY_DIRECTORY", Value: policyMount},
		{Name: "YANET_GATEWAY_AUTH_MANAGED_POLICY_EXPECTED_SCOPE_EPOCH", Value: string(ctx.OwnerRef.UID)},
		{Name: "YANET_GATEWAY_AUTH_MANAGED_POLICY_EXPECTED_SCOPE_NODE_UID", Value: ctx.NodeUID},
		{Name: "YANET_GATEWAY_AUTH_MANAGED_POLICY_EXPECTED_SCOPE_SERVER_URI", Value: scope},
	}
	for _, existing := range target.Env {
		for _, managed := range env {
			if existing.Name == managed.Name && (existing.Value != managed.Value || existing.ValueFrom != nil) {
				return fmt.Errorf("environment override conflicts with managed policy: %s", existing.Name)
			}
		}
	}
	for _, managed := range env {
		found := false
		for _, existing := range target.Env {
			found = found || existing.Name == managed.Name
		}
		if !found {
			target.Env = append(target.Env, managed)
		}
	}
	directory := corev1.HostPathDirectoryOrCreate
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
		Path: path.Join(policyHostPath(ctx), profile.Name), Type: &directory,
	}}})
	target.VolumeMounts = append(target.VolumeMounts, corev1.VolumeMount{Name: name, MountPath: policyMount, ReadOnly: true})
	pod.InitContainers = append([]corev1.Container{{
		Name: gateName, Image: ctx.Certificates.ExporterImage, ImagePullPolicy: ctx.PullPolicy,
		Command: []string{"/manager"}, Args: append(policyArgs(ctx), "--check-role="+profile.Name),
		SecurityContext: policySecurityContext(),
		VolumeMounts:    []corev1.VolumeMount{{Name: name, MountPath: "/persist", ReadOnly: true}},
	}}, pod.InitContainers...)
	return nil
}

func overlapsPolicyMount(mount string) bool {
	mount = path.Clean(mount)
	return mount == "/" || mount == policyMount || strings.HasPrefix(mount, policyMount+"/") || strings.HasPrefix(policyMount, mount+"/")
}

// OnlinePolicyTargets identifies the runtime containers protected by the
// managed policy mount, not the init checks that also mount public policy.
func OnlinePolicyTargets(deployment *appsv1.Deployment) map[string]bool {
	targets := map[string]bool{}
	pod := &deployment.Spec.Template.Spec
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			for _, mount := range container.VolumeMounts {
				if strings.HasPrefix(mount.Name, policyVolumePrefix) && mount.MountPath == policyMount && mount.ReadOnly {
					targets[container.Name] = true
				}
			}
		}
	}
	return targets
}
