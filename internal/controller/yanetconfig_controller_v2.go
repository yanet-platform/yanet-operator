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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
)

// YanetConfigReconcilerV2 watches v2alpha1.YanetConfigV2 and keeps an
// in-memory deep-copy of the latest seen Spec in GlobalConfigV2.
//
// Mirrors the v1 YanetConfigReconciler design (in-memory snapshot,
// mutex-protected, singleton-style). The YanetV2 reconciler reads from
// this snapshot instead of hitting the API on every reconcile, just
// like the v1 path.
type YanetConfigReconcilerV2 struct {
	client.Client
	Scheme         *runtime.Scheme
	GlobalConfigV2 *yanetv2alpha1.MutexYanetConfigSpec
}

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigsv2,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigsv2/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigsv2/finalizers,verbs=update
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetsv2,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile updates the singleton in-memory snapshot whenever the
// cluster-scoped YanetConfigV2 changes.
func (r *YanetConfigReconcilerV2) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("yanetconfig-v2", req.NamespacedName)

	cfg, err := refreshYanetConfigV2Snapshot(ctx, r.Client, r.GlobalConfigV2)
	if err != nil {
		logger.Error(err, "failed to refresh YanetConfigV2 snapshot")
		return ctrl.Result{}, err
	}
	if cfg == nil {
		logger.Info("YanetConfigV2 snapshot cleared; singleton does not exist")
		return ctrl.Result{}, nil
	}

	logger.V(1).Info("YanetConfigV2 v2 snapshot updated",
		"boxTypes", len(cfg.Spec.BoxTypes),
		"patches", len(cfg.Spec.Patches),
		"operators", len(cfg.Spec.Components.Operators),
	)
	if cfg.Spec.Stop {
		logger.Info("YanetConfigV2.spec.stop is true, skipping shared Service reconcile")
		return ctrl.Result{}, nil
	}
	if err := r.reconcileSharedServicesV2(ctx, cfg, logger); err != nil {
		logger.Error(err, "failed to reconcile shared Services")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func refreshYanetConfigV2Snapshot(
	ctx context.Context,
	c client.Client,
	snapshot *yanetv2alpha1.MutexYanetConfigSpec,
) (*yanetv2alpha1.YanetConfigV2, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("GlobalConfigV2 is nil")
	}
	cfg := &yanetv2alpha1.YanetConfigV2{}
	err := c.Get(ctx, client.ObjectKey{Name: yanetv2alpha1.YanetConfigName}, cfg)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	if apierrors.IsNotFound(err) {
		snapshot.Config = yanetv2alpha1.YanetConfigSpec{}
		return nil, nil
	}
	snapshot.Config = *cfg.Spec.DeepCopy()
	return cfg, nil
}

func clearYanetConfigV2Snapshot(snapshot *yanetv2alpha1.MutexYanetConfigSpec) {
	if snapshot == nil {
		return
	}
	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	snapshot.Config = yanetv2alpha1.YanetConfigSpec{}
}

// SetupWithManager wires the controller to watch v2alpha1.YanetConfigV2.
// Named() is required: controller-runtime derives the default name from
// the Kind in For(), which collides with the v1alpha1 YanetConfigV2 reconciler.
func (r *YanetConfigReconcilerV2) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("yanetconfig-v2alpha1").
		For(&yanetv2alpha1.YanetConfigV2{}).
		Watches(&yanetv2alpha1.YanetV2{}, handler.EnqueueRequestsFromMapFunc(enqueueYanetConfigV2Singleton)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(enqueueYanetConfigV2Singleton)).
		Owns(&corev1.Service{}).
		Complete(r)
}

func enqueueYanetConfigV2Singleton(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: yanetv2alpha1.YanetConfigName},
	}}
}
