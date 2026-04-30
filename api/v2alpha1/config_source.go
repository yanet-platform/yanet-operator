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
// comes from. Exactly one of Inline, HostPath or URL must be specified.
//
//   - Inline: configuration is embedded into the CR; the operator creates a
//     ConfigMap (named deterministically by content hash) and mounts it into
//     the Pod at the per-component default directory (/etc/bird for bird,
//     /etc/yanet2 for everything else).
//   - HostPath: a HOST directory mounted into the Pod as a hostPath volume at
//     the per-component default directory. The component binary finds its
//     config file inside that directory by its own default name (e.g.
//     controlplane.conf). This is the default for production hosts.
//   - URL: an HTTP(S) endpoint that returns the configuration body. The
//     operator generates an initContainer that downloads the file (with
//     `?node=<nodeName>` appended automatically) into an emptyDir volume
//     shared with the main container.
//
// FileName, when set, is passed as a command-line argument to the component
// binary (--config=<mountDir>/<fileName> or equivalent). It does NOT change
// the volume mount path — the directory is always mounted whole.
//
// Validation that exactly one of Inline/HostPath/URL is filled is enforced
// by the webhook.
type ConfigSource struct {
	// Inline is the literal configuration body. When set, the operator
	// creates a ConfigMap owned by the CR and mounts it into the Pod.
	// +optional
	Inline string `json:"inline,omitempty"`

	// HostPath is the HOST directory to mount into the container via a
	// hostPath volume. The directory is mounted read-only at the
	// per-component default path (/etc/bird for bird, /etc/yanet2 for
	// everything else). The component binary reads its config file from
	// inside that directory using its own default file name.
	// +optional
	HostPath string `json:"hostPath,omitempty"`

	// URL is an HTTP(S) endpoint that returns the configuration body.
	// The operator runs an initContainer that downloads the body into
	// an emptyDir volume shared with the main container; the parameter
	// `?node=<nodeName>` is appended automatically.
	// +optional
	URL string `json:"url,omitempty"`

	// FileName is the config file name relative to the mount directory.
	// When set it is passed as a command-line argument to the component
	// binary so the process knows which file inside the mounted directory
	// to read. It does NOT change the volume mount path.
	// +optional
	FileName string `json:"fileName,omitempty"`
}

// IsZero reports whether the ConfigSource is empty (no variant chosen).
func (c *ConfigSource) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Inline == "" && c.HostPath == "" && c.URL == ""
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
	if c.URL != "" {
		n++
	}
	return n
}
