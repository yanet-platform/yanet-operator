package manifests

import (
	"fmt"
	"math"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
)

func TestControlplaneNumaLimit(t *testing.T) {
	for _, count := range []int32{-1, 5, math.MaxInt32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			component := &helpers.ResolvedComponent{
				Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
				Image: helpers.ResolvedImage{Name: "controlplane"}, Numa: count,
			}
			if deployments, err := RenderDeployments(ctx(), component, nil); err == nil || !strings.Contains(err.Error(), "numa") || len(deployments) != 0 {
				t.Errorf("unsupported NUMA count rendered workloads: %d / %v", len(deployments), err)
			}
			if services := BuildServices(ctx(), component); len(services) != 0 {
				t.Errorf("unsupported NUMA count rendered %d Services", len(services))
			}
			config, spec := runtimeNetworkFixture()
			config.Components.Controlplane.Numa = helpers.Int32Ptr(count)
			if _, err := WithRuntimeNetwork(ctx(), config, spec); err == nil || !strings.Contains(err.Error(), "numa") {
				t.Errorf("unsupported NUMA count rendered runtime gateways: %v", err)
			}
			if _, err := helpers.ResolveBoxServiceComponent(config, spec.BoxType, helpers.KindControlplane, ""); err == nil || !strings.Contains(err.Error(), "numa") {
				t.Errorf("unsupported persisted NUMA count accepted for shared Services: %v", err)
			}
		})
	}
}

func TestControlplaneNumaMaximumPhysicalDomains(t *testing.T) {
	config, spec := runtimeNetworkFixture()
	config.Components.Controlplane.Numa = helpers.Int32Ptr(4)
	spec.Components.Controlplane.DisabledNuma = []int32{0, 2}
	validateRenderConfig(t, config)
	build, err := WithRuntimeNetwork(ctx(), config, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.Gateways) != 2 || build.Gateways[0].Name != "numa1" || build.Gateways[1].Name != "numa3" {
		t.Fatalf("physical gateways changed: %+v", build.Gateways)
	}
	component, err := helpers.ResolveBoxComponent(config, spec, helpers.KindControlplane, "")
	if err != nil {
		t.Fatal(err)
	}
	component.Config = &api.ConfigSource{HostPath: "/etc/yanet2", Args: []string{"numa{numa}"}}
	deployments, err := RenderDeployments(build, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 2 {
		t.Fatalf("expected two active domains, got %d", len(deployments))
	}
	for index, domain := range []string{"1", "3"} {
		deployment := deployments[index]
		if deployment.Spec.Template.Labels[LabelNuma] != domain || !strings.HasSuffix(deployment.Name, "-numa"+domain) || deployment.Spec.Template.Spec.Containers[0].Args[0] != "numa"+domain {
			t.Fatalf("physical domain %s was compacted: %+v", domain, deployment)
		}
	}
	services := BuildServices(build, component)
	if len(services) != 4 || services[3].Name != "yanet-firewall-controlplane-numa3" || services[3].Selector[LabelNuma] != "3" {
		t.Fatalf("shared Services must retain all four physical domains: %+v", services)
	}
}

func TestControlplaneNumaDefaultAndExplicitZero(t *testing.T) {
	for _, explicitZero := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitZero), func(t *testing.T) {
			config, spec := runtimeNetworkFixture()
			config.Components.Controlplane.Numa = nil
			spec.Components.Controlplane.DisabledNuma = nil
			if explicitZero {
				config.Components.Controlplane.Numa = helpers.Int32Ptr(0)
			}
			build, err := WithRuntimeNetwork(ctx(), config, spec)
			if explicitZero {
				if err == nil || !strings.Contains(err.Error(), "controlplane.numa") {
					t.Fatalf("persisted explicit zero must be rejected: %v", err)
				}
				return
			}
			if err != nil || len(build.Gateways) != 1 || build.Gateways[0].Name != "numa0" {
				t.Fatalf("nil NUMA must retain the single-domain default: %+v / %v", build.Gateways, err)
			}
		})
	}
}
