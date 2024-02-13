/*
Copyright 2023 YANDEX LLC.

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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

// YanetConfigReconciler reconciles a YanetConfig object
type YanetConfigReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	GlobalConfig *yanetv1alpha1.MutexYanetConfigSpec
	Log          logr.Logger
}

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the YanetConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *YanetConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log = log.FromContext(ctx)
	r.Log.Info(fmt.Sprintf("Reconcile config loop called for NamespacedName: %s", req.NamespacedName))

	config := &yanetv1alpha1.YanetConfig{}
	err := r.Client.Get(ctx, req.NamespacedName, config)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info(fmt.Sprintf(
				`Reconcile config: YanetConfig resource not found in cluster for NamespacedName: %s.
				Ignoring since object must be deleted`,
				req.NamespacedName,
			))
		} else {
			r.Log.Error(err, "Failed to get Node object")
			return ctrl.Result{}, err
		}
	} else {
		r.Log.Info(fmt.Sprintf("Reconcile config: successfully found YanetConfig object for NamespacedName: %s", req.NamespacedName))
		r.Log.Info(fmt.Sprintf("Reconcile config: update GlobalConfig with new config: %+v", config))
		// TODO: add config validator
		r.GlobalConfig.Lock.Lock()
		r.GlobalConfig.Config = config.Spec
		r.GlobalConfig.Lock.Unlock()
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *YanetConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&yanetv1alpha1.YanetConfig{}).
		Complete(r)
}
