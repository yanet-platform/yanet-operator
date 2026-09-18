package manifests

import (
	"reflect"
	"slices"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func runtimeNetworkFixture() (*api.YanetConfigSpec, *api.YanetSpec) {
	empty := []api.OperatorListener{}
	config := &api.YanetConfigSpec{
		Components: api.ComponentsSpec{
			Controlplane: api.ControlplaneSpec{Image: api.ImageRef{Name: "controlplane"}, Numa: helpers.Int32Ptr(2)},
			Dataplane: api.DataplaneSpec{Image: api.ImageRef{Name: "dataplane"}, Sidecars: []api.SidecarSpec{
				{Name: "bird", Image: api.ImageRef{Name: "bird"}, Listeners: &empty, Config: &api.ConfigSource{HostPath: "/etc/bird"}},
				{Name: "neighbour-sidecar", Image: api.ImageRef{Name: "neighbour"}, Config: &api.ConfigSource{HostPath: "/etc/yanet2"}},
				{Name: "netconfig", Image: api.ImageRef{Name: "netconfig"}, Listeners: &empty, Config: &api.ConfigSource{HostPath: "/etc/netconfig"}},
			}},
		},
		BoxTypes: []api.BoxType{{Name: "firewall", Components: api.BoxComponents{
			Controlplane: &api.BoxComponent{}, Dataplane: &api.BoxDataplane{Sidecars: map[string]api.BoxDataplaneSidecar{
				"bird": {}, "neighbour-sidecar": {}, "netconfig": {},
			}},
		}}},
	}
	spec := &api.YanetSpec{BoxType: "firewall", Components: &api.YanetComponentsOverride{
		Controlplane: &api.YanetControlplaneOverride{DisabledNuma: []int32{0}},
	}}
	return config, spec
}

func renderRuntimeDataplane(t *testing.T, config *api.YanetConfigSpec, spec *api.YanetSpec) (*appsv1.Deployment, *helpers.ResolvedComponent) {
	t.Helper()
	build, err := WithRuntimeNetwork(ctxV2(), config, spec)
	if err != nil {
		t.Fatal(err)
	}
	component, err := helpers.ResolveBoxComponent(config, spec, helpers.KindDataplane, "")
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := RenderDeployments(build, component, NewPatchRegistry(config.Patches))
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 {
		t.Fatalf("expected one dataplane, got %d", len(deployments))
	}
	return deployments[0], component
}

func TestRuntimeNetwork_AllSidecarsOwnSlotsAndHostEnvironment(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	before := config.DeepCopy()
	deployment, component := renderRuntimeDataplane(t, config, spec)
	containers := deployment.Spec.Template.Spec.InitContainers
	if len(containers) != 3 {
		t.Fatalf("expected three native sidecars, got %d", len(containers))
	}
	for index, want := range []struct{ image, grpc string }{
		{"bird", "[::]:8080"},
		{"neighbour", "[::]:8082"},
		{"netconfig", "[::]:8084"},
	} {
		container := containers[index]
		variables := envValues(container.Env)
		if container.Image != want.image || variables["YANET_SERVER_ENDPOINT"] != want.grpc {
			t.Fatalf("sidecar %d: image/env = %s/%v, want %+v", index, container.Image, variables, want)
		}
		if variables["YANET_KUBERNETES_GATEWAYS"] != `[{"name":"numa1","endpoint":"yanet-firewall-controlplane-numa1.yanet.svc.cluster.local:8080"}]` {
			t.Fatalf("sidecar %d lost the active physical NUMA gateway: %v", index, variables)
		}
		if container.RestartPolicy == nil || *container.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Fatalf("sidecar %d is not restartable", index)
		}
		services := BuildServices(ctxV2(), component.Sidecars[index])
		if index == 1 {
			if len(services) != 1 || services[0].Ports[0].Port != 8080 || len(container.Ports) != 1 || container.Ports[0].ContainerPort != 8082 ||
				services[0].Ports[0].TargetPortName != container.Ports[0].Name || variables["YANET_SERVER_ADVERTISE_ENDPOINT"] != "yanet-firewall-neighbour-sidecar.yanet.svc.cluster.local:8080" {
				t.Fatalf("bind/Service/advertise mismatch: %+v %+v", services, container)
			}
		} else if len(services) != 0 || len(container.Ports) != 0 || variables["YANET_SERVER_ADVERTISE_ENDPOINT"] != "" {
			t.Fatalf("listeners: [] must not create exposure: %+v %+v", services, container)
		}
	}
	if !reflect.DeepEqual(before, config) {
		t.Fatal("rendering mutated the shared palette")
	}
}

func TestRuntimeNetwork_SelectionDoesNotCompactSlots(t *testing.T) {
	for _, mode := range []string{"disabled", "unwired", "inline", "renamed"} {
		t.Run(mode, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			switch mode {
			case "disabled":
				spec.Components.Dataplane = &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{"bird": {Enabled: helpers.PtrFalse()}}}
			case "unwired":
				delete(config.BoxTypes[0].Components.Dataplane.Sidecars, "bird")
			case "inline":
				config.Components.Dataplane.Sidecars[0].Config = &api.ConfigSource{Inline: "opaque application configuration"}
			case "renamed":
				config.Components.Dataplane.Sidecars[0].Name = "arbitrary-name"
				delete(config.BoxTypes[0].Components.Dataplane.Sidecars, "bird")
				config.BoxTypes[0].Components.Dataplane.Sidecars["arbitrary-name"] = api.BoxDataplaneSidecar{}
			}
			deployment, _ := renderRuntimeDataplane(t, config, spec)
			seen := map[string]string{}
			for _, container := range deployment.Spec.Template.Spec.InitContainers {
				seen[container.Image] = envValues(container.Env)["YANET_SERVER_ENDPOINT"]
			}
			if seen["neighbour"] != "[::]:8082" || seen["netconfig"] != "[::]:8084" {
				t.Fatalf("selection changed reserved slots: %v", seen)
			}
			if mode == "renamed" && seen["bird"] != "[::]:8080" {
				t.Fatal("network contract depends on the sidecar name")
			}
		})
	}
}

func TestRuntimeNetwork_UsesFinalManagedConfigVolume(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   *api.ConfigSource
		fragment string
		wantBind string
	}{
		{name: "host", source: &api.ConfigSource{HostPath: "/etc/runtime"}, wantBind: "[::]:8080"},
		{name: "inline with unrelated host mount", source: &api.ConfigSource{Inline: "opaque"}},
		{name: "URL", source: &api.ConfigSource{URL: "https://example.com/config"}},
		{name: "no managed config"},
		{name: "host volume replaced by ConfigMap", source: &api.ConfigSource{HostPath: "/etc/runtime"}, fragment: `"volumes":[{"name":"config","hostPath":null,"configMap":{"name":"external"}}],`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			config.Components.Dataplane.Sidecars[0].Config = tc.source
			mounts := ""
			if tc.fragment == "" {
				tc.fragment = `"volumes":[{"name":"other","hostPath":{"path":"/etc/other"}}],`
				mounts = `,"volumeMounts":[{"name":"other","mountPath":"/etc/other"}]`
			}
			config.Patches = []api.NamedPatch{patch("config", `{"spec":{"template":{"spec":{`+tc.fragment+`
				"containers":[{"name":"bird","env":[{"name":"CUSTOM","value":"kept"}]`+mounts+`}]}}}}`)}
			config.BoxTypes[0].Components.Dataplane.Sidecars["bird"] = api.BoxDataplaneSidecar{Patches: []string{"config"}}
			deployment, _ := renderRuntimeDataplane(t, config, spec)
			variables := envValues(deployment.Spec.Template.Spec.InitContainers[0].Env)
			if variables["YANET_SERVER_ENDPOINT"] != tc.wantBind || variables["CUSTOM"] != "kept" {
				t.Fatalf("incorrect host-only overlay: %v", variables)
			}
			if tc.wantBind == "" && len(variables) != 1 {
				t.Fatalf("non-host config received managed network env: %v", variables)
			}
		})
	}
}

func TestRuntimeNetwork_ManagedEnvironmentWinsAfterDataplanePatches(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	initial, _ := renderRuntimeDataplane(t, config, spec)
	container := initial.Spec.Template.Spec.InitContainers[1]
	config.Patches = []api.NamedPatch{patch("endpoint", `{"spec":{"template":{"spec":{"initContainers":[{"name":"`+container.Name+`","env":[{"name":"YANET_SERVER_ENDPOINT","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},{"name":"CUSTOM","value":"retained"}]}]}}}}`)}
	config.BoxTypes[0].Components.Dataplane.Patches = []string{"endpoint"}
	deployment, _ := renderRuntimeDataplane(t, config, spec)
	variables := deployment.Spec.Template.Spec.InitContainers[1].Env
	count := 0
	for _, variable := range variables {
		if variable.Name == "YANET_SERVER_ENDPOINT" {
			count++
			if variable.Value != "[::]:8082" || variable.ValueFrom != nil {
				t.Fatalf("managed bind was overridden by a patch: %+v", variable)
			}
		}
	}
	if count != 1 || envValues(variables)["CUSTOM"] != "retained" {
		t.Fatalf("duplicate managed env or lost foreign env: %+v", variables)
	}
}

func TestRuntimeNetwork_AppendAndReorderFollowDeclaration(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	config.Components.Dataplane.Sidecars = append(config.Components.Dataplane.Sidecars,
		api.SidecarSpec{Name: "http-client", Image: api.ImageRef{Name: "http"}, Config: &api.ConfigSource{HostPath: "/etc/http"}, Listeners: &[]api.OperatorListener{"http"}})
	config.BoxTypes[0].Components.Dataplane.Sidecars["http-client"] = api.BoxDataplaneSidecar{}
	deployment, component := renderRuntimeDataplane(t, config, spec)
	for index, bind := range []string{"[::]:8080", "[::]:8082", "[::]:8084", "[::]:8087"} {
		if got := envValues(deployment.Spec.Template.Spec.InitContainers[index].Env)["YANET_SERVER_ENDPOINT"]; got != bind {
			t.Fatalf("append changed slot %d: got %s, want %s", index, got, bind)
		}
	}
	service := BuildServices(ctxV2(), component.Sidecars[3])
	http := deployment.Spec.Template.Spec.InitContainers[3]
	if len(service) != 1 || service[0].Ports[0].Port != 8081 || http.Ports[0].ContainerPort != 8087 || envValues(http.Env)["YANET_SERVER_ADVERTISE_ENDPOINT"] != "" {
		t.Fatalf("HTTP-only bind/Service contract: %+v %+v", service, http)
	}
	config.Components.Dataplane.Sidecars[0], config.Components.Dataplane.Sidecars[2] = config.Components.Dataplane.Sidecars[2], config.Components.Dataplane.Sidecars[0]
	reordered, _ := renderRuntimeDataplane(t, config, spec)
	if reflect.DeepEqual(deployment.Spec.Template, reordered.Spec.Template) {
		t.Fatal("reordering did not change the Pod template")
	}
	for index, image := range []string{"netconfig", "neighbour", "bird", "http"} {
		if reordered.Spec.Template.Spec.InitContainers[index].Image != image {
			t.Fatal("native startup order did not follow the palette")
		}
	}
	if got := envValues(reordered.Spec.Template.Spec.InitContainers[2].Env)["YANET_SERVER_ENDPOINT"]; got != "[::]:8084" {
		t.Fatalf("reordered BIRD kept its former slot: %s", got)
	}
}

func TestRuntimeNetwork_StandaloneRuntimeContracts(t *testing.T) {
	for _, tc := range []struct {
		role      helpers.ComponentKind
		name      string
		listeners *[]api.OperatorListener
		want      map[string]string
	}{
		{helpers.KindControlplane, "", nil, map[string]string{"YANET_GATEWAY_SERVER_ENDPOINT": "[::]:8080", "YANET_GATEWAY_SERVER_HTTP_ENDPOINT": "[::]:8081"}},
		{helpers.KindBirdAdapter, "", nil, map[string]string{"YANET_LISTEN_ADDR": "[::]:8080", "YANET_ROUTE_OPERATOR_ENDPOINT": "yanet-firewall-route.yanet.svc.cluster.local:8080"}},
		{helpers.KindOperator, "route", nil, map[string]string{"YANET_SERVER_ENDPOINT": "[::]:8080", "YANET_SERVER_ADVERTISE_ENDPOINT": "yanet-firewall-route.yanet.svc.cluster.local:8080"}},
		{helpers.KindOperator, "http", &[]api.OperatorListener{"http"}, map[string]string{"YANET_SERVER_ENDPOINT": "[::]:8081"}},
	} {
		t.Run(string(tc.role)+tc.name, func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			source := &api.ConfigSource{HostPath: "/etc/runtime"}
			config.Components.Controlplane.Config = source
			config.Components.BirdAdapter = &api.BirdAdapterComp{Image: api.ImageRef{Name: "adapter"}, Config: source}
			config.BoxTypes[0].Components.BirdAdapter = &api.BoxComponent{}
			if tc.role == helpers.KindOperator {
				config.Components.Operators = []api.OperatorSpec{{Name: tc.name, Listeners: tc.listeners, Containers: []api.OperatorContainer{{Name: "primary", Image: api.ImageRef{Name: "operator"}, Config: source}}}}
				config.BoxTypes[0].Operators = map[string]api.BoxOperator{tc.name: {}}
			}
			build, err := WithRuntimeNetwork(ctxV2(), config, spec)
			if err != nil {
				t.Fatal(err)
			}
			component, err := helpers.ResolveBoxComponent(config, spec, tc.role, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			deployments, err := RenderDeployments(build, component, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(deployments) != 1 {
				t.Fatalf("unexpected active NUMA/deployment count: %d", len(deployments))
			}
			variables := envValues(deployments[0].Spec.Template.Spec.Containers[0].Env)
			for key, want := range tc.want {
				if variables[key] != want {
					t.Fatalf("%s: got %q, want %q", key, variables[key], want)
				}
			}
			if tc.name == "http" && variables["YANET_SERVER_ADVERTISE_ENDPOINT"] != "" {
				t.Fatal("HTTP-only role advertised an absent gRPC Service")
			}
			if tc.role == helpers.KindOperator && variables["YANET_KUBERNETES_GATEWAYS"] != `[{"name":"numa1","endpoint":"yanet-firewall-controlplane-numa1.yanet.svc.cluster.local:8080"}]` {
				t.Fatalf("wrong physical NUMA selection: %v", variables)
			}
			if tc.role == helpers.KindControlplane && deployments[0].Labels[LabelNuma] != "1" {
				t.Fatal("physical NUMA index was compacted")
			}
			if variables["YANET_SERVER_HTTP_ENDPOINT"] != "" {
				t.Fatal("injected an unsupported generic HTTP runtime key")
			}
		})
	}
}

func TestRuntimeNetwork_ExplicitNUMAClearAndDefault(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	config.Components.Controlplane.DisabledNuma = []int32{0}
	spec.Components.Controlplane.DisabledNuma = []int32{}
	build, err := WithRuntimeNetwork(ctxV2(), config, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []GatewayEndpointOverride{
		{Name: "numa0", Endpoint: "yanet-firewall-controlplane-numa0.yanet.svc.cluster.local:8080"},
		{Name: "numa1", Endpoint: "yanet-firewall-controlplane-numa1.yanet.svc.cluster.local:8080"},
	}
	if !slices.Equal(build.Gateways, want) {
		t.Fatalf("explicit empty override did not restore all gateways: %+v", build.Gateways)
	}
	config.Components.Controlplane.Numa = nil
	build, err = WithRuntimeNetwork(ctxV2(), config, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(build.Gateways, want[:1]) {
		t.Fatalf("default NUMA must be exactly zero: %+v", build.Gateways)
	}
}
