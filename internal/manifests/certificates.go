package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/credentials"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CertificateGVK keeps the optional cert-manager dependency at its stable v1 API.
var CertificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}

const pemVolumePrefix = "yanet-credential-"

// CertificatePlan separates issuance from node-local delivery. Neither alone
// proves that a running application loaded the new leaf or reconnected its RPCs.
type CertificatePlan struct {
	Certificates []*unstructured.Unstructured
	Delivery     *appsv1.Deployment
}

// ConfigureCertificates applies the typed mount contract after user patches.
// A conflicting patch is rejected rather than granting another container a key.
func ConfigureCertificates(d *appsv1.Deployment, ctx BuildContext) error {
	if ctx.Certificates == nil {
		return nil
	}
	if ctx.NodeUID == "" || ctx.OwnerRef.UID == "" {
		return fmt.Errorf("certificates require installation and Node UIDs")
	}
	for _, p := range ctx.Certificates.Profiles {
		component := p.Component
		containerName := p.Container
		if d.Labels[LabelComponent] != component {
			if d.Spec.Template.Labels[OperatorMembershipLabel(p.Component)] != p.Component {
				continue
			}
			containerName = scopedOperatorName(p.Component, p.Container)
		}
		pod := &d.Spec.Template.Spec
		for _, container := range pod.InitContainers {
			if container.Name == "yanet-credential-"+p.Name+"-check" {
				return fmt.Errorf("patch conflicts with certificate profile %q startup gate", p.Name)
			}
		}
		var target *corev1.Container
		for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
			for i := range containers {
				if containers[i].Name == containerName {
					target = &containers[i]
				}
			}
		}
		if target == nil {
			return fmt.Errorf("certificate profile %q: target container %q is absent from %s", p.Name, containerName, d.Name)
		}
		volumeName := pemVolumePrefix + p.Name
		for _, volume := range pod.Volumes {
			if volume.Name == volumeName {
				return fmt.Errorf("patch conflicts with managed certificate volume %q", volumeName)
			}
		}
		for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
			for _, container := range containers {
				for _, mount := range container.VolumeMounts {
					if mount.Name == volumeName || mount.MountPath == p.MountPath {
						return fmt.Errorf("patch conflicts with certificate profile %q mount", p.Name)
					}
				}
			}
		}
		directory := corev1.HostPathDirectoryOrCreate
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: credentialHostPath(ctx, p), Type: &directory}}})
		target.VolumeMounts = append(target.VolumeMounts, corev1.VolumeMount{Name: volumeName, MountPath: p.MountPath, ReadOnly: true})
	}
	return nil
}

func credentialHostPath(ctx BuildContext, p api.CertificateProfile) string {
	return path.Join(ctx.Certificates.HostPath, ctx.Namespace, ctx.YanetName, string(ctx.OwnerRef.UID), ctx.NodeUID, p.Name)
}

func certificateName(ctx BuildContext, profile string) string {
	digest := sha256.Sum256([]byte(string(ctx.OwnerRef.UID) + "/" + ctx.NodeUID + "/" + profile))
	return "yanet-" + hex.EncodeToString(digest[:16]) + "-" + profile
}

// BuildCertificatePlan issues only profiles actually mounted by effective
// workloads. NUMA fan-out shares one role identity; disabled/unselected native
// sidecars do not acquire credentials. Delivery lives in a separate Pod so static
// recovery checkpoints of the consumers contain no Secret or ConfigMap volumes.
func BuildCertificatePlan(ctx BuildContext, workloads []*appsv1.Deployment, enabled bool) CertificatePlan {
	var plan CertificatePlan
	if ctx.Certificates == nil {
		return plan
	}
	selected := map[string]bool{}
	for _, d := range workloads {
		for _, v := range d.Spec.Template.Spec.Volumes {
			selected[v.Name] = true
		}
	}
	var accountArgs []string
	for _, profile := range ctx.Certificates.Profiles {
		if !selected[pemVolumePrefix+profile.Name] || len(profile.GatewayPermissions) == 0 {
			continue
		}
		account := credentials.GatewayAccount{
			Username:    api.CertificateURI(ctx.Certificates.TrustDomain, ctx.Namespace, string(ctx.OwnerRef.UID), ctx.NodeUID, profile.Name),
			Permissions: profile.GatewayPermissions,
		}
		encoded, _ := json.Marshal(account) //nolint:errcheck // Strings and string slices cannot fail JSON encoding.
		accountArgs = append(accountArgs, "--gateway-account="+string(encoded))
	}
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: helpers.PtrFalse(),
		NodeSelector:                 nodeSelector(ctx), Tolerations: runtimeTolerations(), ImagePullSecrets: ctx.PullSecrets,
		SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr[int64](0), RunAsGroup: ptr[int64](0), RunAsNonRoot: helpers.PtrFalse(), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Volumes:         []corev1.Volume{{Name: "trust", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: ctx.Certificates.TrustBundle.LocalObjectReference, Items: []corev1.KeyToPath{{Key: ctx.Certificates.TrustBundle.Key, Path: "ca.crt"}}}}}},
	}
	for _, p := range ctx.Certificates.Profiles {
		if !selected[pemVolumePrefix+p.Name] {
			continue
		}
		name := certificateName(ctx, p.Name)
		uri := api.CertificateURI(ctx.Certificates.TrustDomain, ctx.Namespace, string(ctx.OwnerRef.UID), ctx.NodeUID, p.Name)
		var dns []any
		args := []string{"credential-sync", "--uri=" + uri, "--usage=" + p.Usage}
		for _, value := range p.DNSNames {
			value = strings.NewReplacer("{node}", ctx.NodeName, "{namespace}", ctx.Namespace, "{boxType}", ctx.BoxType).Replace(value)
			dns = append(dns, value)
			args = append(args, "--dns="+value)
		}
		if p.Usage == "server" {
			args = append(args, "--gateway-policy")
			args = append(args, accountArgs...)
			configureGatewayAccountGate(ctx, workloads, p, args)
		}
		group := ctx.Certificates.IssuerRef.Group
		if group == "" {
			group = "cert-manager.io"
		}
		spec := map[string]any{
			"secretName": name, "issuerRef": map[string]any{"name": ctx.Certificates.IssuerRef.Name, "kind": ctx.Certificates.IssuerRef.Kind, "group": group},
			"uris": []any{uri}, "usages": []any{"digital signature", p.Usage + " auth"},
			"encodeUsagesInRequest": true,
			"duration":              ctx.Certificates.Duration, "renewBefore": ctx.Certificates.RenewBefore,
			"isCA": false, "revisionHistoryLimit": int64(1),
			"privateKey": map[string]any{"algorithm": "ECDSA", "size": int64(256), "encoding": "PKCS8", "rotationPolicy": "Always"},
		}
		if len(dns) > 0 {
			spec["dnsNames"] = dns
		}
		certificate := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		certificate.SetGroupVersionKind(CertificateGVK)
		certificate.SetName(name)
		certificate.SetNamespace(ctx.Namespace)
		certificate.SetLabels(map[string]string{LabelYanet: ctx.YanetName, LabelNode: ctx.NodeName})
		certificate.SetOwnerReferences([]metav1.OwnerReference{ctx.OwnerRef})
		plan.Certificates = append(plan.Certificates, certificate)
		secretVolume, outputVolume := "source-"+p.Name, "output-"+p.Name
		directory := corev1.HostPathDirectoryOrCreate
		pod.Volumes = append(pod.Volumes,
			corev1.Volume{Name: secretVolume, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: ptr[int32](0o400), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}}}}},
			corev1.Volume{Name: outputVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: credentialHostPath(ctx, p), Type: &directory}}},
		)
		pod.Containers = append(pod.Containers, corev1.Container{
			Name: p.Name, Image: ctx.Certificates.ExporterImage, ImagePullPolicy: ctx.PullPolicy,
			Command: []string{"/manager"}, Args: args,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: helpers.PtrFalse(), ReadOnlyRootFilesystem: helpers.PtrTrue(), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: secretVolume, MountPath: "/source", ReadOnly: true}, {Name: "trust", MountPath: "/trust", ReadOnly: true}, {Name: outputVolume, MountPath: "/persist"}},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: append(append([]string{"/manager"}, args...), "--check")}}, PeriodSeconds: 10, TimeoutSeconds: 5},
		})
	}
	if len(plan.Certificates) == 0 {
		return plan
	}
	component := &helpers.ResolvedComponent{Kind: helpers.KindOperator, Name: "credential-delivery", Enabled: enabled}
	d := buildSingle(ctx, component)
	d.Spec.Template.Spec = pod
	d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	// The writer is deliberately not a runtime recovery workload. It requires
	// the API for renewal; consumers continue to use the durable last generation.
	plan.Delivery = d
	return plan
}

// A gateway reads identities once at startup. Gate each new Pod on the exact
// delivered policy, so it cannot start with an older still-valid PEM generation
// while the independent delivery Deployment is catching up.
func configureGatewayAccountGate(ctx BuildContext, workloads []*appsv1.Deployment, profile api.CertificateProfile, args []string) {
	volumeName := pemVolumePrefix + profile.Name
	digest := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	for _, workload := range workloads {
		for _, volume := range workload.Spec.Template.Spec.Volumes {
			if volume.Name != volumeName {
				continue
			}
			if workload.Spec.Template.Annotations == nil {
				workload.Spec.Template.Annotations = map[string]string{}
			}
			workload.Spec.Template.Annotations["yanet.yanet-platform.io/accounts-"+profile.Name] = hex.EncodeToString(digest[:])
			checkArgs := append([]string{}, args...)
			checkArgs = append(checkArgs, "--destination=/persist", "--trust=/persist/current/ca.crt", "--check")
			gate := corev1.Container{
				Name: "yanet-credential-" + profile.Name + "-check", Image: ctx.Certificates.ExporterImage, ImagePullPolicy: ctx.PullPolicy,
				Command: []string{"/manager"}, Args: checkArgs,
				SecurityContext: &corev1.SecurityContext{RunAsUser: ptr[int64](0), RunAsGroup: ptr[int64](0), RunAsNonRoot: helpers.PtrFalse(), AllowPrivilegeEscalation: helpers.PtrFalse(), ReadOnlyRootFilesystem: helpers.PtrTrue(), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: volumeName, MountPath: "/persist", ReadOnly: true}},
			}
			containers := &workload.Spec.Template.Spec.InitContainers
			index := -1
			for i := range *containers {
				if (*containers)[i].Name == gate.Name {
					index = i
					break
				}
			}
			if index >= 0 {
				(*containers)[index] = gate
			} else {
				*containers = append([]corev1.Container{gate}, (*containers)...)
			}
			break
		}
	}
}

func ptr[T any](value T) *T { return &value }

// RequiresCertificateDelivery reports whether a workload consumes managed PEM.
func RequiresCertificateDelivery(d *appsv1.Deployment) bool {
	for _, volume := range d.Spec.Template.Spec.Volumes {
		if strings.HasPrefix(volume.Name, pemVolumePrefix) {
			return true
		}
	}
	return false
}
