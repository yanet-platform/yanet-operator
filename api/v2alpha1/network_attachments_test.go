package v2alpha1

import (
	"context"
	"strings"
	"testing"
)

func TestNetworkAttachmentsValidation(t *testing.T) {
	valid := NetworkAttachment{Name: "pf", Interface: "eth2", ResourceName: "example.net/pf"}
	for _, tt := range []struct {
		name   string
		change func(*NetworkAttachment)
	}{
		{"missing NAD", func(n *NetworkAttachment) { n.Name = "" }},
		{"invalid NAD", func(n *NetworkAttachment) { n.Name = "namespace/pf" }},
		{"invalid namespace", func(n *NetworkAttachment) { n.Namespace = "Invalid" }},
		{"missing interface", func(n *NetworkAttachment) { n.Interface = "" }},
		{"primary interface", func(n *NetworkAttachment) { n.Interface = "eth0" }},
		{"loopback interface", func(n *NetworkAttachment) { n.Interface = "lo" }},
		{"long interface", func(n *NetworkAttachment) { n.Interface = strings.Repeat("a", 16) }},
		{"invalid interface", func(n *NetworkAttachment) { n.Interface = "a/b" }},
		{"native resource", func(n *NetworkAttachment) { n.ResourceName = "cpu" }},
		{"reserved resource", func(n *NetworkAttachment) { n.ResourceName = "kubernetes.io/pf" }},
		{"invalid resource", func(n *NetworkAttachment) { n.ResourceName = "example.net/invalid!" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			network := valid
			tt.change(&network)
			config := validConfig()
			config.Spec.Components.Dataplane.Networks = []NetworkAttachment{network}
			if _, err := (&YanetConfigCustomValidator{}).ValidateCreate(context.Background(), config); err == nil {
				t.Fatal("invalid palette network accepted")
			}
			overrides := &YanetComponentsOverride{Dataplane: &YanetDataplaneOverride{Networks: []NetworkAttachment{network}}}
			if err := ValidateYanetComponentOverrides(overrides, &validConfig().Spec.Components, &validConfig().Spec.BoxTypes[0]); err == nil {
				t.Fatal("invalid installation network accepted")
			}
		})
	}
	for _, conflict := range []string{"interface", "resource"} {
		t.Run(conflict+" conflict", func(t *testing.T) {
			other := valid
			if conflict == "resource" {
				other.Interface, other.ResourceName = "eth4", "example.net/other"
			}
			if err := ValidateNetworkAttachments([]NetworkAttachment{valid, other}); err == nil {
				t.Fatal("conflicting network declarations accepted")
			}
		})
	}
}
