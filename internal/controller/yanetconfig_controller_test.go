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
	"testing"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMapConfigToYanetsRefreshesSnapshotBeforeEnqueue(t *testing.T) {
	cfg := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
		Spec:       minimalConfig(),
	}
	cfg.Spec.UpdateWindow = 17
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	r, snapshot := makeReconcilerEnv(t, cfg, yanet)
	snapshot.Config.UpdateWindow = 3

	requests := r.mapConfigToYanets(context.Background(), cfg)

	if len(requests) != 1 || requests[0].Name != yanet.Name || requests[0].Namespace != yanet.Namespace {
		t.Fatalf("unexpected requests: %+v", requests)
	}
	if snapshot.Config.UpdateWindow != cfg.Spec.UpdateWindow {
		t.Fatalf("snapshot was not refreshed before enqueue: got %d want %d",
			snapshot.Config.UpdateWindow, cfg.Spec.UpdateWindow)
	}
}

func TestMapConfigToYanetsClearsSnapshotAfterDelete(t *testing.T) {
	cfg := &yanetv1alpha1.YanetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: yanetv1alpha1.YanetConfigName},
		Spec:       minimalConfig(),
	}
	yanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "yanet"},
	}
	r, snapshot := makeReconcilerEnv(t, cfg, yanet)
	snapshot.Config = minimalConfig()
	if err := r.Client.Delete(context.Background(), cfg); err != nil {
		t.Fatalf("delete config: %v", err)
	}

	requests := r.mapConfigToYanets(context.Background(), cfg)

	if len(requests) != 1 {
		t.Fatalf("config deletion must enqueue Yanet resources, got %+v", requests)
	}
	if len(snapshot.Config.BoxTypes) != 0 {
		t.Fatalf("snapshot was not cleared after config deletion: %+v", snapshot.Config.BoxTypes)
	}
}
