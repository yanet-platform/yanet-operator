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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

// Reconcile updates the in-memory snapshot whenever the v2alpha1
// YanetConfigV2 changes. Deletion clears the snapshot back to a zero
// value so the YanetV2 reconciler can detect "no config available".
func (r *YanetConfigReconcilerV2) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("yanetconfig-v2", req.NamespacedName)

	cfg := &yanetv2alpha1.YanetConfigV2{}
	err := r.Client.Get(ctx, req.NamespacedName, cfg)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("YanetConfigV2 v2 deleted; clearing in-memory snapshot")
			r.GlobalConfigV2.Lock.Lock()
			r.GlobalConfigV2.Config = yanetv2alpha1.YanetConfigSpec{}
			r.GlobalConfigV2.Lock.Unlock()
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get YanetConfigV2 v2")
		return ctrl.Result{}, err
	}

	r.GlobalConfigV2.Lock.Lock()
	r.GlobalConfigV2.Config = *cfg.Spec.DeepCopy()
	r.GlobalConfigV2.Lock.Unlock()

	logger.V(1).Info("YanetConfigV2 v2 snapshot updated",
		"boxTypes", len(cfg.Spec.BoxTypes),
		"patches", len(cfg.Spec.Patches),
		"operators", len(cfg.Spec.Components.Operators),
	)
	return ctrl.Result{}, nil
}

// SetupWithManager wires the controller to watch v2alpha1.YanetConfigV2.
// Named() is required: controller-runtime derives the default name from
// the Kind in For(), which collides with the v1alpha1 YanetConfigV2 reconciler.
func (r *YanetConfigReconcilerV2) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("yanetconfig-v2alpha1").
		For(&yanetv2alpha1.YanetConfigV2{}).
		Complete(r)
}
