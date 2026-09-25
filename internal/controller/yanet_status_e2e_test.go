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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
)

var _ = Describe("Status Reporting E2E Tests", func() {
	testContext := context.Background()

	Context("Status reporting", func() {
		const (
			ns        = "e2e-status-v2"
			nodeName  = "status-v2-node"
			selKey    = "e2e-status-v2"
			selVal    = "yes"
			boxTypeNm = "status-box"
		)
		var config *yanetv1alpha1.YanetConfig
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(testContext, ns)

			config = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "docker.io/test/cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "docker.io/test/dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name: boxTypeNm,
						Components: yanetv1alpha1.BoxComponents{
							Controlplane: &yanetv1alpha1.BoxComponent{},
							Dataplane:    &yanetv1alpha1.BoxDataplane{},
						},
					}},
				},
			}
			Expect(k8sClient.Create(testContext, config)).Should(Succeed())

			// Give YanetConfigReconciler time to update GlobalConfig snapshot
			time.Sleep(1000 * time.Millisecond)

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
			Expect(k8sClient.Create(testContext, node)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanet(testContext, ns)
			cleanupDeployments(testContext, ns)
			cleanupServices(testContext, ns)
			if node != nil {
				_ = k8sClient.Delete(testContext, node)
			}
			if config != nil {
				_ = k8sClient.Delete(testContext, config)
			}
		})

		It("Should populate Status.Sync.Synced when autoSync=true", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-synced-v2", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-synced-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Synced)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Synced should be populated after reconciliation")
		})

		It("Should track NodesStatus per node", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-nodes-v2", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			Eventually(func() bool {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-nodes-v2", Namespace: ns}, current); err != nil {
					return false
				}
				_, ok := current.Status.NodesStatus[nodeName]
				return ok
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(),
				"Status.NodesStatus should have an entry for the node")
		})

		It("Should track Services in Status", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-services-v2", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-services-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Services)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Services should track generated services")
		})

		It("Should report OutOfSync when autoSync=false and deployments missing", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-outofsync-v2", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false), // do not create; report drift
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-outofsync-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.OutOfSync)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"missing deployments must be reported as OutOfSync when autoSync=false")

			// And no deployments should actually be created.
			count, err := countDeployments(testContext, ns)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).Should(Equal(0))
		})

		It("Should update Status when toggling autoSync false->true", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-toggle-v2", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			// Initially OutOfSync (nothing created).
			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.OutOfSync)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0))

			// Toggle to true.
			Expect(k8sClient.Get(testContext, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, yanet)).Should(Succeed())
			yanet.Spec.AutoSync = helpers.PtrBool(true)
			Expect(k8sClient.Update(testContext, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Synced)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Synced should grow after enabling autoSync")
		})
	})

	Context("Multi-node Status tracking", func() {
		const (
			ns        = "e2e-status-multinode"
			selKey    = "e2e-status-multinode"
			selVal    = "yes"
			boxTypeNm = "multinode-box"
			node1     = "status-multinode-1"
			node2     = "status-multinode-2"
		)
		var config *yanetv1alpha1.YanetConfig
		var n1, n2 *corev1.Node

		BeforeEach(func() {
			ensureNamespace(testContext, ns)

			config = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "docker.io/test/cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "docker.io/test/dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name: boxTypeNm,
						Components: yanetv1alpha1.BoxComponents{
							Controlplane: &yanetv1alpha1.BoxComponent{},
							Dataplane:    &yanetv1alpha1.BoxDataplane{},
						},
					}},
				},
			}
			Expect(k8sClient.Create(testContext, config)).Should(Succeed())

			// Give YanetConfigReconciler time to update GlobalConfig snapshot
			time.Sleep(1000 * time.Millisecond)

			n1 = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: node1, Labels: map[string]string{selKey: selVal}},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{"hugepages-1Gi": resourceMustParse("10Gi")},
				},
			}
			Expect(k8sClient.Create(testContext, n1)).Should(Succeed())

			n2 = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: node2, Labels: map[string]string{selKey: selVal}},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{"hugepages-1Gi": resourceMustParse("10Gi")},
				},
			}
			Expect(k8sClient.Create(testContext, n2)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanet(testContext, ns)
			cleanupDeployments(testContext, ns)
			cleanupServices(testContext, ns)
			if n1 != nil {
				_ = k8sClient.Delete(testContext, n1)
			}
			if n2 != nil {
				_ = k8sClient.Delete(testContext, n2)
			}
			if config != nil {
				_ = k8sClient.Delete(testContext, config)
			}
		})

		It("Should track multiple nodes in Status.NodesStatus", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "multinode-yanet", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, types.NamespacedName{Name: "multinode-yanet", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.NodesStatus)
			}, 15*time.Second, 500*time.Millisecond).Should(Equal(2),
				"Status.NodesStatus should track both nodes")

			current := &yanetv1alpha1.Yanet{}
			Expect(k8sClient.Get(testContext, types.NamespacedName{Name: "multinode-yanet", Namespace: ns}, current)).Should(Succeed())
			Expect(current.Status.NodesStatus).Should(HaveKey(node1))
			Expect(current.Status.NodesStatus).Should(HaveKey(node2))
		})
	})
})
