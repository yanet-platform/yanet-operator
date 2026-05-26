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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
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

// minimalV2ConfigSpec builds the smallest YanetConfigV2 spec that the v2
// webhook accepts: cp + dp components, one boxType referencing both.
func minimalV2ConfigSpec() yanetv2alpha1.YanetConfigSpec {
	return yanetv2alpha1.YanetConfigSpec{
		Components: yanetv2alpha1.ComponentsSpec{
			Controlplane: yanetv2alpha1.ControlplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "controlplane", Tag: "test"},
				Port:  8080,
			},
			Dataplane: yanetv2alpha1.DataplaneSpec{
				Image: yanetv2alpha1.ImageRef{Name: "dataplane", Tag: "test"},
				Port:  8090,
			},
		},
		BoxTypes: []yanetv2alpha1.BoxType{
			{
				Name: "release",
				Components: yanetv2alpha1.BoxComponents{
					Controlplane: &yanetv2alpha1.BoxComponent{},
					Dataplane:    &yanetv2alpha1.BoxComponent{},
				},
			},
		},
	}
}

var _ = Describe("Validating webhooks", func() {

	// -----------------------------------------------------------
	// v1alpha1 — YanetV2
	// -----------------------------------------------------------
	Context("v1alpha1 YanetV2", func() {
		It("accepts a well-formed CR", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v1-valid", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{NodeName: "node-a", Type: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
		})

		It("rejects an empty nodename", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v1-empty-nodename", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{Type: "release"},
			}
			expectWebhookRejection(k8sClient.Create(ctx, cr), "nodename")
		})

		It("rejects an unknown type", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v1-bad-type", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{NodeName: "node-a", Type: "gibberish"},
			}
			expectWebhookRejection(k8sClient.Create(ctx, cr), "type")
		})

		It("forbids changing nodename on update", func() {
			cr := &yanetv1alpha1.Yanet{
				ObjectMeta: metav1.ObjectMeta{Name: "v1-immutable", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetSpec{NodeName: "node-a", Type: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cr) }()

			// The v1 reconciler may add a finalizer right after Create,
			// bumping the resourceVersion. Retry the Update on conflict
			// so the test exercises the admission webhook (which is the
			// thing under test) rather than racing the reconciler.
			Eventually(func() error {
				fresh := &yanetv1alpha1.Yanet{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), fresh); err != nil {
					return err
				}
				fresh.Spec.NodeName = "node-b"
				return k8sClient.Update(ctx, fresh)
			}, 5*time.Second, 100*time.Millisecond).Should(MatchError(
				ContainSubstring("admission webhook"),
			))
		})
	})

	// -----------------------------------------------------------
	// v1alpha1 — YanetConfigV2
	// -----------------------------------------------------------
	Context("v1alpha1 YanetConfigV2", func() {
		It("accepts UpdateWindow >= 0", func() {
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v1-valid", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetConfigSpec{UpdateWindow: 60},
			}
			Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
		})

		It("rejects negative UpdateWindow", func() {
			cfg := &yanetv1alpha1.YanetConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v1-bad", Namespace: whTestNS},
				Spec:       yanetv1alpha1.YanetConfigSpec{UpdateWindow: -1},
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "updatewindow")
		})
	})

	// -----------------------------------------------------------
	// v2alpha1 — YanetConfigV2
	// -----------------------------------------------------------
	Context("v2alpha1 YanetConfigV2", func() {
		It("accepts a well-formed config", func() {
			cfg := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v2-valid", Namespace: whTestNS},
				Spec:       minimalV2ConfigSpec(),
			}
			Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
		})

		It("rejects overlapping controlplane and dataplane ports", func() {
			s := minimalV2ConfigSpec()
			s.Components.Dataplane.Port = s.Components.Controlplane.Port
			cfg := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v2-portoverlap", Namespace: whTestNS},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "port")
		})

		It("rejects a boxType referencing an undeclared patch", func() {
			s := minimalV2ConfigSpec()
			s.BoxTypes[0].Components.Controlplane = &yanetv2alpha1.BoxComponent{
				Patches: []string{"does-not-exist"},
			}
			cfg := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v2-missingpatch", Namespace: whTestNS},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "patch")
		})

		It("rejects a boxType with duplicate name", func() {
			s := minimalV2ConfigSpec()
			s.BoxTypes = append(s.BoxTypes, s.BoxTypes[0])
			cfg := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-v2-dupbox", Namespace: whTestNS},
				Spec:       s,
			}
			expectWebhookRejection(k8sClient.Create(ctx, cfg), "duplicated")
		})

	})

	// -----------------------------------------------------------
	// v2alpha1 — YanetV2
	// -----------------------------------------------------------
	Context("v2alpha1 YanetV2", func() {
		// One YanetConfigV2 with a "release" boxType is required for
		// the cross-reference check in the YanetV2 webhook.
		var cfg *yanetv2alpha1.YanetConfigV2

		BeforeEach(func() {
			cfg = &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg-for-yanet", Namespace: whTestNS},
				Spec:       minimalV2ConfigSpec(),
			}
			_ = k8sClient.Delete(ctx, cfg) // tolerate leftover
			Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, cfg)
		})

		It("accepts a YanetV2 referencing an existing boxType", func() {
			cr := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-valid", Namespace: whTestNS},
				Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
		})

		It("rejects an unknown boxType", func() {
			cr := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-unknown-box", Namespace: whTestNS},
				Spec:       yanetv2alpha1.YanetSpec{BoxType: "does-not-exist"},
			}
			expectWebhookRejection(k8sClient.Create(ctx, cr), "boxType")
		})

		It("forbids changing boxType on update", func() {
			cr := &yanetv2alpha1.YanetV2{
				ObjectMeta: metav1.ObjectMeta{Name: "v2-immutable", Namespace: whTestNS},
				Spec:       yanetv2alpha1.YanetSpec{BoxType: "release"},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cr) }()

			// Retry on optimistic-concurrency conflicts caused by the
			// YanetV2 reconciler updating status concurrently — we want
			// the webhook verdict, not a 409 from the API server.
			key := client.ObjectKeyFromObject(cr)
			Eventually(func() error {
				fresh := &yanetv2alpha1.YanetV2{}
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
