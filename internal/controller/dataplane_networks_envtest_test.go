package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dataplane network attachments", func() {
	It("round-trips replacement and explicit clearing through the API and renders device reservations", func() {
		testContext := context.Background()
		config := &api.YanetConfig{ObjectMeta: metav1.ObjectMeta{Name: api.YanetConfigName}, Spec: minimalConfigSpec()}
		config.Spec.Stop = true
		config.Spec.Components.Dataplane.Networks = []api.NetworkAttachment{
			{Name: "pf", Interface: "eth2", ResourceName: "example.net/pf"},
			{Name: "pf", Interface: "eth4", ResourceName: "example.net/pf"},
		}
		Expect(k8sClient.Create(testContext, config)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(testContext, config)).To(Succeed()) })
		installation := &api.Yanet{ObjectMeta: metav1.ObjectMeta{Name: "network-api", Namespace: whTestNS},
			Spec: api.YanetSpec{BoxType: "release", Enabled: helpers.PtrFalse(), Components: &api.YanetComponentsOverride{
				Dataplane: &api.YanetDataplaneOverride{Networks: []api.NetworkAttachment{}},
			}}}
		Expect(k8sClient.Create(testContext, installation)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(testContext, installation)).To(Succeed()) })
		fresh := &api.Yanet{}
		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(installation), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Networks).NotTo(BeNil())
		Expect(fresh.Spec.Components.Dataplane.Networks).To(BeEmpty())
		palette := &api.YanetConfig{}
		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(config), palette)).To(Succeed())
		Expect(palette.Spec.Components.Dataplane.Networks).To(HaveLen(2))
		component, err := helpers.ResolveBoxComponent(&palette.Spec, &fresh.Spec, helpers.KindDataplane, "")
		Expect(err).NotTo(HaveOccurred())
		build := manifests.BuildContext{YanetName: installation.Name, Namespace: whTestNS, BoxType: "release",
			OwnerRef: metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "Yanet", Name: installation.Name, UID: installation.UID}}
		deployments, err := manifests.RenderDeployments(build, component, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(deployments[0].Spec.Template.Annotations).NotTo(HaveKey("k8s.v1.cni.cncf.io/networks"))
		Expect(deployments[0].Spec.Template.Spec.Containers[0].Resources.Limits).NotTo(HaveKey(corev1.ResourceName("example.net/pf")))

		fresh.Spec.Components.Dataplane.Networks = []api.NetworkAttachment{{Name: "custom", Interface: "eth6", ResourceName: "example.net/custom"}}
		Expect(k8sClient.Update(testContext, fresh)).To(Succeed())
		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(installation), fresh)).To(Succeed())
		component, err = helpers.ResolveBoxComponent(&palette.Spec, &fresh.Spec, helpers.KindDataplane, "")
		Expect(err).NotTo(HaveOccurred())
		deployments, err = manifests.RenderDeployments(build, component, nil)
		Expect(err).NotTo(HaveOccurred())
		resources := deployments[0].Spec.Template.Spec.Containers[0].Resources
		requested, limited := resources.Requests["example.net/custom"], resources.Limits["example.net/custom"]
		Expect(requested.Value()).To(Equal(int64(1)))
		Expect(limited.Value()).To(Equal(int64(1)))
		Expect(resources.Limits).NotTo(HaveKey(corev1.ResourceName("example.net/pf")))
		Expect(k8sClient.Create(testContext, deployments[0])).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(testContext, deployments[0])).To(Succeed()) })

		fresh.Spec.Components.Dataplane.Networks = append(fresh.Spec.Components.Dataplane.Networks, fresh.Spec.Components.Dataplane.Networks[0])
		Expect(k8sClient.Update(testContext, fresh)).NotTo(Succeed(), "duplicate interface names must be rejected by admission")
		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(installation), fresh)).To(Succeed())
		Expect(fresh.Spec.Components.Dataplane.Networks).To(HaveLen(1))
	})
})
