package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
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

func sidecarConfig() api.YanetConfigSpec {
	config := minimalConfig()
	empty := []api.OperatorListener{}
	config.Components.Dataplane.Sidecars = []api.SidecarSpec{
		{Name: "bird", Image: api.ImageRef{Name: "bird"}, Listeners: &empty},
		{Name: "neighbour", Image: api.ImageRef{Name: "neighbour"}},
	}
	config.BoxTypes[0].Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{"bird": {}, "neighbour": {}}
	return config
}

func TestReconcile_BirdConsumersHaveNoNameBasedDependency(t *testing.T) {
	for _, bird := range []bool{true, false} {
		t.Run(fmt.Sprint(bird), func(t *testing.T) {
			yanet := reviewYanet()
			config := sidecarConfig()
			config.Components.BirdAdapter = &api.BirdAdapterComp{Image: api.ImageRef{Name: "bird-adapter"}}
			config.BoxTypes[0].Components.BirdAdapter = &api.BoxComponent{}
			config.Components.Operators = []api.OperatorSpec{{Name: "announcer", Containers: []api.OperatorContainer{{Name: "announcer", Image: api.ImageRef{Name: "announcer"}}}}}
			config.BoxTypes[0].Operators = map[string]api.BoxOperator{"announcer": {}}
			if !bird {
				delete(config.BoxTypes[0].Components.Dataplane.Sidecars, "bird")
			}
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
			snapshot.Config = config
			before := config.DeepCopy()
			if _, err := reviewReconcile(context.Background(), r, yanet); err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"birdAdapter", "announcer"} {
				deployment := transitionDeployment(t, r, role)
				if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
					t.Fatalf("role %s was not enabled", role)
				}
			}
			if !reflect.DeepEqual(before, &snapshot.Config) {
				t.Fatal("reconcile mutated the palette")
			}
		})
	}
}

func TestReconcile_DisabledSidecarReservedTarget(t *testing.T) {
	for _, mode := range []string{"box-disabled", "override-disabled"} {
		t.Run(mode, func(t *testing.T) {
			yanet := reviewYanet()
			config := sidecarConfig()
			config.Components.Dataplane.Config = &api.ConfigSource{Inline: "opaque configuration"}
			component, err := helpers.ResolveBoxServiceComponent(&config, "release", helpers.KindSidecar, "neighbour")
			if err != nil {
				t.Fatal(err)
			}
			target := manifests.BuildServices(manifests.BuildContext{BoxType: "release"}, component)[0].Ports[0].TargetPortName
			if mode == "box-disabled" {
				config.BoxTypes[0].Components.Dataplane.Sidecars["neighbour"] = api.BoxDataplaneSidecar{Enabled: helpers.PtrFalse()}
			} else {
				yanet.Spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{"neighbour": {Enabled: helpers.PtrFalse()}}}}
			}
			config.Patches = append(config.Patches, api.NamedPatch{Name: "capture-port", Patch: runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"dataplane","ports":[{"name":%q,"containerPort":9000}]}]}}}}`, target))}})
			config.BoxTypes[0].Components.Dataplane.Patches = []string{"capture-port"}
			r, snapshot := makeReconcilerEnv(t, yanet, reviewNode())
			snapshot.Config = config
			if _, err := reviewReconcile(context.Background(), r, yanet); err == nil || !strings.Contains(err.Error(), target) {
				t.Fatalf("disabled sidecar released its Service target: %v", err)
			}
			deployments, configMaps := &appsv1.DeploymentList{}, &corev1.ConfigMapList{}
			if err := r.List(context.Background(), deployments); err != nil {
				t.Fatal(err)
			}
			if err := r.List(context.Background(), configMaps); err != nil {
				t.Fatal(err)
			}
			if len(deployments.Items) != 0 || len(configMaps.Items) != 0 {
				t.Fatal("invalid target caused partial writes")
			}
		})
	}
}

func TestReconcile_DisabledSidecarServiceStatus(t *testing.T) {
	for _, noNodes := range []bool{false, true} {
		for _, mode := range []string{"enabled", "box-disabled", "override-disabled", "dataplane-disabled", "installation-disabled", "unwired"} {
			t.Run(fmt.Sprintf("%s/noNodes=%t", mode, noNodes), func(t *testing.T) {
				testContext := context.Background()
				yanet := reviewYanet()
				config := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName, UID: "config-owner"}, Spec: sidecarConfig()}
				switch mode {
				case "box-disabled":
					config.Spec.BoxTypes[0].Components.Dataplane.Sidecars["neighbour"] = api.BoxDataplaneSidecar{Enabled: helpers.PtrFalse()}
				case "override-disabled":
					yanet.Spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{"neighbour": {Enabled: helpers.PtrFalse()}}}}
				case "dataplane-disabled":
					yanet.Spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{YanetComponentOverride: api.YanetComponentOverride{Enabled: helpers.PtrFalse()}}}
				case "installation-disabled":
					yanet.Spec.Enabled = helpers.PtrFalse()
				case "unwired":
					delete(config.Spec.BoxTypes[0].Components.Dataplane.Sidecars, "neighbour")
				}
				objects := []client.Object{yanet, config}
				if !noNodes {
					objects = append(objects, reviewNode())
				}
				r, snapshot := makeReconcilerEnv(t, objects...)
				shared := &YanetConfigReconciler{Client: r.Client, Scheme: r.Scheme, GlobalConfig: snapshot}
				if _, err := shared.Reconcile(testContext, ctrl.Request{}); err != nil {
					t.Fatal(err)
				}
				if _, err := reviewReconcile(testContext, r, yanet); err != nil {
					t.Fatal(err)
				}
				serviceName := "yanet-release-neighbour"
				service := &corev1.Service{}
				err := r.Get(testContext, client.ObjectKey{Namespace: yanet.Namespace, Name: serviceName}, service)
				wantService := mode != "unwired"
				if wantService && err != nil || !wantService && !apierrors.IsNotFound(err) {
					t.Fatalf("Service existence: %v", err)
				}
				current := &api.Yanet{}
				if err := r.Get(testContext, client.ObjectKeyFromObject(yanet), current); err != nil {
					t.Fatal(err)
				}
				if slices.Contains(current.Status.Services, serviceName) != wantService {
					t.Fatalf("Service missing from status: %v", current.Status.Services)
				}
				if wantService && (len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 8080 || service.Spec.Ports[0].TargetPort.StrVal == "") {
					t.Fatalf("Service contract: %+v", service.Spec.Ports)
				}
				if !noNodes {
					deployment := transitionDeployment(t, r, "dataplane")
					hasNeighbour := false
					for _, container := range deployment.Spec.Template.Spec.InitContainers {
						hasNeighbour = hasNeighbour || container.Image == "neighbour"
					}
					wantNeighbour := mode != "box-disabled" && mode != "override-disabled" && mode != "unwired"
					if hasNeighbour != wantNeighbour {
						t.Fatal("Service planning changed sidecar enablement")
					}
				}
			})
		}
	}
}
