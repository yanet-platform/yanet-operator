package controller

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	extensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type sshWatchObserver struct {
	client.Client
	gets           chan client.ObjectKey
	lock           sync.Mutex
	listNamespaces []string
}

func (o *sshWatchObserver) Get(requestCtx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if getErr := o.Client.Get(requestCtx, key, obj, opts...); getErr != nil {
		return getErr
	}
	if _, ok := obj.(*api.Yanet); ok {
		select {
		case o.gets <- key:
		case <-requestCtx.Done():
			return requestCtx.Err()
		}
	}
	return nil
}

func (o *sshWatchObserver) List(requestCtx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*api.YanetList); ok {
		options := &client.ListOptions{}
		options.ApplyOptions(opts)
		o.lock.Lock()
		o.listNamespaces = append(o.listNamespaces, options.Namespace)
		o.lock.Unlock()
	}
	return o.Client.List(requestCtx, list, opts...)
}

func TestExternalSSHConfigMapWatchEnqueuesOnlyItsNamespace(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("requires envtest assets")
	}
	// Only the certificate watch's discovery/storage boundary is needed here;
	// no fake cert-manager reconciler or issuance behavior participates.
	preserve := true
	certificateCRD := &extensions.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "certificates.cert-manager.io"}, Spec: extensions.CustomResourceDefinitionSpec{Group: "cert-manager.io", Names: extensions.CustomResourceDefinitionNames{Plural: "certificates", Singular: "certificate", Kind: "Certificate", ListKind: "CertificateList"}, Scope: extensions.NamespaceScoped, Versions: []extensions.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true, Schema: &extensions.CustomResourceValidation{OpenAPIV3Schema: &extensions.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve}}}}}}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, CRDs: []*extensions.CustomResourceDefinition{certificateCRD}, ErrorIfCRDPathMissing: true}
	restConfig, startErr := environment.Start()
	if startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if stopErr := environment.Stop(); stopErr != nil {
			t.Error(stopErr)
		}
	})
	scheme := testScheme(t)
	apiClient, clientErr := client.New(restConfig, client.Options{Scheme: scheme})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	testCtx, cancelTest := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelTest()
	keys := []client.ObjectKey{{Namespace: "ssh-a", Name: "edge"}, {Namespace: "ssh-b", Name: "edge"}}
	for _, key := range keys {
		if createErr := apiClient.Create(testCtx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: key.Namespace}}); createErr != nil {
			t.Fatal(createErr)
		}
		if createErr := apiClient.Create(testCtx, &api.Yanet{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Spec: api.YanetSpec{BoxType: "release"}}); createErr != nil {
			t.Fatal(createErr)
		}
	}
	// The existing controller suite starts another manager in this test binary.
	// Metrics are disabled here; permit its process-global controller name reuse.
	manager, managerErr := ctrl.NewManager(restConfig, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", Controller: controllerconfig.Controller{SkipNameValidation: &preserve}})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	observer := &sshWatchObserver{Client: manager.GetClient(), gets: make(chan client.ObjectKey, 32)}
	seed, _, _ := certificateEnvironment(t)
	configuration := seed.GlobalConfig.Config
	configuration.Stop = true
	configuration.Certificates.SSHTrustBundle = &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ssh-trust"}, Key: "ssh-ca.pub"}
	r := &YanetReconciler{Client: observer, Scheme: scheme, GlobalConfig: &api.MutexYanetConfigSpec{Config: configuration}, ManageCertificates: true}
	if setupErr := r.SetupWithManager(manager); setupErr != nil {
		t.Fatal(setupErr)
	}
	managerCtx, cancelManager := context.WithCancel(testCtx)
	done := make(chan error, 1)
	go func() { done <- manager.Start(managerCtx) }()
	t.Cleanup(func() {
		cancelManager()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Error(runErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager did not stop")
		}
	})
	if !manager.GetCache().WaitForCacheSync(testCtx) {
		t.Fatal("manager cache did not synchronize")
	}
	// With stop set, each initial primary event ends after its public Get:
	// there are no status/finalizer writes or scheduled retries to mask a missing
	// ConfigMap watch. Consume both initial events before sending source events.
	seen := map[client.ObjectKey]bool{}
	for len(seen) < len(keys) {
		select {
		case key := <-observer.gets:
			seen[key] = true
		case <-testCtx.Done():
			t.Fatal("initial Yanet events were not reconciled")
		}
	}
	unrelated := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: keys[1].Namespace}}
	if createErr := apiClient.Create(testCtx, unrelated); createErr != nil {
		t.Fatal(createErr)
	}
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ssh-trust", Namespace: keys[0].Namespace}, Data: map[string]string{"ssh-ca.pub": "public source A"}}
	awaitRequest := func() {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case key := <-observer.gets:
			if key != keys[0] {
				t.Fatalf("external SSH source enqueued another namespace: %v", key)
			}
		case <-timer.C:
			t.Fatal("external SSH ConfigMap event did not enqueue the installation")
		}
	}
	if createErr := apiClient.Create(testCtx, source); createErr != nil {
		t.Fatal(createErr)
	}
	awaitRequest()
	source.Data["ssh-ca.pub"] = "public source B"
	if updateErr := apiClient.Update(testCtx, source); updateErr != nil {
		t.Fatal(updateErr)
	}
	awaitRequest()
	if deleteErr := apiClient.Delete(testCtx, source); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	awaitRequest()
	observer.lock.Lock()
	defer observer.lock.Unlock()
	if len(observer.listNamespaces) == 0 {
		t.Fatal("no namespace-scoped source fanout was observed")
	}
	for _, namespace := range observer.listNamespaces {
		if namespace != keys[0].Namespace {
			t.Fatalf("source event used broad/wrong-namespace Yanet inventory: %q", namespace)
		}
	}
}
