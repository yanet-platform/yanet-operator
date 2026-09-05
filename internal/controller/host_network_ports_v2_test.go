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

package controller

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAllocateHostNetworkPortsV2DeterministicallySkipsReservedPorts(t *testing.T) {
	route := operatorComponentForListenerTest("route")
	controlplane := &helpers.ResolvedComponent{Kind: helpers.KindControlplane, Name: "controlplane"}
	workloads := []renderedWorkloadV2{
		{
			component: route,
			deployment: deploymentForListenerTest("route", "route", true,
				corev1.ContainerPort{Name: manifests.ListenerGRPC, ContainerPort: manifests.ServiceGRPCPort},
				corev1.ContainerPort{Name: "patched", ContainerPort: 20000, Protocol: corev1.ProtocolTCP},
				corev1.ContainerPort{Name: "udp", ContainerPort: 20001, Protocol: corev1.ProtocolUDP},
			),
		},
		{
			component: controlplane,
			deployment: deploymentForListenerTest("controlplane", "controlplane", true,
				corev1.ContainerPort{Name: manifests.ListenerGRPC, ContainerPort: manifests.ServiceGRPCPort},
				corev1.ContainerPort{Name: manifests.ListenerHTTP, ContainerPort: manifests.ServiceHTTPPort},
				corev1.ContainerPort{Name: "patched", ContainerPort: 20002, Protocol: corev1.ProtocolTCP},
			),
		},
	}
	portRange := &yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20004}

	assignments, err := allocateHostNetworkPortsV2(workloads, portRange)
	if err != nil {
		t.Fatalf("allocateHostNetworkPortsV2: %v", err)
	}
	want := listenerPortAssignmentsV2{
		"route":        {manifests.ListenerGRPC: 20001},
		"controlplane": {manifests.ListenerGRPC: 20003, manifests.ListenerHTTP: 20004},
	}
	if !reflect.DeepEqual(assignments, want) {
		t.Fatalf("unexpected assignments: got %#v want %#v", assignments, want)
	}
	assertListenerPortAndEnv(t, workloads[0].deployment, "route", manifests.ListenerGRPC, manifests.EnvKubernetesGRPCPort, 20001)
	assertListenerPortAndEnv(t, workloads[1].deployment, "controlplane", manifests.ListenerGRPC, manifests.EnvKubernetesGRPCPort, 20003)
	assertListenerPortAndEnv(t, workloads[1].deployment, "controlplane", manifests.ListenerHTTP, manifests.EnvKubernetesHTTPPort, 20004)
	for _, workload := range workloads {
		if workload.deployment.Spec.Template.Spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
			t.Fatalf(
				"Deployment %s DNS policy = %q, want ClusterFirstWithHostNet",
				workload.deployment.Name,
				workload.deployment.Spec.Template.Spec.DNSPolicy,
			)
		}
	}

	repeated := []renderedWorkloadV2{
		{component: route, deployment: workloads[0].deployment.DeepCopy()},
		{component: controlplane, deployment: workloads[1].deployment.DeepCopy()},
	}
	repeatedAssignments, err := allocateHostNetworkPortsV2(repeated, portRange)
	if err != nil {
		t.Fatalf("repeat allocation: %v", err)
	}
	if !reflect.DeepEqual(repeatedAssignments, want) {
		t.Fatalf("allocation is not deterministic: got %#v want %#v", repeatedAssignments, want)
	}
}

func TestAllocateHostNetworkPortsV2UsesStablePortsOutsideHostNetwork(t *testing.T) {
	route := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest("route", "route", false)

	assignments, err := allocateHostNetworkPortsV2([]renderedWorkloadV2{{
		deployment: deployment,
		component:  route,
	}}, nil)
	if err != nil {
		t.Fatalf("allocateHostNetworkPortsV2: %v", err)
	}
	if len(assignments) != 0 {
		t.Fatalf("pod-network workload must not receive an allocation: %#v", assignments)
	}
	assertListenerPortAndEnv(t, deployment, "route", manifests.ListenerGRPC, manifests.EnvKubernetesGRPCPort, manifests.ServiceGRPCPort)
}

func TestAllocateHostNetworkPortsV2ConfiguresNativeSidecar(t *testing.T) {
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindDataplane,
		Name: "dataplane",
		NativeSidecars: []helpers.ResolvedContainer{{
			Name: yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
		}},
	}
	deployment := deploymentForListenerTest("dataplane", "dataplane", true)
	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{
		{
			Name: yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
			Ports: []corev1.ContainerPort{{
				Name:          manifests.NetlinkGRPCTargetPort,
				ContainerPort: manifests.ServiceGRPCPort,
			}},
		},
		{
			Name:  yanetv2alpha1.BirdSidecarContainerName,
			Ports: []corev1.ContainerPort{{Name: "fixed", ContainerPort: 20000}},
		},
	}
	assignments, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		&yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20001},
	)
	if err != nil {
		t.Fatalf("allocateHostNetworkPortsV2: %v", err)
	}
	want := listenerPortAssignmentsV2{"dataplane": {manifests.ListenerGRPC: 20001}}
	if !reflect.DeepEqual(assignments, want) {
		t.Fatalf("assignments = %#v, want %#v", assignments, want)
	}
	assertListenerPortAndEnv(
		t,
		deployment,
		yanetv2alpha1.NetlinkDataplaneSidecarContainerName,
		manifests.NetlinkGRPCTargetPort,
		manifests.EnvKubernetesGRPCPort,
		20001,
	)
}

func TestAllocateHostNetworkPortsV2RequiresRange(t *testing.T) {
	route := operatorComponentForListenerTest("route")
	_, err := allocateHostNetworkPortsV2([]renderedWorkloadV2{{
		deployment: deploymentForListenerTest("route", "route", true),
		component:  route,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "hostNetworkPortRange is not configured") {
		t.Fatalf("expected missing range error, got %v", err)
	}
}

func TestAllocateHostNetworkPortsV2ReportsExhaustion(t *testing.T) {
	route := operatorComponentForListenerTest("route")
	_, err := allocateHostNetworkPortsV2([]renderedWorkloadV2{{
		deployment: deploymentForListenerTest("route", "route", true,
			corev1.ContainerPort{Name: "reserved", ContainerPort: 20000},
		),
		component: route,
	}}, &yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20000})
	if err == nil || !strings.Contains(err.Error(), "is exhausted") {
		t.Fatalf("expected range exhaustion, got %v", err)
	}
}

func TestAllocateHostNetworkPortsV2RejectsFixedPortCollision(t *testing.T) {
	first := operatorComponentForListenerTest("first")
	second := operatorComponentForListenerTest("second")
	_, err := allocateHostNetworkPortsV2([]renderedWorkloadV2{
		{
			deployment: deploymentForListenerTest("first", "first", true,
				corev1.ContainerPort{Name: "fixed", ContainerPort: 9000},
			),
			component: first,
		},
		{
			deployment: deploymentForListenerTest("second", "second", true,
				corev1.ContainerPort{Name: "fixed", ContainerPort: 9000},
			),
			component: second,
		},
	}, &yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20001})
	if err == nil || !strings.Contains(err.Error(), "same TCP port 9000") {
		t.Fatalf("expected fixed port collision, got %v", err)
	}
}

func TestAllocateHostNetworkPortsV2SkipsDisabledWorkload(t *testing.T) {
	route := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest("route", "route", true)
	zero := int32(0)
	deployment.Spec.Replicas = &zero
	assignments, err := allocateHostNetworkPortsV2([]renderedWorkloadV2{{
		deployment: deployment,
		component:  route,
	}}, nil)
	if err != nil {
		t.Fatalf("disabled host-network workload must not require a range: %v", err)
	}
	if len(assignments) != 0 {
		t.Fatalf("disabled workload received listener assignments: %#v", assignments)
	}
}

func TestAllocateHostNetworkPortsV2RejectsIntraPodPortCollision(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	component := &helpers.ResolvedComponent{Kind: helpers.KindDataplane, Name: "dataplane"}
	deployment := deploymentForListenerTest(
		"dataplane",
		"dataplane",
		true,
		corev1.ContainerPort{Name: "dataplane-fixed", ContainerPort: 179},
	)
	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{{
		Name:          yanetv2alpha1.BirdSidecarContainerName,
		RestartPolicy: &always,
		Ports:         []corev1.ContainerPort{{Name: "bgp", ContainerPort: 179}},
	}}
	_, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "same TCP port 179") {
		t.Fatalf("expected intra-Pod fixed port collision, got %v", err)
	}
}

func TestAllocateHostNetworkPortsV2AllowsSequentialInitPortReuse(t *testing.T) {
	component := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest(
		"route",
		"route",
		true,
	)
	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{{
		Name:  "prepare-network",
		Ports: []corev1.ContainerPort{{Name: "temporary", ContainerPort: 20000}},
	}}
	assignments, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		&yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20000},
	)
	if err != nil {
		t.Fatalf("sequential init/application port reuse was rejected: %v", err)
	}
	if got := assignments[deployment.Name][manifests.ListenerGRPC]; got != 20000 {
		t.Fatalf("listener port = %d, want reused port 20000", got)
	}
}

func TestAllocateHostNetworkPortsV2IgnoresProvisionalManagedPort(t *testing.T) {
	component := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest(
		"route",
		"route",
		true,
		corev1.ContainerPort{Name: manifests.ListenerGRPC, ContainerPort: manifests.ServiceGRPCPort},
	)
	deployment.Spec.Template.Spec.Containers = append(
		deployment.Spec.Template.Spec.Containers,
		corev1.Container{
			Name:  "metrics",
			Ports: []corev1.ContainerPort{{Name: "fixed", ContainerPort: manifests.ServiceGRPCPort}},
		},
	)
	assignments, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		&yanetv2alpha1.HostNetworkPortRange{Start: 20000, End: 20000},
	)
	if err != nil {
		t.Fatalf("provisional managed listener port caused a conflict: %v", err)
	}
	if got := assignments[deployment.Name][manifests.ListenerGRPC]; got != 20000 {
		t.Fatalf("listener port = %d, want 20000", got)
	}
}

func TestAllocateHostNetworkPortsV2IgnoresPodNetworkProvisionalManagedPort(t *testing.T) {
	component := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest(
		"route",
		"route",
		false,
		corev1.ContainerPort{Name: manifests.ListenerGRPC, ContainerPort: 9000},
	)
	deployment.Spec.Template.Spec.Containers = append(
		deployment.Spec.Template.Spec.Containers,
		corev1.Container{
			Name:  "metrics",
			Ports: []corev1.ContainerPort{{Name: "fixed", ContainerPort: 9000}},
		},
	)
	if _, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		nil,
	); err != nil {
		t.Fatalf("provisional pod-network listener port caused a conflict: %v", err)
	}
	assertListenerPortAndEnv(
		t,
		deployment,
		"route",
		manifests.ListenerGRPC,
		manifests.EnvKubernetesGRPCPort,
		manifests.ServiceGRPCPort,
	)
}

func TestAllocateHostNetworkPortsV2RejectsPodNetworkPortCollision(t *testing.T) {
	component := operatorComponentForListenerTest("route")
	deployment := deploymentForListenerTest(
		"route",
		"route",
		false,
		corev1.ContainerPort{Name: manifests.ListenerGRPC, ContainerPort: manifests.ServiceGRPCPort},
	)
	deployment.Spec.Template.Spec.Containers = append(
		deployment.Spec.Template.Spec.Containers,
		corev1.Container{
			Name:  "metrics",
			Ports: []corev1.ContainerPort{{Name: "fixed", ContainerPort: manifests.ServiceGRPCPort}},
		},
	)
	if _, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		nil,
	); err == nil || !strings.Contains(err.Error(), "same TCP port 8080") {
		t.Fatalf("expected pod-network listener collision, got %v", err)
	}
}

func TestAllocateHostNetworkPortsV2RejectsSidecarAndLaterInitPortCollision(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	component := &helpers.ResolvedComponent{Kind: helpers.KindDataplane, Name: "dataplane"}
	deployment := deploymentForListenerTest("dataplane", "dataplane", true)
	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{
		{
			Name:          yanetv2alpha1.BirdSidecarContainerName,
			RestartPolicy: &always,
			Ports:         []corev1.ContainerPort{{Name: "bgp", ContainerPort: 179}},
		},
		{
			Name:  "prepare-network",
			Ports: []corev1.ContainerPort{{Name: "temporary", ContainerPort: 179}},
		},
	}
	if _, err := allocateHostNetworkPortsV2(
		[]renderedWorkloadV2{{deployment: deployment, component: component}},
		nil,
	); err == nil || !strings.Contains(err.Error(), "run concurrently") {
		t.Fatalf("expected sidecar/later-init port collision, got %v", err)
	}
}

func TestReserveHostPortsRejectsMultiplePodNetworkDataplaneReplicas(t *testing.T) {
	deployment := deploymentForListenerTest("dataplane", "dataplane", false)
	deployment.Spec.Template.Labels = map[string]string{manifests.LabelComponent: "dataplane"}
	replicas := int32(2)
	deployment.Spec.Replicas = &replicas

	err := reserveHostPorts(make(map[hostPortKey]hostPortOwnerV2), deployment)
	if err == nil || !strings.Contains(err.Error(), "node-pinned dataplane") {
		t.Fatalf("expected multiple dataplane replicas to be rejected, got %v", err)
	}
}

func operatorComponentForListenerTest(name string) *helpers.ResolvedComponent {
	return &helpers.ResolvedComponent{
		Kind: helpers.KindOperator,
		Name: name,
		Containers: []helpers.ResolvedContainer{{
			Name: name,
		}},
	}
}

func deploymentForListenerTest(
	name string,
	containerName string,
	hostNetwork bool,
	ports ...corev1.ContainerPort,
) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "yanet"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			HostNetwork: hostNetwork,
			Containers: []corev1.Container{{
				Name:  containerName,
				Ports: ports,
			}},
		}}},
	}
}

func assertListenerPortAndEnv(
	t *testing.T,
	deployment *appsv1.Deployment,
	containerName string,
	portName string,
	envName string,
	want int32,
) {
	t.Helper()
	var container *corev1.Container
	for index := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[index].Name == containerName {
			container = &deployment.Spec.Template.Spec.Containers[index]
			break
		}
	}
	if container == nil {
		for index := range deployment.Spec.Template.Spec.InitContainers {
			if deployment.Spec.Template.Spec.InitContainers[index].Name == containerName {
				container = &deployment.Spec.Template.Spec.InitContainers[index]
				break
			}
		}
	}
	if container == nil {
		t.Fatalf("container %q not found", containerName)
	}
	foundPort := false
	for _, port := range container.Ports {
		if port.Name == portName {
			foundPort = true
			if port.ContainerPort != want {
				t.Errorf("listener %s port: got %d want %d", portName, port.ContainerPort, want)
			}
			break
		}
	}
	if !foundPort {
		t.Errorf("listener port %q not found", portName)
	}
	for _, variable := range container.Env {
		if variable.Name == envName {
			if variable.Value != strconv.FormatInt(int64(want), 10) {
				t.Errorf("listener %s env: got %q want %d", envName, variable.Value, want)
			}
			return
		}
	}
	t.Errorf("listener env %q not found", envName)
}
