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
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestValidatePodContainerNamesRejectsDisabledSidecarsInRegularContainers(t *testing.T) {
	for _, name := range []string{
		yanetv2alpha1.BirdSidecarContainerName,
		yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
	} {
		t.Run(name, func(t *testing.T) {
			component := &helpers.ResolvedComponent{
				Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
				Image: helpers.ResolvedImage{Name: "dataplane", Tag: "v2"},
			}
			deployments, err := BuildDeployments(ctxV2(), component)
			if err != nil {
				t.Fatalf("BuildDeployments: %v", err)
			}
			deployment := deployments[0]
			identity := CaptureWorkloadIdentity(deployment)
			registry := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
				patch("legacy-sidecar", fmt.Sprintf(
					`{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":"sidecar:v2"}]}}}}`, name,
				)),
			})
			if err := ApplyPatches(deployment, []string{"legacy-sidecar"}, registry); err != nil {
				t.Fatalf("ApplyPatches: %v", err)
			}
			RestoreWorkloadIdentity(deployment, identity)
			if err := ValidatePodContainerNames(deployment); err == nil || !strings.Contains(err.Error(), "initContainers") {
				t.Fatalf("disabled sidecar in regular containers must be rejected, got %v", err)
			}
		})
	}
}

func TestValidatePodContainerNamesAllowsOperatorContainerNamedBird(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindOperator, Name: "route", Enabled: true,
		Containers: []helpers.ResolvedContainer{{
			Name: "bird", Image: helpers.ResolvedImage{Name: "route", Tag: "v2"},
		}},
	}
	deployments, err := BuildDeployments(ctxV2(), component)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	if err := ValidatePodContainerNames(deployments[0]); err != nil {
		t.Fatalf("operator container names are not dataplane sidecar slots: %v", err)
	}
}

func TestRestoreWorkloadIdentityPreservesNativeSidecarPatchFields(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane, Name: "dataplane", Enabled: true,
		Image:     helpers.ResolvedImage{Name: "dataplane", Tag: "v2"},
		Hugepages: &yanetv2alpha1.Hugepages{Size: "1Gi", Count: 2},
		Config:    &yanetv2alpha1.ConfigSource{HostPath: "/etc/yanet2"},
		NativeSidecars: []helpers.ResolvedContainer{
			{
				Name:   yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
				Image:  helpers.ResolvedImage{Name: "netlink", Tag: "v2"},
				Config: &yanetv2alpha1.ConfigSource{HostPath: "/etc/yanet2"},
			},
			{
				Name:   yanetv2alpha1.BirdSidecarContainerName,
				Image:  helpers.ResolvedImage{Name: "bird", Tag: "v2"},
				Config: &yanetv2alpha1.ConfigSource{HostPath: "/etc/bird"},
			},
		},
	}
	deployments, err := BuildDeployments(ctxV2(), component)
	if err != nil {
		t.Fatalf("BuildDeployments: %v", err)
	}
	deployment := deployments[0]
	baseline := deployment.DeepCopy()
	identity := CaptureWorkloadIdentity(deployment)
	registry := NewPatchRegistry([]yanetv2alpha1.NamedPatch{
		patch("runtime", `{"spec":{"strategy":{"type":"RollingUpdate"},"template":{"spec":{
			"hostNetwork":true,
			"containers":[{"name":"dataplane","resources":{"limits":{"memory":"1Gi"}}}],
			"$setElementOrder/initContainers":[{"name":"bird"},{"name":"netlink-dataplane-sidecar"}],
			"initContainers":[
				{"name":"bird","restartPolicy":null,"resources":{"requests":{"cpu":"100m"}}},
				{"name":"netlink-dataplane-sidecar","restartPolicy":null,"securityContext":{"runAsUser":0},
				 "env":[{"name":"CUSTOM","value":"kept"},{"name":"YANET_SERVER_ENDPOINT","value":"wrong"}]}
			]
		}}}}`),
	})
	if err := ApplyPatches(deployment, []string{"runtime"}, registry); err != nil {
		t.Fatalf("ApplyPatches: %v", err)
	}
	RestoreWorkloadIdentity(deployment, identity)
	if err := ValidatePodContainerNames(deployment); err != nil {
		t.Fatalf("ValidatePodContainerNames: %v", err)
	}
	if err := ConfigureListeners(deployment, component, map[string]int32{ListenerGRPC: 20000}); err != nil {
		t.Fatalf("ConfigureListeners: %v", err)
	}
	pod := &deployment.Spec.Template.Spec
	if !pod.HostIPC || !pod.HostNetwork || pod.DNSPolicy != corev1.DNSClusterFirstWithHostNet ||
		deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("pod namespace or rollout invariants changed: %+v", deployment.Spec)
	}
	if diff := cmp.Diff(baseline.Spec.Template.Spec.Volumes, pod.Volumes); diff != "" {
		t.Errorf("baseline volumes changed (-want +got):\n%s", diff)
	}
	if len(pod.InitContainers) != 2 {
		t.Fatalf("native sidecars = %+v", pod.InitContainers)
	}
	for i, original := range baseline.Spec.Template.Spec.InitContainers {
		container := &pod.InitContainers[i]
		if container.Name != original.Name || container.RestartPolicy == nil ||
			*container.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Fatalf("native sidecar order/restart policy changed: %+v", pod.InitContainers)
		}
		if diff := cmp.Diff(original.VolumeMounts, container.VolumeMounts); diff != "" {
			t.Errorf("%s mounts changed (-want +got):\n%s", container.Name, diff)
		}
	}
	dataplane := &pod.Containers[0]
	if dataplane.SecurityContext == nil || dataplane.SecurityContext.Privileged == nil ||
		!*dataplane.SecurityContext.Privileged || dataplane.Resources.Limits.Memory().String() != "1Gi" {
		t.Fatalf("dataplane security/resource patch = %+v", dataplane)
	}
	if diff := cmp.Diff(baseline.Spec.Template.Spec.Containers[0].VolumeMounts, dataplane.VolumeMounts); diff != "" {
		t.Errorf("dataplane mounts changed (-want +got):\n%s", diff)
	}
	netlink := &pod.InitContainers[0]
	if netlink.SecurityContext == nil || netlink.SecurityContext.Privileged == nil ||
		!*netlink.SecurityContext.Privileged || netlink.SecurityContext.RunAsUser == nil ||
		*netlink.SecurityContext.RunAsUser != 0 {
		t.Fatalf("netlink security patch = %+v", netlink.SecurityContext)
	}
	env := envValues(netlink.Env)
	if netlink.Env[0].Name != EnvKubernetesGRPCPort || env[EnvKubernetesGRPCPort] != "20000" ||
		env[envNetlinkServerEndpoint] != "[::]:$(YANET_KUBERNETES_GRPC_PORT)" || env["CUSTOM"] != "kept" ||
		env[envNetlinkServerAdvertiseEndpoint] != "yanet-firewall-netlink-dataplane-sidecar:8080" {
		t.Fatalf("netlink listener env = %+v", netlink.Env)
	}
	if pod.InitContainers[1].Resources.Requests.Cpu().String() != "100m" || pod.InitContainers[1].SecurityContext != nil {
		t.Fatalf("BIRD resource/security patch = %+v", pod.InitContainers[1])
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
