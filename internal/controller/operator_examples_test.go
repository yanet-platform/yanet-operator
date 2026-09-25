package controller

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestOperatorPlacementExamples(t *testing.T) {
	for _, example := range []struct {
		name, file     string
		disableNetwork bool
	}{
		{name: "full", file: "v1alpha1-yanetconfig-full.yaml"},
		{name: "disabled-network-sidecars", file: "v1alpha1-yanetconfig-full.yaml", disableNetwork: true},
		{name: "placement", file: "v1alpha1-yanetconfig-placement.yaml"},
	} {
		t.Run(example.name, func(t *testing.T) {
			file := example.file
			raw, err := os.ReadFile("../../deploy/examples/" + file)
			if err != nil {
				t.Fatal(err)
			}
			config := &api.YanetConfig{}
			if err := yaml.UnmarshalStrict(raw, config); err != nil {
				t.Fatal(err)
			}
			if _, err := (&api.YanetConfigCustomValidator{}).ValidateCreate(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			for _, box := range config.Spec.BoxTypes {
				refs, err := helpers.EnabledComponentsForBox(&config.Spec, box.Name)
				if err != nil {
					t.Fatal(err)
				}
				build := manifests.BuildContext{YanetName: "example", Namespace: "test", BoxType: box.Name, NodeName: "test-node"}
				spec := &api.YanetSpec{BoxType: box.Name}
				if example.disableNetwork {
					spec.Components = &api.YanetComponentsOverride{Dataplane: &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{
						"netconfig": {Enabled: helpers.PtrBool(false)}, "neighbour-sidecar": {Enabled: helpers.PtrBool(false)},
					}}}
				}
				build, err = manifests.WithRuntimeNetwork(build, &config.Spec, spec)
				if err != nil {
					t.Fatal(err)
				}
				var workloads []renderedWorkload
				for _, ref := range refs {
					component, err := helpers.ResolveBoxComponent(&config.Spec, spec, ref.Kind, ref.OperatorName)
					if err != nil {
						t.Fatal(err)
					}
					deployments, err := manifests.RenderDeployments(build, component, manifests.NewPatchRegistry(config.Spec.Patches))
					if err != nil {
						t.Fatalf("%s/%s: %v", box.Name, component.Name, err)
					}
					for _, deployment := range deployments {
						workloads = append(workloads, renderedWorkload{deployment: deployment, component: component})
					}
					for _, plan := range manifests.BuildServices(build, component) {
						if err := plan.Validate(); err != nil {
							t.Fatal(err)
						}
						if file == "v1alpha1-yanetconfig-full.yaml" &&
							(plan.Component == "netconfig" || plan.Component == "neighbour-sidecar" || plan.Component == "netlink-dataplane-sidecar") {
							t.Fatalf("%s: network sidecar must not have an automatic Service: %+v", box.Name, plan)
						}
					}
				}
				if file == "v1alpha1-yanetconfig-full.yaml" {
					assertNetworkSidecarExample(t, workloads, example.disableNetwork)
				}
			}
		})
	}
}

func assertNetworkSidecarExample(t *testing.T, workloads []renderedWorkload, disabled bool) {
	t.Helper()
	var dataplanes int
	for _, workload := range workloads {
		if workload.component.Name == "netconfig" || workload.component.Name == "neighbour-sidecar" {
			t.Fatalf("%s must share the dataplane Pod, not a separate Deployment", workload.component.Name)
		}
		pod := workload.deployment.Spec.Template.Spec
		for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
			for _, container := range containers {
				if container.StartupProbe != nil || container.ReadinessProbe != nil || container.LivenessProbe != nil {
					t.Fatalf("runtime %s must use application gRPC readiness, not Kubernetes probes", container.Name)
				}
			}
		}
		if workload.component.Kind != helpers.KindDataplane {
			continue
		}
		dataplanes++
		wantInit := 3
		if disabled {
			wantInit = 1
		}
		if pod.HostNetwork || len(pod.Containers) != 1 || pod.Containers[0].Name != "dataplane" ||
			len(pod.InitContainers) != wantInit || !strings.Contains(pod.InitContainers[0].Image, "/bird:") {
			t.Fatalf("expected private dataplane with BIRD and two network sidecars: %+v", pod)
		}
		for _, container := range pod.InitContainers {
			if container.RestartPolicy == nil || *container.RestartPolicy != corev1.ContainerRestartPolicyAlways ||
				(container.Lifecycle != nil && container.Lifecycle.PostStart != nil) {
				t.Fatalf("%s must start without a blocking init/PostStart step", container.Name)
			}
		}
		if disabled {
			continue
		}
		neighbour, netconfig := pod.InitContainers[1], pod.InitContainers[2]
		if neighbour.Image != "ghcr.io/yanet-platform/yanet2/neighbour-sidecar:example" ||
			netconfig.Image != "ghcr.io/yanet-platform/netconfig:example" {
			t.Fatalf("wrong sidecar images/order: %s, %s", neighbour.Image, netconfig.Image)
		}
		if !reflect.DeepEqual(neighbour.Args, []string{"-c", "/etc/yanet2/yanet-neighbour-sidecar.yaml"}) ||
			!reflect.DeepEqual(netconfig.Args, []string{"-config", "/etc/netconfig/config"}) {
			t.Fatalf("wrong runtime args: neighbour=%v netconfig=%v", neighbour.Args, netconfig.Args)
		}
		if netconfig.SecurityContext == nil || netconfig.SecurityContext.Privileged == nil || !*netconfig.SecurityContext.Privileged ||
			(neighbour.SecurityContext != nil && (neighbour.SecurityContext.Capabilities != nil ||
				(neighbour.SecurityContext.Privileged != nil && *neighbour.SecurityContext.Privileged))) {
			t.Fatal("only netconfig may receive interface-configuration privileges")
		}
		volumes := make(map[string]corev1.Volume)
		for _, volume := range pod.Volumes {
			volumes[volume.Name] = volume
		}
		seen := make(map[string]bool)
		for _, tt := range []struct {
			container corev1.Container
			paths     []string
		}{{neighbour, []string{"/etc/yanet2"}}, {netconfig, []string{"/etc/netconfig", "/etc/netplan/00-interfaces.yaml"}}} {
			if len(tt.container.Ports) != 0 || len(tt.container.VolumeMounts) != len(tt.paths) {
				t.Fatalf("unexpected listeners or mounts for %s: %+v", tt.container.Name, tt.container)
			}
			for _, path := range tt.paths {
				found := false
				for _, mount := range tt.container.VolumeMounts {
					if mount.MountPath != path {
						continue
					}
					volume := volumes[mount.Name]
					if !mount.ReadOnly || seen[mount.Name] {
						t.Fatalf("expected independently scoped read-only %s mount: %+v", path, mount)
					}
					if path == "/etc/netconfig" {
						if volume.ConfigMap == nil || volume.ConfigMap.Name == "" || volume.HostPath != nil {
							t.Fatalf("netconfig selector must come from a generated ConfigMap: %+v", volume)
						}
					} else if volume.HostPath == nil || volume.HostPath.Path != path {
						t.Fatalf("wrong host input for %s: %+v", path, volume)
					} else if path == "/etc/netplan/00-interfaces.yaml" && (volume.HostPath.Type == nil || *volume.HostPath.Type != corev1.HostPathFile) {
						t.Fatalf("netconfig must receive only the selected Netplan file: %+v", volume)
					}
					seen[mount.Name], found = true, true
				}
				if !found {
					t.Fatalf("%s missing config mount %s", tt.container.Name, path)
				}
			}
		}
		if len(netconfig.Env) != 0 {
			t.Fatalf("ConfigMap-backed netconfig must not receive host-config network env: %+v", netconfig.Env)
		}
		for _, sidecar := range workload.component.Sidecars {
			if sidecar.Name == "netconfig" && sidecar.Config.Inline != "source: netplan\nnetplan_path: /etc/netplan/00-interfaces.yaml\n" {
				t.Fatalf("netconfig must use the Netplan selector: %+v", sidecar.Config)
			}
		}
	}
	if dataplanes != 1 {
		t.Fatalf("expected one dataplane Deployment, got %d", dataplanes)
	}
}
