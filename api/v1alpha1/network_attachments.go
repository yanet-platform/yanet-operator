package v1alpha1

import (
	"fmt"
	"regexp"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var networkInterfaceName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// ValidateNetworkAttachments validates the device-backed Multus contract.
// An empty namespace is validated again after resolution to the Pod namespace.
func ValidateNetworkAttachments(networks []NetworkAttachment) error {
	interfaces := make(map[string]bool, len(networks))
	nadResources := make(map[string]string, len(networks))
	for index, network := range networks {
		field := fmt.Sprintf("networks[%d]", index)
		if errors := k8svalidation.IsDNS1123Subdomain(network.Name); len(errors) != 0 {
			return fmt.Errorf("%s.name is invalid: %s", field, strings.Join(errors, "; "))
		}
		if network.Namespace != "" {
			if errors := k8svalidation.IsDNS1123Label(network.Namespace); len(errors) != 0 {
				return fmt.Errorf("%s.namespace is invalid: %s", field, strings.Join(errors, "; "))
			}
		}
		if len(network.Interface) > 15 || !networkInterfaceName.MatchString(network.Interface) ||
			network.Interface == "." || network.Interface == ".." || network.Interface == "lo" || network.Interface == "eth0" {
			return fmt.Errorf("%s.interface must be a non-reserved interface name of 1-15 characters", field)
		}
		if interfaces[network.Interface] {
			return fmt.Errorf("%s.interface %q is duplicated", field, network.Interface)
		}
		interfaces[network.Interface] = true
		prefix, _, qualified := strings.Cut(network.ResourceName, "/")
		if errors := k8svalidation.IsQualifiedName(network.ResourceName); len(errors) != 0 || !qualified ||
			prefix == "kubernetes.io" || strings.HasSuffix(prefix, ".kubernetes.io") || strings.HasPrefix(network.ResourceName, "requests.") {
			return fmt.Errorf("%s.resourceName must be a qualified extended resource name", field)
		}
		identity := network.Namespace + "/" + network.Name
		if resource, exists := nadResources[identity]; exists && resource != network.ResourceName {
			return fmt.Errorf("%s.resourceName conflicts with another attachment of NAD %q", field, identity)
		}
		nadResources[identity] = network.ResourceName
	}
	return nil
}
