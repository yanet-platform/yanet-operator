package helpers

import (
	"reflect"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

func TestOperatorPlacementDeclaredOrder(t *testing.T) {
	config := fixtureConfig()
	box := &config.BoxTypes[1]
	box.Operators = map[string]api.BoxOperator{
		"route":    {Placement: api.OperatorPlacementDataplane},
		"antiddos": {Placement: api.OperatorPlacementDataplane},
	}
	yanet := &api.YanetSpec{BoxType: box.Name, Components: &api.YanetComponentsOverride{
		Operators: map[string]api.YanetComponentOverride{"antiddos": {Enabled: PtrFalse(),
			Containers: map[string]api.YanetContainerOverride{"operator": {Name: "custom", Tag: "v3"}}}},
	}}
	for iteration := 0; iteration < 2; iteration++ {
		before := config.DeepCopy()
		dp, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(dp.ColocatedOperators) != 2 || dp.ColocatedOperators[0].Name != "antiddos" || dp.ColocatedOperators[0].Enabled ||
			dp.ColocatedOperators[0].PortIndex != 0 || dp.ColocatedOperators[1].PortIndex != 1 ||
			dp.ColocatedOperators[0].Containers[0].Image.Name != "custom" {
			t.Fatalf("incorrect declared slots or overrides: %+v", dp.ColocatedOperators)
		}
		if !reflect.DeepEqual(before, config) {
			t.Fatal("resolution mutated the palette")
		}
		config.Components.Operators[0], config.Components.Operators[1] = config.Components.Operators[1], config.Components.Operators[0]
	}
}

func TestOperatorPlacementRejectsRoleIdentityCollision(t *testing.T) {
	// These distinct valid names share the first eight SHA-256 hex digits.
	// Even a disabled role must not inherit another role's membership/target.
	config := fixtureConfig()
	config.Components.Operators = nil
	config.BoxTypes[0].Operators = map[string]api.BoxOperator{}
	for _, name := range []string{"role-47893", "role-89356"} {
		config.Components.Operators = append(config.Components.Operators, api.OperatorSpec{
			Name: name, Containers: []api.OperatorContainer{{Name: "worker", Image: api.ImageRef{Name: "test"}}},
		})
		config.BoxTypes[0].Operators[name] = api.BoxOperator{Placement: api.OperatorPlacementDataplane}
	}
	for name := range config.BoxTypes[0].Operators {
		if _, err := ResolveBoxServiceComponent(config, "release", KindOperator, name); err == nil {
			t.Fatal("colliding role identities must fail before Service planning")
		}
	}
}
