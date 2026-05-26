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

func TestDeploymentForDataplane(t *testing.T) {
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
			Dataplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-dataplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{
		EnabledOpts: yanetv1alpha1.EnabledOpts{
			Release: yanetv1alpha1.DepOpts{
				Dataplain: yanetv1alpha1.OptsNames{
					HostIpc:    true,
					Privileged: true,
					Resources: v1.ResourceRequirements{
						Limits: v1.ResourceList{
							"memory": resource.MustParse("32Gi"),
						},
					},
				},
			},
		},
	}

	nodes := v1.NodeList{
		Items: []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: v1.NodeStatus{
					Capacity: v1.ResourceList{
						"hugepages-1Gi": resource.MustParse("8Gi"),
					},
				},
			},
		},
	}

	dep := DeploymentForDataplane(context.Background(), yanet, config, nodes)

	// Test: Deployment name
	expectedName := "dataplane-test-node"
	if dep.Name != expectedName {
		t.Errorf("expected name %q, got %q", expectedName, dep.Name)
	}

	// Test: Replicas
	if *dep.Spec.Replicas != 1 {
		t.Errorf("expected 1 replica, got %d", *dep.Spec.Replicas)
	}

	// Test: Container
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}

	container := dep.Spec.Template.Spec.Containers[0]
	if container.Name != "dataplane" {
		t.Errorf("expected container name 'dataplane', got %q", container.Name)
	}

	expectedImage := "docker.io/test/yanet-dataplane:1.0.0"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}

	// Test: HostIPC
	if !dep.Spec.Template.Spec.HostIPC {
		t.Error("expected HostIPC to be true")
	}

	// Test: Privileged
	if container.SecurityContext == nil || !*container.SecurityContext.Privileged {
		t.Error("expected Privileged to be true")
	}

	// Test: Hugepages
	hugepages := container.Resources.Limits["hugepages-1Gi"]
	expectedHugepages := resource.MustParse("8Gi")
	if !hugepages.Equal(expectedHugepages) {
		t.Errorf("expected hugepages %v, got %v", expectedHugepages, hugepages)
	}

	// Test: Node selector
	if dep.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "test-node" {
		t.Error("expected node selector to match node name")
	}
}

func TestDeploymentForDataplane_Disabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Dataplane: yanetv1alpha1.Dep{
				Enable: false, // Disabled
				Image:  "yanet-dataplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForDataplane(context.Background(), yanet, config, nodes)

	if *dep.Spec.Replicas != 0 {
		t.Errorf("expected 0 replicas for disabled deployment, got %d", *dep.Spec.Replicas)
	}
}

func TestDeploymentForDataplane_CustomTag(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Tag:      "1.0.0",
			Dataplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-dataplane",
				Tag:    "2.0.0", // Custom tag overrides global tag
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForDataplane(context.Background(), yanet, config, nodes)

	container := dep.Spec.Template.Spec.Containers[0]
	expectedImage := "yanet-dataplane:2.0.0"
	if container.Image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, container.Image)
	}
}

func TestDeploymentForDataplane_IntelEnabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-yanet",
			Namespace: "default",
		},
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Tag:      "1.0.0",
			Intel:    true,
			Dataplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-dataplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForDataplane(context.Background(), yanet, config, nodes)

	// Check that ice-ddp VolumeMount is present
	container := dep.Spec.Template.Spec.Containers[0]
	foundMount := false
	for _, vm := range container.VolumeMounts {
		if vm.Name == intelIceDDPVolumeName && vm.MountPath == intelIceDDPHostPath {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Errorf("expected VolumeMount %q at %q, not found in %v", intelIceDDPVolumeName, intelIceDDPHostPath, container.VolumeMounts)
	}

	// Check that ice-ddp Volume (HostPath Directory) is present
	foundVolume := false
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name != intelIceDDPVolumeName {
			continue
		}
		if vol.HostPath == nil {
			t.Errorf("expected HostPath volume for %q, but HostPath is nil", intelIceDDPVolumeName)
			break
		}
		if vol.HostPath.Path != intelIceDDPHostPath {
			t.Errorf("expected HostPath.Path %q, got %q", intelIceDDPHostPath, vol.HostPath.Path)
		}
		if vol.HostPath.Type == nil || *vol.HostPath.Type != v1.HostPathDirectory {
			t.Errorf("expected HostPath.Type Directory, got %v", vol.HostPath.Type)
		}
		foundVolume = true
		break
	}
	if !foundVolume {
		t.Errorf("expected Volume %q not found in %v", intelIceDDPVolumeName, dep.Spec.Template.Spec.Volumes)
	}
}

func TestDeploymentForDataplane_IntelDisabled(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
			Tag:      "1.0.0",
			Intel:    false,
			Dataplane: yanetv1alpha1.Dep{
				Enable: true,
				Image:  "yanet-dataplane",
			},
		},
	}

	config := yanetv1alpha1.YanetConfigSpec{}
	nodes := v1.NodeList{}

	dep := DeploymentForDataplane(context.Background(), yanet, config, nodes)

	// Ensure ice-ddp VolumeMount is absent when Intel is false
	container := dep.Spec.Template.Spec.Containers[0]
	for _, vm := range container.VolumeMounts {
		if vm.Name == intelIceDDPVolumeName {
			t.Errorf("unexpected VolumeMount %q found when Intel=false", intelIceDDPVolumeName)
		}
	}

	// Ensure ice-ddp Volume is absent when Intel is false
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == intelIceDDPVolumeName {
			t.Errorf("unexpected Volume %q found when Intel=false", intelIceDDPVolumeName)
		}
	}
}

func TestDataplaneVolumeMounts(t *testing.T) {
	tests := []struct {
		name          string
		intel         bool
		expectIceDDP  bool
		expectedCount int
	}{
		{
			name:          "intel disabled",
			intel:         false,
			expectIceDDP:  false,
			expectedCount: 3,
		},
		{
			name:          "intel enabled",
			intel:         true,
			expectIceDDP:  true,
			expectedCount: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mounts := dataplaneVolumeMounts(tt.intel)
			if len(mounts) != tt.expectedCount {
				t.Errorf("expected %d mounts, got %d", tt.expectedCount, len(mounts))
			}
			foundIceDDP := false
			for _, m := range mounts {
				if m.Name != intelIceDDPVolumeName {
					continue
				}
				foundIceDDP = true
				if m.MountPath != intelIceDDPHostPath {
					t.Errorf("expected MountPath %q, got %q", intelIceDDPHostPath, m.MountPath)
				}
			}
			if foundIceDDP != tt.expectIceDDP {
				t.Errorf("expectIceDDP=%v but foundIceDDP=%v", tt.expectIceDDP, foundIceDDP)
			}
		})
	}
}

func TestDataplaneVolumes(t *testing.T) {
	tests := []struct {
		name         string
		intel        bool
		expectIceDDP bool
	}{
		{
			name:         "intel disabled",
			intel:        false,
			expectIceDDP: false,
		},
		{
			name:         "intel enabled",
			intel:        true,
			expectIceDDP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumes := dataplaneVolumes(tt.intel)
			foundIceDDP := false
			for _, vol := range volumes {
				if vol.Name != intelIceDDPVolumeName {
					continue
				}
				foundIceDDP = true
				if vol.HostPath == nil {
					t.Fatal("expected HostPath to be non-nil for ice-ddp volume")
				}
				if vol.HostPath.Path != intelIceDDPHostPath {
					t.Errorf("expected path %q, got %q", intelIceDDPHostPath, vol.HostPath.Path)
				}
				if vol.HostPath.Type == nil || *vol.HostPath.Type != v1.HostPathDirectory {
					t.Errorf("expected HostPathDirectory type, got %v", vol.HostPath.Type)
				}
			}
			if foundIceDDP != tt.expectIceDDP {
				t.Errorf("expectIceDDP=%v but foundIceDDP=%v", tt.expectIceDDP, foundIceDDP)
			}
		})
	}
}
