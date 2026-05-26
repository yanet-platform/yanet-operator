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
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// makeBaseDeployment returns a Deployment skeleton resembling what the
// builder produces: a single container named after the component, one
// volume placeholder, no annotations, no resources.
func makeBaseDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "yanet-controlplane",
			Namespace: "yanet",
			Labels:    map[string]string{"app.kubernetes.io/component": "controlplane"},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"existing": "1"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "controlplane", Image: "controlplane:v2.1"},
					},
				},
			},
		},
	}
}

func patch(name, raw string) yanetv2alpha1.NamedPatch {
	return yanetv2alpha1.NamedPatch{
		Name:  name,
		Patch: runtime.RawExtension{Raw: []byte(raw)},
	}
}

func TestApplyPatches_Noop(t *testing.T) {
	d := makeBaseDeployment()
	if err := ApplyPatches(d, nil, nil); err != nil {
		t.Errorf("nil patchNames must be a no-op: %v", err)
	}
	if err := ApplyPatches(d, []string{}, nil); err != nil {
		t.Errorf("empty patchNames must be a no-op: %v", err)
	}
	if d.Spec.Template.Annotations["existing"] != "1" {
		t.Errorf("base mutated by no-op")
	}
}

func TestApplyPatches_NilDeployment(t *testing.T) {
	if err := ApplyPatches(nil, []string{"x"}, nil); err == nil {
		t.Errorf("nil deployment must error")
	}
}

func TestApplyPatches_SinglePatch_AddsAnnotation(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("telegraf", `{"spec":{"template":{"metadata":{"annotations":{"telegraf.influxdata.com/ports":"8080"}}}}}`),
	})
	if err := ApplyPatches(d, []string{"telegraf"}, reg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	a := d.Spec.Template.Annotations
	if a["telegraf.influxdata.com/ports"] != "8080" {
		t.Errorf("missing telegraf annotation: %v", a)
	}
	if a["existing"] != "1" {
		t.Errorf("strategic merge dropped existing annotation: %v", a)
	}
}

func TestApplyPatches_ContainerMergeByName(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("cp-resources", `{"spec":{"template":{"spec":{"containers":[{"name":"controlplane","resources":{"limits":{"cpu":"6","memory":"128Gi"}}}]}}}}`),
	})
	if err := ApplyPatches(d, []string{"cp-resources"}, reg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(d.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("container merged into duplicate: %d", len(d.Spec.Template.Spec.Containers))
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Name != "controlplane" || c.Image != "controlplane:v2.1" {
		t.Errorf("base container fields lost: %+v", c)
	}
	if c.Resources.Limits.Cpu().String() != "6" {
		t.Errorf("cpu limit not merged: %v", c.Resources)
	}
}

func TestApplyPatches_OrderMatters(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("first", `{"spec":{"template":{"metadata":{"annotations":{"k":"first"}}}}}`),
		patch("second", `{"spec":{"template":{"metadata":{"annotations":{"k":"second"}}}}}`),
	})

	d1 := makeBaseDeployment()
	if err := ApplyPatches(d1, []string{"first", "second"}, reg); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	if got := d1.Spec.Template.Annotations["k"]; got != "second" {
		t.Errorf("first→second order: k=%q, want second", got)
	}

	d2 := makeBaseDeployment()
	if err := ApplyPatches(d2, []string{"second", "first"}, reg); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if got := d2.Spec.Template.Annotations["k"]; got != "first" {
		t.Errorf("second→first order: k=%q, want first", got)
	}
	_ = d
}

func TestApplyPatches_MultiplePatches_Compose(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("telegraf", `{"spec":{"template":{"metadata":{"annotations":{"telegraf":"on"}}}}}`),
		patch("checkpointer", `{"spec":{"template":{"metadata":{"annotations":{"checkpointer":"on"}}}}}`),
		patch("hostipc", `{"spec":{"template":{"spec":{"hostIPC":true}}}}`),
	})
	if err := ApplyPatches(d, []string{"telegraf", "checkpointer", "hostipc"}, reg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	a := d.Spec.Template.Annotations
	if a["telegraf"] != "on" || a["checkpointer"] != "on" || a["existing"] != "1" {
		t.Errorf("annotations after compose: %v", a)
	}
	if !d.Spec.Template.Spec.HostIPC {
		t.Errorf("hostIPC should be true")
	}
}

func TestApplyPatches_MissingPatch(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry(nil)
	err := ApplyPatches(d, []string{"ghost"}, reg)
	if err == nil {
		t.Fatalf("expected error for missing patch")
	}
}

func TestApplyPatches_EmptyPatch(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		{Name: "empty", Patch: runtime.RawExtension{}},
	})
	if err := ApplyPatches(d, []string{"empty"}, reg); err == nil {
		t.Errorf("expected error for empty patch")
	}
}

func TestApplyPatches_InvalidJSON(t *testing.T) {
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("bad", `{not json`),
	})
	if err := ApplyPatches(d, []string{"bad"}, reg); err == nil {
		t.Errorf("expected error for invalid JSON")
	}
}

func TestApplyPatches_NonDeploymentField_IsTolerated(t *testing.T) {
	// Strategic merge for unknown top-level keys does NOT error;
	// the key is simply preserved in JSON but won't survive
	// json.Unmarshal back into appsv1.Deployment. The function
	// should still succeed (no error from StrategicMergePatch).
	d := makeBaseDeployment()
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("bogus-field", `{"spec":{"template":{"metadata":{"annotations":{"k":"v"}}}},"bogusTopLevel":"x"}`),
	})
	if err := ApplyPatches(d, []string{"bogus-field"}, reg); err != nil {
		t.Errorf("strategic merge with unknown top-level key: %v", err)
	}
	if d.Spec.Template.Annotations["k"] != "v" {
		t.Errorf("real annotation should still merge: %v", d.Spec.Template.Annotations)
	}
}

func TestNewPatchRegistry_DuplicateLastWins(t *testing.T) {
	reg := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("dup", `{"a":1}`),
		patch("dup", `{"a":2}`),
	})
	got := string(reg["dup"].Patch.Raw)
	if got != `{"a":2}` {
		t.Errorf("last-wins on duplicate name: got %q", got)
	}
}
