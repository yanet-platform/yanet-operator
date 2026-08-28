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
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func envValues(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for i := range env {
		out[env[i].Name] = env[i].Value
	}
	return out
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

func TestBuildDeployments_Controlplane_ServicePortsAndBindEnv(t *testing.T) {
	literal := "[::]:8080"
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image:    helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		GRPCPort: 8080, HTTPPort: 8081, Numa: 2, ServiceEnabled: true,
		Bind: &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{
			{Key: "YANET_GATEWAY_ENDPOINT", Value: &literal},
			{Key: "YANET_GATEWAY_ADVERTISE_ENDPOINT", Service: &yanetv2alpha1.ServiceRef{Port: 8080}},
		}},
	}
	deployments, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deployments) != 2 {
		t.Fatalf("deployments = %d, want 2", len(deployments))
	}
	for i := range deployments {
		container := &deployments[i].Spec.Template.Spec.Containers[0]
		if len(container.Ports) != 2 ||
			container.Ports[0].Name != "grpc" || container.Ports[0].ContainerPort != 8080 ||
			container.Ports[1].Name != "http" || container.Ports[1].ContainerPort != 8081 {
			t.Errorf("deployment[%d] ports = %+v", i, container.Ports)
		}
		env := envValues(container.Env)
		if env["YANET_GATEWAY_ENDPOINT"] != literal {
			t.Errorf("deployment[%d] literal env = %q", i, env["YANET_GATEWAY_ENDPOINT"])
		}
		wantService := fmt.Sprintf("edge-controlplane-numa%d.yanet.svc.cluster.local:8080", i)
		if env["YANET_GATEWAY_ADVERTISE_ENDPOINT"] != wantService {
			t.Errorf("deployment[%d] Service env = %q, want %q", i,
				env["YANET_GATEWAY_ADVERTISE_ENDPOINT"], wantService)
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

func TestApplyHugepages_MultipliesPageSizeByCount(t *testing.T) {
	tests := []struct {
		name  string
		size  string
		count int32
		want  string
	}{
		{name: "one GiB pages", size: "1Gi", count: 8, want: "8Gi"},
		{name: "two MiB pages", size: "2Mi", count: 1024, want: "2Gi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := corev1.Container{}
			var volumes []corev1.Volume
			applyHugepages(&container, &volumes, &yanetv2alpha1.Hugepages{
				Size:  tt.size,
				Count: tt.count,
			})

			resourceName := corev1.ResourceName("hugepages-" + tt.size)
			if got := container.Resources.Requests[resourceName]; got.String() != tt.want {
				t.Errorf("request = %s, want %s", got.String(), tt.want)
			}
			if got := container.Resources.Limits[resourceName]; got.String() != tt.want {
				t.Errorf("limit = %s, want %s", got.String(), tt.want)
			}
		})
	}
}

func TestBuildDeployments_Dataplane_InvalidHugepagesReturnsError(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image:     helpers.ResolvedImage{Name: "dp", Tag: "v2"},
		Hugepages: &yanetv2alpha1.Hugepages{Size: "invalid", Count: 1},
	}
	if _, err := BuildDeployments(ctxV2(), c); err == nil {
		t.Fatal("invalid hugepage size must return an error")
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

// hasMount reports whether the container mounts the given path (and,
// when wantRO, that it is read-only).
func hasMount(mounts []corev1.VolumeMount, path string, wantRO bool) bool {
	for _, m := range mounts {
		if m.MountPath == path {
			return !wantRO || m.ReadOnly
		}
	}
	return false
}

func hasVolume(vols []corev1.Volume, name string) bool {
	for _, v := range vols {
		if v.Name == name {
			return true
		}
	}
	return false
}

// TestBuildDeployments_Dataplane_SecurityBaseline pins the privileged +
// minimal-device baseline the builder must emit for the DPDK dataplane,
// without any YanetConfigV2 patch.
func TestBuildDeployments_Dataplane_SecurityBaseline(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image:     helpers.ResolvedImage{Name: "dp", Tag: "v2"},
		Port:      8090,
		Hugepages: &yanetv2alpha1.Hugepages{Size: "2Mi", Count: 4096},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"/etc/yanet2/dataplane.yaml"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	pod := deps[0].Spec.Template.Spec
	cont := pod.Containers[0]

	if cont.SecurityContext == nil || cont.SecurityContext.Privileged == nil || !*cont.SecurityContext.Privileged {
		t.Errorf("dataplane must be privileged: %+v", cont.SecurityContext)
	}
	if !pod.HostIPC {
		t.Errorf("dataplane must set hostIPC")
	}
	if !pod.HostNetwork {
		t.Errorf("dataplane must set hostNetwork")
	}
	// Minimal host devices.
	for _, p := range []string{"/dev/vfio", "/dev/vhost-net", "/dev/net"} {
		if !hasMount(cont.VolumeMounts, p, false) {
			t.Errorf("missing device mount %s", p)
		}
	}
	if !hasMount(cont.VolumeMounts, "/sys", true) {
		t.Errorf("/sys must be mounted read-only")
	}
	// Builder-provided mounts must survive alongside the devices.
	if !hasMount(cont.VolumeMounts, "/etc/yanet2", true) {
		t.Errorf("config mount missing or not read-only")
	}
	if !hasMount(cont.VolumeMounts, "/dev/hugepages", false) {
		t.Errorf("hugepages mount missing")
	}
	for _, n := range []string{"host-vfio", "host-vhost-net", "host-net", "host-sys", "config", "hugepages"} {
		if !hasVolume(pod.Volumes, n) {
			t.Errorf("missing volume %q", n)
		}
	}
	// /dev/vhost-net is a single char device → typeless hostPath.
	for _, v := range pod.Volumes {
		if v.Name == "host-vhost-net" && v.HostPath != nil && v.HostPath.Type != nil {
			t.Errorf("host-vhost-net must be typeless, got %v", *v.HostPath.Type)
		}
	}
}

// TestBuildDeployments_Controlplane_ShmemBaseline pins the shmem-arena
// mount + hostIPC the controlplane needs, and asserts it is NOT privileged
// and gets no device nodes.
func TestBuildDeployments_Controlplane_ShmemBaseline(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:  8080,
		Numa:  1,
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"-c", "/etc/yanet2/controlplane.yaml"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	pod := deps[0].Spec.Template.Spec
	cont := pod.Containers[0]

	if !pod.HostIPC {
		t.Errorf("controlplane must set hostIPC for shmem")
	}
	if !hasMount(cont.VolumeMounts, "/dev/hugepages", false) {
		t.Errorf("controlplane missing hugepages shmem mount")
	}
	if cont.SecurityContext != nil && cont.SecurityContext.Privileged != nil && *cont.SecurityContext.Privileged {
		t.Errorf("controlplane must NOT be privileged")
	}
	for _, p := range []string{"/dev/vfio", "/dev/vhost-net", "/dev/net"} {
		if hasMount(cont.VolumeMounts, p, false) {
			t.Errorf("controlplane must not mount device %s", p)
		}
	}
}

// TestBuildDeployments_BirdSocketMounts checks that bird gets the shared
// /run/bird socket dir read-write, while bird-adapter and announcer get it
// read-only — all as a hostPath so the separate Pods share the socket.
func TestBuildDeployments_BirdSocketMounts(t *testing.T) {
	cases := []struct {
		kind   helpers.ComponentKind
		name   string
		wantRO bool
	}{
		{helpers.KindBird, "bird", false},
		{helpers.KindBirdAdapter, "birdAdapter", true},
		{helpers.KindAnnouncer, "announcer", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &helpers.ResolvedComponent{
				Kind: tc.kind, Name: tc.name, Enabled: true,
				Image:  helpers.ResolvedImage{Name: tc.name, Tag: "v1"},
				Config: &yanetv2alpha1.ConfigSource{HostPath: "/etc/x"},
			}
			deps, err := BuildDeployments(ctxV2(), c)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			pod := deps[0].Spec.Template.Spec
			if !hasMount(pod.Containers[0].VolumeMounts, "/run/bird", tc.wantRO) {
				t.Errorf("%s: /run/bird mount missing or RO=%v wrong: %+v",
					tc.name, tc.wantRO, pod.Containers[0].VolumeMounts)
			}
			var v *corev1.Volume
			for i := range pod.Volumes {
				if pod.Volumes[i].Name == "run-bird" {
					v = &pod.Volumes[i]
				}
			}
			if v == nil || v.HostPath == nil || v.HostPath.Path != "/run/bird" {
				t.Errorf("%s: run-bird must be a hostPath /run/bird: %+v", tc.name, pod.Volumes)
			}
			// Config mount must survive alongside the socket mount.
			if !hasMount(pod.Containers[0].VolumeMounts, defaultConfigMountPath(tc.kind), true) {
				t.Errorf("%s: config mount missing", tc.name)
			}
			// Only bird peers BGP over the host network.
			wantHostNet := tc.kind == helpers.KindBird
			if pod.HostNetwork != wantHostNet {
				t.Errorf("%s: hostNetwork = %v, want %v", tc.name, pod.HostNetwork, wantHostNet)
			}
		})
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
	// The hostIPC container (agent) is a shmem peer → gets the arena;
	// the non-hostIPC container (operator) must not.
	if hasMount(pod.Containers[0].VolumeMounts, "/dev/hugepages", false) {
		t.Errorf("non-hostIPC operator must not mount shmem: %+v", pod.Containers[0].VolumeMounts)
	}
	if !hasMount(pod.Containers[1].VolumeMounts, "/dev/hugepages", false) {
		t.Errorf("hostIPC agent must mount shmem: %+v", pod.Containers[1].VolumeMounts)
	}
	// Exactly one shared shmem volume regardless of peer count.
	n := 0
	for _, v := range pod.Volumes {
		if v.Name == "hugepages" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected exactly 1 shmem volume, got %d: %+v", n, pod.Volumes)
	}
}

// TestBuildDeployments_Operator_NoHostIPC_NoShmem ensures operators that
// never request hostIPC get neither the arena mount nor the volume.
func TestBuildDeployments_Operator_NoHostIPC_NoShmem(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "route", Enabled: true,
		Image: helpers.ResolvedImage{Name: "route-op", Tag: "v0.4"},
		Port:  9001,
		Containers: []helpers.ResolvedContainer{
			{Name: "route", Image: helpers.ResolvedImage{Name: "route-op", Tag: "v0.4"}},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	pod := deps[0].Spec.Template.Spec
	if pod.HostIPC {
		t.Errorf("route operator must not set hostIPC")
	}
	if hasVolume(pod.Volumes, "hugepages") {
		t.Errorf("no-hostIPC operator must not get shmem volume: %+v", pod.Volumes)
	}
}

func TestBuildDeployments_Operator_BindEnv(t *testing.T) {
	literal := "[::]:9000"
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "route", Enabled: true,
		Image: helpers.ResolvedImage{Name: "route-op", Tag: "v1"},
		Port:  9000, ServiceEnabled: true, ServiceName: "route-api",
		Containers: []helpers.ResolvedContainer{{
			Name: "route", Image: helpers.ResolvedImage{Name: "route-op", Tag: "v1"},
			Bind: &yanetv2alpha1.BindSpec{Env: []yanetv2alpha1.BindEnv{
				{Key: "YANET_SERVER_ENDPOINT", Value: &literal},
				{Key: "YANET_SERVER_ADVERTISE_ENDPOINT", Service: &yanetv2alpha1.ServiceRef{Port: 9000}},
			}},
		}},
	}
	deployments, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	env := envValues(deployments[0].Spec.Template.Spec.Containers[0].Env)
	if env["YANET_SERVER_ENDPOINT"] != literal {
		t.Errorf("literal env = %q", env["YANET_SERVER_ENDPOINT"])
	}
	wantService := "route-api.yanet.svc.cluster.local:9000"
	if env["YANET_SERVER_ADVERTISE_ENDPOINT"] != wantService {
		t.Errorf("Service env = %q, want %q", env["YANET_SERVER_ADVERTISE_ENDPOINT"], wantService)
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
	// The config volume is built first; bird also gets the shared
	// /run/bird socket volume (see TestBuildDeployments_BirdSocketMounts).
	if pod.Volumes[0].HostPath == nil || pod.Volumes[0].HostPath.Path != "/etc/bird" {
		t.Errorf("hostPath config volume not set: %+v", pod.Volumes)
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

func TestBuildDeployments_RejectsStaleServiceReference(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindAnnouncer, Name: "announcer", Enabled: true,
		Image: helpers.ResolvedImage{Name: "announcer", Tag: "v1"}, Port: 9090,
		Bind: &yanetv2alpha1.BindSpec{
			Env: []yanetv2alpha1.BindEnv{{
				Key: "YANET_ENDPOINT", Service: &yanetv2alpha1.ServiceRef{Port: 9090},
			}},
		},
	}
	if _, err := BuildDeployments(ctxV2(), c); err == nil || !strings.Contains(err.Error(), "requires service.enabled") {
		t.Fatalf("stale Service reference must fail before Deployment apply, got %v", err)
	}
}

func TestBuildDeployments_RejectsServiceWithoutBind(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindAnnouncer, Name: "announcer", Enabled: true,
		Image: helpers.ResolvedImage{Name: "announcer", Tag: "v1"}, Port: 9090,
		ServiceEnabled: true,
	}
	if _, err := BuildDeployments(ctxV2(), c); err == nil || !strings.Contains(err.Error(), "non-empty bind") {
		t.Fatalf("Service without bind must fail before resource apply, got %v", err)
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

// TestBuildDeployments_HostPathArgs verifies that source args are passed
// verbatim without imposing a generic config flag convention.
func TestBuildDeployments_HostPathArgs(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindDataplane,
		Name:    "dataplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Name: "dataplane", Tag: "latest"},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"/etc/yanet2/dataplane.yaml"},
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
	want := "/etc/yanet2/dataplane.yaml"
	if len(args) != 1 || args[0] != want {
		t.Errorf("container Args = %v, want [%q]", args, want)
	}
}

func TestBuildDeployments_ConfigSourceArgs(t *testing.T) {
	tests := []struct {
		name string
		kind helpers.ComponentKind
		args []string
		want []string
	}{
		{
			name: "dataplane positional path", kind: helpers.KindDataplane,
			args: []string{"/etc/yanet2/dataplane.yaml"},
			want: []string{"/etc/yanet2/dataplane.yaml"},
		},
		{
			// The controlplane fans out per NUMA, so its config
			// path always carries the NUMA index — even on a
			// single-NUMA host, where only index 0 exists.
			name: "controlplane short option", kind: helpers.KindControlplane,
			args: []string{"-c", "/etc/yanet2/controlplane.yaml"},
			want: []string{"-c", "/etc/yanet2/controlplane-0.yaml"},
		},
		{
			name: "bird adapter subcommand", kind: helpers.KindBirdAdapter,
			args: []string{"server", "-c", "/etc/yanet2/bird-adapter.yaml"},
			want: []string{"server", "-c", "/etc/yanet2/bird-adapter.yaml"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := &helpers.ResolvedComponent{
				Kind: tt.kind, Name: string(tt.kind), Enabled: true,
				Image: helpers.ResolvedImage{Name: "component", Tag: "latest"},
				Config: &yanetv2alpha1.ConfigSource{
					HostPath: "/etc/yanet2",
					Args:     tt.args,
				},
			}
			deployments, err := BuildDeployments(ctxV2(), component)
			if err != nil {
				t.Fatalf("BuildDeployments: %v", err)
			}
			got := deployments[0].Spec.Template.Spec.Containers[0].Args
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- per-NUMA config paths --------------------------------------------------

// TestBuildDeployments_Controlplane_PerNumaConfigArgs verifies that every
// per-NUMA controlplane instance is pointed at its own config file. The
// controlplane reads gateway.instance_id and all endpoints from that file and
// accepts only `-c <path>`, so sharing one file would make every instance
// serve dataplane instance 0.
func TestBuildDeployments_Controlplane_PerNumaConfigArgs(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:  8080,
		Numa:  3,
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"-c", "/etc/yanet2/controlplane.yaml"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 3 {
		t.Fatalf("want 3 deployments, got %d", len(deps))
	}
	for i, d := range deps {
		want := []string{"-c", fmt.Sprintf("/etc/yanet2/controlplane-%d.yaml", i)}
		got := d.Spec.Template.Spec.Containers[0].Args
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("d[%d] args mismatch (-want +got):\n%s", i, diff)
		}
	}
}

// TestNumaConfigArgs verifies the path rewriting in isolation: only YAML path
// elements are touched, flags and subcommands survive untouched.
func TestNumaConfigArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		numa int32
		want []string
	}{
		{
			name: "short option", args: []string{"-c", "/etc/yanet2/controlplane.yaml"}, numa: 1,
			want: []string{"-c", "/etc/yanet2/controlplane-1.yaml"},
		},
		{
			name: "yml extension", args: []string{"-c", "/etc/yanet2/cp.yml"}, numa: 2,
			want: []string{"-c", "/etc/yanet2/cp-2.yml"},
		},
		{
			name: "subcommand preserved", args: []string{"run", "--config", "/etc/yanet2/cp.yaml"}, numa: 0,
			want: []string{"run", "--config", "/etc/yanet2/cp-0.yaml"},
		},
		{
			name: "no yaml element untouched", args: []string{"-c", "/etc/yanet2/config"}, numa: 1,
			want: []string{"-c", "/etc/yanet2/config"},
		},
		{
			name: "empty args", args: nil, numa: 1, want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := numaConfigArgs(tt.args, tt.numa)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("numaConfigArgs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestNumaConfigArgs_DoesNotMutateInput guards against aliasing: the resolved
// ConfigSource is shared by every NUMA instance, so rewriting must not edit it
// in place.
func TestNumaConfigArgs_DoesNotMutateInput(t *testing.T) {
	in := []string{"-c", "/etc/yanet2/controlplane.yaml"}
	_ = numaConfigArgs(in, 1)
	if in[1] != "/etc/yanet2/controlplane.yaml" {
		t.Errorf("input mutated: %v", in)
	}
}

// --- disabled NUMA ----------------------------------------------------------

// TestBuildDeployments_Controlplane_DisabledNuma verifies that a NUMA domain
// listed in DisabledNuma gets no Deployment at all. The typical case is a NUMA
// without a NIC, where the dataplane runs no instance.
func TestBuildDeployments_Controlplane_DisabledNuma(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image:        helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:         8080,
		Numa:         2,
		DisabledNuma: []int32{1},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"-c", "/etc/yanet2/controlplane.yaml"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("want 1 deployment for the single enabled NUMA, got %d", len(deps))
	}
	d := deps[0]
	if got := d.Spec.Template.Labels[labelNuma]; got != "0" {
		t.Errorf("surviving instance numa label = %q, want %q", got, "0")
	}
	// The kept instance must retain its own index, not be renumbered.
	want := []string{"-c", "/etc/yanet2/controlplane-0.yaml"}
	if diff := cmp.Diff(want, d.Spec.Template.Spec.Containers[0].Args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
	if port := d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; port != 8080 {
		t.Errorf("container port = %d, want 8080", port)
	}
}

// TestBuildDeployments_Controlplane_DisabledNumaKeepsIndices verifies that
// disabling a LOW index does not shift the remaining instances: NUMA 1 keeps
// index 1, its own config file and port 8080+1.
func TestBuildDeployments_Controlplane_DisabledNumaKeepsIndices(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image:        helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:         8080,
		Numa:         2,
		DisabledNuma: []int32{0},
		Config: &yanetv2alpha1.ConfigSource{
			HostPath: "/etc/yanet2",
			Args:     []string{"-c", "/etc/yanet2/controlplane.yaml"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("want 1 deployment, got %d", len(deps))
	}
	d := deps[0]
	if got := d.Spec.Template.Labels[labelNuma]; got != "1" {
		t.Errorf("numa label = %q, want %q", got, "1")
	}
	if !strings.Contains(d.Name, "numa1") {
		t.Errorf("deployment name %q must keep the numa1 suffix", d.Name)
	}
	want := []string{"-c", "/etc/yanet2/controlplane-1.yaml"}
	if diff := cmp.Diff(want, d.Spec.Template.Spec.Containers[0].Args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
	if port := d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; port != 8081 {
		t.Errorf("container port = %d, want 8081", port)
	}
}

// TestBuildDeployments_Controlplane_DisabledNumaOutOfRange verifies that
// indices beyond the fan-out count (and duplicates) are harmless.
func TestBuildDeployments_Controlplane_DisabledNumaOutOfRange(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image:        helpers.ResolvedImage{Name: "cp", Tag: "v2"},
		Port:         8080,
		Numa:         2,
		DisabledNuma: []int32{7, 7, -1},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if len(deps) != 2 {
		t.Errorf("out-of-range disabled indices must not drop instances, got %d", len(deps))
	}
}

func TestBuildDeployments_OperatorConfigSourceArgs(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "acl", Enabled: true,
		Containers: []helpers.ResolvedContainer{{
			Name:  "acl",
			Image: helpers.ResolvedImage{Name: "acl-operator", Tag: "latest"},
			Config: &yanetv2alpha1.ConfigSource{
				HostPath: "/etc/yanet2",
				Args:     []string{"run", "-c", "/etc/yanet2/yanet-acl-operator.yaml"},
			},
		}},
	}
	deployments, err := BuildDeployments(ctxV2(), component)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	want := []string{"run", "-c", "/etc/yanet2/yanet-acl-operator.yaml"}
	got := deployments[0].Spec.Template.Spec.Containers[0].Args
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildDeployments_InlineArgs verifies that inline content remains under
// the stable "config" key and explicit args can reference that path.
func TestBuildDeployments_InlineArgs(t *testing.T) {
	c := &helpers.ResolvedComponent{
		Kind:    helpers.KindControlplane,
		Name:    "controlplane",
		Enabled: true,
		Image:   helpers.ResolvedImage{Name: "controlplane", Tag: "latest"},
		Config: &yanetv2alpha1.ConfigSource{
			Inline: "some: config",
			Args:   []string{"-c", "/etc/yanet2/config"},
		},
	}
	deps, err := BuildDeployments(ctxV2(), c)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	d := deps[0]
	args := d.Spec.Template.Spec.Containers[0].Args
	wantArgs := []string{"-c", "/etc/yanet2/config"}
	if diff := cmp.Diff(wantArgs, args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
	if len(d.Spec.Template.Spec.Volumes) == 0 {
		t.Fatal("expected at least one volume")
	}
	vol := d.Spec.Template.Spec.Volumes[0]
	if vol.ConfigMap == nil {
		t.Fatal("expected ConfigMap volume source")
	}
	if len(vol.ConfigMap.Items) != 0 {
		t.Fatalf("inline config must retain the default config key, got Items=%v", vol.ConfigMap.Items)
	}
}

// TestBuildDeployments_NoArgs verifies that mounting a config source does not
// inject any implicit process arguments.
func TestBuildDeployments_NoArgs(t *testing.T) {
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
		t.Errorf("expected no implicit args, got %v", args)
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
