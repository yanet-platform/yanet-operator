package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = DescribeTable("Retired v2 API fields", func(path []string, value interface{}, field string) {
	config := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: minimalConfigSpec()}
	config.Spec.Stop = true
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
	Expect(err).NotTo(HaveOccurred())
	object["apiVersion"], object["kind"] = api.GroupVersion.String(), "YanetConfig"
	Expect(unstructured.SetNestedField(object, value, path...)).To(Succeed())
	resource := &unstructured.Unstructured{Object: object}
	err = k8sClient.Create(ctx, resource, &client.CreateOptions{
		FieldValidation: metav1.FieldValidationStrict,
	})
	if err == nil {
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, resource)).To(Succeed()) })
	}
	Expect(err).To(HaveOccurred(), "removed fields must not be accepted or silently pruned")
	Expect(err.Error()).To(ContainSubstring(field))
},
	Entry("unused discovery", []string{"spec", "autoDiscovery"}, map[string]interface{}{"enable": true}, "autoDiscovery"),
	Entry("unimplemented config downloader", []string{"spec", "components", "dataplane", "config"}, map[string]interface{}{"url": "https://config.example/config"}, "url"),
	Entry("container-level pod IPC", []string{"spec", "components", "operators"}, []interface{}{
		map[string]interface{}{"name": "worker", "containers": []interface{}{
			map[string]interface{}{"name": "worker", "image": map[string]interface{}{"name": "worker"}, "hostIPC": true},
		}},
	}, "hostIPC"),
)

var _ = Describe("Ordered sidecar API", func() {
	It("round-trips explicit empty listeners and preserves atomic SSA order", func() {
		listeners := []api.OperatorListener{"grpc", "http"}
		empty := []api.OperatorListener{}
		config := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: minimalConfigSpec()}
		config.Spec.Stop = true
		config.Spec.Components.Operators = []api.OperatorSpec{
			{Name: "legacy", Containers: []api.OperatorContainer{{Name: "worker", Image: api.ImageRef{Name: "test"}}}},
		}
		config.Spec.Components.Dataplane.Sidecars = []api.SidecarSpec{
			{Name: "monalive", Listeners: &listeners, Image: api.ImageRef{Name: "test"}},
			{Name: "client", Listeners: &empty, Image: api.ImageRef{Name: "test"},
				Config: &api.ConfigSource{Inline: "opaque", MountPath: "/etc/client"}},
		}
		config.Spec.BoxTypes[0].Operators = map[string]api.BoxOperator{"legacy": {}}
		config.Spec.BoxTypes[0].Components.Dataplane.Sidecars = map[string]api.BoxDataplaneSidecar{"monalive": {}, "client": {}}
		Expect(k8sClient.Create(ctx, config, client.FieldOwner("palette-author"))).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
		fresh := &api.YanetConfig{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Name).To(Equal("monalive"))
		Expect(fresh.Spec.Components.Dataplane.Sidecars[0].Listeners).NotTo(BeNil())
		Expect(*fresh.Spec.Components.Dataplane.Sidecars[0].Listeners).To(ConsistOf(api.OperatorListener("grpc"), api.OperatorListener("http")))
		Expect(fresh.Spec.Components.Dataplane.Sidecars[1].Listeners).NotTo(BeNil())
		Expect(*fresh.Spec.Components.Dataplane.Sidecars[1].Listeners).To(BeEmpty())
		Expect(fresh.Spec.Components.Dataplane.Sidecars[1].Config.MountPath).To(Equal("/etc/client"))
		Expect(fresh.Spec.Components.Operators[0].Listeners).To(BeNil())
		component, err := helpers.ResolveBoxComponent(&fresh.Spec, &api.YanetSpec{BoxType: "release"}, helpers.KindDataplane, "")
		Expect(err).NotTo(HaveOccurred())
		build := manifests.BuildContext{YanetName: "placement-api", Namespace: whTestNS, BoxType: "release",
			OwnerRef: metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "YanetConfig", Name: config.Name, UID: config.UID}}
		deployments, err := manifests.RenderDeployments(build, component, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(deployments).To(HaveLen(1))
		Expect(deployments[0].Spec.Template.Spec.InitContainers).To(HaveLen(2))
		Expect(deployments[0].Spec.Template.Spec.InitContainers[1].VolumeMounts).To(HaveLen(1))
		Expect(deployments[0].Spec.Template.Spec.InitContainers[1].VolumeMounts[0].MountPath).To(Equal("/etc/client"))
		Expect(k8sClient.Create(ctx, deployments[0])).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, deployments[0])).To(Succeed()) })
		operator, err := helpers.ResolveBoxServiceComponent(&fresh.Spec, "release", helpers.KindSidecar, "monalive")
		Expect(err).NotTo(HaveOccurred())
		service := manifests.BuildServices(build, operator)[0].ToService(whTestNS, build.OwnerRef)
		Expect(k8sClient.Create(ctx, service)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, service)).To(Succeed()) })

		// Another manager cannot independently append, reorder or own one entry.
		reorder := client.RawPatch(types.ApplyPatchType, []byte(`{"apiVersion":"yanet.yanet-platform.io/v1alpha1","kind":"YanetConfig","metadata":{"name":"config"},"spec":{"components":{"dataplane":{"sidecars":[{"name":"client","image":{"name":"test"},"listeners":[]},{"name":"monalive","image":{"name":"test"},"listeners":["grpc","http"]}]}}}}`))
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
