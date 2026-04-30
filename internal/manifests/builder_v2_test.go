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
	"strings"
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ctxV2() BuildContextV2 {
	return BuildContextV2{
		YanetName:   "edge",
		Namespace:   "yanet",
		NodeName:    "node-1",
		PullPolicy:  corev1.PullIfNotPresent,
		PullSecrets: []corev1.LocalObjectReference{{Name: "cr-secret"}},
		OwnerRef:    metav1.OwnerReference{APIVersion: "v2alpha1", Kind: "YanetV2", Name: "edge", UID: "1"},
	}
}

// --- controlplane fan-out ---------------------------------------------------

func TestBuildDeployments_Controlplane_NUMAFanout(t *testing.T) {
	ctx := ctxV2()
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindControlplane,
		Name:    "controlplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Registry: "cr.io", Name: "cp", Tag: "v2"},
		Port:    8080,
		Numa:    3,
	}
	deps, err := BuildDeployments(ctx, c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(deps) != 3 {
		t.Fatalf("want 3 fan-out deployments, got %d", len(deps))
	}
	for i, d := range deps {
		if !strings.Contains(d.Name, "numa") {
			t.Errorf("d[%d].Name=%q lacks numa suffix", i, d.Name)
		}
		port := d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort
		want := int32(8080 + i)
		if port != want {
			t.Errorf("d[%d] container port = %d, want %d", i, port, want)
		}
		if v := d.Spec.Template.Labels[labelNuma]; v == "" {
			t.Errorf("d[%d] missing numa label", i)
		}
		if d.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "node-1" {
			t.Errorf("d[%d] missing node selector", i)
		}
	}
}

func TestBuildDeployments_Controlplane_NoNumaFallsBackToContext(t *testing.T) {
	ctx := ctxV2()
	ctx.NumaCount = 2
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "cp", Tag: "v2"}, Port: 8080,
	}
	deps, _ := BuildDeployments(ctx, c)
	if len(deps) != 2 {
		t.Errorf("ctx NumaCount=2: got %d", len(deps))
	}
}

func TestBuildDeployments_Controlplane_DefaultsToOne(t *testing.T) {
	ctx := ctxV2()
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "cp", Tag: "v2"}, Port: 8080,
	}
	deps, _ := BuildDeployments(ctx, c)
	if len(deps) != 1 {
		t.Errorf("default numa=1: got %d", len(deps))
	}
}

// --- dataplane / hugepages --------------------------------------------------

func TestBuildDeployments_Dataplane_Hugepages_HostNetwork(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image:     helpers.ResolvedImage{Name: "dp", Tag: "v2"},
		Port:      8081,
		Hugepages: &yanetv2alpha1.Hugepages{Size: "1Gi", Count: 8},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	d := deps[0]
	if !d.Spec.Template.Spec.HostNetwork {
		t.Errorf("dataplane defaults to hostNetwork=true")
	}
	cont := d.Spec.Template.Spec.Containers[0]
	if cont.Resources.Limits.Name("hugepages-1Gi", "Gi").String() != "8Gi" {
		t.Errorf("hugepage limit = %v", cont.Resources.Limits)
	}
	if cont.Resources.Requests.Name("hugepages-1Gi", "Gi").String() != "8Gi" {
		t.Errorf("hugepage request = %v", cont.Resources.Requests)
	}
	foundHP := false
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.Name == "hugepages" && v.HostPath != nil && v.HostPath.Path == "/dev/hugepages" {
			foundHP = true
		}
	}
	if !foundHP {
		t.Errorf("missing hugepages volume")
	}
}

func TestBuildDeployments_Dataplane_HostNetworkOverride(t *testing.T) {
	false_ := false
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image:       helpers.ResolvedImage{Name: "dp", Tag: "v2"},
		HostNetwork: &false_,
	}
	deps, _ := BuildDeployments(ctxV2(), c)
	if deps[0].Spec.Template.Spec.HostNetwork {
		t.Errorf("hostNetwork override to false ignored")
	}
}

// --- replicas / disabled ----------------------------------------------------

func TestBuildDeployments_DisabledHasZeroReplicas(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindBird, Name: "bird", Enabled: false,
		Image: helpers.ResolvedImage{Name: "bird", Tag: "x"}, Port: 179,
	}
	deps, _ := BuildDeployments(ctxV2(), c)
	if r := deps[0].Spec.Replicas; r == nil || *r != 0 {
		t.Errorf("disabled replicas = %v, want 0", r)
	}
}

// --- operator multi-container + HostIPC ------------------------------------

func TestBuildDeployments_Operator_MultiContainerHostIPC(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "antiddos", Enabled: true,
		Image: helpers.ResolvedImage{Name: "antiddos-op", Tag: "v0.5"},
		Port:  9001,
		Containers: []helpers.ResolvedContainer{
			{Name: "operator", Image: helpers.ResolvedImage{Name: "antiddos-op", Tag: "v0.5"}},
			{Name: "agent", Image: helpers.ResolvedImage{Name: "antiddos-agent", Tag: "v0.5"}, HostIPC: true},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("operator: 1 deployment expected")
	}
	pod := deps[0].Spec.Template.Spec
	if !pod.HostIPC {
		t.Errorf("any container HostIPC=true escalates pod-level: %v", pod.HostIPC)
	}
	if len(pod.Containers) != 2 {
		t.Fatalf("containers = %d", len(pod.Containers))
	}
	if pod.Containers[0].Name != "operator" || len(pod.Containers[0].Ports) != 1 {
		t.Errorf("primary container missing port: %+v", pod.Containers[0])
	}
	if len(pod.Containers[1].Ports) != 0 {
		t.Errorf("non-primary should have no Service ports: %+v", pod.Containers[1])
	}
}

// --- ConfigSource branches --------------------------------------------------

func TestBuildDeployments_Config_HostPath(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindBird, Name: "bird", Enabled: true,
		Image:  helpers.ResolvedImage{Name: "bird", Tag: "x"},
		Port:   179,
		Config: &yanetv2alpha1.ConfigSource{HostPath: "/etc/bird"},
	}
	deps, _ := BuildDeployments(ctxV2(), c)
	pod := deps[0].Spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].HostPath == nil || pod.Volumes[0].HostPath.Path != "/etc/bird" {
		t.Errorf("hostPath volume not set: %+v", pod.Volumes)
	}
	if mp := pod.Containers[0].VolumeMounts[0].MountPath; mp != "/etc/bird" {
		t.Errorf("bird mount path = %q, want /etc/bird", mp)
	}
}

func TestBuildDeployments_Config_Inline_GeneratesConfigMap(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image:  helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:   8080,
		Config: &yanetv2alpha1.ConfigSource{Inline: "foo: bar"},
		Numa:   1,
	}
	ctx := ctxV2()
	deps, _ := BuildDeployments(ctx, c)
	pod := deps[0].Spec.Template.Spec
	if pod.Volumes[0].ConfigMap == nil {
		t.Fatalf("expected configMap volume: %+v", pod.Volumes)
	}
	cmName := pod.Volumes[0].ConfigMap.Name

	cms := InlineConfigMaps(ctx, c)
	if cms[cmName] != "foo: bar" {
		t.Errorf("InlineConfigMaps mismatch: %v", cms)
	}
	// Stable hash → same content yields same name.
	cms2 := InlineConfigMaps(ctx, c)
	if cms2[cmName] != "foo: bar" {
		t.Errorf("inline map non-deterministic")
	}
}

func TestBuildDeployments_Config_URL_EmptyDir(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindAnnouncer, Name: "announcer", Enabled: true,
		Image:  helpers.ResolvedImage{Name: "an", Tag: "x"},
		Port:   9090,
		Config: &yanetv2alpha1.ConfigSource{URL: "https://x/y"},
	}
	deps, _ := BuildDeployments(ctxV2(), c)
	pod := deps[0].Spec.Template.Spec
	if pod.Volumes[0].EmptyDir == nil {
		t.Errorf("URL config: expected emptyDir, got %+v", pod.Volumes[0].VolumeSource)
	}
}

func TestBuildDeployments_Operator_PerContainerInlineConfig(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "route", Enabled: true,
		Image: helpers.ResolvedImage{Name: "route-op", Tag: "v0.4"},
		Containers: []helpers.ResolvedContainer{
			{Name: "route", Image: helpers.ResolvedImage{Name: "route-op", Tag: "v0.4"}, Config: &yanetv2alpha1.ConfigSource{Inline: "k: v"}},
		},
	}
	cms := InlineConfigMaps(ctxV2(), c)
	if len(cms) != 1 {
		t.Fatalf("operator inline: want 1 CM, got %v", cms)
	}
}

// --- nil / errors -----------------------------------------------------------

func TestBuildDeployments_NilComponent(t *testing.T) {
	if _, err := BuildDeployments(ctxV2(), nil); err == nil {
		t.Errorf("nil component must error")
	}
}

func TestBuildDeployments_NoNodeName_NoNodeSelector(t *testing.T) {
	ctx := ctxV2()
	ctx.NodeName = ""
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindBird, Name: "bird", Enabled: true,
		Image: helpers.ResolvedImage{Name: "bird", Tag: "x"}, Port: 179,
	}
	deps, _ := BuildDeployments(ctx, c)
	if deps[0].Spec.Template.Spec.NodeSelector != nil {
		t.Errorf("no NodeName: expect empty selector, got %v", deps[0].Spec.Template.Spec.NodeSelector)
	}
}

// --- trimUnitPrefix ---------------------------------------------------------

func TestTrimUnitPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1Gi", "Gi"},
		{"10Gi", "Gi"},
		{"100Mi", "Mi"},
		{"2Mi", "Mi"},
		{"1Ti", "Ti"},
		{"1.5Gi", "Gi"},
		{"+2Mi", "Mi"},
		{"-1Gi", "Gi"},
		{"Gi", "Gi"},
		{"", ""},
		{"1234", ""}, // no suffix at all
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := trimUnitPrefix(tc.in); got != tc.want {
				t.Errorf("trimUnitPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestToLowerKebab(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"birdAdapter", "bird-adapter"},
		{"controlplane", "controlplane"},
		{"dataplane", "dataplane"},
		{"bird", "bird"},
		{"announcer", "announcer"},
		{"myOperatorName", "my-operator-name"},
		{"ABC", "a-b-c"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := toLowerKebab(tc.in); got != tc.want {
				t.Errorf("toLowerKebab(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSingleDeploymentName_LowerKebab(t *testing.T) {
	ctx := BuildContextV2{
		YanetName: "test-yanet",
		NodeName:  "test-node",
		Namespace: "yanet",
	}
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindBirdAdapter, Name: "birdAdapter", Enabled: true,
		Image: helpers.ResolvedImage{Name: "bird-adapter", Tag: "v0.3"},
		Port:  50052,
	}
	name := singleDeploymentName(ctx, c)
	for _, ch := range name {
		if ch >= 'A' && ch <= 'Z' {
			t.Errorf("singleDeploymentName returned uppercase char in %q", name)
		}
	}
}

// TestBuildDeployments_BirdAdapter_ContainerNameIsRFC1123 verifies that the
// birdAdapter component produces a container name that satisfies RFC 1123
// (lowercase alphanumeric + hyphens), i.e. "bird-adapter" and NOT "birdAdapter".
func TestBuildDeployments_BirdAdapter_ContainerNameIsRFC1123(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindBirdAdapter, Name: "birdAdapter", Enabled: true,
		Image: helpers.ResolvedImage{Name: "bird-adapter", Tag: "v0.3"},
		Port:  50052,
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(deps))
	}
	containerName := deps[0].Spec.Template.Spec.Containers[0].Name
	if containerName != "bird-adapter" {
		t.Errorf("container name = %q, want %q (RFC 1123 kebab-case)", containerName, "bird-adapter")
	}
}

// TestBuildDeployments_FileName_HostPath verifies that when ConfigSource.FileName
// is set, the container receives --config=<mountPath>/<fileName> as Args.
func TestBuildDeployments_FileName_HostPath(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindDataplane,
		Name:    "dataplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Name: "dataplane", Tag: "latest"},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			FileName: "dataplane.yaml",
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(deps))
	}
	args := deps[0].Spec.Template.Spec.Containers[0].Args
	want := "--config=/etc/yanet2/dataplane.yaml"
	if len(args) != 1 || args[0] != want {
		t.Errorf("container Args = %v, want [%q]", args, want)
	}
}

// TestBuildDeployments_FileName_Inline verifies that FileName with inline config:
//  1. Adds --config=<mountPath>/<fileName> to container Args.
//  2. Remaps the ConfigMap "config" key to FileName via Items so the file
//     appears at the correct path inside the Pod.
func TestBuildDeployments_FileName_Inline(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindControlplane,
		Name:    "controlplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Name: "controlplane", Tag: "latest"},
		Config: &yanetv2alpha1.ConfigSource{
			Inline:   "some: config",
			FileName: "controlplane.conf",
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	d := deps[0]
	// Check --config arg.
	args := d.Spec.Template.Spec.Containers[0].Args
	wantArg := "--config=/etc/yanet2/controlplane.conf"
	if len(args) != 1 || args[0] != wantArg {
		t.Errorf("container Args = %v, want [%q]", args, wantArg)
	}
	// Check ConfigMap Items remapping.
	if len(d.Spec.Template.Spec.Volumes) == 0 {
		t.Fatal("expected at least one volume")
	}
	vol := d.Spec.Template.Spec.Volumes[0]
	if vol.ConfigMap == nil {
		t.Fatal("expected ConfigMap volume source")
	}
	if len(vol.ConfigMap.Items) != 1 {
		t.Fatalf("expected 1 Items entry, got %d", len(vol.ConfigMap.Items))
	}
	item := vol.ConfigMap.Items[0]
	if item.Key != "config" || item.Path != "controlplane.conf" {
		t.Errorf("Items[0] = {Key:%q Path:%q}, want {Key:%q Path:%q}",
			item.Key, item.Path, "config", "controlplane.conf")
	}
}

// TestBuildDeployments_NoFileName_NoArgs verifies that when FileName is empty,
// container Args is not set.
func TestBuildDeployments_NoFileName_NoArgs(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindDataplane,
		Name:    "dataplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Name: "dataplane", Tag: "latest"},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	args := deps[0].Spec.Template.Spec.Containers[0].Args
	if len(args) != 0 {
		t.Errorf("expected no Args when FileName is empty, got %v", args)
	}
}

// TestBuildDeployments_PullPolicy_Propagated verifies that the PullPolicy from
// BuildContextV2 is propagated to the generated container.
func TestBuildDeployments_PullPolicy_Propagated(t *testing.T) {
	tests := []struct {
		name       string
		pullPolicy corev1.PullPolicy
	}{
		{"IfNotPresent", corev1.PullIfNotPresent},
		{"Always", corev1.PullAlways},
		{"Never", corev1.PullNever},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctxV2()
			ctx.PullPolicy = tt.pullPolicy
			c := &helpers.ResolvedComponent{
				Kind:    helpers.KindDataplane,
				Name:    "dataplane",
				Enabled: true,
				Image:   helpers.ResolvedImage{Name: "dataplane", Tag: "latest"},
			}
			deps, err := BuildDeployments(ctx, c)
			if err != nil {
				t.Fatalf("BuildDeployments: %v", err)
			}
			got := deps[0].Spec.Template.Spec.Containers[0].ImagePullPolicy
			if got != tt.pullPolicy {
				t.Errorf("ImagePullPolicy = %q, want %q", got, tt.pullPolicy)
			}
		})
	}
}
