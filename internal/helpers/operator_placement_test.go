package helpers

import (
	"reflect"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

func TestOperatorPlacementDeclaredOrder(t *testing.T) {
	config := fixtureConfig()
	box := &config.BoxTypes[1]
	config.Components.Dataplane.Sidecars = []api.SidecarSpec{
		{Name: "z-worker", Image: api.ImageRef{Name: "worker"}},
		{Name: "a-worker", Image: api.ImageRef{Name: "worker"}},
	}
	box.Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{
		"a-worker": {}, "z-worker": {},
	}
	yanet := &api.YanetSpec{BoxType: box.Name, Components: &api.YanetComponentsOverride{
		Dataplane: &api.YanetDataplaneOverride{Sidecars: map[string]api.YanetContainerOverride{
			"z-worker": {Enabled: PtrFalse(), Name: "custom", Tag: "v3"},
		}},
	}}
	for iteration := 0; iteration < 2; iteration++ {
		before := config.DeepCopy()
		dp, err := ResolveBoxComponent(config, yanet, KindDataplane, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(dp.Sidecars) != 2 || dp.Sidecars[0].Name != "z-worker" || dp.Sidecars[0].Enabled ||
			dp.Sidecars[0].PortIndex != 0 || dp.Sidecars[1].PortIndex != 1 || dp.Sidecars[1].Name != "a-worker" ||
			dp.Sidecars[0].Image.Name != "custom" {
			t.Fatalf("incorrect declared slots or overrides: %+v", dp.Sidecars)
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
	config.Components.Dataplane.Sidecars = nil
	config.BoxTypes[0].Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{}
	for _, name := range []string{"role-47893", "role-89356"} {
		config.Components.Dataplane.Sidecars = append(config.Components.Dataplane.Sidecars, api.SidecarSpec{
			Name: name, Image: api.ImageRef{Name: "test"},
		})
		config.BoxTypes[0].Components.Dataplane.Sidecars[name] = api.BoxDataplaneSidecar{}
	}
	for name := range config.BoxTypes[0].Components.Dataplane.Sidecars {
		if _, err := ResolveBoxServiceComponent(config, "release", KindSidecar, name); err == nil {
			t.Fatal("colliding role identities must fail before Service planning")
		}
	}
}
