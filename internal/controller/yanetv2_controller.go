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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
)

// YanetV2Reconciler reconciles YanetV2 (yanetsv2.yanet.yanet-platform.io)
// resources. It is completely independent from YanetReconciler (which
// handles v1alpha1.Yanet via the legacy CRD yanets.yanet-platform.io).
// The two API kinds live in the same Kubernetes API group but are
// stored as separate CRDs — there is no conversion or storage-version
// dispatching between them.
type YanetV2Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// GlobalConfigV2 is the in-memory v2alpha1 YanetConfigV2 snapshot
	// maintained by YanetConfigReconcilerV2.
	GlobalConfigV2 *yanetv2alpha1.MutexYanetConfigSpec

	// lock guards lastUpdateTS / lastUpdateHost — the per-controller
	// "updateWindow" throttle. Independent from YanetReconciler's
	// state so v1 and v2 do not interfere with each other.
	lock           sync.Mutex
	lastUpdateTS   time.Time
	lastUpdateHost string
}

// checkUpdateRequeue throttles concurrent updates across nodes within
// the configured updateWindow. Mirrors YanetReconciler.checkUpdateRequeue
// (v1 path) but uses this reconciler's own state to keep v1 and v2
// fully independent.
func (r *YanetV2Reconciler) checkUpdateRequeue(logger logr.Logger, updateWindow time.Duration, updateHost string) time.Duration {
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

//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetsv2,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetsv2/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetsv2/finalizers,verbs=update
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigsv2,verbs=get;list;watch
//+kubebuilder:rbac:groups=yanet.yanet-platform.io,resources=yanetconfigsv2/status,verbs=get;update;patch

// Reconcile fetches the YanetV2 instance and delegates to the v2 logic
// in reconcileYanetV2 (yanet_reconciler_v2.go). Node events and v1
// Yanet CRs are NOT handled here — they belong to YanetReconciler.
func (r *YanetV2Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)
	logger.Info("V2 reconcile loop called", "namespacedName", req.NamespacedName)

	yanet := &yanetv2alpha1.YanetV2{}
	if err := r.Client.Get(ctx, req.NamespacedName, yanet); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error while getting YanetV2 object")
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
		return ctrl.Result{}, err
	}

	result, reconcileErr := r.reconcileYanetV2(ctx, yanet)

	duration := time.Since(startTime).Seconds()
	yanetReconcileDuration.WithLabelValues(req.Name, req.Namespace).Observe(duration)
	if reconcileErr != nil {
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "error").Inc()
	} else {
		yanetReconcileTotal.WithLabelValues(req.Name, req.Namespace, "success").Inc()
	}
	return result, reconcileErr
}

// SetupWithManager wires the v2 controller.
//
// Watches:
//   - v2alpha1.YanetV2 (primary)
//   - v2alpha1.YanetConfigV2 (mapped to all YanetV2 CRs so a config
//     change — e.g. hugepages update — triggers re-reconcile)
//   - corev1.Node (mapped to YanetV2 via nodeSelector)
//   - appsv1.Deployment (Owns)
//   - corev1.Pod (filtered by manifests.LabelYanet to enqueue the
//     owning YanetV2 when a managed Pod changes phase)
func (r *YanetV2Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Filter Pod events by the v2 ownership label so we don't dequeue
	// work for every Pod in the cluster.
	yanetPodPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetLabels()[manifests.LabelYanet]
		return ok
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&yanetv2alpha1.YanetV2{}).
		Watches(&yanetv2alpha1.YanetConfigV2{}, handler.EnqueueRequestsFromMapFunc(r.mapConfigToV2Yanets)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapNodeToV2Yanets)).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToYanetV2),
			builder.WithPredicates(yanetPodPredicate),
		).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// mapConfigToV2Yanets enqueues all YanetV2 CRs whenever the
// YanetConfigV2 object changes. This is required so that component
// spec changes (e.g. hugepages count/size) trigger a re-reconcile of
// all installations that depend on the config snapshot.
func (r *YanetV2Reconciler) mapConfigToV2Yanets(ctx context.Context, _ client.Object) []ctrl.Request {
	list := &yanetv2alpha1.YanetV2List{}
	if err := r.Client.List(ctx, list); err != nil {
		return nil
	}
	out := make([]ctrl.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}

// mapNodeToV2Yanets enqueues every YanetV2 whose nodeSelector matches
// the labels of the changed Node.
func (r *YanetV2Reconciler) mapNodeToV2Yanets(ctx context.Context, obj client.Object) []ctrl.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}
	list := &yanetv2alpha1.YanetV2List{}
	if err := r.Client.List(ctx, list); err != nil {
		return nil
	}
	var out []ctrl.Request
	for i := range list.Items {
		y := &list.Items[i]
		if labelsMatchSelector(node.Labels, y.Spec.NodeSelector) {
			out = append(out, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(y)})
		}
	}
	return out
}

// mapPodToYanetV2 enqueues the owning YanetV2 when a managed Pod
// changes phase. The YanetV2 name is read from the Pod's
// "yanet.yanet-platform.io/yanet" label set by the v2 builder.
func (r *YanetV2Reconciler) mapPodToYanetV2(_ context.Context, obj client.Object) []ctrl.Request {
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
