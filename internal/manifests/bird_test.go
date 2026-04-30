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

// TestDeploymentForBird verifies bird deployment generation
func TestDeploymentForBird(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-yanet",
			Namespace: "default",
		},
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Type:     "release",
			Tag:      "1.0.0",
			Registry: "docker.io/test",
			Bird: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-bird",
				Tag:    "2.0.12", // Custom tag for bird
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{
		EnabledOpts: yanetv1alpha1.EnabledOpts{
			Release: yanetv1alpha1.DepOpts{
				Bird: yanetv1alpha1.OptsNames{
					Annotations: []string{"checkpointer"},
					Privileged:  false,
					Resources: v1.ResourceRequirements{
						Limits: v1.ResourceList{
							"cpu":    resource.MustParse("6"),
							"memory": resource.MustParse("64Gi"),
						},
						Requests: v1.ResourceList{
							"cpu":    resource.MustParse("100m"),
							"memory": resource.MustParse("4Gi"),
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

	dep := DeploymentForBird(context.Background(), yanet, config, nodes)

	// Test deployment name
	expectedName := "bird-test-node"
	if dep.Name != expectedName {
		t.Errorf("expected name %q, got %q", expectedName, dep.Name)
	}

	// Test replicas
	if *dep.Spec.Replicas != 1 {
		t.Errorf("expected 1 replica, got %d", *dep.Spec.Replicas)
	}

	// Test container
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}

	container := dep.Spec.Template.Spec.Containers[0]
	if container.Name != "bird" {
		t.Errorf("expected container name 'bird', got %q", container.Name)
	}

	// Test image with custom tag
	expectedImage := "docker.io/test/yanet-bird:2.0.12"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}

	// Test command and args
	expectedCommand := []string{"/usr/sbin/bird"}
	if len(container.Command) != 1 || container.Command[0] != expectedCommand[0] {
		t.Errorf("expected command %v, got %v", expectedCommand, container.Command)
	}

	expectedArgs := []string{"-f"}
	if len(container.Args) != 1 || container.Args[0] != "-f" {
		t.Errorf("expected args %v, got %v", expectedArgs, container.Args)
	}

	// Test privileged
	if container.SecurityContext == nil {
		t.Fatal("expected SecurityContext to be set")
	}
	if *container.SecurityContext.Privileged {
		t.Error("expected Privileged to be false for bird")
	}

	// Test resources
	cpuLimit := container.Resources.Limits["cpu"]
	expectedCPU := resource.MustParse("6")
	if !cpuLimit.Equal(expectedCPU) {
		t.Errorf("expected CPU limit %v, got %v", expectedCPU, cpuLimit)
	}

	memLimit := container.Resources.Limits["memory"]
	expectedMem := resource.MustParse("64Gi")
	if !memLimit.Equal(expectedMem) {
		t.Errorf("expected memory limit %v, got %v", expectedMem, memLimit)
	}

	// Test annotations
	annotations := dep.Spec.Template.ObjectMeta.Annotations
	if annotations["checkpointer.ydb.tech/checkpoint"] != "true" {
		t.Error("expected checkpointer annotation to be set")
	}

	// Test volumes - bird requires etc-bird, run-yanet, run-bird
	expectedVolumes := []string{"etc-bird", "run-yanet", "run-bird"}
	if len(dep.Spec.Template.Spec.Volumes) != len(expectedVolumes) {
		t.Errorf("expected %d volumes, got %d", len(expectedVolumes), len(dep.Spec.Template.Spec.Volumes))
	}

	// Test volume mounts
	expectedMounts := 3
	if len(container.VolumeMounts) != expectedMounts {
		t.Errorf("expected %d volume mounts, got %d", expectedMounts, len(container.VolumeMounts))
	}
}

// TestDeploymentForBird_InitContainer verifies wait-controlplane init container
// Bird depends on controlplane and must wait for controlplane.sock
func TestDeploymentForBird_InitContainer(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Type:     "release",
			Tag:      "1.0.0",
			Registry: "docker.io/test",
			Controlplane: yanetv1alpha1.Dep{
				Image: "yanet-controlplane",
			},
			Bird: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-bird",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForBird(context.Background(), yanet, config, nodes)

	// Test: Must have at least 1 init container (wait-controlplane)
	if len(dep.Spec.Template.Spec.InitContainers) < 1 {
		t.Fatal("expected at least 1 init container (wait-controlplane)")
	}

	initContainer := dep.Spec.Template.Spec.InitContainers[0]

	// Test: Init container name
	if initContainer.Name != "wait-controlplane" {
		t.Errorf("expected init container name 'wait-controlplane', got %q", initContainer.Name)
	}

	// Test: Init container uses controlplane image for yanet-cli
	expectedInitImage := "docker.io/test/yanet-controlplane:1.0.0"
	if initContainer.Image != expectedInitImage {
		t.Errorf("expected init container image %q, got %q", expectedInitImage, initContainer.Image)
	}

	// Test: Command should be /bin/sh
	if len(initContainer.Command) != 1 || initContainer.Command[0] != "/bin/sh" {
		t.Errorf("expected command [/bin/sh], got %v", initContainer.Command)
	}

	// Test: Args should contain logic to wait for controlplane.sock
	if len(initContainer.Args) < 2 {
		t.Fatal("expected at least 2 args (-c and script)")
	}

	script := initContainer.Args[1]
	if !contains(script, "controlplane.sock") {
		t.Error("expected init container script to wait for controlplane.sock")
	}

	if !contains(script, "/run/yanet/controlplane.sock") {
		t.Error("expected init container script to check /run/yanet/controlplane.sock")
	}

	// Test: Script should use yanet-cli for verification
	if !contains(script, "yanet-cli version") {
		t.Error("expected init container script to use yanet-cli version")
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

// TestDeploymentForBird_Disabled verifies deployment with replicas=0 when Enable=false
func TestDeploymentForBird_Disabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Bird: yanetv1alpha1.Dep{
				Enable: false,
				Image:  "yanet-bird",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForBird(context.Background(), yanet, config, nodes)

	if *dep.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas for disabled deployment, got %d", *dep.Spec.Replicas)
	}
}

// TestDeploymentForBird_CustomTagOverride verifies custom tag overrides global tag
// Bird often has its own version tag different from other components
func TestDeploymentForBird_CustomTagOverride(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Tag:      "1.0.0",
			Bird: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-bird",
				Tag:    "2.0.12-7", // Custom tag
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForBird(context.Background(), yanet, config, nodes)

	container := dep.Spec.Template.Spec.Containers[0]
	expectedImage := "yanet-bird:2.0.12-7"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}
}
