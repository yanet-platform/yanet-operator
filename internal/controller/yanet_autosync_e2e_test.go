/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"k8s.io/apimachinery/pkg/runtime"
)

// ensureNamespace creates a namespace if it does not exist (envtest
// does not garbage-collect namespaces, so reusing a fixed name across
// runs is fine).
func ensureNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

var _ = Describe("AutoSync Behavior E2E Tests", func() {
	ctx := context.Background()

	// Each context uses a dedicated namespace + unique node-selector
	// label so its Deployments and Nodes never collide with the
	// throttling suite (which runs in "default") or with each other.

	Context("V1 API - AutoSync behavior", func() {
		const (
			ns       = "e2e-autosync-v1"
			nodeName = "autosync-v1-node"
		)
		var config *yanetv1alpha1.YanetConfig
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "autosync-config-v1",
					Namespace: ns,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					UpdateWindow: 0,
					Stop:         false,
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						"hugepages-1Gi": resourceMustParse("10Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanetV1(ctx, ns)
			cleanupDeployments(ctx, ns)
			if node != nil {
				_ = k8sClient.Delete(ctx, node)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should not create Deployments when autoSync=false", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-false-v1", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: false,
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			// Give the reconciler time; expect no Deployments appear.
			Consistently(func() int {
				return countDeployments(ctx, ns)
			}, 3*time.Second, 500*time.Millisecond).Should(Equal(0),
				"no deployments should be created when autoSync=false")
		})

		It("Should create Deployments when autoSync=true", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-true-v1", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: true,
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				return countDeployments(ctx, ns)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"deployments should be created when autoSync=true")
		})

		It("Should create Deployments when toggling autoSync from false to true", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-toggle-v1", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: false,
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Consistently(func() int {
				return countDeployments(ctx, ns)
			}, 2*time.Second, 500*time.Millisecond).Should(Equal(0))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autosync-toggle-v1", Namespace: ns}, yanet)).Should(Succeed())
			yanet.Spec.AutoSync = true
			Expect(k8sClient.Update(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				return countDeployments(ctx, ns)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"deployments should be created after toggling autoSync to true")
		})
	})

	Context("V2 API - AutoSync behavior", func() {
		const (
			ns        = "e2e-autosync-v2"
			nodeName  = "autosync-v2-node"
			selKey    = "e2e-autosync-v2"
			selVal    = "yes"
			boxTypeNm = "autosync-box"
		)
		var config *yanetv2alpha1.YanetConfigV2
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "autosync-config-v2",
					Namespace: ns,
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/cp", Tag: "v1"},
							Port:  8080,
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/dp", Tag: "v1"},
							Port:  8081,
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name: boxTypeNm,
						Components: yanetv2alpha1.BoxComponents{
							Controlplane: &yanetv2alpha1.BoxComponent{},
							Dataplane:    &yanetv2alpha1.BoxComponent{},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{selKey: selVal},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						"hugepages-1Gi": resourceMustParse("10Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanetV2(ctx, ns)
			cleanupDeployments(ctx, ns)
			if node != nil {
				_ = k8sClient.Delete(ctx, node)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should not create Deployments when autoSync=false", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-false-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Consistently(func() int {
				return countDeployments(ctx, ns)
			}, 3*time.Second, 500*time.Millisecond).Should(Equal(0),
				"no deployments should be created when autoSync=false")
		})

		It("Should create Deployments when autoSync=true", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-true-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				return countDeployments(ctx, ns)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"deployments should be created when autoSync=true")
		})

		It("Should create Deployments when toggling autoSync from false to true", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-toggle-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Consistently(func() int {
				return countDeployments(ctx, ns)
			}, 2*time.Second, 500*time.Millisecond).Should(Equal(0))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autosync-toggle-v2", Namespace: ns}, yanet)).Should(Succeed())
			yanet.Spec.AutoSync = helpers.PtrBool(true)
			Expect(k8sClient.Update(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				return countDeployments(ctx, ns)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"deployments should be created after toggling autoSync to true")
		})
	})

	Context("V2 API - AutoSync with patches", func() {
		const (
			ns        = "e2e-autosync-patch"
			nodeName  = "autosync-patch-node"
			selKey    = "e2e-autosync-patch"
			selVal    = "yes"
			boxTypeNm = "patched-box"
		)
		var config *yanetv2alpha1.YanetConfigV2
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "autosync-patch-config",
					Namespace: ns,
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "docker.io/test/dp", Tag: "v1"},
						},
					},
					Patches: []yanetv2alpha1.NamedPatch{{
						Name: "test-patch",
						Patch: runtime.RawExtension{
							Raw: []byte(`{"spec":{"template":{"metadata":{"annotations":{"patched":"true"}}}}}`),
						},
					}},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name: boxTypeNm,
						Components: yanetv2alpha1.BoxComponents{
							Controlplane: &yanetv2alpha1.BoxComponent{
								Patches: []string{"test-patch"},
							},
							Dataplane: &yanetv2alpha1.BoxComponent{},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{selKey: selVal},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						"hugepages-1Gi": resourceMustParse("10Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanetV2(ctx, ns)
			cleanupDeployments(ctx, ns)
			if node != nil {
				_ = k8sClient.Delete(ctx, node)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should apply patches when autoSync=true", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "autosync-with-patch", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(ns)); err != nil {
					return false
				}
				for i := range depList.Items {
					ann := depList.Items[i].Spec.Template.Annotations
					if ann != nil && ann["patched"] == "true" {
						return true
					}
				}
				return false
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(),
				"patch should be applied to deployment when autoSync=true")
		})
	})
})
