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
	"sigs.k8s.io/controller-runtime/pkg/client"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// validBoxComponents returns a BoxComponents wiring both controlplane
// and dataplane, satisfying the webhook contract that every boxType
// must wire at least these two hardcoded components.
func validBoxComponents() yanetv1alpha1.BoxComponents {
	return yanetv1alpha1.BoxComponents{
		Controlplane: &yanetv1alpha1.BoxComponent{},
		Dataplane:    &yanetv1alpha1.BoxDataplane{},
	}
}

var _ = Describe("Webhook Validation E2E Tests", func() {
	testContext := context.Background()

	Context("YanetConfig validation", func() {
		It("Should reject YanetConfig with duplicate patch names", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv1alpha1.NamedPatch{
						{Name: "patch1", Patch: runtime.RawExtension{Raw: []byte(`{}`)}},
						{Name: "patch1", Patch: runtime.RawExtension{Raw: []byte(`{}`)}}, // Duplicate!
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name:       "test",
						Components: validBoxComponents(),
					}},
				},
			}

			err := k8sClient.Create(testContext, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should reject YanetConfig with an invalid sidecar image", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image:    yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
							Sidecars: []yanetv1alpha1.SidecarSpec{{Name: "missing-image"}},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name:       "test",
						Components: validBoxComponents(),
					}},
				},
			}

			err := k8sClient.Create(testContext, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetConfig with valid spec", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name:       "test-box",
						Components: validBoxComponents(),
					}},
				},
			}

			Expect(k8sClient.Create(testContext, config)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(testContext, config)).Should(Succeed())
		})
	})

	Context("Yanet validation", func() {
		BeforeEach(func() {
			// Create valid YanetConfig
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name:       "test-box",
						Components: validBoxComponents(),
					}},
				},
			}
			Expect(k8sClient.Create(testContext, config)).Should(Succeed())
		})

		AfterEach(func() {
			config := &yanetv1alpha1.YanetConfig{}
			if err := k8sClient.Get(testContext, client.ObjectKey{Name: yanetv1alpha1.YanetConfigName}, config); err == nil {
				Expect(k8sClient.Delete(testContext, config)).Should(Succeed())
			}
		})

		It("Should reject Yanet with unknown boxType", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unknown-boxtype",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType: "non-existent-box", // Invalid: not in YanetConfig
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "test-node",
					},
				},
			}

			err := k8sClient.Create(testContext, yanet)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept Yanet with valid boxType", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-yanet-v2",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType: "test-box", // Valid: exists in YanetConfig
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "nonexistent-node-no-match",
					},
					AutoSync: helpers.PtrBool(false), // avoid spawning deployments
				},
			}

			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(testContext, yanet)).Should(Succeed())
		})
	})

	Context("Immutability validation", func() {
		var config *yanetv1alpha1.YanetConfig
		var yanet *yanetv1alpha1.Yanet

		BeforeEach(func() {
			// Clean up any leftover resources from previous tests
			oldYanet := &yanetv1alpha1.Yanet{}
			if err := k8sClient.Get(testContext, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, oldYanet); err == nil {
				_ = k8sClient.Delete(testContext, oldYanet)
				// Wait for deletion to complete
				Eventually(func() bool {
					err := k8sClient.Get(testContext, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, oldYanet)
					return err != nil
				}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())
			}

			oldConfig := &yanetv1alpha1.YanetConfig{}
			if err := k8sClient.Get(testContext, client.ObjectKey{Name: yanetv1alpha1.YanetConfigName}, oldConfig); err == nil {
				_ = k8sClient.Delete(testContext, oldConfig)
				// Wait for deletion to complete
				Eventually(func() bool {
					err := k8sClient.Get(testContext, client.ObjectKey{Name: yanetv1alpha1.YanetConfigName}, oldConfig)
					return err != nil
				}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())
			}

			// Create YanetConfig with two boxTypes (both must wire CP+DP)
			config = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{
						{
							Name:       "box-a",
							Components: validBoxComponents(),
						},
						{
							Name:       "box-b",
							Components: validBoxComponents(),
						},
					},
				},
			}
			Expect(k8sClient.Create(testContext, config)).Should(Succeed())

			// Create Yanet (no matching node ⇒ no deployments spawned)
			yanet = &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "immutable-yanet",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					BoxType: "box-a",
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "nonexistent-node-no-match",
					},
					AutoSync: helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(testContext, yanet)).Should(Succeed())
		})

		AfterEach(func() {
			if yanet != nil {
				_ = k8sClient.Delete(testContext, yanet)
			}
			if config != nil {
				_ = k8sClient.Delete(testContext, config)
			}
		})

		It("Should reject update to immutable boxType field", func() {
			// Get current yanet
			current := &yanetv1alpha1.Yanet{}
			Expect(k8sClient.Get(testContext, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, current)).Should(Succeed())

			// Try to change boxType
			current.Spec.BoxType = "box-b"

			err := k8sClient.Update(testContext, current)
			Expect(err).Should(HaveOccurred())
		})

		It("Should allow update to mutable fields", func() {
			// Retry update to handle conflicts from concurrent status updates
			Eventually(func() error {
				// Get fresh copy each time
				current := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(testContext, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, current); err != nil {
					return err
				}

				// Change mutable field (nodeSelector), keep no-match to avoid deployments
				current.Spec.NodeSelector = map[string]string{
					"kubernetes.io/hostname": "another-nonexistent-node",
				}

				return k8sClient.Update(testContext, current)
			}, 10*time.Second, 500*time.Millisecond).Should(Succeed())
		})
	})

	Context("Patch reference validation", func() {
		It("Should reject YanetConfig with boxType referencing non-existent patch", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv1alpha1.NamedPatch{
						{Name: "existing-patch", Patch: runtime.RawExtension{Raw: []byte(`{}`)}},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name: "test",
						Components: yanetv1alpha1.BoxComponents{
							Controlplane: &yanetv1alpha1.BoxComponent{
								Patches: []string{"non-existent-patch"}, // Invalid reference!
							},
							Dataplane: &yanetv1alpha1.BoxDataplane{},
						},
					}},
				},
			}

			err := k8sClient.Create(testContext, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetConfig with valid patch references", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: yanetv1alpha1.YanetConfigName,
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					Components: yanetv1alpha1.ComponentsSpec{
						Controlplane: yanetv1alpha1.ControlplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv1alpha1.DataplaneSpec{
							Image: yanetv1alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv1alpha1.NamedPatch{
						{
							Name: "my-patch",
							Patch: runtime.RawExtension{
								Raw: []byte(`{"spec":{"template":{"metadata":{"annotations":{"test":"value"}}}}}`),
							},
						},
					},
					BoxTypes: []yanetv1alpha1.BoxType{{
						Name: "test",
						Components: yanetv1alpha1.BoxComponents{
							Controlplane: &yanetv1alpha1.BoxComponent{
								Patches: []string{"my-patch"}, // Valid reference
							},
							Dataplane: &yanetv1alpha1.BoxDataplane{},
						},
					}},
				},
			}

			Expect(k8sClient.Create(testContext, config)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(testContext, config)).Should(Succeed())
		})
	})
})
