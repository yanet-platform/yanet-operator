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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// validBoxComponents returns a BoxComponents wiring both controlplane
// and dataplane, satisfying the webhook contract that every boxType
// must wire at least these two hardcoded components.
func validBoxComponents() yanetv2alpha1.BoxComponents {
	return yanetv2alpha1.BoxComponents{
		Controlplane: &yanetv2alpha1.BoxComponent{},
		Dataplane:    &yanetv2alpha1.BoxComponent{},
	}
}

var _ = Describe("Webhook Validation E2E Tests", func() {
	ctx := context.Background()

	Context("V1 API - YanetConfig validation", func() {
		It("Should reject YanetConfig with negative updateWindow", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-updatewindow",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					UpdateWindow: -10, // Invalid: negative
				},
			}

			err := k8sClient.Create(ctx, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetConfig with valid updateWindow", func() {
			config := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-updatewindow",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetConfigSpec{
					UpdateWindow: 60,
					Stop:         false,
				},
			}

			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
		})
	})

	Context("V1 API - Yanet validation", func() {
		It("Should reject Yanet with empty nodeName", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-nodename",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: "", // Invalid: empty
					Type:     "release",
				},
			}

			err := k8sClient.Create(ctx, yanet)
			Expect(err).Should(HaveOccurred())
		})

		It("Should reject Yanet with invalid type", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-type",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: "test-node",
					Type:     "invalid-type", // Invalid: not in allowed list
				},
			}

			err := k8sClient.Create(ctx, yanet)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept Yanet with valid spec", func() {
			yanet := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-yanet-v1",
					Namespace: "default",
				},
				Spec: yanetv1alpha1.YanetSpec{
					NodeName: "test-node",
					Type:     "release",
					AutoSync: false, // avoid spawning deployments in default ns
				},
			}

			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
		})
	})

	Context("V2 API - YanetConfigV2 validation", func() {
		It("Should reject YanetConfigV2 with duplicate patch names", func() {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-patches",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv2alpha1.NamedPatch{
						{Name: "patch1", Patch: runtime.RawExtension{Raw: []byte(`{}`)}},
						{Name: "patch1", Patch: runtime.RawExtension{Raw: []byte(`{}`)}}, // Duplicate!
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name:       "test",
						Components: validBoxComponents(),
					}},
				},
			}

			err := k8sClient.Create(ctx, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should reject YanetConfigV2 with port overlap", func() {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "port-overlap",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
							Port:  8080,
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
							Port:  8080, // Same port as controlplane!
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name:       "test",
						Components: validBoxComponents(),
					}},
				},
			}

			err := k8sClient.Create(ctx, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetConfigV2 with valid spec", func() {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-config-v2",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
							Port:  8080,
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
							Port:  8081, // Different port
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name:       "test-box",
						Components: validBoxComponents(),
					}},
				},
			}

			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
		})
	})

	Context("V2 API - YanetV2 validation", func() {
		BeforeEach(func() {
			// Create valid YanetConfigV2
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config-v2",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name:       "test-box",
						Components: validBoxComponents(),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())
		})

		AfterEach(func() {
			config := &yanetv2alpha1.YanetConfigV2{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: "test-config-v2", Namespace: "default"}, config); err == nil {
				Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
			}
		})

		It("Should reject YanetV2 with unknown boxType", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unknown-boxtype",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "non-existent-box", // Invalid: not in YanetConfigV2
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "test-node",
					},
				},
			}

			err := k8sClient.Create(ctx, yanet)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetV2 with valid boxType", func() {
			yanet := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-yanet-v2",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "test-box", // Valid: exists in YanetConfigV2
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "nonexistent-node-no-match",
					},
					AutoSync: helpers.PtrBool(false), // avoid spawning deployments
				},
			}

			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, yanet)).Should(Succeed())
		})
	})

	Context("V2 API - Immutability validation", func() {
		var config *yanetv2alpha1.YanetConfigV2
		var yanet *yanetv2alpha1.YanetV2

		BeforeEach(func() {
			// Create YanetConfigV2 with two boxTypes (both must wire CP+DP)
			config = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "immutability-config",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{
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
			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Create YanetV2 (no matching node ⇒ no deployments spawned)
			yanet = &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "immutable-yanet",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetSpec{
					BoxType: "box-a",
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": "nonexistent-node-no-match",
					},
					AutoSync: helpers.PtrBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, yanet)).Should(Succeed())
		})

		AfterEach(func() {
			if yanet != nil {
				_ = k8sClient.Delete(ctx, yanet)
			}
			if config != nil {
				_ = k8sClient.Delete(ctx, config)
			}
		})

		It("Should reject update to immutable boxType field", func() {
			// Get current yanet
			current := &yanetv2alpha1.YanetV2{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, current)).Should(Succeed())

			// Try to change boxType
			current.Spec.BoxType = "box-b"

			err := k8sClient.Update(ctx, current)
			Expect(err).Should(HaveOccurred())
		})

		It("Should allow update to mutable fields", func() {
			// Get current yanet
			current := &yanetv2alpha1.YanetV2{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "immutable-yanet", Namespace: "default"}, current)).Should(Succeed())

			// Change mutable field (nodeSelector), keep no-match to avoid deployments
			current.Spec.NodeSelector = map[string]string{
				"kubernetes.io/hostname": "another-nonexistent-node",
			}

			Expect(k8sClient.Update(ctx, current)).Should(Succeed())
		})
	})

	Context("V2 API - Patch reference validation", func() {
		It("Should reject YanetConfigV2 with boxType referencing non-existent patch", func() {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-patch-ref",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv2alpha1.NamedPatch{
						{Name: "existing-patch", Patch: runtime.RawExtension{Raw: []byte(`{}`)}},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name: "test",
						Components: yanetv2alpha1.BoxComponents{
							Controlplane: &yanetv2alpha1.BoxComponent{
								Patches: []string{"non-existent-patch"}, // Invalid reference!
							},
							Dataplane: &yanetv2alpha1.BoxComponent{},
						},
					}},
				},
			}

			err := k8sClient.Create(ctx, config)
			Expect(err).Should(HaveOccurred())
		})

		It("Should accept YanetConfigV2 with valid patch references", func() {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-patch-ref",
					Namespace: "default",
				},
				Spec: yanetv2alpha1.YanetConfigSpec{
					Components: yanetv2alpha1.ComponentsSpec{
						Controlplane: yanetv2alpha1.ControlplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "cp", Tag: "v1"},
						},
						Dataplane: yanetv2alpha1.DataplaneSpec{
							Image: yanetv2alpha1.ImageRef{Name: "dp", Tag: "v1"},
						},
					},
					Patches: []yanetv2alpha1.NamedPatch{
						{
							Name: "my-patch",
							Patch: runtime.RawExtension{
								Raw: []byte(`{"spec":{"template":{"metadata":{"annotations":{"test":"value"}}}}}`),
							},
						},
					},
					BoxTypes: []yanetv2alpha1.BoxType{{
						Name: "test",
						Components: yanetv2alpha1.BoxComponents{
							Controlplane: &yanetv2alpha1.BoxComponent{
								Patches: []string{"my-patch"}, // Valid reference
							},
							Dataplane: &yanetv2alpha1.BoxComponent{},
						},
					}},
				},
			}

			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
		})
	})
})
