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
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func sidecarConfigV2() yanetv2alpha1.YanetConfigSpec {
	config := minimalConfigV2()
	config.Components.Dataplane.Sidecars = &yanetv2alpha1.DataplaneSidecarsSpec{
		Bird:                    &yanetv2alpha1.DataplaneSidecarSpec{Image: yanetv2alpha1.ImageRef{Name: "bird"}},
		NetlinkDataplaneSidecar: &yanetv2alpha1.DataplaneSidecarSpec{Image: yanetv2alpha1.ImageRef{Name: "netlink"}},
	}
	config.BoxTypes[0].Components.Dataplane.Sidecars = &yanetv2alpha1.BoxDataplaneSidecars{
		Bird:                    &yanetv2alpha1.BoxDataplaneSidecar{},
		NetlinkDataplaneSidecar: &yanetv2alpha1.BoxDataplaneSidecar{},
	}
	return config
}

func TestReconcileV2_BirdConsumerDependencies(t *testing.T) {
	for _, consumer := range []string{"birdAdapter", "announcer"} {
		for _, mode := range []string{"enabled", "absent", "box-disabled", "override-disabled", "dataplane-disabled", "consumer-disabled", "installation-disabled", "patched-dataplane-zero", "bird-free"} {
			t.Run(consumer+"/"+mode, func(t *testing.T) {
				testContext := context.Background()
				yanet := reviewYanetV2()
				config := sidecarConfigV2()
				config.Components.BirdAdapter = &yanetv2alpha1.BirdAdapterComp{Image: yanetv2alpha1.ImageRef{Name: "bird-adapter"}}
				config.Components.Announcer = &yanetv2alpha1.AnnouncerComp{Image: yanetv2alpha1.ImageRef{Name: "announcer"}}
				box := &config.BoxTypes[0]
				if consumer == "birdAdapter" {
					box.Components.BirdAdapter = &yanetv2alpha1.BoxComponent{}
				} else {
					box.Components.Announcer = &yanetv2alpha1.BoxComponent{}
				}
				yanet.Spec.Components = &yanetv2alpha1.YanetComponentsOverride{Dataplane: &yanetv2alpha1.YanetComponentOverride{}}
				wantErr := true
				switch mode {
				case "enabled":
					wantErr = false
				case "absent", "bird-free":
					yanet.Spec.Components = nil
					config.Components.Dataplane.Sidecars.Bird = nil
					box.Components.Dataplane.Sidecars.Bird = nil
					if mode == "bird-free" {
						box.Components.BirdAdapter, box.Components.Announcer = nil, nil
						wantErr = false
					}
				case "box-disabled":
					box.Components.Dataplane.Sidecars.Bird.Enabled = helpers.PtrFalse()
				case "dataplane-disabled":
					yanet.Spec.Components.Dataplane.Enabled = helpers.PtrFalse()
				case "patched-dataplane-zero":
					config.Patches = append(config.Patches, yanetv2alpha1.NamedPatch{
						Name: "stop-dataplane", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"replicas":0}}`)},
					})
					box.Components.Dataplane.Patches = []string{"stop-dataplane"}
				default:
					yanet.Spec.Components.Dataplane.Containers = map[string]yanetv2alpha1.YanetContainerOverride{
						yanetv2alpha1.BirdSidecarContainerName: {Enabled: helpers.PtrFalse()},
					}
					if mode == "consumer-disabled" {
						override := &yanetv2alpha1.YanetComponentOverride{Enabled: helpers.PtrFalse()}
						if consumer == "birdAdapter" {
							yanet.Spec.Components.BirdAdapter = override
						} else {
							yanet.Spec.Components.Announcer = override
						}
						wantErr = false
					}
					if mode == "installation-disabled" {
						yanet.Spec.Enabled = helpers.PtrFalse()
						wantErr = false
					}
				}
				r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
				snapshot.Config = config
				before := config.DeepCopy()
				result, err := reviewReconcileV2(testContext, r, yanet)
				deployments := &appsv1.DeploymentList{}
				if listErr := r.List(testContext, deployments); listErr != nil {
					t.Fatal(listErr)
				}
				if wantErr {
					if err == nil && result.RequeueAfter == 0 {
						t.Fatal("invalid BIRD dependency must request a retry")
					}
					if len(deployments.Items) != 0 {
						t.Fatal("invalid BIRD dependency must fail before workload writes")
					}
					current := &yanetv2alpha1.YanetV2{}
					if getErr := r.Get(testContext, client.ObjectKeyFromObject(yanet), current); getErr != nil {
						t.Fatal(getErr)
					}
					degraded := false
					for _, condition := range current.Status.Conditions {
						if condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue &&
							strings.Contains(condition.Message, "managed BIRD") && strings.Contains(condition.Message, consumer) {
							degraded = true
						}
					}
					if !degraded {
						t.Fatalf("missing actionable dependency condition: %+v", current.Status.Conditions)
					}
				} else {
					if err != nil || len(deployments.Items) < 2 {
						t.Fatalf("valid topology did not reconcile: %v, deployments=%d", err, len(deployments.Items))
					}
					for _, deployment := range deployments.Items {
						if mode == "installation-disabled" || mode == "consumer-disabled" && deployment.Labels[manifests.LabelComponent] == consumer {
							if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
								t.Fatalf("disabled workload was re-enabled: %s", deployment.Name)
							}
						}
					}
				}
				if !reflect.DeepEqual(before, &snapshot.Config) {
					t.Fatal("reconciliation mutated the config snapshot")
				}
			})
		}
	}
}

func TestReconcileV2_DisabledNetlinkReservedTarget(t *testing.T) {
	for _, mode := range []string{"box-disabled", "override-disabled"} {
		t.Run(mode, func(t *testing.T) {
			testContext := context.Background()
			yanet := reviewYanetV2()
			config := sidecarConfigV2()
			config.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "dataplane config"}
			if mode == "box-disabled" {
				config.BoxTypes[0].Components.Dataplane.Sidecars.NetlinkDataplaneSidecar.Enabled = helpers.PtrFalse()
			} else {
				yanet.Spec.Components = &yanetv2alpha1.YanetComponentsOverride{Dataplane: &yanetv2alpha1.YanetComponentOverride{
					Containers: map[string]yanetv2alpha1.YanetContainerOverride{
						yanetv2alpha1.NetlinkDataplaneSidecarContainerName: {Enabled: helpers.PtrFalse()},
					},
				}}
			}
			config.Patches = append(config.Patches, yanetv2alpha1.NamedPatch{
				Name: "capture-port", Patch: runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"spec":{"containers":[
					{"name":"other","image":"other","ports":[{"name":"netlink-grpc","containerPort":9000}]}
				]}}}}`)},
			})
			config.BoxTypes[0].Components.Dataplane.Patches = []string{"capture-port"}
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNodeV2())
			snapshot.Config = config
			if _, err := reviewReconcileV2(testContext, r, yanet); err == nil || !strings.Contains(err.Error(), "netlink-grpc") {
				t.Fatalf("disabled sidecar must not release its reserved Service target: %v", err)
			}
			deployments := &appsv1.DeploymentList{}
			configMaps := &corev1.ConfigMapList{}
			if err := r.List(testContext, deployments); err != nil {
				t.Fatal(err)
			}
			if err := r.List(testContext, configMaps); err != nil {
				t.Fatal(err)
			}
			if len(deployments.Items) != 0 || len(configMaps.Items) != 0 {
				t.Fatal("reserved target validation must precede all workload writes")
			}
		})
	}
}

func TestReconcileV2_DisabledNetlinkServiceStatus(t *testing.T) {
	for _, noNodes := range []bool{false, true} {
		for _, mode := range []string{"enabled", "box-disabled", "override-disabled", "dataplane-disabled", "installation-disabled", "unwired"} {
			name := mode
			if noNodes {
				name += "/no-nodes"
			}
			t.Run(name, func(t *testing.T) {
				testContext := context.Background()
				yanet := reviewYanetV2()
				config := &yanetv2alpha1.YanetConfigV2{
					ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName, UID: "config-owner"},
					Spec:       sidecarConfigV2(),
				}
				switch mode {
				case "box-disabled":
					config.Spec.BoxTypes[0].Components.Dataplane.Sidecars.NetlinkDataplaneSidecar.Enabled = helpers.PtrFalse()
				case "override-disabled":
					yanet.Spec.Components = &yanetv2alpha1.YanetComponentsOverride{Dataplane: &yanetv2alpha1.YanetComponentOverride{
						Containers: map[string]yanetv2alpha1.YanetContainerOverride{
							yanetv2alpha1.NetlinkDataplaneSidecarContainerName: {Enabled: helpers.PtrFalse()},
						},
					}}
				case "dataplane-disabled":
					yanet.Spec.Components = &yanetv2alpha1.YanetComponentsOverride{Dataplane: &yanetv2alpha1.YanetComponentOverride{Enabled: helpers.PtrFalse()}}
				case "installation-disabled":
					yanet.Spec.Enabled = helpers.PtrFalse()
				case "unwired":
					config.Spec.BoxTypes[0].Components.Dataplane.Sidecars.NetlinkDataplaneSidecar = nil
				}
				objects := []client.Object{yanet, config}
				if !noNodes {
					objects = append(objects, reviewNodeV2())
				}
				r, snapshot := makeReconcilerEnv(t, objects...)
				shared := &YanetConfigReconcilerV2{Client: r.Client, Scheme: r.Scheme, GlobalConfigV2: snapshot}
				if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
					t.Fatal(err)
				}
				if _, err := reviewReconcileV2(testContext, r, yanet); err != nil {
					t.Fatal(err)
				}
				serviceName := "yanet-release-netlink-dataplane-sidecar"
				service := &corev1.Service{}
				err := r.Get(testContext, client.ObjectKey{Namespace: yanet.Namespace, Name: serviceName}, service)
				wantService := mode != "unwired"
				if wantService && err != nil || !wantService && !apierrors.IsNotFound(err) {
					t.Fatalf("unexpected shared Service existence: %v", err)
				}
				current := &yanetv2alpha1.YanetV2{}
				if getErr := r.Get(testContext, client.ObjectKeyFromObject(yanet), current); getErr != nil {
					t.Fatal(getErr)
				}
				if slices.Contains(current.Status.Services, serviceName) != wantService {
					t.Fatalf("status must report the declared shared Service: %v", current.Status.Services)
				}
				if wantService && (len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8080 ||
					service.Spec.Ports[0].TargetPort.StrVal != manifests.NetlinkGRPCTargetPort) {
					t.Fatalf("netlink metrics Service contract changed: %+v", service.Spec.Ports)
				}
				deployments := &appsv1.DeploymentList{}
				if listErr := r.List(testContext, deployments); listErr != nil {
					t.Fatal(listErr)
				}
				for _, deployment := range deployments.Items {
					if deployment.Labels[manifests.LabelComponent] != "dataplane" {
						continue
					}
					hasNetlink := false
					for _, container := range deployment.Spec.Template.Spec.InitContainers {
						hasNetlink = hasNetlink || container.Name == yanetv2alpha1.NetlinkDataplaneSidecarContainerName
					}
					wantNetlink := mode != "box-disabled" && mode != "override-disabled" && mode != "unwired"
					if hasNetlink != wantNetlink {
						t.Fatal("Service planning changed workload sidecar enablement")
					}
				}
			})
		}
	}
}
