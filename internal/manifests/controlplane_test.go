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
	"context"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDeploymentForControlplane verifies controlplane deployment generation
func TestDeploymentForControlplane(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-yanet",
			Namespace: "default",
		},
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Type:     "release", // Important: must match config.EnabledOpts.Release
			Tag:      "1.0.0",
			Registry: "docker.io/test",
			Controlplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-controlplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{
		EnabledOpts: yanetv1alpha1.EnabledOpts{
			Release: yanetv1alpha1.DepOpts{
				Controlplane: yanetv1alpha1.OptsNames{
					Annotations: []string{"checkpointer"},
					HostIpc:     true,
					Privileged:  false,
					Resources: v1.ResourceRequirements{
						Limits: v1.ResourceList{
							"cpu":    resource.MustParse("6"),
							"memory": resource.MustParse("128Gi"),
						},
						Requests: v1.ResourceList{
							"cpu":    resource.MustParse("1"),
							"memory": resource.MustParse("16Gi"),
						},
					},
				},
			},
		},
		AdditionalOpts: yanetv1alpha1.AdditionalOpts{
			Annotations: []yanetv1alpha1.NamedAnnotations{
				{
					Name: "checkpointer",
					Annotations: map[string]string{
						"checkpointer.ydb.tech/checkpoint":      "true",
						"checkpointer.ydb.tech/manual-recovery": "true",
					},
				},
			},
		},
	}

	nodes := v1.NodeList{}

	dep := DeploymentForControlplane(context.Background(), yanet, config, nodes)

	// Test 1: Deployment name
	expectedName := "controlplane-test-node"
	if dep.Name != expectedName {
		t.Errorf("expected name %q, got %q", expectedName, dep.Name)
	}

	// Test 2: Replicas
	if *dep.Spec.Replicas != 1 {
		t.Errorf("expected 1 replica, got %d", *dep.Spec.Replicas)
	}

	// Test 3: Container
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}

	container := dep.Spec.Template.Spec.Containers[0]
	if container.Name != "controlplane" {
		t.Errorf("expected container name 'controlplane', got %q", container.Name)
	}

	// Test 4: Image
	expectedImage := "docker.io/test/yanet-controlplane:1.0.0"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}

	// Test 5: Command and Args (из production test-case.txt строки 685-686)
	expectedCommand := []string{"/usr/bin/yanet-controlplane"}
	if len(container.Command) != 1 || container.Command[0] != expectedCommand[0] {
		t.Errorf("expected command %v, got %v", expectedCommand, container.Command)
	}

	expectedArgs := []string{"-c", "/etc/yanet/controlplane.conf"}
	if len(container.Args) != 2 || container.Args[0] != "-c" || container.Args[1] != "/etc/yanet/controlplane.conf" {
		t.Errorf("expected args %v, got %v", expectedArgs, container.Args)
	}

	// Test 6: HostIPC (из production test-case.txt строка 725)
	if !dep.Spec.Template.Spec.HostIPC {
		t.Error("expected HostIPC to be true")
	}

	// Test 7: Privileged (из production test-case.txt строка 712)
	if container.SecurityContext == nil {
		t.Fatal("expected SecurityContext to be set")
	}
	if *container.SecurityContext.Privileged {
		t.Error("expected Privileged to be false for controlplane")
	}

	// Test 8: Resources (из production test-case.txt строки 697-703)
	cpuLimit := container.Resources.Limits["cpu"]
	expectedCPU := resource.MustParse("6")
	if !cpuLimit.Equal(expectedCPU) {
		t.Errorf("expected CPU limit %v, got %v", expectedCPU, cpuLimit)
	}

	memLimit := container.Resources.Limits["memory"]
	expectedMem := resource.MustParse("128Gi")
	if !memLimit.Equal(expectedMem) {
		t.Errorf("expected memory limit %v, got %v", expectedMem, memLimit)
	}

	// Test 9: InitContainer wait-dataplane (из production test-case.txt строки 728-745)
	if len(dep.Spec.Template.Spec.InitContainers) < 1 {
		t.Fatal("expected at least 1 init container (wait-dataplane)")
	}

	initContainer := dep.Spec.Template.Spec.InitContainers[0]
	if initContainer.Name != "wait-dataplane" {
		t.Errorf("expected init container name 'wait-dataplane', got %q", initContainer.Name)
	}

	// Проверяем, что init container использует тот же image что и controlplane
	expectedInitImage := "docker.io/test/yanet-controlplane:1.0.0"
	if initContainer.Image != expectedInitImage {
		t.Errorf("expected init container image %q, got %q", expectedInitImage, initContainer.Image)
	}

	// Test 10: Volumes (из production test-case.txt строки 759-775)
	// Controlplane должен иметь 4 volume: etc-yanet, run-yanet, run-bird, spool-yanet-agent
	expectedVolumes := []string{"etc-yanet", "run-yanet", "run-bird", "spool-yanet-agent"}
	if len(dep.Spec.Template.Spec.Volumes) != len(expectedVolumes) {
		t.Errorf("expected %d volumes, got %d", len(expectedVolumes), len(dep.Spec.Template.Spec.Volumes))
	}

	volumeNames := make(map[string]bool)
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		volumeNames[vol.Name] = true
	}

	for _, expectedVol := range expectedVolumes {
		if !volumeNames[expectedVol] {
			t.Errorf("expected volume %q not found", expectedVol)
		}
	}

	// Test 11: VolumeMounts (из production test-case.txt строки 715-723)
	expectedMounts := 4
	if len(container.VolumeMounts) != expectedMounts {
		t.Errorf("expected %d volume mounts, got %d", expectedMounts, len(container.VolumeMounts))
	}

	// Test 12: NodeSelector (из production test-case.txt строка 747)
	if dep.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "test-node" {
		t.Error("expected node selector to match node name")
	}

	// Test 13: Annotations (из production test-case.txt строки 671-673)
	annotations := dep.Spec.Template.ObjectMeta.Annotations
	if annotations["checkpointer.ydb.tech/checkpoint"] != "true" {
		t.Error("expected checkpointer annotation to be set")
	}
	if annotations["checkpointer.ydb.tech/manual-recovery"] != "true" {
		t.Error("expected manual-recovery annotation to be set")
	}
}

// TestDeploymentForControlplane_Disabled verifies deployment with replicas=0 when Enable=false
func TestDeploymentForControlplane_Disabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Controlplane: yanetv1alpha1.Dep{
				Enable: false, // Disabled
				Image:  "yanet-controlplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForControlplane(context.Background(), yanet, config, nodes)

	if *dep.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas for disabled deployment, got %d", *dep.Spec.Replicas)
	}
}

// TestDeploymentForControlplane_CustomTag verifies custom tag overrides global tag
func TestDeploymentForControlplane_CustomTag(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Tag:      "1.0.0",
			Controlplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-controlplane",
				Tag:    "2.0.0", // Custom tag overrides global tag
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForControlplane(context.Background(), yanet, config, nodes)

	container := dep.Spec.Template.Spec.Containers[0]
	expectedImage := "yanet-controlplane:2.0.0"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}

	// Также проверяем, что init container использует тот же custom tag
	initContainer := dep.Spec.Template.Spec.InitContainers[0]
	// Init container использует image из yanet.Spec.Controlplane, но с global tag
	// (см. controlplane.go строки 16-19)
	expectedInitImage := "yanet-controlplane:1.0.0"
	if initContainer.Image != expectedInitImage {
		t.Errorf("expected init container image %q, got %q", expectedInitImage, initContainer.Image)
	}
}

// TestDeploymentForControlplane_InitContainerLogic verifies wait-dataplane init container logic
func TestDeploymentForControlplane_InitContainerLogic(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Type:     "release",
			Controlplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-controlplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForControlplane(context.Background(), yanet, config, nodes)

	initContainer := dep.Spec.Template.Spec.InitContainers[0]

	// Test: Command should be /bin/sh
	if len(initContainer.Command) != 1 || initContainer.Command[0] != "/bin/sh" {
		t.Errorf("expected command [/bin/sh], got %v", initContainer.Command)
	}

	// Test: Args should contain logic to wait for dataplane.sock
	if len(initContainer.Args) < 2 {
		t.Fatal("expected at least 2 args (-c and script)")
	}

	if initContainer.Args[0] != "-c" {
		t.Errorf("expected first arg '-c', got %q", initContainer.Args[0])
	}

	script := initContainer.Args[1]
	if !contains(script, "dataplane.sock") {
		t.Error("expected init container script to wait for dataplane.sock")
	}

	if !contains(script, "/run/yanet/dataplane.sock") {
		t.Error("expected init container script to check /run/yanet/dataplane.sock")
	}

	// Test: VolumeMounts should include run-yanet
	hasRunYanet := false
	for _, mount := range initContainer.VolumeMounts {
		if mount.Name == "run-yanet" && mount.MountPath == "/run/yanet" {
			hasRunYanet = true
			break
		}
	}
	if !hasRunYanet {
		t.Error("expected init container to have run-yanet volume mount")
	}
}

// Helper function to check substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
