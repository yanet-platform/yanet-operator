package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func colocatedConfig(t *testing.T) yanetv1alpha1.YanetConfigSpec {
	t.Helper()
	var config yanetv1alpha1.YanetConfigSpec
	if err := json.Unmarshal([]byte(`{
		"components":{
			"controlplane":{"image":{"name":"cp"}},
			"dataplane":{"image":{"name":"dp"},"sidecars":[
				{"name":"neighbour","image":{"name":"netlink"}},
				{"name":"monalive","listeners":["http","grpc"],"image":{"name":"monalive","tag":"v1"},"config":{"hostPath":"/etc/monitor"}},
				{"name":"z-probe","image":{"name":"probe"},"config":{"inline":"probe"}}
			]}
		},
		"patches":[{"name":"monitor-runtime","patch":{"spec":{"template":{"spec":{
			"containers":[{"name":"monalive","env":[
				{"name":"CUSTOM","value":"retained"}
			],"volumeMounts":[{"name":"extra","mountPath":"/extra"}]}],
			"volumes":[{"name":"extra","emptyDir":{}}]
		}}}}}],
		"boxTypes":[{"name":"release","components":{"controlplane":{},"dataplane":{"sidecars":{
			"neighbour":{},"monalive":{"patches":["monitor-runtime"]},"z-probe":{}}}}}]
	}`), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestOperatorPlacementTransitionOldReplicaSet(t *testing.T) {
	for _, mode := range []string{"creates-pods", "replicas-remain", "unobserved-scale-down", "drained"} {
		t.Run(mode, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanet()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
			snapshot.Config = colocatedConfig(t)
			moveMonitorRole(&snapshot.Config, false)
			if _, err := reviewReconcile(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			old := transitionDeployment(t, r, "monalive")
			zero := int32(0)
			set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old-producer", Namespace: yanet.Namespace, Generation: 2},
				Spec:   appsv1.ReplicaSetSpec{Replicas: &zero, Template: old.Spec.Template},
				Status: appsv1.ReplicaSetStatus{ObservedGeneration: 2}}
			switch mode {
			case "creates-pods":
				set.Spec.Replicas = nil
			case "replicas-remain":
				set.Status.Replicas = 1
			case "unobserved-scale-down":
				set.Status.ObservedGeneration = 1
			}
			if err := r.Create(testContext, set); err != nil {
				t.Fatal(err)
			}
			if err := r.Delete(testContext, old); err != nil {
				t.Fatal(err)
			}
			moveMonitorRole(&snapshot.Config, true)
			_, err := reviewReconcile(testContext, r, yanet)
			if mode == "drained" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "ReplicaSet") {
				t.Fatalf("old ReplicaSet must reserve the producer: %v", err)
			}
		})
	}
}

func TestOperatorPlacementTransitionListFailure(t *testing.T) {
	yanet := reviewYanet()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
	snapshot.Config = colocatedConfig(t)
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*appsv1.ReplicaSetList); ok {
			return errors.New("replicaset read denied")
		}
		return cl.List(ctx, list, opts...)
	}})
	if _, err := reviewReconcile(context.Background(), r, yanet); err == nil || !strings.Contains(err.Error(), "read denied") {
		t.Fatalf("cannot prove drain without live reads: %v", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(context.Background(), deployments); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("failed preflight wrote workloads: %v", err)
	}
}

func placementService(t *testing.T, r *YanetReconciler, name string) *corev1.Service {
	t.Helper()
	service := &corev1.Service{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "yanet", Name: "yanet-release-" + name}, service); err != nil {
		t.Fatal(err)
	}
	return service
}

func targetContainer(t *testing.T, deployment *appsv1.Deployment, service *corev1.Service, listener string, wantPort int32) *corev1.Container {
	t.Helper()
	for _, port := range service.Spec.Ports {
		if port.Name != listener {
			continue
		}
		for i := range deployment.Spec.Template.Spec.InitContainers {
			container := &deployment.Spec.Template.Spec.InitContainers[i]
			for _, target := range container.Ports {
				if target.Name == port.TargetPort.StrVal {
					if target.ContainerPort != wantPort {
						t.Fatalf("%s/%s target port = %d, want %d", service.Name, listener, target.ContainerPort, wantPort)
					}
					return container
				}
			}
		}
	}
	t.Fatalf("missing native target for %s/%s", service.Name, listener)
	return nil
}

func TestOperatorPlacementColocation(t *testing.T) {
	testContext := context.Background()
	yanet := reviewYanet()
	config := &yanetv1alpha1.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: "config", UID: "config-owner"}, Spec: colocatedConfig(t)}
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNode(), config)
	shared := &YanetConfigReconciler{Client: r.Client, Scheme: r.Scheme, GlobalConfig: snapshot}
	if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	before := snapshot.Config.DeepCopy()
	if _, err := reviewReconcile(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(testContext, deployments); err != nil {
		t.Fatal(err)
	}
	if len(deployments.Items) != 2 {
		t.Fatalf("want only controlplane and dataplane Deployments, got %d", len(deployments.Items))
	}
	dataplane := transitionDeployment(t, r, "dataplane")
	if dataplane.Spec.Template.Spec.HostNetwork || len(dataplane.Spec.Template.Spec.InitContainers) != 3 {
		t.Fatalf("wrong shared private netns topology: %+v", dataplane.Spec.Template.Spec)
	}
	monitor := placementService(t, r, "monalive")
	probe := placementService(t, r, "z-probe")
	for _, service := range []*corev1.Service{monitor, probe} {
		if !labels.SelectorFromSet(service.Spec.Selector).Matches(labels.Set(dataplane.Spec.Template.Labels)) {
			t.Fatalf("Service %s does not select the colocated role", service.Name)
		}
		for _, port := range service.Spec.Ports {
			if port.Name == "grpc" && port.Port != 8080 || port.Name == "http" && port.Port != 8081 {
				t.Fatalf("external ports changed: %+v", service.Spec.Ports)
			}
		}
	}
	worker := targetContainer(t, dataplane, monitor, "grpc", 8082)
	if targetContainer(t, dataplane, monitor, "http", 8083).Name != worker.Name {
		t.Fatal("listeners must target the first logical container")
	}
	if targetContainer(t, dataplane, probe, "grpc", 8084).Name == worker.Name {
		t.Fatal("different operators captured the same container")
	}
	variables := map[string]string{}
	for _, variable := range worker.Env {
		variables[variable.Name] = variable.Value
	}
	if variables["YANET_SERVER_ENDPOINT"] != "[::]:8082" || variables["CUSTOM"] != "retained" ||
		variables["YANET_SERVER_ADVERTISE_ENDPOINT"] != "yanet-release-monalive.yanet.svc.cluster.local:8080" {
		t.Fatalf("wrong bind/advertise contract: %+v", worker.Env)
	}
	volumeNames := map[string]bool{}
	for _, volume := range dataplane.Spec.Template.Spec.Volumes {
		if volumeNames[volume.Name] {
			t.Fatalf("duplicate composed volume %s", volume.Name)
		}
		volumeNames[volume.Name] = true
	}
	for _, container := range dataplane.Spec.Template.Spec.InitContainers {
		if container.RestartPolicy == nil || *container.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Fatalf("%s is not a native sidecar", container.Name)
		}
		for _, mount := range container.VolumeMounts {
			if !volumeNames[mount.Name] {
				t.Fatalf("unresolved composed volume mount %s/%s", container.Name, mount.Name)
			}
		}
	}
	if !reflect.DeepEqual(before, &snapshot.Config) {
		t.Fatal("composition mutated the config snapshot")
	}

	// Disable the role, retaining its Service and port slot. A dataplane patch
	// must not resurrect a managed container or forge its Service membership.
	if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
		t.Fatal(err)
	}
	yanet.Spec.Components = &yanetv1alpha1.YanetComponentsOverride{Dataplane: &yanetv1alpha1.YanetDataplaneOverride{
		Sidecars: map[string]yanetv1alpha1.YanetContainerOverride{"monalive": {Enabled: helpers.PtrFalse()}},
	}}
	if err := r.Update(testContext, yanet); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"labels": monitor.Spec.Selector},
		"spec":     map[string]any{"initContainers": []corev1.Container{*worker}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Config.Patches = append(snapshot.Config.Patches, yanetv1alpha1.NamedPatch{Name: "resurrect", Patch: runtime.RawExtension{Raw: raw}})
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"resurrect"}
	if _, err := reviewReconcile(testContext, r, yanet); err == nil {
		t.Fatal("a patch must not resurrect a disabled sidecar")
	}
	unchanged := transitionDeployment(t, r, "dataplane")
	if !reflect.DeepEqual(dataplane.Spec, unchanged.Spec) {
		t.Fatal("invalid composition partially updated the workload")
	}
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = nil
	if _, err := reviewReconcile(testContext, r, yanet); err != nil {
		t.Fatal(err)
	}
	dataplane = transitionDeployment(t, r, "dataplane")
	if labels.SelectorFromSet(monitor.Spec.Selector).Matches(labels.Set(dataplane.Spec.Template.Labels)) {
		t.Fatal("disabled role still matches its shared Service")
	}
	for _, container := range dataplane.Spec.Template.Spec.InitContainers {
		if container.Name == worker.Name {
			t.Fatal("patch resurrected the disabled producer")
		}
	}
	targetContainer(t, dataplane, probe, "grpc", 8084)
}

func TestOperatorPlacementRejectsUnsafePatches(t *testing.T) {
	for _, fragment := range []string{
		`{"spec":{"replicas":2}}`,
		`{"spec":{"template":{"spec":{"hostNetwork":true}}}}`,
		`{"spec":{"template":{"spec":{"nodeSelector":{"pool":"other"}}}}}`,
		`{"metadata":{"annotations":{"unexpected":"value"}}}`,
		`{"spec":{"template":{"spec":{"containers":[{"name":"monalive","$patch":"delete"}]}}}}`,
		`{"spec":{"template":{"spec":{"volumes":[{"name":"bad","emptyDir":{}},{"name":"bad","emptyDir":{}}]}}}}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			yanet := reviewYanet()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
			snapshot.Config = colocatedConfig(t)
			snapshot.Config.Patches[0].Patch.Raw = []byte(fragment)
			if _, err := reviewReconcile(context.Background(), r, yanet); err == nil {
				t.Fatal("unsupported colocated operator patch must fail before apply")
			}
			deployments := &appsv1.DeploymentList{}
			if err := r.List(context.Background(), deployments); err != nil || len(deployments.Items) != 0 {
				t.Fatalf("preflight wrote workloads: count=%d err=%v", len(deployments.Items), err)
			}
		})
	}
}

func TestOperatorPlacementRejectsHostNetwork(t *testing.T) {
	yanet := reviewYanet()
	r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
	snapshot.Config = colocatedConfig(t)
	snapshot.Config.Patches = append(snapshot.Config.Patches, yanetv1alpha1.NamedPatch{Name: "host-network", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"hostNetwork":true}}}}`)}})
	snapshot.Config.BoxTypes[0].Components.Dataplane.Patches = []string{"host-network"}
	if _, err := reviewReconcile(context.Background(), r, yanet); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("colocation in hostNetwork must fail clearly: %v", err)
	}
}

func TestOperatorPlacementTransitionRequiresDrain(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanet()
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
			snapshot.Config = colocatedConfig(t)
			setPlacement := func(colocated bool) {
				moveMonitorRole(&snapshot.Config, colocated)
			}
			setPlacement(reverse)
			if _, err := reviewReconcile(testContext, r, yanet); err != nil {
				t.Fatal(err)
			}
			oldComponent := "monalive"
			if reverse {
				oldComponent = "dataplane"
			}
			old := transitionDeployment(t, r, oldComponent)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old-producer", Namespace: yanet.Namespace, Labels: old.Spec.Template.Labels}, Spec: *old.Spec.Template.Spec.DeepCopy()}
			if err := r.Create(testContext, pod); err != nil {
				t.Fatal(err)
			}
			setPlacement(!reverse)
			before := &appsv1.DeploymentList{}
			if err := r.List(testContext, before); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewReconcile(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "drain") {
				t.Fatalf("placement change must fail closed: %v", err)
			}
			after := &appsv1.DeploymentList{}
			if err := r.List(testContext, after); err != nil || !reflect.DeepEqual(before.Items, after.Items) {
				t.Fatalf("unsafe transition wrote Deployments: %v", err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
				t.Fatal(err)
			}
			yanet.Spec.Enabled = helpers.PtrFalse()
			if err := r.Update(testContext, yanet); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewReconcile(testContext, r, yanet); err != nil {
				t.Fatalf("whole-installation disable must permit drain: %v", err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), yanet); err != nil {
				t.Fatal(err)
			}
			yanet.Spec.Enabled = helpers.PtrTrue()
			if err := r.Update(testContext, yanet); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewReconcile(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "Pod") {
				t.Fatalf("old live Pod must still block enablement: %v", err)
			}
			if err := r.Delete(testContext, pod); err != nil {
				t.Fatal(err)
			}
			if _, err := reviewReconcile(testContext, r, yanet); err != nil {
				t.Fatalf("drained migration must succeed: %v", err)
			}
		})
	}
}
