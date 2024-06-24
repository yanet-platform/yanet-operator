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

import v1 "k8s.io/api/core/v1"

// AdditionalOpts contains a struct of additional options (initContainers, Labels, etc) that MAY be applied to Deployments
type AdditionalOpts struct {
	Controlplane OptsSpec `json:"controlplane,omitempty"`
	Dataplain    OptsSpec `json:"dataplane,omitempty"`
	Bird         OptsSpec `json:"bird,omitempty"`
	Announcer    OptsSpec `json:"announcer,omitempty"`
}

// AdditionalOpts contains a struct of additional options (initContainers, Labels, etc) that SHOULD be applied to Deployments
type EnabledOpts struct {
	FireWall DepOpts `json:"firewall,omitempty"`
	Release  DepOpts `json:"release,omitempty"`
	Balancer DepOpts `json:"balancer,omitempty"`
}
type OptsSpec struct {
	InitContainers []v1.Container `json:"initcontainers,omitempty"`
}

type OptsNames struct {
	InitContainers []string `json:"initcontainers,omitempty"`
}

// AdditionalOpts contains a struct of additional options (initContainers, Labels, etc) that SHOULD be applied to Deployments
type DepOpts struct {
	Controlplane OptsNames `json:"controlplane,omitempty"`
	Dataplain    OptsNames `json:"dataplane,omitempty"`
	Bird         OptsNames `json:"bird,omitempty"`
	Announcer    OptsNames `json:"announcer,omitempty"`
}
