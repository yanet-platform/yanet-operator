package manifests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestComposedManifestsAgainstLocalAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for strict local API validation")
	}
	localOnly := false
	env := &envtest.Environment{
		UseExistingCluster: &localOnly, CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := env.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})
	client, clientErr := kubernetes.NewForConfig(cfg)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "manifest-fixtures"}}
	if _, createErr := client.CoreV1().Namespaces().Create(t.Context(), ns, metav1.CreateOptions{}); createErr != nil {
		t.Fatal(createErr)
	}
	strictDryRun := metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict, DryRun: []string{metav1.DryRunAll}}
	t.Run("patched-affinity", func(t *testing.T) {
		workloads, renderErr := renderPatchedNodePlacement()
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		deployment := workloads[0]
		deployment.Namespace = ns.Name
		if _, createErr := client.AppsV1().Deployments(ns.Name).Create(t.Context(), deployment, strictDryRun); createErr != nil {
			t.Fatalf("API rejected rendered affinity: %v", createErr)
		}
	})
	t.Run("mount-paths", func(t *testing.T) {
		workloads, renderErr := renderMountPaths("/other", "/other")
		if renderErr != nil {
			t.Fatalf("the same path in different containers must be allowed: %v", renderErr)
		}
		deployment := workloads[0]
		deployment.Namespace = ns.Name
		pod := &deployment.Spec.Template.Spec
		for _, container := range []corev1.Container{pod.Containers[0], pod.InitContainers[0]} {
			if len(container.VolumeMounts) != 2 || container.VolumeMounts[0].MountPath != "/fixture" {
				t.Fatalf("render dropped shared-path mount for %q: %v", container.Name, container.VolumeMounts)
			}
		}
		deployments := client.AppsV1().Deployments(ns.Name)
		if _, createErr := deployments.Create(t.Context(), deployment, strictDryRun); createErr != nil {
			t.Fatalf("API rejected valid rendered Deployment: %v", createErr)
		}
	})
	t.Run("kustomize-samples", func(t *testing.T) {
		dynamicClient, dynamicErr := dynamic.NewForConfig(cfg)
		if dynamicErr != nil {
			t.Fatal(dynamicErr)
		}
		seen := map[string]bool{}
		rendered, readErr := os.ReadFile("../../bin/test-samples.yaml")
		if readErr != nil {
			t.Fatalf("read samples rendered by make render-test-samples: %v", readErr)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
		for {
			var sample unstructured.Unstructured
			if decodeErr := decoder.Decode(&sample); errors.Is(decodeErr, io.EOF) {
				break
			} else if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(sample.Object) == 0 {
				continue
			}
			kind := sample.GetKind()
			gvr := schema.GroupVersionResource{Group: "yanet.yanet-platform.io", Version: "v1alpha1", Resource: strings.ToLower(kind) + "s"}
			resource := dynamicClient.Resource(gvr)
			var target dynamic.ResourceInterface = resource
			if kind == "Yanet" {
				sample.SetNamespace(ns.Name)
				target = resource.Namespace(ns.Name)
			}
			if _, createErr := target.Create(t.Context(), &sample, strictDryRun); createErr != nil {
				t.Fatalf("strict CRD schema rejected %s/%s: %v", kind, sample.GetName(), createErr)
			}
			seen[kind] = true
		}
		if !seen["Yanet"] || !seen["YanetConfig"] {
			t.Fatalf("expected both sample kinds, saw %v", seen)
		}
	})
}
