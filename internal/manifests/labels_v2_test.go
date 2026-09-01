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
