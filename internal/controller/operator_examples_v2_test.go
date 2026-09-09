package controller

import (
	"context"
	"os"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	"sigs.k8s.io/yaml"
)

func TestOperatorPlacementExamples(t *testing.T) {
	for _, file := range []string{"v2alpha1-yanetconfig-full.yaml", "v2alpha1-yanetconfig-placement.yaml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("../../deploy/examples/" + file)
			if err != nil {
				t.Fatal(err)
			}
			config := &api.YanetConfigV2{}
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
				build := manifests.BuildContextV2{YanetName: "example", Namespace: "test", BoxType: box.Name, NodeName: "test-node", NumaCount: 2}
				var workloads []renderedWorkloadV2
				for _, ref := range refs {
					component, err := helpers.ResolveBoxComponent(&config.Spec, &api.YanetSpec{BoxType: box.Name}, ref.Kind, ref.OperatorName)
					if err != nil {
						t.Fatal(err)
					}
					deployments, err := manifests.RenderDeployments(build, component, manifests.NewPatchRegistry(config.Spec.Patches))
					if err != nil {
						t.Fatalf("%s/%s: %v", box.Name, component.Name, err)
					}
					for _, deployment := range deployments {
						workloads = append(workloads, renderedWorkloadV2{deployment: deployment, component: component})
					}
					for _, plan := range manifests.BuildServices(build, component) {
						if err := plan.Validate(); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := allocateHostNetworkPortsV2(workloads, config.Spec.HostNetworkPortRange); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
