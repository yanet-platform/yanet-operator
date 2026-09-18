package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Ordered sidecar API", func() {
	It("round-trips explicit empty listeners and preserves atomic SSA order", func() {
		listeners := []api.OperatorListener{"grpc", "http"}
		empty := []api.OperatorListener{}
		config := &api.YanetConfigV2{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: minimalV2ConfigSpec()}
		config.Spec.Stop = true
		config.Spec.Components.Operators = []api.OperatorSpec{
			{Name: "legacy", Containers: []api.OperatorContainer{{Name: "worker", Image: api.ImageRef{Name: "test"}}}},
		}
		config.Spec.Components.Dataplane.Sidecars = []api.SidecarSpec{
			{Name: "monalive", Listeners: &listeners, Image: api.ImageRef{Name: "test"}},
			{Name: "client", Listeners: &empty, Image: api.ImageRef{Name: "test"}},
		}
		config.Spec.BoxTypes[0].Operators = map[string]api.BoxOperator{"legacy": {}}
		config.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{"monalive": {}, "client": {}}
		Expect(k8sClient.Create(ctx, config, client.FieldOwner("palette-author"))).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
		fresh := &api.YanetConfigV2{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Name).To(Equal("monalive"))
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Listeners).NotTo(BeNil())
		Expect(*fresh.Spec.Components.Dataplane.Sidecars[0].Listeners).To(ConsistOf(api.OperatorListener("grpc"), api.OperatorListener("http")))
		Expect(fresh.Spec.Components.Dataplane.Sidecars[1].Listeners).NotTo(BeNil())
		Expect(*fresh.Spec.Components.Dataplane.Sidecars[1].Listeners).To(BeEmpty())
		Expect(fresh.Spec.Components.Operators[0].Listeners).To(BeNil())
		component, err := helpers.ResolveBoxComponent(&fresh.Spec, &api.YanetSpec{BoxType: "release"}, helpers.KindDataplane, "")
		Expect(err).NotTo(HaveOccurred())
		build := manifests.BuildContextV2{YanetName: "placement-api", Namespace: whTestNS, BoxType: "release",
			OwnerRef: metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "YanetConfigV2", Name: config.Name, UID: config.UID}}
		deployments, err := manifests.RenderDeployments(build, component, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(deployments).To(HaveLen(1))
		Expect(deployments[0].Spec.Template.Spec.InitContainers).To(HaveLen(2))
		Expect(k8sClient.Create(ctx, deployments[0])).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, deployments[0])).To(Succeed()) })
		operator, err := helpers.ResolveBoxServiceComponent(&fresh.Spec, "release", helpers.KindSidecar, "monalive")
		Expect(err).NotTo(HaveOccurred())
		service := manifests.BuildServices(build, operator)[0].ToService(whTestNS, build.OwnerRef)
		Expect(k8sClient.Create(ctx, service)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, service)).To(Succeed()) })

		// Another manager cannot independently append, reorder or own one entry.
		reorder := client.RawPatch(types.ApplyPatchType, []byte(`{"apiVersion":"yanet.yanet-platform.io/v2alpha1","kind":"YanetConfigV2","metadata":{"name":"config"},"spec":{"components":{"dataplane":{"sidecars":[{"name":"client","image":{"name":"test"},"listeners":[]},{"name":"monalive","image":{"name":"test"},"listeners":["grpc","http"]}]}}}}`))
		Expect(apierrors.IsConflict(k8sClient.Patch(ctx, fresh, reorder, client.FieldOwner("another-author")))).To(BeTrue())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Name).To(Equal("monalive"))
		Expect(k8sClient.Patch(ctx, fresh, reorder, client.FieldOwner("another-author"), client.ForceOwnership)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Name).To(Equal("client"))
		Expect(fresh.Spec.Components.Dataplane.Sidecars[1].Name).To(Equal("monalive"))
		Expect(*fresh.Spec.Components.Dataplane.Sidecars[0].Listeners).To(BeEmpty())
	})
})
