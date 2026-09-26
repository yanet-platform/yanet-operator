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

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

// YanetConfigReconciler watches v1alpha1.YanetConfig and keeps an
// in-memory deep-copy of the latest seen Spec in GlobalConfig.
type YanetConfigReconciler struct {
	client.Client
	APIReader    client.Reader
	Scheme       *runtime.Scheme
	GlobalConfig *yanetv1alpha1.MutexYanetConfigSpec
}

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile updates the singleton in-memory snapshot whenever the
// cluster-scoped YanetConfig changes.
func (r *YanetConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("yanetconfig", req.NamespacedName)

	cfg, err := refreshYanetConfigSnapshot(ctx, r.Client, r.GlobalConfig)
	if err != nil {
		logger.Error(err, "failed to refresh YanetConfig snapshot")
		return ctrl.Result{}, err
	}
	if cfg == nil {
		logger.Info("YanetConfig snapshot cleared; singleton does not exist")
		return ctrl.Result{}, nil
	}

	logger.V(1).Info("YanetConfig snapshot updated",
		"boxTypes", len(cfg.Spec.BoxTypes),
		"patches", len(cfg.Spec.Patches),
		"operators", len(cfg.Spec.Components.Operators),
	)
	if cfg.Spec.Stop {
		logger.Info("YanetConfig.spec.stop is true, skipping shared Service reconcile")
		return ctrl.Result{}, nil
	}
	if err := r.reconcileSharedServices(ctx, cfg, logger); err != nil {
		logger.Error(err, "failed to reconcile shared Services")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func refreshYanetConfigSnapshot(
	ctx context.Context,
	c client.Client,
	snapshot *yanetv1alpha1.MutexYanetConfigSpec,
) (*yanetv1alpha1.YanetConfig, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("GlobalConfig is nil")
	}
	// Both the config reconciler and the Yanet watch mapper refresh this
	// snapshot. Serialize the read as well as publication so an older in-flight
	// read cannot overwrite a newer config (including the global stop flag).
	snapshot.Lock.Lock()
	defer snapshot.Lock.Unlock()
	cfg := &yanetv1alpha1.YanetConfig{}
	err := c.Get(ctx, client.ObjectKey{Name: yanetv1alpha1.YanetConfigName}, cfg)
	if err != nil && !apierrors.IsNotFound(err) {
		snapshot.Config = yanetv1alpha1.YanetConfigSpec{}
		return nil, err
	}

	if apierrors.IsNotFound(err) {
		snapshot.Config = yanetv1alpha1.YanetConfigSpec{}
		return nil, nil
	}
	snapshot.Config = *cfg.Spec.DeepCopy()
	return cfg, nil
}

// SetupWithManager wires the controller to watch v1alpha1.YanetConfig.
func (r *YanetConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	return ctrl.NewControllerManagedBy(mgr).
		For(&yanetv1alpha1.YanetConfig{}).
		Watches(&yanetv1alpha1.Yanet{}, handler.EnqueueRequestsFromMapFunc(enqueueYanetConfigSingleton)).
		Owns(&corev1.Service{}).
		Complete(r)
}

func enqueueYanetConfigSingleton(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: yanetv1alpha1.YanetConfigName},
	}}
}
