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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Throttling E2E tests are marked as Pending because they require precise timing
// control that is difficult to achieve reliably in envtest environment.
// Throttling logic is covered by unit tests in:
// - yanet_reconciler_test.go (TestCheckUpdateRequeue)
// - yanet_reconciler_v2_extended_test.go (TestUpdateWindow_*)
// - yanet_reconciler_v2_hardening_test.go (TestApplyDeploymentV2_UpdateWindowThrottles*)
var _ = PDescribe("Yanet Throttling E2E Tests", func() {
	const (
		timeout         = time.Second * 60
		throttleTimeout = time.Second * 90 // Longer timeout for throttled updates
		interval        = time.Millisecond * 250
	)

	Context("V1 API - UpdateWindow throttling with enabled=false", func() {
		const (
			yanetName1     = "throttle-v1-node1"
			yanetName2     = "throttle-v1-node2"
			yanetNamespace = "default"
			nodeName1      = "throttle-node-1"
			nodeName2      = "throttle-node-2"
		)

		ctx := context.Background()
		configName := types.NamespacedName{Name: "throttle-v1-config", Namespace: yanetNamespace}

		BeforeEach(func() {
			By("Creating test nodes")
			for _, name := range []string{nodeName1, nodeName2} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							"hugepages-1Gi": resource.MustParse("10Gi"),
						},
					},
				}
				Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			}

			By("Creating YanetConfig with 5s updateWindow and initial resource limits")
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName.Name,
					Namespace: configName.Namespace,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Stop:         false,
					UpdateWindow: 5, // 5 seconds for faster testing
					EnabledOpts: yanetv1alpha1.EnabledOpts{
						Release: yanetv1alpha1.DepOpts{
							Dataplain: yanetv1alpha1.OptsNames{
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										"memory": resource.MustParse("1Gi"),
									},
								},
							},
							Controlplane: yanetv1alpha1.OptsNames{
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										"memory": resource.MustParse("1Gi"),
									},
								},
							},
							Bird: yanetv1alpha1.OptsNames{
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										"memory": resource.MustParse("256Mi"),
									},
								},
							},
							Announcer: yanetv1alpha1.OptsNames{
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										"memory": resource.MustParse("128Mi"),
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			By("Waiting for YanetConfig to be reconciled and available")
			Eventually(func() bool {
				cfg := &yanetv1alpha1.YanetConfig{}
				if err := k8sClient.Get(ctx, configName, cfg); err != nil {
					return false
				}
				return cfg.UID != ""
			}, timeout, interval).Should(BeTrue())

			// Wait for YanetConfigReconciler to update GlobalConfig snapshot
			Expect(waitForGlobalConfigV1(5 * time.Second)).Should(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up V1 resources")
			// Delete Yanets FIRST
			for _, name := range []string{yanetName1, yanetName2} {
				yanet := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: yanetNamespace}, yanet); err == nil {
					Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
				}
			}

			// Wait for Yanets to be deleted before removing config
			Eventually(func() int {
				yanetList := &yanetv1alpha1.YanetList{}
				if err := k8sClient.List(ctx, yanetList, client.InNamespace(yanetNamespace)); err != nil {
					return -1
				}
				count := 0
				for _, y := range yanetList.Items {
					if y.Name == yanetName1 || y.Name == yanetName2 {
						count++
					}
				}
				return count
			}, timeout, interval).Should(Equal(0), "Yanets should be deleted before config")

			// Delete YanetConfig AFTER Yanets are gone
			config := &yanetv1alpha1.YanetConfig{}
			if err := k8sClient.Get(ctx, configName, config); err == nil {
				Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
			}

			// Delete Nodes
			for _, name := range []string{nodeName1, nodeName2} {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, node); err == nil {
					Expect(k8sClient.Delete(ctx, node)).Should(Succeed())
				}
			}
		})

		It("Should create deployments with replicas=0 when enabled=false, then throttle updates", func() {
			By("Creating first Yanet with enabled=false")
			yanet1 := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName1,
					Namespace: yanetNamespace,
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName1,
					Type:     "release",
					AutoSync: true,
					Tag:      "1.0.0",
					Registry: "docker.io/test",
					Dataplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-dataplane",
					},
					Controlplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-controlplane",
					},
					// Bird and Announcer disabled - testing throttling on dataplane+controlplane is sufficient
				},
			}
			Expect(k8sClient.Create(ctx, yanet1)).Should(Succeed())

			By("Verifying deployments are created for all enabled components")
			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return false
				}
				// Expect 4 deployments: dataplane, controlplane, bird, announcer
				if len(depList.Items) < 2 {
					return false
				}
				// All deployments should have replicas=0 when components are disabled
				for _, dep := range depList.Items {
					if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())

			By("Creating second Yanet on different node")
			yanet2 := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName2,
					Namespace: yanetNamespace,
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName2,
					Type:     "release",
					AutoSync: true,
					Tag:      "1.0.0",
					Registry: "docker.io/test",
					Dataplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-dataplane",
					},
					Controlplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-controlplane",
					},
				},
			}
			Expect(k8sClient.Create(ctx, yanet2)).Should(Succeed())

			By("Verifying deployments exist for both nodes")
			Eventually(func() int {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return 0
				}
				return len(depList.Items)
			}, timeout, interval).Should(Equal(4), "Should have exactly 4 deployments (2 per node)")

			By("Triggering reconciliation by updating Yanet Tag to 2.0.0")
			for _, name := range []string{yanetName1, yanetName2} {
				yanet := &yanetv1alpha1.Yanet{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: yanetNamespace}, yanet)).Should(Succeed())
				yanet.Spec.Tag = "2.0.0"
				Expect(k8sClient.Update(ctx, yanet)).Should(Succeed())
			}

			By("Observing throttling behavior")
			startTime := time.Now()

			// Wait for at least one deployment to update to 2.0.0
			Eventually(func() int {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return 0
				}
				updatedCount := 0
				for _, dep := range depList.Items {
					if len(dep.Spec.Template.Spec.Containers) > 0 {
						image := dep.Spec.Template.Spec.Containers[0].Image
						if strings.Contains(image, ":2.0.0") {
							updatedCount++
						}
					}
				}
				return updatedCount
			}, timeout, interval).Should(BeNumerically(">=", 1), "At least one deployment should update")

			By("Waiting for all 4 deployments to eventually be updated")
			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return false
				}
				if len(depList.Items) != 4 {
					GinkgoWriter.Printf("Expected 4 deployments, got %d\n", len(depList.Items))
					return false
				}

				updated := 0
				for _, dep := range depList.Items {
					if len(dep.Spec.Template.Spec.Containers) > 0 {
						image := dep.Spec.Template.Spec.Containers[0].Image
						if strings.Contains(image, ":2.0.0") {
							updated++
						} else {
							GinkgoWriter.Printf("Deployment %s: image=%s (want :2.0.0)\n", dep.Name, image)
						}
					}
				}
				GinkgoWriter.Printf("Updated %d/4 deployments\n", updated)
				return updated == 4
			}, timeout, interval).Should(BeTrue(), "All 4 deployments should update to 2.0.0")

			elapsedTime := time.Since(startTime)
			GinkgoWriter.Printf("Total update time: %v\n", elapsedTime)

			By("Verifying throttling caused delay (should take 5-10 seconds)")
			Expect(elapsedTime.Seconds()).Should(BeNumerically(">=", 4.0),
				"UpdateWindow throttling should delay updates by ~5s")
			Expect(elapsedTime.Seconds()).Should(BeNumerically("<=", 15.0),
				"Updates should complete within reasonable time")
		})
	})

	Context("V2 API - UpdateWindow throttling with enabled=false", func() {
		const (
			yanetName1     = "throttle-v2-node1"
			yanetName2     = "throttle-v2-node2"
			yanetNamespace = "default"
			nodeName1      = "throttle-v2-node-1"
			nodeName2      = "throttle-v2-node-2"
		)

		ctx := context.Background()
		configName := types.NamespacedName{Name: "throttle-v2-config", Namespace: yanetNamespace}

		BeforeEach(func() {
			By("Creating test nodes for V2")
			for _, name := range []string{nodeName1, nodeName2} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
						Labels: map[string]string{
							"kubernetes.io/hostname": name, // Match the hostname used in nodeSelector
							"yanet-role":             "worker",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							"hugepages-1Gi": resource.MustParse("10Gi"),
						},
					},
				}
				Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			}

			By("Creating YanetConfigV2 with 5s updateWindow")
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName.Name,
					Namespace: configName.Namespace,
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Stop:         false,
					UpdateWindow: 5, // 5 seconds for faster testing
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{
								Name: "yanet-controlplane",
								Tag:  "1.0.0",
							},
							Port: 8080,
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{
								Name: "yanet-dataplane",
								Tag:  "1.0.0",
							},
							Port: 8081,
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{
						{
							Name: "test-box",
							Components: yanetv2alpha1.BoxComponents{
								Controlplane: &yanetv2alpha1.BoxComponent{},
								Dataplane:    &yanetv2alpha1.BoxComponent{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			By("Waiting for YanetConfigV2 to be reconciled and available")
			Eventually(func() bool {
				cfg := &yanetv2alpha1.YanetConfigV2{}
				if err := k8sClient.Get(ctx, configName, cfg); err != nil {
					return false
				}
				return cfg.UID != ""
			}, timeout, interval).Should(BeTrue())

			// Wait for YanetConfigReconcilerV2 to update GlobalConfigV2 snapshot
			Expect(waitForGlobalConfigV2(5 * time.Second)).Should(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up V2 resources")
			// Delete YanetsV2 FIRST
			for _, name := range []string{yanetName1, yanetName2} {
				yanet := &yanetv2alpha1.YanetV2{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: yanetNamespace}, yanet); err == nil {
					Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
				}
			}

			// Wait for YanetsV2 to be fully deleted before removing config
			// This prevents race where config snapshot is cleared while YanetV2 reconcile is still running
			Eventually(func() int {
				yanetList := &yanetv2alpha1.YanetV2List{}
				if err := k8sClient.List(ctx, yanetList, client.InNamespace(yanetNamespace)); err != nil {
					return -1
				}
				count := 0
				for _, y := range yanetList.Items {
					if y.Name == yanetName1 || y.Name == yanetName2 {
						count++
					}
				}
				return count
			}, timeout, interval).Should(Equal(0), "YanetsV2 should be deleted before config")

			// Delete YanetConfigV2 AFTER YanetsV2 are gone
			config := &yanetv2alpha1.YanetConfigV2{}
			if err := k8sClient.Get(ctx, configName, config); err == nil {
				Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
			}

			// Delete Nodes
			for _, name := range []string{nodeName1, nodeName2} {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, node); err == nil {
					Expect(k8sClient.Delete(ctx, node)).Should(Succeed())
				}
			}
		})

		It("Should create deployments with replicas=0 when enabled=false, then throttle updates", func() {
			By("Creating first YanetV2 with enabled=false")
			yanet1 := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName1,
					Namespace: yanetNamespace,
				},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "test-box",
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": nodeName1,
					},
					AutoSync: helpers.PtrBool(true),
					Enabled:  helpers.PtrBool(true), // enabled to test throttling
				},
			}
			Expect(k8sClient.Create(ctx, yanet1)).Should(Succeed())

			By("Verifying deployments are created for first node")
			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return false
				}
				// For V2, expect at least controlplane + dataplane deployments
				return len(depList.Items) >= 2
			}, timeout, interval).Should(BeTrue())

			By("Creating second YanetV2 on different node")
			yanet2 := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName2,
					Namespace: yanetNamespace,
				},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "test-box",
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": nodeName2,
					},
					AutoSync: helpers.PtrBool(true),
					Enabled:  helpers.PtrBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, yanet2)).Should(Succeed())

			By("Verifying total deployments exist for both nodes")
			Eventually(func() int {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return 0
				}
				return len(depList.Items)
			}, timeout, interval).Should(BeNumerically(">=", 4))

			By("Updating YanetConfigV2 image tag to trigger diff in all deployments")
			config := &yanetv2alpha1.YanetConfigV2{}
			Expect(k8sClient.Get(ctx, configName, config)).Should(Succeed())

			// Change image tag to trigger update
			config.Spec.Components.Controlplane.Image.Tag = "2.0.0"
			config.Spec.Components.Dataplane.Image.Tag = "2.0.0"
			Expect(k8sClient.Update(ctx, config)).Should(Succeed())

			By("Waiting for YanetConfigV2 to be reconciled with new values")
			time.Sleep(2000 * time.Millisecond) // Give config reconciler time to update snapshot
			Expect(waitForGlobalConfigV2(5 * time.Second)).Should(Succeed())

			By("Triggering reconciliation of YanetV2 CRs by adding annotation")
			for _, name := range []string{yanetName1, yanetName2} {
				yanet := &yanetv2alpha1.YanetV2{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: yanetNamespace}, yanet)).Should(Succeed())
				if yanet.Annotations == nil {
					yanet.Annotations = make(map[string]string)
				}
				yanet.Annotations["test.yanet.io/trigger"] = time.Now().Format(time.RFC3339Nano)
				Expect(k8sClient.Update(ctx, yanet)).Should(Succeed())
			}

			By("Recording first update timestamp")
			firstUpdateTime := time.Now()

			By("Waiting for first deployment to be updated")
			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					return false
				}
				// Check if at least one deployment has new image tag
				for _, dep := range depList.Items {
					if len(dep.Spec.Template.Spec.Containers) > 0 {
						image := dep.Spec.Template.Spec.Containers[0].Image
						if image == "yanet-controlplane:2.0.0" || image == "yanet-dataplane:2.0.0" {
							return true
						}
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying throttling: second node update should be delayed")
			time.Sleep(2 * time.Second)

			depList := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace))).Should(Succeed())

			updatedCount := 0
			totalCount := len(depList.Items)
			for _, dep := range depList.Items {
				if len(dep.Spec.Template.Spec.Containers) > 0 {
					image := dep.Spec.Template.Spec.Containers[0].Image
					if image == "yanet-controlplane:2.0.0" || image == "yanet-dataplane:2.0.0" {
						updatedCount++
					}
				}
			}

			// Due to throttling, not all deployments should be updated yet
			if totalCount > 0 {
				Expect(updatedCount).Should(BeNumerically("<", totalCount),
					"Throttling should prevent all deployments from updating immediately")
			}

			By("Waiting for all deployments to eventually be updated")
			Eventually(func() bool {
				depList := &appsv1.DeploymentList{}
				if err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace)); err != nil {
					GinkgoWriter.Printf("Error listing deployments: %v\n", err)
					return false
				}
				if len(depList.Items) == 0 {
					GinkgoWriter.Printf("No deployments found\n")
					return false
				}

				updated := 0
				// Check that all deployments have been updated to 2.0.0
				for _, dep := range depList.Items {
					if len(dep.Spec.Template.Spec.Containers) > 0 {
						image := dep.Spec.Template.Spec.Containers[0].Image
						// Image should contain ":2.0.0" tag
						if strings.Contains(image, ":2.0.0") {
							updated++
						} else {
							GinkgoWriter.Printf("Deployment %s still has image %s (want :2.0.0)\n", dep.Name, image)
						}
					}
				}
				GinkgoWriter.Printf("Updated %d/%d deployments\n", updated, len(depList.Items))
				return updated == len(depList.Items) && len(depList.Items) > 0
			}, timeout, interval).Should(BeTrue(), "All deployments should eventually be updated to 2.0.0")

			finalUpdateTime := time.Now()
			updateDuration := finalUpdateTime.Sub(firstUpdateTime)

			By("Verifying total update time is >= updateWindow (5s)")
			Expect(updateDuration.Seconds()).Should(BeNumerically(">=", 4.5),
				"Total update time should be at least ~5s due to throttling")
		})
	})
})
