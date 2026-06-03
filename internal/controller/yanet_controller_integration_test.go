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

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("YanetReconciler Integration Tests", func() {
	const (
		timeout  = time.Second * 60 // Increased from 30s to 60s for slower systems
		interval = time.Millisecond * 250
	)

	Context("When reconciling a YanetV2 resource with type=release", func() {
		const (
			yanetName      = "test-node-1"
			yanetNamespace = "default"
			nodeName       = "test-node-1"
		)

		ctx := context.Background()
		yanetConfigName := types.NamespacedName{Name: "test-config", Namespace: yanetNamespace}
		yanetLookupKey := types.NamespacedName{Name: yanetName, Namespace: yanetNamespace}

		BeforeEach(func() {
			By("Cleaning up any existing deployments in default namespace")
			cleanupDeployments(ctx, yanetNamespace)

			By("Creating a test Node")
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
				Status: v1.NodeStatus{
					Capacity: v1.ResourceList{
						"hugepages-1Gi": resource.MustParse("45Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())

			By("Creating a YanetConfigV2")
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetConfigName.Name,
					Namespace: yanetConfigName.Namespace,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Stop:         false,
					UpdateWindow: 60,
					EnabledOpts: yanetv1alpha1.EnabledOpts{
						Release: yanetv1alpha1.DepOpts{
							Dataplain: yanetv1alpha1.OptsNames{
								Annotations: []string{"checkpointer"},
								HostIpc:     true,
								Privileged:  true,
								Resources: v1.ResourceRequirements{
									Limits: v1.ResourceList{
										"memory": resource.MustParse("32Gi"),
									},
								},
							},
							Controlplane: yanetv1alpha1.OptsNames{
								Annotations: []string{"checkpointer"},
								HostIpc:     true,
								Privileged:  false,
								Resources: v1.ResourceRequirements{
									Limits: v1.ResourceList{
										"cpu":    resource.MustParse("6"),
										"memory": resource.MustParse("128Gi"),
									},
									Requests: v1.ResourceList{
										"cpu":    resource.MustParse("1"),
										"memory": resource.MustParse("16Gi"),
									},
								},
							},
							Announcer: yanetv1alpha1.OptsNames{
								Annotations: []string{"checkpointer"},
								Privileged:  false,
								Resources: v1.ResourceRequirements{
									Limits: v1.ResourceList{
										"cpu":    resource.MustParse("4"),
										"memory": resource.MustParse("32Gi"),
									},
									Requests: v1.ResourceList{
										"cpu":    resource.MustParse("100m"),
										"memory": resource.MustParse("4Gi"),
									},
								},
							},
							Bird: yanetv1alpha1.OptsNames{
								Annotations: []string{"checkpointer"},
								Privileged:  false,
								Resources: v1.ResourceRequirements{
									Limits: v1.ResourceList{
										"cpu":    resource.MustParse("6"),
										"memory": resource.MustParse("64Gi"),
									},
									Requests: v1.ResourceList{
										"cpu":    resource.MustParse("100m"),
										"memory": resource.MustParse("4Gi"),
									},
								},
							},
						},
					},
					AdditionalOpts: yanetv1alpha1.AdditionalOpts{
						Annotations: []yanetv1alpha1.NamedAnnotations{
							{
								Name: "checkpointer",
								Annotations: map[string]string{
									"checkpointer.ydb.tech/checkpoint":      "true",
									"checkpointer.ydb.tech/manual-recovery": "true",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Give YanetConfigReconciler time to update GlobalConfig snapshot
			time.Sleep(1000 * time.Millisecond)
		})

		AfterEach(func() {
			By("Cleaning up resources")
			// Delete YanetV2
			yanet := &yanetv1alpha1.Yanet{}
			err := k8sClient.Get(ctx, yanetLookupKey, yanet)
			if err == nil {
				Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
			}

			// Delete YanetConfigV2
			config := &yanetv1alpha1.YanetConfig{}
			err = k8sClient.Get(ctx, yanetConfigName, config)
			if err == nil {
				Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
			}

			// Delete Node
			node := &v1.Node{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err == nil {
				Expect(k8sClient.Delete(ctx, node)).Should(Succeed())
			}
		})

		It("Should create all 4 deployments with correct configuration", func() {
			By("Creating a YanetV2 resource")
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName,
					Namespace: yanetNamespace,
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
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
					Announcer: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-announcer",
					},
					Bird: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-bird",
						Tag:    "2.0.12",
					},
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			By("Checking that all 4 deployments are created")
			Eventually(func() int {
				depList := &appsv1.DeploymentList{}
				err := k8sClient.List(ctx, depList, client.InNamespace(yanetNamespace))
				if err != nil {
					return 0
				}
				return len(depList.Items)
			}, timeout, interval).Should(Equal(4))

			By("Verifying dataplane deployment")
			dataplaneKey := types.NamespacedName{
				Name:      "dataplane-" + nodeName,
				Namespace: yanetNamespace,
			}
			dataplaneDep := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, dataplaneKey, dataplaneDep)
			}, timeout, interval).Should(Succeed())

			Expect(dataplaneDep.Spec.Template.Spec.Containers).Should(HaveLen(1))
			Expect(dataplaneDep.Spec.Template.Spec.Containers[0].Name).Should(Equal("dataplane"))
			Expect(dataplaneDep.Spec.Template.Spec.Containers[0].Image).Should(Equal("docker.io/test/yanet-dataplane:1.0.0"))
			Expect(dataplaneDep.Spec.Template.Spec.HostIPC).Should(BeTrue())
			Expect(*dataplaneDep.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).Should(BeTrue())
			Expect(dataplaneDep.Spec.Template.Spec.Containers[0].Resources.Limits["hugepages-1Gi"]).Should(Equal(resource.MustParse("45Gi")))

			By("Verifying controlplane deployment")
			controlplaneKey := types.NamespacedName{
				Name:      "controlplane-" + nodeName,
				Namespace: yanetNamespace,
			}
			controlplaneDep := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, controlplaneKey, controlplaneDep)
			}, timeout, interval).Should(Succeed())

			Expect(controlplaneDep.Spec.Template.Spec.Containers).Should(HaveLen(1))
			Expect(controlplaneDep.Spec.Template.Spec.Containers[0].Name).Should(Equal("controlplane"))
			Expect(controlplaneDep.Spec.Template.Spec.HostIPC).Should(BeTrue())
			Expect(*controlplaneDep.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).Should(BeFalse())

			By("Verifying bird deployment with custom tag")
			birdKey := types.NamespacedName{
				Name:      "bird-" + nodeName,
				Namespace: yanetNamespace,
			}
			birdDep := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, birdKey, birdDep)
			}, timeout, interval).Should(Succeed())

			Expect(birdDep.Spec.Template.Spec.Containers[0].Image).Should(Equal("docker.io/test/yanet-bird:2.0.12"))

			By("Verifying announcer deployment")
			announcerKey := types.NamespacedName{
				Name:      "announcer-" + nodeName,
				Namespace: yanetNamespace,
			}
			announcerDep := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, announcerKey, announcerDep)
			}, timeout, interval).Should(Succeed())

			Expect(announcerDep.Spec.Template.Spec.InitContainers).Should(HaveLen(1))
			Expect(announcerDep.Spec.Template.Spec.InitContainers[0].Name).Should(Equal("wait-bird"))

			By("Checking YanetV2 status is updated")
			Eventually(func() bool {
				updatedYanet := &yanetv1alpha1.Yanet{}
				err := k8sClient.Get(ctx, yanetLookupKey, updatedYanet)
				if err != nil {
					return false
				}
				return len(updatedYanet.Status.Sync.Synced) == 4
			}, timeout, interval).Should(BeTrue())
		})

		It("Should respect AutoSync=false and not create deployments", func() {
			By("Creating a YanetV2 resource with AutoSync=false")
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName + "-nosync",
					Namespace: yanetNamespace,
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: nodeName,
					Type:     "release",
					AutoSync: false, // Disabled
					Dataplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-dataplane",
					},
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			By("Verifying no deployments are created (AutoSync=false)")
			Consistently(func() int {
				depList := &appsv1.DeploymentList{}
				err := k8sClient.List(ctx, depList,
					client.InNamespace(yanetNamespace),
					client.MatchingLabels{"topology-location-host": nodeName})
				if err != nil {
					return -1
				}
				return len(depList.Items)
			}, time.Second*5, interval).Should(Equal(0))

			// Cleanup
			Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
		})

		It("Should create deployments with replicas=0 when Enable=false", func() {
			disabledNodeName := "test-node-disabled"

			By("Creating a test Node for disabled test")
			disabledNode := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: disabledNodeName,
				},
				Status: v1.NodeStatus{
					Capacity: v1.ResourceList{
						"hugepages-1Gi": resource.MustParse("45Gi"),
					},
				},
			}
			Expect(k8sClient.Create(ctx, disabledNode)).Should(Succeed())

			By("Creating a YanetV2 resource with dataplane disabled")
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      yanetName + "-disabled",
					Namespace: yanetNamespace,
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: disabledNodeName,
					Type:     "release",
					AutoSync: true,
					Dataplane: yanetv1alpha1.Dep{
						Enable: false, // Disabled
						Image:  "yanet-dataplane",
					},
					Controlplane: yanetv1alpha1.Dep{
						Enable: true,
						Image:  "yanet-controlplane",
					},
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			By("Checking dataplane deployment has 0 replicas")
			dataplaneKey := types.NamespacedName{
				Name:      "dataplane-" + disabledNodeName,
				Namespace: yanetNamespace,
			}
			Eventually(func() int32 {
				dep := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, dataplaneKey, dep)
				if err != nil {
					return -1
				}
				return *dep.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			By("Checking status shows deployment as disabled")
			Eventually(func() bool {
				updatedYanet := &yanetv1alpha1.Yanet{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      yanet.Name,
					Namespace: yanet.Namespace,
				}, updatedYanet)
				if err != nil {
					return false
				}
				for _, disabled := range updatedYanet.Status.Sync.Disabled {
					if disabled == "dataplane-"+disabledNodeName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, disabledNode)).Should(Succeed())
		})
	})

	Context("When YanetConfigV2 is updated", func() {
		const (
			configName      = "test-config-update"
			configNamespace = "default"
		)

		ctx := context.Background()
		configKey := types.NamespacedName{Name: configName, Namespace: configNamespace}

		It("Should update GlobalConfig in memory", func() {
			By("Creating initial YanetConfigV2")
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: configNamespace,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Stop:         false,
					UpdateWindow: 30,
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			By("Triggering reconcile")
			configReconciler := &YanetConfigReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				GlobalConfig: &yanetv1alpha1.MutexYanetConfigSpec{},
			}
			_, err := configReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: configKey,
			})
			Expect(err).ShouldNot(HaveOccurred())

			By("Verifying GlobalConfig is updated")
			configReconciler.GlobalConfig.Lock.Lock()
			Expect(configReconciler.GlobalConfig.Config.UpdateWindow).Should(Equal(30))
			Expect(configReconciler.GlobalConfig.Config.Stop).Should(BeFalse())
			configReconciler.GlobalConfig.Lock.Unlock()

			By("Updating YanetConfigV2")
			updatedConfig := &yanetv1alpha1.YanetConfig{}
			Expect(k8sClient.Get(ctx, configKey, updatedConfig)).Should(Succeed())
			updatedConfig.Spec.UpdateWindow = 60
			updatedConfig.Spec.Stop = true
			Expect(k8sClient.Update(ctx, updatedConfig)).Should(Succeed())

			By("Triggering reconcile again")
			_, err = configReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: configKey,
			})
			Expect(err).ShouldNot(HaveOccurred())

			By("Verifying GlobalConfig is updated with new values")
			configReconciler.GlobalConfig.Lock.Lock()
			Expect(configReconciler.GlobalConfig.Config.UpdateWindow).Should(Equal(60))
			Expect(configReconciler.GlobalConfig.Config.Stop).Should(BeTrue())
			configReconciler.GlobalConfig.Lock.Unlock()

			// Cleanup
			Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
		})
	})
})
