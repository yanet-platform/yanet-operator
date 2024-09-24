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
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	helpers "github.com/yanet-platform/yanet-operator/internal/helpers"
	manifests "github.com/yanet-platform/yanet-operator/internal/manifests"
)

func (r *YanetReconciler) checkUpdateRequeue(updateWindow time.Duration, updateHost string) time.Duration {
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
		r.Log.Info(fmt.Sprintf(
			`Reconcile: Yanet update try too early. Last update occured at: %s on host %s. Retry in %s \n`,
			r.lastUpdateTS,
			r.lastUpdateHost,
			retryTimer,
		))
	} else {
		r.lastUpdateTS = timeNow
		r.lastUpdateHost = updateHost
	}

	return retryTimer
}

// Reconcile logic for Yanet object
func (r *YanetReconciler) reconcilerYanet(ctx context.Context, yanet *yanetv1alpha1.Yanet, config yanetv1alpha1.YanetConfigSpec) (ctrl.Result, error) {
	// Check if the deployments already exists, if not create a new one
	deps := []*appsv1.Deployment{
		manifests.DeploymentForDataplane(ctx, yanet, config),
		manifests.DeploymentForAnnouncer(ctx, yanet, config),
		manifests.DeploymentForControlplane(ctx, yanet, config),
		manifests.DeploymentForBird(ctx, yanet, config),
	}
	sync := yanetv1alpha1.Sync{}
	updateWindow := time.Duration(config.UpdateWindow) * time.Second
	var requeueTimer time.Duration
	for _, dep := range deps {
		// Set Yanet instance as the owner and controller
		err := ctrl.SetControllerReference(yanet, dep, r.Scheme)
		if err != nil {
			r.Log.Error(err, "Can not set Yanet instance as the owner and controller")
			return ctrl.Result{}, err
		}
		found := &appsv1.Deployment{}
		err = r.Client.Get(
			ctx,
			types.NamespacedName{Name: dep.Name, Namespace: yanet.Namespace},
			found,
		)
		if err != nil && errors.IsNotFound(err) {
			if !yanet.Spec.AutoSync {
				r.Log.Info(fmt.Sprintf(
					"Deployment %s not found, but AutoSync for this host is disabled, do nothing.",
					dep.Name,
				))
				continue
			}
			r.Log.Info(fmt.Sprintf("Creating new Deployment: %s in Namespace: %s", dep.Name, dep.Namespace))
			err = r.Client.Create(ctx, dep)
			if err != nil {
				r.Log.Error(
					err,
					"Failed to create new Deployment",
					"Deployment.Namespace",
					dep.Namespace,
					"Deployment.Name",
					dep.Name,
				)
				continue // FIXME: why do not we need to skip diff check if we create new dep???????
			}
			// Deployment created successfully
		} else if err != nil {
			r.Log.Error(err, "Failed to get Deployment")
		}

		// Check deployment for the needed to update
		//r.Log.Info(fmt.Sprintf("existing deployment: %s", found.String()))
		if helpers.DeploymentDiff(ctx, dep, found) {
			r.Log.Info(fmt.Sprintf("Found diff for Deployment: %s", dep.Name))
			if !yanet.Spec.AutoSync {
				r.Log.Info(fmt.Sprintf(
					"Deployment %s requires update, but AutoSync for this host is disabled, do nothing.",
					dep.Name,
				))
				sync.OutOfSync = append(sync.OutOfSync, dep.Name)
				continue
			}
			requeueTimer = r.checkUpdateRequeue(updateWindow, yanet.Spec.NodeName)
			if requeueTimer > 0 {
				sync.SyncWaiting = append(sync.SyncWaiting, dep.Name)
				continue
			}
			err = r.Client.Update(ctx, dep)
			if err != nil {
				r.Log.Error(
					err,
					"Failed to update Deployment",
					"Deployment.Namespace",
					dep.Namespace,
					"Deployment.Name",
					dep.Name,
				)
				sync.Error = append(sync.Error, dep.Name)
				continue
			}
		}
		if *dep.Spec.Replicas == 0 {
			sync.Disabled = append(sync.Disabled, dep.Name)
		} else {
			sync.Synced = append(sync.Synced, dep.Name)
		}
	}

	// Update the Yanet status
	// List the pods for this yanet's crds
	podList := &v1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(yanet.Namespace),
		client.MatchingLabels(map[string]string{
			"topology-location-host":       yanet.Spec.NodeName,
			"app.kubernetes.io/created-by": "yanet-operator",
		}),
	}
	err := r.List(ctx, podList, listOpts...)
	if err != nil {
		r.Log.Error(
			err,
			"Can not find pods for status update, may be replicaCount = 0 in config",
			"Yanet.Namespace",
			yanet.Namespace,
			"host",
			yanet.Spec.NodeName,
		)
		return ctrl.Result{}, nil
	}

	podNames := helpers.GetPods(ctx, podList.Items)

	// Update status if needed
	if !reflect.DeepEqual(podNames, yanet.Status.Pods) || !reflect.DeepEqual(sync, yanet.Status.Sync) {
		yanet.Status.Pods = podNames
		yanet.Status.Sync = sync
		err := r.Status().Update(ctx, yanet)
		if err != nil {
			r.Log.Error(err, "Failed to update Yanet status")
			return ctrl.Result{}, err
		}
	}
	// Requeue if waiting object available
	if len(sync.SyncWaiting) != 0 {
		requeueTimer = r.checkUpdateRequeue(updateWindow, yanet.Spec.NodeName)
		return ctrl.Result{RequeueAfter: requeueTimer}, nil
	}
	return ctrl.Result{}, nil
}
