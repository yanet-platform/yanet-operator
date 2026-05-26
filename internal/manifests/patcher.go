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

package manifests

import (
	"encoding/json"
	"fmt"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

// PatchRegistry indexes the cluster-wide patch palette by name. The
// reconciler builds it once per reconcile from
// YanetConfigV2.spec.patches[] and reuses across components.
type PatchRegistry map[string]yanetv2alpha1.NamedPatch

// NewPatchRegistry builds an indexed view of the patch palette. The
// caller is expected to have validated uniqueness of names through
// the webhook; on duplicates the LAST entry wins to keep the function
// deterministic in the presence of (forbidden but possible) duplicate
// admission bypass.
func NewPatchRegistry(patches []yanetv2alpha1.NamedPatch) PatchRegistry {
	r := make(PatchRegistry, len(patches))
	for i := range patches {
		r[patches[i].Name] = patches[i]
	}
	return r
}

// ApplyPatches layers the named strategic-merge patches on top of the
// given Deployment, in declared order. A nil or empty patchNames list
// is a no-op.
//
// Each patch is loaded from `registry`. Missing patches return an
// error — the webhook ensures all references resolve, but the
// reconciler still double-checks against transient inconsistency.
//
// The function uses k8s.io/apimachinery strategic merge: arrays with
// `patchMergeKey` are merged by key (containers by name, volumes by
// name, etc), other fields override. Non-Deployment patches will
// fail at this layer with a clear message.
func ApplyPatches(d *appsv1.Deployment, patchNames []string, registry PatchRegistry) error {
	if d == nil {
		return fmt.Errorf("applyPatches: deployment is nil")
	}
	if len(patchNames) == 0 {
		return nil
	}
	current, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("applyPatches: marshal base: %w", err)
	}
	for _, name := range patchNames {
		patch, ok := registry[name]
		if !ok {
			return fmt.Errorf("applyPatches: patch %q is not defined in YanetConfigV2.spec.patches", name)
		}
		raw := patch.Patch.Raw
		if len(raw) == 0 {
			return fmt.Errorf("applyPatches: patch %q is empty", name)
		}
		// Re-marshal to canonical JSON so we accept both YAML and
		// JSON inputs uniformly. runtime.RawExtension always
		// stores JSON internally after the API server, but local
		// tests (and webhooks) may pass YAML-decoded structures.
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			return fmt.Errorf("applyPatches: patch %q is not valid JSON: %w", name, err)
		}
		patchBytes, err := json.Marshal(probe)
		if err != nil {
			return fmt.Errorf("applyPatches: patch %q re-marshal: %w", name, err)
		}
		merged, err := strategicpatch.StrategicMergePatch(current, patchBytes, appsv1.Deployment{})
		if err != nil {
			return fmt.Errorf("applyPatches: patch %q failed strategic merge: %w", name, err)
		}
		current = merged
	}
	// Decode the final result back into the Deployment struct.
	// Reset receiver fields first so leftover slices/maps don't
	// shadow merged values.
	*d = appsv1.Deployment{}
	if err := json.Unmarshal(current, d); err != nil {
		return fmt.Errorf("applyPatches: decode merged Deployment: %w", err)
	}
	return nil
}
