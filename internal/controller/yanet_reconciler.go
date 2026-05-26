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
	"reflect"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	helpers "github.com/yanet-platform/yanet-operator/internal/helpers"
	manifests "github.com/yanet-platform/yanet-operator/internal/manifests"
)

const yanetFinalizer = "yanet.yanet-platform.io/finalizer"

// checkUpdateRequeue checks if enough time has passed since the last update on a different host.
// logger is passed as parameter because this method does not have access to a context.
func (r *YanetReconciler) checkUpdateRequeue(logger logr.Logger, updateWindow time.Duration, updateHost string) time.Duration {
	var retryTimer time.Duration
	if updateWindow == 0 {
		return retryTimer
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	timeNow := time.Now()
	timerExpired := r.lastUpdateTS.Add(updateWindow).Before(timeNow)
	if !timerExpired && updateHost != r.lastUpdateHost {
		retryTimer = updateWindow - timeNow.Sub(r.lastUpdateTS)
		logger.Info("YanetV2 update try too early, will retry",
			"lastUpdateTime", r.lastUpdateTS,
			"lastUpdateHost", r.lastUpdateHost,
			"retryIn", retryTimer)
	} else {
		r.lastUpdateTS = timeNow
		r.lastUpdateHost = updateHost
	}

	return retryTimer
}

// Reconcile logic for YanetV2 object
func (r *YanetReconciler) reconcilerYanet(ctx context.Context, yanet *yanetv1alpha1.Yanet, config yanetv1alpha1.YanetConfigSpec) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Handle deletion
	if !yanet.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
			// Perform cleanup if needed
			logger.Info("YanetV2 is being deleted, running cleanup (no-op; deletion is handled by ownerReferences GC)")
			r.Recorder.Eventf(yanet, nil, v1.EventTypeNormal, "Cleanup", "Finalize",
				"Cleanup (no-op; deletion is handled by ownerReferences GC)")

			// Remove finalizer to allow deletion
			controllerutil.RemoveFinalizer(yanet, yanetFinalizer)
			if err := r.Update(ctx, yanet); err != nil {
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
		controllerutil.AddFinalizer(yanet, yanetFinalizer)
		if err := r.Update(ctx, yanet); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue with reconciliation
		return ctrl.Result{Requeue: true}, nil
	}

	// Get nodes for capacity check
	nodes, err := helpers.GetNodes(ctx, r.Client)
	if err != nil {
		logger.Error(err, "Failed to get nodes")
		return ctrl.Result{}, err
	}
	// Check if the deployments already exists, if not create a new one
	deps := []*appsv1.Deployment{
		manifests.DeploymentForDataplane(ctx, yanet, config, nodes),
		manifests.DeploymentForAnnouncer(ctx, yanet, config, nodes),
		manifests.DeploymentForControlplane(ctx, yanet, config, nodes),
		manifests.DeploymentForBird(ctx, yanet, config, nodes),
	}
	sync := yanetv1alpha1.Sync{}
	updateWindow := time.Duration(config.UpdateWindow) * time.Second
	var requeueTimer time.Duration
	for _, dep := range deps {
		// Set YanetV2 instance as the owner and controller
		if setErr := ctrl.SetControllerReference(yanet, dep, r.Scheme); setErr != nil {
			logger.Error(setErr, "Can not set YanetV2 instance as the owner and controller")
			return ctrl.Result{}, setErr
		}
		found := &appsv1.Deployment{}
		err = r.Client.Get(
			ctx,
			types.NamespacedName{Name: dep.Name, Namespace: yanet.Namespace},
			found,
		)
		if err != nil && errors.IsNotFound(err) {
			if !yanet.Spec.AutoSync {
				logger.Info("Deployment not found, but AutoSync disabled",
					"deployment", dep.Name,
					"host", yanet.Spec.NodeName)
				continue
			}
			logger.Info("Creating new Deployment",
				"deployment", dep.Name,
				"namespace", dep.Namespace)
			err = r.Client.Create(ctx, dep)
			if err != nil {
				logger.Error(
					err,
					"Failed to create new Deployment",
					"Deployment.Namespace",
					dep.Namespace,
					"Deployment.Name",
					dep.Name,
				)
				r.Recorder.Eventf(yanet, nil, v1.EventTypeWarning, "DeploymentCreateFailed", "Create",
					"Failed to create deployment %s: %v", dep.Name, err)
				sync.Error = append(sync.Error, dep.Name)
				continue
			}
			r.Recorder.Eventf(yanet, nil, v1.EventTypeNormal, "DeploymentCreated", "Create",
				"Created deployment %s", dep.Name)
			// Deployment created successfully — record in sync status and skip diff check
			if *dep.Spec.Replicas == 0 {
				sync.Disabled = append(sync.Disabled, dep.Name)
			} else {
				sync.Synced = append(sync.Synced, dep.Name)
			}
			continue
		} else if err != nil {
			// Non-NotFound error (network issue, timeout, etc.) — skip this deployment
			// to avoid comparing against an empty found object which would produce a false diff.
			logger.Error(err, "Failed to get Deployment")
			sync.Error = append(sync.Error, dep.Name)
			continue
		}

		// Check deployment for the needed to update
		if helpers.DeploymentDiff(ctx, dep, found) {
			logger.Info("Found diff for Deployment", "deployment", dep.Name)
			if !yanet.Spec.AutoSync {
				logger.Info("Deployment requires update, but AutoSync disabled",
					"deployment", dep.Name,
					"host", yanet.Spec.NodeName)
				sync.OutOfSync = append(sync.OutOfSync, dep.Name)
				continue
			}
			requeueTimer = r.checkUpdateRequeue(logger, updateWindow, yanet.Spec.NodeName)
			if requeueTimer > 0 {
				r.Recorder.Eventf(yanet, nil, v1.EventTypeNormal, "UpdateWindowWait", "Update",
					"Waiting %s before updating %s (UpdateWindow)", requeueTimer, dep.Name)
				sync.SyncWaiting = append(sync.SyncWaiting, dep.Name)
				continue
			}
			// Copy desired spec fields from dep to found to preserve ResourceVersion
			found.Spec.Replicas = dep.Spec.Replicas
			found.Spec.Template = dep.Spec.Template
			err = r.Client.Update(ctx, found)
			if err != nil {
				logger.Error(
					err,
					"Failed to update Deployment",
					"Deployment.Namespace",
					dep.Namespace,
					"Deployment.Name",
					dep.Name,
				)
				r.Recorder.Eventf(yanet, nil, v1.EventTypeWarning, "DeploymentUpdateFailed", "Update",
					"Failed to update deployment %s: %v", dep.Name, err)
				sync.Error = append(sync.Error, dep.Name)
				continue
			}
			r.Recorder.Eventf(yanet, nil, v1.EventTypeNormal, "DeploymentUpdated", "Update",
				"Updated deployment %s", dep.Name)
		}
		if *dep.Spec.Replicas == 0 {
			sync.Disabled = append(sync.Disabled, dep.Name)
		} else {
			sync.Synced = append(sync.Synced, dep.Name)
		}
	}

	// Update the YanetV2 status
	// List the pods for this yanet's crds
	podList := &v1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(yanet.Namespace),
		client.MatchingLabels(map[string]string{
			"topology-location-host":       yanet.Spec.NodeName,
			"app.kubernetes.io/created-by": "yanet-operator",
		}),
	}
	err = r.List(ctx, podList, listOpts...)
	if err != nil {
		logger.Error(
			err,
			"Can not find pods for status update, may be replicaCount = 0 in config",
			"YanetV2.Namespace",
			yanet.Namespace,
			"host",
			yanet.Spec.NodeName,
		)
		return ctrl.Result{}, nil
	}

	podNames := helpers.GetPods(ctx, podList.Items)

	// Update conditions based on sync status
	conditions := r.computeConditions(yanet, sync, podNames)

	// Update metrics for out-of-sync deployments
	outOfSyncCount := len(sync.OutOfSync) + len(sync.Error)
	yanetDeploymentsOutOfSync.WithLabelValues(yanet.Name, yanet.Namespace).Set(float64(outOfSyncCount))

	// Update status if needed. Wrap in RetryOnConflict to handle the 409
	// that occurs when two replicas (or two rapid reconcile cycles triggered
	// by Pod/Deployment events) race to write status with the same
	// resourceVersion.
	if !reflect.DeepEqual(podNames, yanet.Status.Pods) ||
		!reflect.DeepEqual(sync, yanet.Status.Sync) ||
		!reflect.DeepEqual(conditions, yanet.Status.Conditions) {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Re-fetch the latest version before each attempt so the
			// resourceVersion is always current.
			fresh := &yanetv1alpha1.Yanet{}
			if getErr := r.Client.Get(ctx, types.NamespacedName{
				Name:      yanet.Name,
				Namespace: yanet.Namespace,
			}, fresh); getErr != nil {
				return getErr
			}
			fresh.Status.Pods = podNames
			fresh.Status.Sync = sync
			fresh.Status.Conditions = conditions
			return r.Status().Update(ctx, fresh)
		})
		if retryErr != nil {
			logger.Error(retryErr, "Failed to update Yanet status")
			return ctrl.Result{}, retryErr
		}
	}
	// Requeue if waiting object available
	if len(sync.SyncWaiting) != 0 {
		requeueTimer = r.checkUpdateRequeue(logger, updateWindow, yanet.Spec.NodeName)
		return ctrl.Result{RequeueAfter: requeueTimer}, nil
	}
	return ctrl.Result{}, nil
}
