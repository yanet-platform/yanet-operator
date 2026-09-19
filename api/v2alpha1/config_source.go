/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v2alpha1

// ConfigSource describes where the configuration of a yanet component
// comes from. Exactly one of Inline or HostPath must be specified.
//
//   - Inline: configuration is embedded into the CR; the operator creates a
//     ConfigMap (named deterministically by content hash) and mounts it into
//     the Pod at MountPath (default /etc/yanet2).
//   - HostPath: a HOST directory mounted into the Pod as a hostPath volume at
//     configured MountPath (default /etc/yanet2). The component binary finds its
//     config file inside that directory by its own default name (e.g.
//     controlplane.conf). This is the default for production hosts.
//
// Args, when set, are passed to the component binary verbatim. For HostPath
// sources the whole directory is mounted, so args can reference any file in
// that directory. Inline content is available as <mountDir>/config.
//
// Validation that exactly one of Inline/HostPath is filled is enforced
// by the webhook.
type ConfigSource struct {
	// Inline is the literal configuration body. When set, the operator
	// creates a ConfigMap owned by the CR and mounts it into the Pod.
	// +optional
	Inline string `json:"inline,omitempty"`

	// HostPath is the HOST directory to mount into the container via a
	// hostPath volume. The directory is mounted read-only at the
	// default path /etc/yanet2, unless MountPath or a patch customizes it.
	// The component binary reads its config file from
	// inside that directory using its own default file name.
	// +optional
	HostPath string `json:"hostPath,omitempty"`

	// MountPath is the absolute container directory for the managed config volume.
	// Defaults to /etc/yanet2 when omitted. Inline content is stored as config
	// inside this directory; Args are never rewritten to match the mount.
	// +kubebuilder:validation:Pattern=`^(/.*)?$`
	// +optional
	MountPath string `json:"mountPath,omitempty"`

	// Args defines command-line arguments passed to the component verbatim.
	// Examples: ["/etc/yanet2/dataplane.yaml"] for dataplane, ["-c",
	// "/etc/yanet2/controlplane.yaml"] for controlplane and ["server", "-c",
	// "/etc/yanet2/bird-adapter.yaml"] for bird-adapter.
	// +optional
	Args []string `json:"args,omitempty"`
}

// IsZero reports whether the ConfigSource is empty (no variant chosen).
func (c *ConfigSource) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Inline == "" && c.HostPath == ""
}

// VariantsSet returns the number of variants populated. The webhook
// enforces VariantsSet() <= 1.
func (c *ConfigSource) VariantsSet() int {
	if c == nil {
		return 0
	}
	n := 0
	if c.Inline != "" {
		n++
	}
	if c.HostPath != "" {
		n++
	}
	return n
}
