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
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// YanetConfigSpec defines the desired state of YanetConfig
type YanetConfigSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// (Optional) Stop means global stop. Do nothing in main reconcile loop.
	// When turned off, the operator may skip some events.
	// When turned on, we recommend restarting.
	// Default: false
	// +kubebuilder:default=false
	Stop bool `json:"stop,omitempty"`
	// (Optional) AutoDiscovery configure new worker node initializer.
	AutoDiscovery AutoDiscovery `json:"autodiscovery,omitempty"`
}

// YanetConfigSpec with mutex for controllers.
// +kubebuilder:object:generate=false
type MutexYanetConfigSpec struct {
	Config YanetConfigSpec `json:"config,omitempty"`
	Lock   sync.Mutex      `json:"-"`
}

// TODO: add NodeSelector/whiteList/blackList for AutoDiscovery
// AutoDiscovery struct configure new worker node initializer.
type AutoDiscovery struct {
	// (Optional) Enable or disable discovery.
	// Default: false
	// +kubebuilder:default=false
	Enable bool `json:"enable,omitempty"`
	// (Optional) Uri for node type resolver.
	// This uri get node type by node name.
	// Example: `curl localhost:80/type/test.yanet-platform.io` -> release
	// Node will be ignored if the return values is "none".
	TypeUri string `json:"typeuri,omitempty"`
	// (Optional) Namespace for new autogenerated Yanet object.
	// Default: default
	// +kubebuilder:default=default
	Namespace string `json:"namespace,omitempty"`
	// (Optional) registry for new autogenerated Yanet object.
	// Default: dockerhub.io
	// +kubebuilder:default=dockerhub.io
	Registry string `json:"registry,omitempty"`
	// (Optional) Uri for yanet version resolver.
	// This uri get yanet version(aka docker tag) by node name.
	// Example: `curl localhost:80/version/yanet/test.yanet-platform.io` -> 51.2
	VersionUri string `json:"versionuri,omitempty"`
	// (Optional) Uri for arc resolver.
	// This uri get arch by node name.
	// Example: `curl localhost:80/arch/test.yanet-platform.io` -> corei7
	ArchUri string `json:"archuri,omitempty"`
	// (Optional) Uri for configs version resolver.
	// This uri get configs version by yanet version.
	// e.g. revision of git repo
	// Example: `curl localhost:80/configs/51.2` -> 13162235
	ConfigsUri string `json:"configsuri,omitempty"`
	// Images name for deployments.
	Images Images `json:"images,omitempty"`
}

// Images defines images for deployments
type Images struct {
	// (Optional) Dataplane image name.
	// Default: dataplane
	// +kubebuilder:default=dataplane
	Dataplane string `json:"dataplane,omitempty"`
	// (Optional) Controlplane image name.
	// Default: controlplane
	// +kubebuilder:default=controlplane
	Controlplane string `json:"controlplane,omitempty"`
	// (Optional) Announcer image name.
	// Default: announcer
	// +kubebuilder:default=announcer
	Announcer string `json:"announcer,omitempty"`
	// (Optional) Bird image name.
	// Default: bird
	// +kubebuilder:default=bird
	Bird string `json:"bird,omitempty"`
}

// YanetConfigStatus defines the observed state of YanetConfig
type YanetConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// YanetConfig is the Schema for the yanetconfigs API
type YanetConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   YanetConfigSpec   `json:"spec,omitempty"`
	Status YanetConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// YanetConfigList contains a list of YanetConfig
type YanetConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []YanetConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&YanetConfig{}, &YanetConfigList{})
}
