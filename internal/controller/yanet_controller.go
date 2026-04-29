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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

// YanetReconciler reconciles a Yanet object
type YanetReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	GlobalConfig   *yanetv1alpha1.MutexYanetConfigSpec
	lock           sync.Mutex
	lastUpdateTS   time.Time
	lastUpdateHost string
}

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;update;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=configmap,verbs=get;list;watch;create;update;patch;update;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;update;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Yanet object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *YanetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)
	logger.Info("Reconcile loop called", "namespacedName", req.NamespacedName)

	// Deep copy config under lock to avoid data race on nested slices/maps.
	r.GlobalConfig.Lock.Lock()
	config := *r.GlobalConfig.Config.DeepCopy()
	r.GlobalConfig.Lock.Unlock()

	if config.Stop {
		logger.Info("Reconcile loop detected global stop options, do nothing!")
		return ctrl.Result{}, nil
	}

	yanet := &yanetv1alpha1.Yanet{}
	err := r.Client.Get(ctx, req.NamespacedName, yanet)
	if err != nil {
		if !errors.IsNotFound(err) {
			// Error reading the object - requeue the request.
			logger.Error(err, "Error while getting Yanet object")
			yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
			return ctrl.Result{}, err
		}
	} else {
		logger.Info("Successfully found Yanet object", "namespacedName", req.NamespacedName)
		result, reconcileErr := r.reconcilerYanet(ctx, yanet, config)

		// Record metrics
		duration := time.Since(startTime).Seconds()
		yanetReconcileDuration.WithLabelValues(req.Name, req.Namespace).Observe(duration)

		if reconcileErr != nil {
			yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
		} else {
			yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "success").Inc()
		}

		return result, reconcileErr
	}

	// Handle Node events for AutoDiscovery and cleanup
	node := &v1.Node{}
	err = r.Client.Get(ctx, req.NamespacedName, node)
	if err != nil {
		if errors.IsNotFound(err) {
			// Node deleted - find and delete corresponding Yanet CRD
			logger.Info("Node deleted, looking for corresponding Yanet CRD", "node", req.NamespacedName.Name)
			return r.handleNodeDeletion(ctx, req.NamespacedName.Name)
		} else {
			logger.Error(err, "Failed to get Node object")
			return ctrl.Result{}, err
		}
	}

	// Node exists - handle AutoDiscovery if enabled
	logger.Info("Successfully found Node object", "namespacedName", req.NamespacedName)
	if config.AutoDiscovery.Enable {
		return r.reconcilerNode(ctx, &config, node)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *YanetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&yanetv1alpha1.Yanet{}).
		Watches(&v1.Node{}, &handler.EnqueueRequestForObject{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
