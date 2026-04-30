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

// TestDeploymentForAnnouncer verifies announcer deployment generation
func TestDeploymentForAnnouncer(t *testing.T) {
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
			Announcer: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-announcer",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{
		EnabledOpts: yanetv1alpha1.EnabledOpts{
			Release: yanetv1alpha1.DepOpts{
				Announcer: yanetv1alpha1.OptsNames{
					Annotations: []string{"checkpointer"},
					Privileged:  false,
					Resources: v1.ResourceRequirements{
						Limits: v1.ResourceList{
							"cpu":    resource.MustParse("4"),
							"memory": resource.MustParse("32Gi"),
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

	dep := DeploymentForAnnouncer(context.Background(), yanet, config, nodes)

	// Test 1: Deployment name
	expectedName := "announcer-test-node"
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
	if container.Name != "announcer" {
		t.Errorf("expected container name 'announcer', got %q", container.Name)
	}

	// Test 4: Image
	expectedImage := "docker.io/test/yanet-announcer:1.0.0"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}

	// Test 5: Command and Args (из production test-case.txt строки 388-389)
	expectedCommand := []string{"/usr/bin/yanet-announcer"}
	if len(container.Command) != 1 || container.Command[0] != expectedCommand[0] {
		t.Errorf("expected command %v, got %v", expectedCommand, container.Command)
	}

	expectedArgs := []string{"--run"}
	if len(container.Args) != 1 || container.Args[0] != "--run" {
		t.Errorf("expected args %v, got %v", expectedArgs, container.Args)
	}

	// Test 6: Privileged (из production test-case.txt строка 415)
	if container.SecurityContext == nil {
		t.Fatal("expected SecurityContext to be set")
	}
	if *container.SecurityContext.Privileged {
		t.Error("expected Privileged to be false for announcer")
	}

	// Test 7: Resources (из production test-case.txt строки 400-406)
	cpuLimit := container.Resources.Limits["cpu"]
	expectedCPU := resource.MustParse("4")
	if !cpuLimit.Equal(expectedCPU) {
		t.Errorf("expected CPU limit %v, got %v", expectedCPU, cpuLimit)
	}

	memLimit := container.Resources.Limits["memory"]
	expectedMem := resource.MustParse("32Gi")
	if !memLimit.Equal(expectedMem) {
		t.Errorf("expected memory limit %v, got %v", expectedMem, memLimit)
	}

	// Test 8: Annotations (из production test-case.txt строки 375-377)
	annotations := dep.Spec.Template.ObjectMeta.Annotations
	if annotations["checkpointer.ydb.tech/checkpoint"] != "true" {
		t.Error("expected checkpointer annotation to be set")
	}

	// Test 9: Volumes (из production test-case.txt строки 456-468)
	// Announcer должен иметь 3 volume: etc-yanet, run-yanet, run-bird
	expectedVolumes := []string{"etc-yanet", "run-yanet", "run-bird"}
	if len(dep.Spec.Template.Spec.Volumes) != len(expectedVolumes) {
		t.Errorf("expected %d volumes, got %d", len(expectedVolumes), len(dep.Spec.Template.Spec.Volumes))
	}

	// Test 10: VolumeMounts (из production test-case.txt строки 418-424)
	expectedMounts := 3
	if len(container.VolumeMounts) != expectedMounts {
		t.Errorf("expected %d volume mounts, got %d", expectedMounts, len(container.VolumeMounts))
	}
}

// TestDeploymentForAnnouncer_InitContainer verifies wait-bird init container
// Announcer depends on bird and must wait for bird.ctl
func TestDeploymentForAnnouncer_InitContainer(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Type:     "release",
			Announcer: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-announcer",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForAnnouncer(context.Background(), yanet, config, nodes)

	// Test: Must have at least 1 init container (wait-bird)
	if len(dep.Spec.Template.Spec.InitContainers) < 1 {
		t.Fatal("expected at least 1 init container (wait-bird)")
	}

	initContainer := dep.Spec.Template.Spec.InitContainers[0]

	// Test: Init container name
	if initContainer.Name != "wait-bird" {
		t.Errorf("expected init container name 'wait-bird', got %q", initContainer.Name)
	}

	// Test: Image should be busybox
	if initContainer.Image != "busybox" {
		t.Errorf("expected init container image 'busybox', got %q", initContainer.Image)
	}

	// Test: Command should be /bin/sh
	if len(initContainer.Command) != 1 || initContainer.Command[0] != "/bin/sh" {
		t.Errorf("expected command [/bin/sh], got %v", initContainer.Command)
	}

	// Test: Args should contain logic to wait for bird.ctl
	if len(initContainer.Args) < 2 {
		t.Fatal("expected at least 2 args (-c and script)")
	}

	script := initContainer.Args[1]
	if !contains(script, "bird.ctl") {
		t.Error("expected init container script to wait for bird.ctl")
	}

	if !contains(script, "/run/bird/bird.ctl") {
		t.Error("expected init container script to check /run/bird/bird.ctl")
	}

	// Test: VolumeMounts should include run-bird
	hasRunBird := false
	for _, mount := range initContainer.VolumeMounts {
		if mount.Name == "run-bird" && mount.MountPath == "/run/bird" {
			hasRunBird = true
			break
		}
	}
	if !hasRunBird {
		t.Error("expected init container to have run-bird volume mount")
	}
}

// TestDeploymentForAnnouncer_Disabled verifies deployment with replicas=0 when Enable=false
func TestDeploymentForAnnouncer_Disabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Announcer: yanetv1alpha1.Dep{
				Enable: false, // Disabled
				Image:  "yanet-announcer",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForAnnouncer(context.Background(), yanet, config, nodes)

	if *dep.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas for disabled deployment, got %d", *dep.Spec.Replicas)
	}
}
