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
	"reflect"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGetVolumes verifies volume generation for different path types
func TestGetVolumes(t *testing.T) {
	tests := []struct {
		name          string
		paths         []string
		expectedCount int
		checkVolume   func([]v1.Volume) error
	}{
		{
			name:          "empty paths",
			paths:         []string{},
			expectedCount: 0,
		},
		{
			name:          "single hostpath",
			paths:         []string{"/etc/yanet"},
			expectedCount: 1,
			checkVolume: func(volumes []v1.Volume) error {
				if volumes[0].Name != "etc-yanet" {
					t.Errorf("expected volume name 'etc-yanet', got %q", volumes[0].Name)
				}
				if volumes[0].VolumeSource.HostPath == nil {
					t.Error("expected HostPath volume source")
				}
				if volumes[0].VolumeSource.HostPath.Path != "/etc/yanet" {
					t.Errorf("expected path '/etc/yanet', got %q", volumes[0].VolumeSource.HostPath.Path)
				}
				return nil
			},
		},
		{
			name:          "hugepages volume",
			paths:         []string{"/dev/hugepages"},
			expectedCount: 1,
			checkVolume: func(volumes []v1.Volume) error {
				if volumes[0].Name != "hugepage" {
					t.Errorf("expected volume name 'hugepage', got %q", volumes[0].Name)
				}
				if volumes[0].VolumeSource.EmptyDir == nil {
					t.Fatal("expected EmptyDir volume source for hugepages")
				}
				if volumes[0].VolumeSource.EmptyDir.Medium != v1.StorageMediumHugePages {
					t.Error("expected HugePages medium")
				}
				return nil
			},
		},
		{
			name:          "multiple volumes",
			paths:         []string{"/etc/yanet", "/run/yanet", "/run/bird"},
			expectedCount: 3,
		},
		{
			name:          "mixed hugepages and hostpath",
			paths:         []string{"/dev/hugepages", "/etc/yanet", "/run/yanet"},
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumes := GetVolumes(tt.paths)

			if len(volumes) != tt.expectedCount {
				t.Errorf("expected %d volumes, got %d", tt.expectedCount, len(volumes))
			}

			if tt.checkVolume != nil {
				tt.checkVolume(volumes)
			}
		})
	}
}

// TestLabelsForYanet verifies label generation
func TestLabelsForYanet(t *testing.T) {
	yanet := &yanetv1alpha1.Yanet{
		Spec: yanetv1alpha1.YanetSpec{
			NodeName: "test-node",
		},
	}

	// Test: Basic labels without additions
	labels := LabelsForYanet(nil, yanet, "dataplane")

	expectedLabels := map[string]string{
		"app":                          "dataplane",
		"app.kubernetes.io/name":       "dataplane",
		"app.kubernetes.io/created-by": "yanet-operator",
		"topology-location-host":       "test-node",
	}

	if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("expected labels %v, got %v", expectedLabels, labels)
	}

	// Test: Labels with additions
	additions := map[string]string{
		"custom-label": "custom-value",
		"app":          "should-be-overridden", // This will override base label
	}

	labelsWithAdditions := LabelsForYanet(additions, yanet, "controlplane")

	if labelsWithAdditions["custom-label"] != "custom-value" {
		t.Error("expected custom-label to be added")
	}

	// maps.Copy overwrites existing keys
	if labelsWithAdditions["app"] != "should-be-overridden" {
		t.Error("expected app label to be overridden by additions")
	}
}

// TestAnnotationsForYanet verifies annotation filtering and merging
func TestAnnotationsForYanet(t *testing.T) {
	annotations := []yanetv1alpha1.NamedAnnotations{
		{
			Name: "checkpointer",
			Annotations: map[string]string{
				"checkpointer.ydb.tech/checkpoint": "true",
			},
		},
		{
			Name: "telegraf",
			Annotations: map[string]string{
				"telegraf.influxdata.com/ports": "8080",
			},
		},
	}

	tests := []struct {
		name     string
		names    []string
		expected map[string]string
	}{
		{
			name:     "no annotations selected",
			names:    []string{},
			expected: nil,
		},
		{
			name:  "single annotation",
			names: []string{"checkpointer"},
			expected: map[string]string{
				"checkpointer.ydb.tech/checkpoint": "true",
			},
		},
		{
			name:  "multiple annotations",
			names: []string{"checkpointer", "telegraf"},
			expected: map[string]string{
				"checkpointer.ydb.tech/checkpoint": "true",
				"telegraf.influxdata.com/ports":    "8080",
			},
		},
		{
			name:     "non-existent annotation",
			names:    []string{"non-existent"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnnotationsForYanet(annotations, tt.names)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestTolerationsForYanet verifies toleration generation
func TestTolerationsForYanet(t *testing.T) {
	tolerations := TolerationsForYanet()

	// Test: Should have 3 tolerations
	if len(tolerations) != 3 {
		t.Errorf("expected 3 tolerations, got %d", len(tolerations))
	}

	// Test: CriticalAddonsOnly toleration
	hasCriticalAddons := false
	for _, tol := range tolerations {
		if tol.Key == "CriticalAddonsOnly" && tol.Effect == v1.TaintEffectNoSchedule {
			hasCriticalAddons = true
			break
		}
	}
	if !hasCriticalAddons {
		t.Error("expected CriticalAddonsOnly toleration")
	}

	// Test: NoSchedule toleration with Exists operator
	hasNoScheduleExists := false
	for _, tol := range tolerations {
		if tol.Operator == v1.TolerationOpExists && tol.Effect == v1.TaintEffectNoSchedule {
			hasNoScheduleExists = true
			break
		}
	}
	if !hasNoScheduleExists {
		t.Error("expected NoSchedule toleration with Exists operator")
	}

	// Test: NoExecute toleration with Exists operator
	hasNoExecuteExists := false
	for _, tol := range tolerations {
		if tol.Operator == v1.TolerationOpExists && tol.Effect == v1.TaintEffectNoExecute {
			hasNoExecuteExists = true
			break
		}
	}
	if !hasNoExecuteExists {
		t.Error("expected NoExecute toleration with Exists operator")
	}
}

// TestGetResources verifies resource generation with hugepages
func TestGetResources(t *testing.T) {
	ctx := context.Background()

	// Test: Without hugepages
	resources := v1.ResourceRequirements{
		Limits: v1.ResourceList{
			"cpu": resource.MustParse("4"),
		},
	}

	result := GetResources(ctx, "test-node", resources, v1.NodeList{}, false)

	if !reflect.DeepEqual(result, resources) {
		t.Error("expected resources to be unchanged when hugepages disabled")
	}

	// Test: With hugepages
	nodes := v1.NodeList{
		Items: []v1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: v1.NodeStatus{
					Capacity: v1.ResourceList{
						"hugepages-1Gi": resource.MustParse("16Gi"),
					},
				},
			},
		},
	}

	resourcesWithHugepages := GetResources(ctx, "test-node", resources, nodes, true)

	// Should add hugepages limit from node capacity
	hugepages := resourcesWithHugepages.Limits["hugepages-1Gi"]
	expectedHugepages := resource.MustParse("16Gi")
	if !hugepages.Equal(expectedHugepages) {
		t.Errorf("expected hugepages %v, got %v", expectedHugepages, hugepages)
	}

	// Should add default memory limit
	memory := resourcesWithHugepages.Limits["memory"]
	expectedMemory := resource.MustParse("8Gi")
	if !memory.Equal(expectedMemory) {
		t.Errorf("expected memory %v, got %v", expectedMemory, memory)
	}

	// Original CPU limit should be preserved
	cpu := resourcesWithHugepages.Limits["cpu"]
	expectedCPU := resource.MustParse("4")
	if !cpu.Equal(expectedCPU) {
		t.Errorf("expected CPU %v, got %v", expectedCPU, cpu)
	}
}

// TestGetPostStartExec verifies poststart exec command generation
func TestGetPostStartExec(t *testing.T) {
	execs := []yanetv1alpha1.NamedLifecycleHandler{
		{
			Name: "reloader",
			Exec: "sleep 60; /usr/bin/yanet-cli reload",
		},
		{
			Name: "netconfig",
			Exec: "ip link add ...",
		},
	}

	tests := []struct {
		name     string
		names    yanetv1alpha1.LifecycleHandler
		expected []string
	}{
		{
			name:  "no execs selected",
			names: yanetv1alpha1.LifecycleHandler{Exec: []string{}},
			expected: []string{
				"/bin/bash",
				"-c",
				"echo starting...",
			},
		},
		{
			name:  "single exec",
			names: yanetv1alpha1.LifecycleHandler{Exec: []string{"reloader"}},
			expected: []string{
				"/bin/bash",
				"-c",
				"echo starting...;sleep 60; /usr/bin/yanet-cli reload",
			},
		},
		{
			name:  "multiple execs",
			names: yanetv1alpha1.LifecycleHandler{Exec: []string{"reloader", "netconfig"}},
			expected: []string{
				"/bin/bash",
				"-c",
				"echo starting...;sleep 60; /usr/bin/yanet-cli reload;ip link add ...",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPostStartExec(execs, tt.names)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestGetAdditionalInitContainers verifies init container filtering
func TestGetAdditionalInitContainers(t *testing.T) {
	initContainers := []v1.Container{
		{
			Name:  "init1",
			Image: "image1",
		},
		{
			Name:  "init2",
			Image: "image2",
		},
		{
			Name:  "init3",
			Image: "image3",
		},
	}

	tests := []struct {
		name          string
		names         []string
		expectedCount int
		expectedNames []string
	}{
		{
			name:          "no init containers selected",
			names:         []string{},
			expectedCount: 0,
		},
		{
			name:          "single init container",
			names:         []string{"init1"},
			expectedCount: 1,
			expectedNames: []string{"init1"},
		},
		{
			name:          "multiple init containers",
			names:         []string{"init1", "init3"},
			expectedCount: 2,
			expectedNames: []string{"init1", "init3"},
		},
		{
			name:          "non-existent init container",
			names:         []string{"non-existent"},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetAdditionalInitContainers(initContainers, tt.names)

			if len(result) != tt.expectedCount {
				t.Errorf("expected %d init containers, got %d", tt.expectedCount, len(result))
			}

			if tt.expectedNames != nil {
				for i, name := range tt.expectedNames {
					if result[i].Name != name {
						t.Errorf("expected init container name %q at index %d, got %q", name, i, result[i].Name)
					}
				}
			}
		})
	}
}
