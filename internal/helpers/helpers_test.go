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

package helpers

import (
	"context"
	"reflect"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetPods(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		pods     []v1.Pod
		expected map[v1.PodPhase][]string
	}{
		{
			name:     "empty pods",
			pods:     []v1.Pod{},
			expected: map[v1.PodPhase][]string{},
		},
		{
			name: "single running pod",
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1"},
					Status:     v1.PodStatus{Phase: v1.PodRunning},
				},
			},
			expected: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1"},
			},
		},
		{
			name: "multiple pods with different phases",
			pods: []v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod1"},
					Status:     v1.PodStatus{Phase: v1.PodRunning},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod2"},
					Status:     v1.PodStatus{Phase: v1.PodPending},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod3"},
					Status:     v1.PodStatus{Phase: v1.PodRunning},
				},
			},
			expected: map[v1.PodPhase][]string{
				v1.PodRunning: {"pod1", "pod3"},
				v1.PodPending: {"pod2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPods(ctx, tt.pods)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("GetPods() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestUniqueSliceElements(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "no duplicates",
			input:    []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "with duplicates",
			input:    []string{"a", "b", "a", "c", "b"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "empty slice",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "all same",
			input:    []string{"a", "a", "a"},
			expected: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UniqueSliceElements(tt.input)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("UniqueSliceElements() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetNodeNames(t *testing.T) {
	tests := []struct {
		name     string
		nodeList *v1.NodeList
		expected []string
	}{
		{
			name:     "empty node list",
			nodeList: &v1.NodeList{},
			expected: nil, // GetNodeNames returns nil for empty list, not []string{}
		},
		{
			name: "single node",
			nodeList: &v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				},
			},
			expected: []string{"node1"},
		},
		{
			name: "multiple nodes",
			nodeList: &v1.NodeList{
				Items: []v1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
				},
			},
			expected: []string{"node1", "node2", "node3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetNodeNames(tt.nodeList)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("GetNodeNames() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetTypeOpts(t *testing.T) {
	releaseOpts := yanetv1alpha1.DepOpts{
		Dataplain: yanetv1alpha1.OptsNames{
			Privileged: true,
		},
	}
	balancerOpts := yanetv1alpha1.DepOpts{
		Dataplain: yanetv1alpha1.OptsNames{
			Privileged: false,
		},
	}

	opts := yanetv1alpha1.EnabledOpts{
		Release:  releaseOpts,
		Balancer: balancerOpts,
	}

	tests := []struct {
		name        string
		typeStr     string
		expectedOk  bool
		expectedOpt yanetv1alpha1.DepOpts
	}{
		{
			name:        "release type",
			typeStr:     "release",
			expectedOk:  true,
			expectedOpt: releaseOpts,
		},
		{
			name:        "balancer type",
			typeStr:     "balancer",
			expectedOk:  true,
			expectedOpt: balancerOpts,
		},
		{
			name:        "unknown type",
			typeStr:     "unknown",
			expectedOk:  false,
			expectedOpt: yanetv1alpha1.DepOpts{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, result := GetTypeOpts(opts, tt.typeStr)
			if ok != tt.expectedOk {
				t.Errorf("GetTypeOpts() ok = %v, want %v", ok, tt.expectedOk)
			}
			if !reflect.DeepEqual(result, tt.expectedOpt) {
				t.Errorf("GetTypeOpts() result = %v, want %v", result, tt.expectedOpt)
			}
		})
	}
}

func TestDeploymentDiff(t *testing.T) {
	ctx := context.Background()
	replicas1 := int32(1)
	replicas2 := int32(2)

	tests := []struct {
		name     string
		first    *appsv1.Deployment
		second   *appsv1.Deployment
		expected bool
	}{
		{
			name: "identical deployments",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas1,
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: "test", Image: "test:1.0"},
							},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas1,
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: "test", Image: "test:1.0"},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "different replicas",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas1,
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas2,
				},
			},
			expected: true,
		},
		{
			name: "different container image",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: "test", Image: "test:1.0"},
							},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{Name: "test", Image: "test:2.0"},
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeploymentDiff(ctx, tt.first, tt.second)
			if result != tt.expected {
				t.Errorf("DeploymentDiff() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestGetLabeledNodes tests GetLabeledNodes function
func TestGetLabeledNodes(t *testing.T) {
	tests := []struct {
		name     string
		nodeList *v1.NodeList
		expected int
	}{
		{
			name: "empty node list",
			nodeList: &v1.NodeList{
				Items: []v1.Node{},
			},
			expected: 0,
		},
		{
			name: "nodes with labels",
			nodeList: &v1.NodeList{
				Items: []v1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:   "node1",
							Labels: map[string]string{"role": "worker"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:   "node2",
							Labels: map[string]string{"role": "master"},
						},
					},
				},
			},
			expected: 2,
		},
		{
			name: "nodes without labels",
			nodeList: &v1.NodeList{
				Items: []v1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node1",
						},
					},
				},
			},
			expected: 0,
		},
		{
			name: "mixed nodes",
			nodeList: &v1.NodeList{
				Items: []v1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:   "node1",
							Labels: map[string]string{"role": "worker"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node2",
						},
					},
				},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetLabeledNodes(tt.nodeList)
			if len(result) != tt.expected {
				t.Errorf("GetLabeledNodes() returned %d nodes, want %d", len(result), tt.expected)
			}
		})
	}
}

// TestDeploymentDiff_EdgeCases tests edge cases for DeploymentDiff
func TestDeploymentDiff_EdgeCases(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		first    *appsv1.Deployment
		second   *appsv1.Deployment
		expected bool
	}{
		{
			name: "different volumes",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Volumes: []v1.Volume{
								{Name: "vol1"},
							},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Volumes: []v1.Volume{
								{Name: "vol2"},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different tolerations",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Tolerations: []v1.Toleration{
								{Key: "key1", Operator: v1.TolerationOpExists},
							},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							Tolerations: []v1.Toleration{
								{Key: "key2", Operator: v1.TolerationOpExists},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different annotations",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{"key1": "value1"},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{"key2": "value2"},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different HostIPC",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							HostIPC: true,
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							HostIPC: false,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "different init containers",
			first: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							InitContainers: []v1.Container{
								{Name: "init1", Image: "busybox"},
							},
						},
					},
				},
			},
			second: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: v1.PodTemplateSpec{
						Spec: v1.PodSpec{
							InitContainers: []v1.Container{
								{Name: "init2", Image: "alpine"},
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeploymentDiff(ctx, tt.first, tt.second)
			if result != tt.expected {
				t.Errorf("DeploymentDiff() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestGetNodes tests GetNodes function with fake client
func TestGetNodes(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		nodes         []v1.Node
		expectedCount int
		expectError   bool
	}{
		{
			name:          "empty cluster",
			nodes:         []v1.Node{},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "single node",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "multiple nodes",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node3",
					},
				},
			},
			expectedCount: 3,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with nodes
			fakeClient := fake.NewClientBuilder().
				WithObjects(nodesToObjects(tt.nodes)...).
				Build()

			// Call GetNodes
			nodeList, err := GetNodes(ctx, fakeClient)

			// Check error
			if (err != nil) != tt.expectError {
				t.Errorf("GetNodes() error = %v, expectError %v", err, tt.expectError)
				return
			}

			// Check node count
			if len(nodeList.Items) != tt.expectedCount {
				t.Errorf("GetNodes() returned %d nodes, want %d", len(nodeList.Items), tt.expectedCount)
			}
		})
	}
}

// nodesToObjects converts []v1.Node to []client.Object for fake client
func nodesToObjects(nodes []v1.Node) []client.Object {
	objects := make([]client.Object, len(nodes))
	for i := range nodes {
		objects[i] = &nodes[i]
	}
	return objects
}
