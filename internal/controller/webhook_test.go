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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

// Webhook integration suite. The webhook server runs in-process and the
// apiserver was configured with the ValidatingWebhookConfiguration from
// config/webhook/manifests.yaml (see suite_test.go). Every Create here
// actually round-trips through admission.
//
// We never write CRs to a real cluster in these tests, but envtest is
// close enough to catch:
//   - missing WithValidator() registration (today's bug),
//   - broken validation logic,
//   - shape mismatches between the webhook manifest and the served path.

const whTestNS = "default"

// expectWebhookRejection asserts the apiserver returned an admission
// error containing `wantSubstr` (case-insensitive). We use Invalid as
// a guard against accidentally matching network/TLS errors.
func expectWebhookRejection(err error, wantSubstr string) {
	GinkgoHelper()
	ExpectWithOffset(1, err).To(HaveOccurred())
	ExpectWithOffset(1, apierrors.IsInvalid(err) ||
		strings.Contains(err.Error(), "admission webhook") ||
		strings.Contains(err.Error(), "denied the request"),
	).To(BeTrue(), "expected admission error, got: %v", err)
	if wantSubstr != "" {
		ExpectWithOffset(1, strings.ToLower(err.Error())).
			To(ContainSubstring(strings.ToLower(wantSubstr)))
	}
}

// minimalConfigSpec builds the smallest YanetConfig spec that the
// webhook accepts: cp + dp components, one boxType referencing both.
func minimalConfigSpec() yanetv1alpha1.YanetConfigSpec {
	return yanetv1alpha1.YanetConfigSpec{
		Components: yanetv1alpha1.ComponentsSpec{
			Controlplane: yanetv1alpha1.ControlplaneSpec{
				Image: yanetv1alpha1.ImageRef{Name: "controlplane", Tag: "test"},
			},
			Dataplane: yanetv1alpha1.DataplaneSpec{
				Image: yanetv1alpha1.ImageRef{Name: "dataplane", Tag: "test"},
			},
		},
		BoxTypes: []yanetv1alpha1.BoxType{
			{
				Name: "release",
				Components: yanetv1alpha1.BoxComponents{
					Controlplane: &yanetv1alpha1.BoxComponent{},
					Dataplane:    &yanetv1alpha1.BoxDataplane{},
				},
			},
		},
	}
}

var _ = Describe("Validating webhooks", func() {

	// -----------------------------------------------------------
	// v1alpha1 — YanetConfig
	// -----------------------------------------------------------
	Context("v1alpha1 YanetConfig", func() {
		It("rejects a non-canonical singleton name", func() {
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "other"},
				Spec:       minimalConfigSpec(),
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "metadata.name")
		})

		It("accepts a well-formed config", func() {
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
				Spec:       minimalConfigSpec(),
			}
			Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
		})

		It("rejects duplicate ordered sidecar names", func() {
			s := minimalConfigSpec()
			s.Components.Dataplane.Sidecars = []yanetv1alpha1.SidecarSpec{
				{Name: "duplicate", Image: yanetv1alpha1.ImageRef{Name: "test"}},
				{Name: "duplicate", Image: yanetv1alpha1.ImageRef{Name: "test"}},
			}
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "duplicates a role")
		})

		It("rejects a boxType referencing an undeclared patch", func() {
			s := minimalConfigSpec()
			s.BoxTypes[0].Components.Controlplane = &yanetv1alpha1.BoxComponent{
				Patches: []string{"does-not-exist"},
			}
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "patch")
		})

		It("rejects a boxType with duplicate name", func() {
			s := minimalConfigSpec()
			s.BoxTypes = append(s.BoxTypes, s.BoxTypes[0])
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "duplicated")
		})

	})

	// -----------------------------------------------------------
	// v1alpha1 — Yanet
	// -----------------------------------------------------------
	Context("v1alpha1 Yanet", func() {
		// One YanetConfig with a "release" boxType is required for
		// the cross-reference check in the Yanet webhook.
		var cfg *yanetv1alpha1.YanetConfig

		BeforeEach(func() {
			cfg = &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
				Spec:       minimalConfigSpec(),
			}
			_ = k8sClient.Delete(ctx, cfg) // tolerate leftover
			Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, cfg)
		})

		It("accepts a Yanet referencing an existing boxType", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-valid", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
		})

		It("rejects an unknown boxType", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-unknown-box", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{BoxType: "does-not-exist"},
			}
			expectWebhookRejection(k8sClient.Create(ctx, cr), "boxType")
		})

		It("forbids changing boxType on update", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-immutable", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{BoxType: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cr) }()

			// Retry on optimistic-concurrency conflicts caused by the
			// Yanet reconciler updating status concurrently — we want
			// the webhook verdict, not a 409 from the API server.
			key := client.ObjectKeyFromObject(cr)
			Eventually(func() error {
				fresh := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(ctx, key, fresh); err != nil {
					return err
				}
				fresh.Spec.BoxType = "another"
				err := k8sClient.Update(ctx, fresh)
				if err == nil {
					return fmt.Errorf("update unexpectedly succeeded")
				}
				if apierrors.IsConflict(err) {
					return err // retry
				}
				// Any other error (expected: admission denial) — stop retrying.
				Expect(err.Error()).To(ContainSubstring("immutable"))
				return nil
			}, "5s", "100ms").Should(Succeed())
		})
	})
})
