/*
Copyright 2023 YANDEX LLC.

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

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// YanetSpec defines the desired state of Yanet
type YanetSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// (Optional) Global docker registry.
	Registry string `json:"registry,omitempty"`
	// (Optional) Tag for dataplane/controlplane/anouncer/bird images.
	// Default: latest
	// +kubebuilder:default=latest
	Tag string `json:"tag,omitempty"`
	// Worker node name for deploy.
	// Only one Yanet on node!
	// Do not use regex!
	NodeName string `json:"nodename,omitempty"`
	// (Optional) Type of dataplane(release or balancer).
	// Default: release
	// +kubebuilder:default=release
	Type string `json:"type,omitempty"`
	// (Optional) Operator enable autosync for this node.
	// Default: false
	// +kubebuilder:default=false
	AutoSync bool `json:"autosync,omitempty"`
	// (Optional) base configs for announcer deployment.
	Announcer Dep `json:"announcer,omitempty"`
	// (Optional) base configs for contorlplane deployment.
	Controlplane Dep `json:"controlplane,omitempty"`
	// (Optional) base configs for dataplane deployment.
	Dataplane Dep `json:"dataplane,omitempty"`
	// (Optional) base configs for bird deployment.
	Bird Dep `json:"bird,omitempty"`
	// (Optional) oneshot host prepare job.
	PrepareJob Dep `json:"preparejob,omitempty"`
	// (Optional) Allow reboot on prepare stage.
	// Default: false
	// +kubebuilder:default=false
	AllowReboot bool `json:"allowreboot,omitempty"`
}

// Deployment base configs.
type Dep struct {
	// (Optional) replicas for this deployment. One with true options and zero with false.
	// You can make deployment with zero replicas with this option.
	// Default: true
	// +kubebuilder:default=true
	Enable bool `json:"enable,omitempty"`
	// image name.
	Image string `json:"image,omitempty"`
	// (Optional) image tag.
	Tag string `json:"tag,omitempty"`
}

// YanetStatus defines the observed state of Yanet.
type YanetStatus struct {
	// Resulting pods by status.
	Pods map[v1.PodPhase][]string `json:"pods"`
	Sync Sync                     `json:"sync"`
}

// Sync defines sync state of Yanet objects.
type Sync struct {
	Synced      []string `json:"synced"`
	OutOfSync   []string `json:"outofsync"`
	SyncWaiting []string `json:"syncwaiting"`
	Error       []string `json:"error"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Yanet is the Schema for the yanets API
type Yanet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   YanetSpec   `json:"spec,omitempty"`
	Status YanetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// YanetList contains a list of Yanet
type YanetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Yanet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Yanet{}, &YanetList{})
}
