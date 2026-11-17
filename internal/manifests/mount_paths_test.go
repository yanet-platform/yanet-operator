package manifests

import (
	"fmt"
	"strings"
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func renderMountPaths(pathForPrimary, pathForInit string) ([]*appsv1.Deployment, error) {
	patch := api.NamedPatch{Name: "mounts", Patch: runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{
  "spec": {"template": {"spec": {
    "volumes": [{"name":"first", "emptyDir":{}}, {"name":"second", "emptyDir":{}}, {"name":"shared", "emptyDir":{}}],
    "containers": [{"name":"controlplane", "volumeMounts":[
      {"name":"first", "mountPath":"/fixture"}, {"name":"second", "mountPath":%q}, {"$patch":"replace"}
    ]}],
    "initContainers": [{"name":"initialize", "image":"fixture:example", "volumeMounts":[
      {"name":"shared", "mountPath":"/fixture"}, {"name":"second", "mountPath":%q}
    ]}]
  }}}
}`, pathForPrimary, pathForInit))}}
	build := ctx()
	component := &helpers.ResolvedComponent{
		Kind: helpers.KindControlplane, Name: "controlplane", Enabled: true,
		Image: helpers.ResolvedImage{Name: "fixture", Tag: "example"}, Patches: []string{patch.Name},
	}
	return RenderDeployments(build, component, NewPatchRegistry([]api.NamedPatch{patch}))
}

func TestRenderRejectsDuplicateMountPathsPerContainer(t *testing.T) {
	for _, tc := range []struct{ container, primary, init string }{
		{"controlplane", "/fixture", "/other"},
		{"initialize", "/other", "/fixture"},
	} {
		t.Run(tc.container, func(t *testing.T) {
			workloads, err := renderMountPaths(tc.primary, tc.init)
			want := fmt.Sprintf("container %q has duplicate volumeMount mountPath %q", tc.container, "/fixture")
			if err == nil || !strings.Contains(err.Error(), want) || workloads != nil {
				t.Fatalf("preflight must reject duplicate mounts before returning any workload: count=%d err=%v", len(workloads), err)
			}
		})
	}
}
