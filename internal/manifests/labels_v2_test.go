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
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRestoreWorkloadIdentityRejectsPatchedReservedLabels(t *testing.T) {
	controller := true
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "edge-route",
			Namespace: "yanet",
			Labels: map[string]string{
				labelYanet: "edge", labelComponent: "route", labelBoxType: "release",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "yanet.yanet-platform.io/v2alpha1",
				Kind:       "YanetV2",
				Name:       "edge",
				UID:        "owner",
				Controller: &controller,
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				labelYanet: "edge", labelComponent: "route",
			}},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					labelYanet: "edge", labelComponent: "route", labelBoxType: "release",
				}},
				Spec: corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/hostname": "node-1"}},
			},
		},
	}
	identity := CaptureWorkloadIdentity(deployment)
	deployment.Name = "other"
	deployment.Namespace = "other"
	deployment.OwnerReferences = nil
	deployment.Labels[labelYanet] = "other"
	deployment.Spec.Selector.MatchLabels[labelBoxType] = "injected"
	deployment.Spec.Template.Labels[labelComponent] = "other"
	deployment.Spec.Template.Labels["foreign"] = "kept"
	deployment.Spec.Template.Spec.NodeName = "node-2"
	deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] = "node-2"
	deployment.Spec.Strategy.Type = appsv1.RollingUpdateDeploymentStrategyType

	RestoreWorkloadIdentity(deployment, identity)

	if deployment.Labels[labelYanet] != "edge" {
		t.Fatalf("Deployment identity was not restored: %v", deployment.Labels)
	}
	if deployment.Name != "edge-route" || deployment.Namespace != "yanet" ||
		len(deployment.OwnerReferences) != 1 || deployment.OwnerReferences[0].UID != "owner" {
		t.Fatalf("Deployment metadata identity was not restored: %#v", deployment.ObjectMeta)
	}
	if _, present := deployment.Spec.Selector.MatchLabels[labelBoxType]; present {
		t.Fatalf("patch-added reserved selector label was retained: %v", deployment.Spec.Selector.MatchLabels)
	}
	if deployment.Spec.Template.Labels[labelComponent] != "route" ||
		deployment.Spec.Template.Labels["foreign"] != "kept" {
		t.Fatalf("Pod labels were not restored safely: %v", deployment.Spec.Template.Labels)
	}
	if deployment.Spec.Template.Spec.NodeName != "" ||
		deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "node-1" {
		t.Fatalf("Pod placement was not restored: %#v", deployment.Spec.Template.Spec)
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("Deployment strategy was not restored: %#v", deployment.Spec.Strategy)
	}
}

func TestRestoreWorkloadIdentityRestoresNativeSidecars(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "dataplane", Tag: "v1"},
		NativeSidecars: []helpers.ResolvedContainer{
			{
				Name:  yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
				Image: helpers.ResolvedImage{Name: "netlink-dataplane-sidecar", Tag: "v1"},
			},
			{
				Name:  yanetv2alpha1.BirdSidecarContainerName,
				Image: helpers.ResolvedImage{Name: "bird", Tag: "v1"},
			},
		},
	}
	deployments, err := BuildDeployments(ctxV2(), component)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	deployment := deployments[0]
	identity := CaptureWorkloadIdentity(deployment)
	deployment.Spec.Template.Spec.InitContainers[0].RestartPolicy = nil
	deployment.Spec.Template.Spec.InitContainers[1].RestartPolicy = nil
	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{
		deployment.Spec.Template.Spec.InitContainers[0],
		{Name: "prepare-network"},
		deployment.Spec.Template.Spec.InitContainers[1],
	}

	RestoreWorkloadIdentity(deployment, identity)

	initContainers := deployment.Spec.Template.Spec.InitContainers
	if len(initContainers) != 3 ||
		initContainers[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName ||
		initContainers[1].Name != "prepare-network" ||
		initContainers[2].Name != yanetv2alpha1.BirdSidecarContainerName {
		t.Fatalf("restored init containers = %+v", initContainers)
	}
	for _, index := range []int{0, 2} {
		if initContainers[index].RestartPolicy == nil ||
			*initContainers[index].RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Fatalf("restored native sidecar restartPolicy = %v", initContainers[index].RestartPolicy)
		}
	}

	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "prepare-network"}}
	RestoreWorkloadIdentity(deployment, identity)
	initContainers = deployment.Spec.Template.Spec.InitContainers
	if len(initContainers) != 3 ||
		initContainers[0].Name != yanetv2alpha1.NetlinkDataplaneSidecarContainerName ||
		initContainers[1].Name != yanetv2alpha1.BirdSidecarContainerName ||
		initContainers[2].Name != "prepare-network" {
		t.Fatalf("restored deleted native sidecar order = %+v", initContainers)
	}
}

func TestValidatePodContainerNamesRejectsCrossListCollision(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dataplane"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: yanetv2alpha1.BirdSidecarContainerName}},
			InitContainers: []corev1.Container{{Name: yanetv2alpha1.BirdSidecarContainerName}},
		}}},
	}
	if err := ValidatePodContainerNames(deployment); err == nil {
		t.Fatal("expected duplicate regular/init container name to be rejected")
	}
}

func TestRestoreWorkloadIdentityLeavesNonDataplaneInitContainers(t *testing.T) {
	deployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelComponent: "operator"}},
				Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
					Name: yanetv2alpha1.BirdSidecarContainerName,
				}}},
			},
		},
	}
	identity := CaptureWorkloadIdentity(deployment)
	RestoreWorkloadIdentity(deployment, identity)
	if len(deployment.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("non-dataplane init container was removed: %+v", deployment.Spec.Template.Spec.InitContainers)
	}
}
