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
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestYanetConfigReconcileV2SerializesSnapshotReadAndPublication(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
		Spec:       minimalConfigV2(),
	}
	scheme := newSchemeForTest(t)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
	snapshot := &yanetv2alpha1.MutexYanetConfigSpec{}
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRead) }) }
	defer release()
	cl := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			cfg := obj.(*yanetv2alpha1.YanetConfigV2)
			if cfg.Spec.Stop {
				return nil
			}
			close(readStarted)
			select {
			case <-releaseRead:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}
	firstDone := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(testContext, ctrl.Request{})
		firstDone <- err
	}()
	select {
	case <-readStarted:
	case <-testContext.Done():
		t.Fatal("snapshot read did not start")
	}
	if snapshot.Lock.TryLock() {
		snapshot.Lock.Unlock()
		t.Error("snapshot fetch must hold the publication lock so an old read cannot overwrite a newer config")
	}
	latest := &yanetv2alpha1.YanetConfigV2{}
	if err := base.Get(testContext, client.ObjectKeyFromObject(config), latest); err != nil {
		t.Fatalf("get config: %v", err)
	}
	latest.Spec.Stop = true
	if err := base.Update(testContext, latest); err != nil {
		t.Fatalf("set global stop: %v", err)
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(testContext, ctrl.Request{})
		secondDone <- err
	}()
	// The newer read must wait for the older read to publish, rather than
	// publish stop=true and then let the older read overwrite it with false.
	select {
	case err := <-secondDone:
		if err != nil {
			t.Errorf("newer reconcile: %v", err)
		}
		t.Error("newer refresh completed before the in-flight snapshot was published")
		secondDone <- nil
	case <-time.After(50 * time.Millisecond):
	}
	release()
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("reconcile: %v", err)
			}
		case <-testContext.Done():
			t.Fatal("concurrent snapshot refresh did not finish")
		}
	}
	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	if !snapshot.Config.Stop {
		t.Fatal("older in-flight refresh replaced the latest global stop")
	}
}

func TestYanetConfigReconcileV2PublishesIndependentSnapshot(t *testing.T) {
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
		Spec:       minimalConfigV2(),
	}
	config.Spec.BoxTypes[0].Operators = map[string]yanetv2alpha1.BoxOperator{"route": {}}
	scheme := newSchemeForTest(t)
	var fetched *yanetv2alpha1.YanetConfigV2
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				fetched = obj.(*yanetv2alpha1.YanetConfigV2)
				return nil
			},
		}).Build()
	snapshot := &yanetv2alpha1.MutexYanetConfigSpec{}
	r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fetched.Spec.BoxTypes[0].Name = "changed"
	delete(fetched.Spec.BoxTypes[0].Operators, "route")
	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	if snapshot.Config.BoxTypes[0].Name != "release" {
		t.Fatal("snapshot retained a reference to the fetched slice")
	}
	if _, ok := snapshot.Config.BoxTypes[0].Operators["route"]; !ok {
		t.Fatal("snapshot retained a reference to the fetched map")
	}
}

func TestYanetConfigReconcileV2ClearsSnapshotOnRefreshError(t *testing.T) {
	for _, readErr := range []error{errors.New("config read failed"), context.Canceled} {
		t.Run(readErr.Error(), func(t *testing.T) {
			config := &yanetv2alpha1.YanetConfigV2{
				ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
				Spec:       minimalConfigV2(),
			}
			config.Spec.Stop = true
			config.Spec.UpdateWindow = 17
			scheme := newSchemeForTest(t)
			failNext := true
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if failNext {
							failNext = false
							return readErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
			snapshot := &yanetv2alpha1.MutexYanetConfigSpec{Config: minimalConfigV2()}
			r := &YanetConfigReconcilerV2{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{}); !errors.Is(err, readErr) {
				t.Fatalf("refresh failure must be returned for retry: %v", err)
			}
			snapshot.Lock.Lock()
			cleared := reflect.DeepEqual(snapshot.Config, yanetv2alpha1.YanetConfigSpec{})
			snapshot.Lock.Unlock()
			if !cleared {
				t.Fatal("failed refresh must clear the snapshot without relying on a watch mapper")
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
				t.Fatalf("retry refresh: %v", err)
			}
			snapshot.Lock.Lock()
			defer snapshot.Lock.Unlock()
			if !snapshot.Config.Stop || snapshot.Config.UpdateWindow != 17 {
				t.Fatal("successful retry must publish the newer snapshot after the failed read")
			}
		})
	}
}

func TestMapConfigToV2YanetsRefreshErrorPreservesSubsequentPublication(t *testing.T) {
	testContext := context.Background()
	config := &yanetv2alpha1.YanetConfigV2{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv2alpha1.YanetConfigName},
		Spec:       minimalConfigV2(),
	}
	config.Spec.Stop = true
	installation := &yanetv2alpha1.YanetV2{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "yanet"},
	}
	scheme := newSchemeForTest(t)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, installation).Build()
	snapshot := &yanetv2alpha1.MutexYanetConfigSpec{Config: minimalConfigV2()}
	configReconciler := &YanetConfigReconcilerV2{Client: base, Scheme: scheme, GlobalConfigV2: snapshot}
	cl := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("config read failed")
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			snapshot.Lock.Lock()
			cleared := reflect.DeepEqual(snapshot.Config, yanetv2alpha1.YanetConfigSpec{})
			snapshot.Lock.Unlock()
			if !cleared {
				t.Error("failed refresh must clear the stale snapshot before fan-out")
			}
			// Publish a newer stop while the failed watch mapper is still running.
			if _, err := configReconciler.Reconcile(ctx, ctrl.Request{}); err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
	})
	r := &YanetV2Reconciler{Client: cl, Scheme: scheme, GlobalConfigV2: snapshot}
	requests := r.mapConfigToV2Yanets(testContext, config)
	if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(installation) {
		t.Fatalf("refresh failure must still enqueue installations: %+v", requests)
	}
	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	if !snapshot.Config.Stop {
		t.Fatal("failed watch mapper erased the subsequently published stop")
	}
}
