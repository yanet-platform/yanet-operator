package v1alpha1

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestCertificateConfigurationBoundaries(t *testing.T) {
	base := CertificateManagementSpec{IssuerRef: CertificateIssuerRef{Name: "issuer", Kind: "Issuer"}, TrustDomain: "example.test", TrustBundle: corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "trust"}, Key: "ca.crt"}, HostPath: "/var/lib/yanet/pki", ExporterImage: "example.test/operator@sha256:" + strings.Repeat("a", 64), Duration: "720h", RenewBefore: "480h", Profiles: []CertificateProfile{{Name: "gateway", Component: "controlplane", Container: "controlplane", MountPath: "/etc/yanet2/pki/gateway", Usage: "server", DNSNames: []string{"{node}", "yanet-{boxType}.{namespace}.svc.cluster.local"}}}}
	components := ComponentsSpec{}
	if err := ValidateCertificates(&base, &components); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	external := base.DeepCopy()
	external.IssuerRef = CertificateIssuerRef{Name: "external", Group: "pki.example.test", Kind: "ClusterWorkloadIssuer"}
	if err := ValidateCertificates(external, &components); err != nil {
		t.Errorf("external issuer kind rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*CertificateManagementSpec)
	}{
		{"ephemeral host path", func(c *CertificateManagementSpec) { c.HostPath = "/run/yanet/pki" }},
		{"path traversal", func(c *CertificateManagementSpec) { c.HostPath = "/var/lib/../run/yanet" }},
		{"relative mount", func(c *CertificateManagementSpec) { c.Profiles[0].MountPath = "pki" }},
		{"floating exporter", func(c *CertificateManagementSpec) { c.ExporterImage = "example.test/operator:latest" }},
		{"incomplete digest", func(c *CertificateManagementSpec) { c.ExporterImage = "example.test/operator@sha256:1234" }},
		{"renew beyond lifetime", func(c *CertificateManagementSpec) { c.RenewBefore = "721h" }},
		{"missing trust", func(c *CertificateManagementSpec) { c.TrustBundle.Name = "" }},
		{"invalid trust name", func(c *CertificateManagementSpec) { c.TrustBundle.Name = "invalid/name" }},
		{"invalid trust key", func(c *CertificateManagementSpec) { c.TrustBundle.Key = "invalid key" }},
		{"missing component", func(c *CertificateManagementSpec) { c.Profiles[0].Component = "undeclared" }},
		{"wrong container", func(c *CertificateManagementSpec) { c.Profiles[0].Container = "wrong" }},
		{"unknown placeholder", func(c *CertificateManagementSpec) { c.Profiles[0].DNSNames = []string{"{unknown}"} }},
		{"client server SAN", func(c *CertificateManagementSpec) { c.Profiles[0].Usage = "client" }},
		{"server without SAN", func(c *CertificateManagementSpec) { c.Profiles[0].DNSNames = nil }},
		{"duplicate role", func(c *CertificateManagementSpec) { c.Profiles = append(c.Profiles, c.Profiles[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := base.DeepCopy()
			test.mutate(c)
			if err := ValidateCertificates(c, &components); err == nil {
				t.Fatal("invalid certificate contract accepted")
			}
		})
	}
}
