package v1alpha1

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	validation "k8s.io/apimachinery/pkg/util/validation"
)

// CertificateManagementSpec delegates signing/renewal to cert-manager. Only
// public trust is read by the exporter; neither it nor the manager needs a CA key.
type CertificateManagementSpec struct {
	IssuerRef CertificateIssuerRef `json:"issuerRef"`
	// TrustDomain identifies role-specific SPIFFE URI SANs, with no CommonName.
	TrustDomain string `json:"trustDomain"`
	// TrustBundle is an independently managed public CA bundle in each installation
	// namespace. The leaf Secret's ca.crt is deliberately not a trust source.
	TrustBundle corev1.ConfigMapKeySelector `json:"trustBundle"`
	// HostPath must be a persistent directory, not /run, /tmp or kubelet cache.
	HostPath string `json:"hostPath"`
	// ExporterImage is an exact operator image containing /manager credential-sync.
	ExporterImage string `json:"exporterImage"`
	// Duration and RenewBefore are explicit Go durations accepted by cert-manager.
	Duration    string `json:"duration"`
	RenewBefore string `json:"renewBefore"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Profiles []CertificateProfile `json:"profiles"`
}

// CertificateIssuerRef references an externally provisioned, policy-constrained issuer.
type CertificateIssuerRef struct {
	Name string `json:"name"`
	// Kind is Issuer/ClusterIssuer for cert-manager.io, or an external issuer kind.
	Kind string `json:"kind"`
	// Group defaults to cert-manager.io.
	// +optional
	Group string `json:"group,omitempty"`
}

// CertificateProfile is one node-bound role, shared by its NUMA instances.
// Each selected container receives only this profile's read-only HostPath.
type CertificateProfile struct {
	// +kubebuilder:validation:MaxLength=40
	Name string `json:"name"`
	// Component is controlplane, dataplane, birdAdapter, or a palette operator/sidecar name.
	Component string `json:"component"`
	// Container is the logical container name, before native-sidecar namespacing.
	Container string `json:"container"`
	// MountPath contains current/{tls.crt,tls.key,ca.crt} and immutable generations.
	MountPath string `json:"mountPath"`
	// +kubebuilder:validation:Enum=client;server
	Usage string `json:"usage"`
	// DNSNames supports {node}, {namespace}, {boxType} placeholders for server SANs.
	// +optional
	DNSNames []string `json:"dnsNames,omitempty"`
	// GatewayPermissions grants this client profile's UID-bound account explicit
	// gRPC method patterns. Server profiles receive the effective client accounts
	// in persistent current/{identities.yaml,permissions.yaml} files.
	// +optional
	// +kubebuilder:validation:MaxItems=128
	GatewayPermissions []string `json:"gatewayPermissions,omitempty"`
}

var gatewayMethodPattern = regexp.MustCompile(`^/[A-Za-z0-9_.*]+/[A-Za-z0-9_*]+$`)

// ValidateGatewayPermissions accepts full gRPC paths with optional '*' globs.
func ValidateGatewayPermissions(permissions []string) error {
	if len(permissions) > 128 {
		return fmt.Errorf("gatewayPermissions accepts at most 128 method patterns")
	}
	for _, permission := range permissions {
		if len(permission) > 256 || !gatewayMethodPattern.MatchString(permission) {
			return fmt.Errorf("invalid gateway permission %q: use /service/method with optional '*' globs", permission)
		}
	}
	return nil
}

// ValidateCertificates is shared by admission and rendering of persisted objects.
func ValidateCertificates(c *CertificateManagementSpec, components *ComponentsSpec) error {
	if c == nil {
		return nil
	}
	if len(validation.IsDNS1123Subdomain(c.IssuerRef.Name)) != 0 || len(validation.IsCIdentifier(c.IssuerRef.Kind)) != 0 {
		return fmt.Errorf("certificates.issuerRef requires a valid name and kind")
	}
	if c.IssuerRef.Group != "" && len(validation.IsDNS1123Subdomain(c.IssuerRef.Group)) != 0 {
		return fmt.Errorf("certificates.issuerRef.group must be a DNS subdomain")
	}
	if (c.IssuerRef.Group == "" || c.IssuerRef.Group == "cert-manager.io") && c.IssuerRef.Kind != "Issuer" && c.IssuerRef.Kind != "ClusterIssuer" {
		return fmt.Errorf("certificates.issuerRef.kind must be Issuer or ClusterIssuer for cert-manager.io")
	}
	if len(validation.IsDNS1123Subdomain(c.TrustDomain)) != 0 || strings.Contains(c.TrustDomain, ":") {
		return fmt.Errorf("certificates.trustDomain must be a DNS subdomain")
	}
	if len(validation.IsDNS1123Subdomain(c.TrustBundle.Name)) != 0 || c.TrustBundle.Key == "" || len(validation.IsConfigMapKey(c.TrustBundle.Key)) != 0 || (c.TrustBundle.Optional != nil && *c.TrustBundle.Optional) {
		return fmt.Errorf("certificates.trustBundle requires a valid non-optional ConfigMap name/key")
	}
	if !persistentPath(c.HostPath) || c.HostPath == "/" {
		return fmt.Errorf("certificates.hostPath must be a clean persistent absolute directory")
	}
	image, digest, pinned := strings.Cut(c.ExporterImage, "@sha256:")
	if !pinned || image == "" || len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return fmt.Errorf("certificates.exporterImage must be digest-pinned")
	}
	duration, err := time.ParseDuration(c.Duration)
	if err != nil || duration < time.Hour {
		return fmt.Errorf("certificates.duration must be at least 1h")
	}
	renew, err := time.ParseDuration(c.RenewBefore)
	if err != nil || renew < time.Hour || renew >= duration {
		return fmt.Errorf("certificates.renewBefore must be at least 1h and less than duration")
	}
	if len(c.Profiles) == 0 || len(c.Profiles) > 32 {
		return fmt.Errorf("certificates requires 1..32 profiles")
	}
	names := map[string]bool{}
	for _, op := range components.Operators {
		if op.Name == "credential-delivery" {
			return fmt.Errorf("credential-delivery is reserved when certificates are enabled")
		}
	}
	for _, p := range c.Profiles {
		if len(p.Name) > 40 || len(validation.IsDNS1123Label(p.Name)) != 0 || names[p.Name] {
			return fmt.Errorf("certificate profile names must be unique DNS labels up to 40 characters")
		}
		names[p.Name] = true
		if !path.IsAbs(p.MountPath) || path.Clean(p.MountPath) != p.MountPath || p.MountPath == "/" {
			return fmt.Errorf("certificate profile %q requires a clean absolute mountPath", p.Name)
		}
		if p.Usage != "client" && p.Usage != "server" {
			return fmt.Errorf("certificate profile %q usage must be client or server", p.Name)
		}
		if (p.Usage == "server") != (len(p.DNSNames) > 0) {
			return fmt.Errorf("certificate profile %q requires DNSNames only for server usage", p.Name)
		}
		if p.Usage != "client" && len(p.GatewayPermissions) > 0 {
			return fmt.Errorf("certificate profile %q grants gatewayPermissions only for client usage", p.Name)
		}
		if err := ValidateGatewayPermissions(p.GatewayPermissions); err != nil {
			return fmt.Errorf("certificate profile %q: %w", p.Name, err)
		}
		for _, name := range p.DNSNames {
			name = strings.NewReplacer("{node}", "node.example", "{namespace}", "default", "{boxType}", "edge").Replace(name)
			if len(validation.IsDNS1123Subdomain(name)) != 0 {
				return fmt.Errorf("certificate profile %q has an invalid DNS name/template", p.Name)
			}
		}
		valid := false
		switch p.Component {
		case "controlplane":
			valid = p.Container == "controlplane"
		case "dataplane":
			valid = p.Container == DataplaneContainerName
		case "birdAdapter":
			valid = components.BirdAdapter != nil && p.Container == BirdAdapterContainerName
		default:
			for _, op := range components.Operators {
				if op.Name == p.Component {
					for _, container := range op.Containers {
						valid = valid || container.Name == p.Container
					}
				}
			}
			for _, sidecar := range components.Dataplane.Sidecars {
				valid = valid || sidecar.Name == p.Component && p.Container == sidecar.Name
			}
		}
		if !valid {
			return fmt.Errorf("certificate profile %q references an undeclared component/container", p.Name)
		}
	}
	return nil
}

func persistentPath(value string) bool {
	if !path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, prefix := range []string{"/run", "/var/run", "/tmp", "/var/tmp", "/var/lib/kubelet", "/proc", "/sys", "/dev"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return false
		}
	}
	return true
}

// CertificateURI binds an identity to both object UIDs, not a reusable hostname.
func CertificateURI(domain, namespace, installationUID, nodeUID, role string) string {
	return "spiffe://" + domain + "/ns/" + url.PathEscape(namespace) + "/installation/" +
		url.PathEscape(installationUID) + "/node/" + url.PathEscape(nodeUID) + "/role/" + url.PathEscape(role)
}
