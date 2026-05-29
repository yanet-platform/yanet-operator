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
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
)

var _ = Describe("Status Reporting E2E Tests", func() {
	ctx := context.Background()

	Context("V1 API - Status reporting", func() {
		const (
			ns       = "e2e-status-v1"
			nodeName = "status-v1-node"
		)
		var config *yanetv1alpha1.YanetConfig
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "status-config-v1",
					Namespace: ns,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					UpdateWindow: 0,
					Stop:         false,
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
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

		It("Should populate Status.Sync.Synced when autoSync=true", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-synced-v1", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: true,
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-synced-v1", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Synced)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Synced should track created deployments")
		})

		It("Should populate Status.Sync.Disabled when components have Enable=false", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "status-disabled-v1", Namespace: ns},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: true, // create deployments, but disabled (replicas=0)
					Dataplane: yanetv1alpha1.Dep{
						Enable: false,
						Image:  "yanet-dataplane",
					},
					Controlplane: yanetv1alpha1.Dep{
						Enable: false,
						Image:  "yanet-controlplane",
					},
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-disabled-v1", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Disabled)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Disabled should track replicas=0 deployments")
		})
	})

	Context("V2 API - Status reporting", func() {
		const (
			ns        = "e2e-status-v2"
			nodeName  = "status-v2-node"
			selKey    = "e2e-status-v2"
			selVal    = "yes"
			boxTypeNm = "status-box"
		)
		var config *yanetv2alpha1.YanetConfigV2
		var node *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "status-config-v2",
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
			cleanupServices(ctx, ns)
			if node != nil {
				_ = k8sClient.Delete(ctx, node)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should populate Status.Sync.Synced when autoSync=true", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "status-synced-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-synced-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Synced)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Synced should be populated after reconciliation")
		})

		It("Should track NodesStatus per node", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "status-nodes-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() bool {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-nodes-v2", Namespace: ns}, current); err != nil {
					return false
				}
				_, ok := current.Status.NodesStatus[nodeName]
				return ok
			}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(),
				"Status.NodesStatus should have an entry for the node")
		})

		It("Should track Services in Status", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "status-services-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-services-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Services)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Services should track generated services")
		})

		It("Should report OutOfSync when autoSync=false and deployments missing", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "status-outofsync-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false), // do not create; report drift
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-outofsync-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.OutOfSync)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"missing deployments must be reported as OutOfSync when autoSync=false")

			// And no deployments should actually be created.
			Expect(countDeployments(ctx, ns)).Should(Equal(0))
		})

		It("Should update Status when toggling autoSync false->true", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "status-toggle-v2", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			// Initially OutOfSync (nothing created).
			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.OutOfSync)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0))

			// Toggle to true.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, yanet)).Should(Succeed())
			yanet.Spec.AutoSync = helpers.PtrBool(true)
			Expect(k8sClient.Update(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "status-toggle-v2", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.Sync.Synced)
			}, 15*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0),
				"Status.Sync.Synced should grow after enabling autoSync")
		})
	})

	Context("V2 API - Multi-node Status tracking", func() {
		const (
			ns        = "e2e-status-multinode"
			selKey    = "e2e-status-multinode"
			selVal    = "yes"
			boxTypeNm = "multinode-box"
			node1     = "status-multinode-1"
			node2     = "status-multinode-2"
		)
		var config *yanetv2alpha1.YanetConfigV2
		var n1, n2 *corev1.Node

		BeforeEach(func() {
			ensureNamespace(ctx, ns)

			config = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multinode-config",
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

			n1 = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: node1, Labels: map[string]string{selKey: selVal}},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{"hugepages-1Gi": resourceMustParse("10Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, n1)).Should(Succeed())

			n2 = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: node2, Labels: map[string]string{selKey: selVal}},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{"hugepages-1Gi": resourceMustParse("10Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, n2)).Should(Succeed())
		})

		AfterEach(func() {
			cleanupYanetV2(ctx, ns)
			cleanupDeployments(ctx, ns)
			cleanupServices(ctx, ns)
			if n1 != nil {
				_ = k8sClient.Delete(ctx, n1)
			}
			if n2 != nil {
				_ = k8sClient.Delete(ctx, n2)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should track multiple nodes in Status.NodesStatus", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "multinode-yanet", Namespace: ns},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType:      boxTypeNm,
					NodeSelector: map[string]string{selKey: selVal},
					AutoSync:     helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			Eventually(func() int {
				current := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "multinode-yanet", Namespace: ns}, current); err != nil {
					return 0
				}
				return len(current.Status.NodesStatus)
			}, 15*time.Second, 500*time.Millisecond).Should(Equal(2),
				"Status.NodesStatus should track both nodes")

			current := &yanetv2alpha1.YanetV2{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "multinode-yanet", Namespace: ns}, current)).Should(Succeed())
			Expect(current.Status.NodesStatus).Should(HaveKey(node1))
			Expect(current.Status.NodesStatus).Should(HaveKey(node2))
		})
	})
})
