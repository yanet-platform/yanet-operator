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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("YanetV2 owned resource watches", func() {
	It("repairs ConfigMaps and observes removal of a Pod ownership label", func() {
		const namespace = "yanetv2-watch-review"
		ensureNamespace(ctx, namespace)
		config := &yanetv2alpha1.YanetConfigV2{
			ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
			Spec:       minimalConfigV2(),
		}
		config.Spec.Patches = nil
		config.Spec.Components.Dataplane.Config = &yanetv2alpha1.ConfigSource{Inline: "expected config"}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, config) })

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "watch-review-node", Labels: map[string]string{"watch-review": "yes"},
		}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
		autoSync := true
		yanet := &yanetv2alpha1.YanetV2{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-review", Namespace: namespace},
			Spec: yanetv2alpha1.YanetSpec{
				BoxType: "release", AutoSync: &autoSync, NodeSelector: map[string]string{"watch-review": "yes"},
			},
		}
		Expect(k8sClient.Create(ctx, yanet)).To(Succeed())
		DeferCleanup(func() {
			cleanupYanetV2(ctx, namespace)
			cleanupDeployments(ctx, namespace)
		})

		cm := &corev1.ConfigMap{}
		Eventually(func() bool {
			list := &corev1.ConfigMapList{}
			if err := k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
				return false
			}
			cm = list.Items[0].DeepCopy()
			return cm.Data["config"] == "expected config"
		}, 15*time.Second, 100*time.Millisecond).Should(BeTrue())

		// Let creation-triggered reconciles settle. The correction below must
		// be triggered by the ConfigMap event, not a pending Deployment event.
		Eventually(func() error { return k8sClient.Get(ctx, client.ObjectKeyFromObject(yanet), yanet) },
			5*time.Second, 100*time.Millisecond).Should(Succeed())
		Eventually(func() bool {
			before := yanet.ResourceVersion
			time.Sleep(200 * time.Millisecond)
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(yanet), yanet) == nil && yanet.ResourceVersion == before
		}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		cm.Data["config"] = "manual drift"
		Expect(k8sClient.Update(ctx, cm)).To(Succeed())
		Eventually(func() string {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
				return ""
			}
			return cm.Data["config"]
		}, 5*time.Second, 100*time.Millisecond).Should(Equal("expected config"))

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "watch-review-pod", Namespace: namespace,
				Labels: map[string]string{manifests.LabelYanet: yanet.Name},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "docker.io/test/image:v1"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, pod) })
		podIsReported := func() bool {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(yanet), yanet)).To(Succeed())
			for _, names := range yanet.Status.Pods {
				for _, name := range names {
					if name == pod.Name {
						return true
					}
				}
			}
			return false
		}
		Eventually(podIsReported, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		Consistently(podIsReported, time.Second, 100*time.Millisecond).Should(BeTrue())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		delete(pod.Labels, manifests.LabelYanet)
		Expect(k8sClient.Update(ctx, pod)).To(Succeed())
		Eventually(podIsReported, 5*time.Second, 100*time.Millisecond).Should(BeFalse())
	})
})
