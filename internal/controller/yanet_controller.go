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

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
)

// YanetReconciler reconciles component-based installations.
type YanetReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// GlobalConfig is the in-memory v1alpha1 YanetConfig snapshot
	// maintained by YanetConfigReconciler.
	GlobalConfig *yanetv1alpha1.MutexYanetConfigSpec

	// lock guards lastUpdateTS / lastUpdateHost for the updateWindow throttle.
	lock           sync.Mutex
	lastUpdateTS   time.Time
	lastUpdateHost string
}

// checkUpdateRequeue throttles concurrent updates across nodes within
// the configured updateWindow.
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
		logger.Info("Yanet update try too early, will retry",
			"lastUpdateTime", r.lastUpdateTS,
			"lastUpdateHost", r.lastUpdateHost,
			"retryIn", retryTimer)
	} else {
		r.lastUpdateTS = timeNow
		r.lastUpdateHost = updateHost
	}
	return retryTimer
}

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanets/finalizers,verbs=update
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=nodes;pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch;update

// Reconcile fetches the installation and reconciles its desired workloads.
func (r *YanetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)
	logger.Info("Reconcile loop called", "namespacedName", req.NamespacedName)

	yanet := &yanetv1alpha1.Yanet{}
	if err := r.Client.Get(ctx, req.NamespacedName, yanet); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error while getting Yanet object")
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
		return ctrl.Result{}, err
	}

	result, reconcileErr := r.reconcileYanet(ctx, yanet)

	duration := time.Since(startTime).Seconds()
	yanetReconcileDuration.WithLabelValues(req.Name, req.Namespace).Observe(duration)
	if reconcileErr != nil {
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
	} else {
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "success").Inc()
	}
	return result, reconcileErr
}

// SetupWithManager wires the controller.
//
// Watches:
//   - v1alpha1.Yanet (primary)
//   - v1alpha1.YanetConfig (mapped to all Yanet CRs so a config
//     change — e.g. hugepages update — triggers re-reconcile)
//   - corev1.Node (mapped to Yanet via nodeSelector)
//   - appsv1.Deployment and corev1.ConfigMap (Owns)
//   - corev1.Pod (mapped by manifests.LabelYanet to enqueue the
//     owning Yanet when a managed Pod changes phase or loses its label)
func (r *YanetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&yanetv1alpha1.Yanet{}).
		Watches(&yanetv1alpha1.YanetConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapConfigToYanets)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapNodeToYanets)).
		// The mapper filters unlabelled Pods and handles both sides of an
		// update. A new-object-only predicate would drop label-removal events.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToYanet)).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

// mapConfigToYanets enqueues all Yanet CRs whenever the
// YanetConfig object changes. This is required so that component
// spec changes (e.g. hugepages count/size) trigger a re-reconcile of
// all installations that depend on the config snapshot.
func (r *YanetReconciler) mapConfigToYanets(ctx context.Context, _ client.Object) []ctrl.Request {
	if _, err := refreshYanetConfigSnapshot(ctx, r.Client, r.GlobalConfig); err != nil {
		// Refresh already cleared the failed read under the publication lock.
		// Clearing here could erase a newer snapshot published in the meantime.
		log.FromContext(ctx).Error(err, "failed to refresh YanetConfig snapshot before enqueueing Yanet resources")
	}
	list := &yanetv1alpha1.YanetList{}
	if err := r.Client.List(ctx, list); err != nil {
		return nil
	}
	out := make([]ctrl.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

// mapNodeToYanets enqueues every Yanet whose nodeSelector matches
// the labels of the changed Node.
func (r *YanetReconciler) mapNodeToYanets(ctx context.Context, obj client.Object) []ctrl.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}
	list := &yanetv1alpha1.YanetList{}
	if err := r.Client.List(ctx, list); err != nil {
		return nil
	}
	var out []ctrl.Request
	for i := range list.Items {
		y := &list.Items[i]
		if labels.SelectorFromSet(y.Spec.NodeSelector).Matches(labels.Set(node.Labels)) {
			out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
		}
	}
	return out
}

// mapPodToYanet enqueues the owning Yanet when a managed Pod
// changes phase. The Yanet name is read from the Pod's
// "yanet.yanet-platform.io/yanet" label set by the builder.
func (r *YanetReconciler) mapPodToYanet(_ context.Context, obj client.Object) []ctrl.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	name := pod.Labels[manifests.LabelYanet]
	if name == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{Namespace: pod.Namespace, Name: name},
	}}
}
