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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// updateStatusV2 fetches the latest version of the YanetV2 CR and
// applies the given mutator to its in-memory copy, then writes Status
// via Status().Update wrapped in retry.RetryOnConflict to handle the
// 409 Conflict that occurs when another writer (or another replica)
// changed the resourceVersion in between Get and Update.
//
// The mutator MUST only mutate the .Status subtree; spec mutations
// will be silently dropped because we use the status subresource.
//
// On success the original `yanet` argument's Status is also synced to
// the freshly written values so downstream code observing the local
// object sees the same state as the API server.
func (r *YanetV2Reconciler) updateStatusV2(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	mutate func(*yanetv2alpha1.YanetV2),
) error {
	key := types.NamespacedName{Name: yanet.Name, Namespace: yanet.Namespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &yanetv2alpha1.YanetV2{}
		if err := r.Client.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(fresh)
		if err := r.Status().Update(ctx, fresh); err != nil {
			return err
		}
		yanet.Status = fresh.Status
		yanet.ResourceVersion = fresh.ResourceVersion
		return nil
	})
}

// reconcileYanetV2 is the entry point of the v2alpha1 reconcile path.
//
// Flow:
//  1. Manage the finalizer (add on first reconcile, run cleanup +
//     remove on DeletionTimestamp).
//  2. Fetch the cluster-wide YanetConfigV2 snapshot. Bail out with a
//     requeue when it is empty.
//  3. Honour spec.stop and spec.enabled.
//  4. List the nodes matched by YanetV2.spec.nodeSelector.
//  5. Build a PatchRegistry once for the whole reconcile.
//  6. For each node × component slot in the boxType:
//     resolve → build deployments → apply patches → CreateOrUpdate.
//     Inline ConfigMaps are applied first so the Pod can roll them in.
//     The global UpdateWindow throttles cross-node Deployment updates.
//  7. Aggregate Services (per-node Local + cluster-wide RR) and
//     CreateOrUpdate them, deduplicated by name.
//  8. Prune orphan Deployments / Services / ConfigMaps owned by this
//     YanetV2 but no longer in the desired set.
//  9. Aggregate Pods, compute conditions and write Status.
func (r *YanetV2Reconciler) reconcileYanetV2(ctx context.Context, yanet *yanetv2alpha1.YanetV2) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("yanet", yanet.Name, "namespace", yanet.Namespace)

	// 1. Finalizer / deletion handling -------------------------
	if !yanet.DeletionTimestamp.IsZero() {
		return r.handleYanetV2Deletion(ctx, yanet, logger)
	}
	if !controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
		controllerutil.AddFinalizer(yanet, yanetFinalizer)
		if err := r.Update(ctx, yanet); err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Continue with reconcile after adding the finalizer; the
		// next requeue will see the finalizer in place.
		return ctrl.Result{Requeue: true}, nil
	}

	// spec.enabled is a "scale-to-zero" switch, not a reconcile
	// pause. The reconciler keeps rendering Deployments/Services
	// (so the user can inspect generated specs and so patches still
	// take effect) but forces replicas=0 on every Deployment when
	// the CR is disabled. To fully freeze the operator's view of a
	// CR — keep existing Deployments untouched, including any hand
	// edits — use spec.autoSync=false instead.
	installationEnabled := helpers.BoolValue(yanet.Spec.Enabled, true)

	cfgSpec, ok := r.snapshotYanetConfigV2()
	if !ok {
		logger.Info("YanetConfigV2 v2 snapshot is empty; requeue")
		if r.Recorder != nil {
			r.Recorder.Eventf(yanet, nil, corev1.EventTypeWarning, "ConfigNotLoaded", "Reconcile",
				"YanetConfigV2 snapshot is empty; reconcile is paused")
		}
		if uerr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "ConfigNotLoaded", "YanetConfigV2 snapshot is empty")
		}); uerr != nil {
			logger.Info("status update failed (continuing)", "error", uerr)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	cfg := &yanetv2alpha1.YanetConfigV2{Spec: cfgSpec}
	if cfg.Spec.Stop {
		logger.Info("YanetConfigV2.spec.stop is true, skipping reconcile")
		return ctrl.Result{}, nil
	}

	// FindBoxType is called purely as an existence check so we can
	// surface a distinct "BoxTypeNotFound" reason on the status (the
	// downstream EnabledComponentsForBox would otherwise conflate
	// missing boxType with a malformed one under "BoxTypeInvalid").
	if _, err := helpers.FindBoxType(&cfg.Spec, yanet.Spec.BoxType); err != nil {
		logger.Error(err, "boxType resolution failed")
		if r.Recorder != nil {
			r.Recorder.Eventf(yanet, nil, corev1.EventTypeWarning, "BoxTypeNotFound", "Reconcile",
				"boxType %q not found in YanetConfigV2: %v", yanet.Spec.BoxType, err)
		}
		if uerr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "BoxTypeNotFound", err.Error())
		}); uerr != nil {
			logger.Info("status update failed (continuing)", "error", uerr)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	enabled, err := helpers.EnabledComponentsForBox(&cfg.Spec, yanet.Spec.BoxType)
	if err != nil {
		logger.Error(err, "could not enumerate boxType components")
		if r.Recorder != nil {
			r.Recorder.Eventf(yanet, nil, corev1.EventTypeWarning, "BoxTypeInvalid", "Reconcile",
				"boxType %q has invalid components: %v", yanet.Spec.BoxType, err)
		}
		if uerr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "BoxTypeInvalid", err.Error())
		}); uerr != nil {
			logger.Info("status update failed (continuing)", "error", uerr)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	nodes, err := r.listNodesForYanetV2(ctx, yanet)
	if err != nil {
		logger.Error(err, "node listing failed")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if len(nodes) == 0 {
		logger.Info("no nodes matched spec.nodeSelector; nothing to do")
	}

	registry := manifests.NewPatchRegistry(cfg.Spec.Patches)
	owner := metav1.OwnerReference{
		APIVersion:         yanet.APIVersion,
		Kind:               yanet.Kind,
		Name:               yanet.Name,
		UID:                yanet.UID,
		Controller:         helpers.PtrTrue(),
		BlockOwnerDeletion: helpers.PtrTrue(),
	}
	if owner.APIVersion == "" {
		owner.APIVersion = yanetv2alpha1.GroupVersion.String()
		owner.Kind = "YanetV2"
	}

	autoSync := helpers.BoolValue(yanet.Spec.AutoSync, false)
	updateWindow := time.Duration(cfg.Spec.UpdateWindow) * time.Second

	servicePlans := make(map[string]manifests.ServicePlan)
	nodesStatus := make(map[string]yanetv2alpha1.NodeStatus, len(nodes))
	desired := newDesiredSet()
	missingOperators := map[string]struct{}{}
	syncWaiting := false
	var earliestRequeue time.Duration

	// per-node × per-component reconcile loop ------------------
	for i := range nodes {
		node := &nodes[i]
		ns := yanetv2alpha1.NodeStatus{
			NodeName:    node.Name,
			Deployments: map[string]string{},
		}
		pullPolicy := cfg.Spec.Images.PullPolicy
		if pullPolicy == "" {
			pullPolicy = corev1.PullIfNotPresent
		}
		buildCtx := manifests.BuildContextV2{
			YanetName:   yanet.Name,
			Namespace:   yanet.Namespace,
			NodeName:    node.Name,
			NumaCount:   readNumaFromNode(node),
			PullPolicy:  pullPolicy,
			PullSecrets: cfg.Spec.Images.PullSecrets,
			OwnerRef:    owner,
		}
		ns.NumaCount = buildCtx.NumaCount

		for _, ref := range enabled {
			rc, rerr := helpers.ResolveBoxComponent(&cfg.Spec, &yanet.Spec, ref.Kind, ref.OperatorName)
			if rerr != nil {
				logger.Error(rerr, "resolve failed", "kind", ref.Kind, "operator", ref.OperatorName)
				continue
			}
			if rc == nil {
				if ref.OperatorName != "" {
					missingOperators[ref.OperatorName] = struct{}{}
				}
				continue
			}

			// ConfigMaps for inline configs (must land before the
			// Deployment to avoid CreateContainerConfigError).
			cmNames, cmErr := r.applyInlineConfigMapsV2(ctx, yanet, buildCtx, rc, autoSync)
			if cmErr != nil {
				logger.Error(cmErr, "configmap apply failed", "component", rc.Name)
				continue
			}
			for _, n := range cmNames {
				desired.ConfigMaps[n] = struct{}{}
			}

			deployments, berr := manifests.BuildDeployments(buildCtx, rc)
			if berr != nil {
				logger.Error(berr, "build failed", "component", rc.Name)
				continue
			}
			for _, d := range deployments {
				if perr := manifests.ApplyPatches(d, rc.Patches, registry); perr != nil {
					logger.Error(perr, "patch failed", "component", rc.Name, "deployment", d.Name)
					continue
				}
				// Global scale-to-zero gate: spec.enabled=false
				// overrides any per-component or patch-set
				// replicas value. Applied AFTER patches so a
				// patch cannot accidentally re-enable a disabled
				// installation.
				if !installationEnabled {
					zero := int32(0)
					d.Spec.Replicas = &zero
				}
				state, requeue := r.applyDeploymentV2(ctx, d, autoSync, updateWindow, node.Name, logger)
				ns.Deployments[d.Name] = state
				desired.Deployments[d.Name] = struct{}{}
				if state == "sync-waiting" {
					syncWaiting = true
				}
				if requeue > 0 {
					if r.Recorder != nil {
						r.Recorder.Eventf(yanet, nil, corev1.EventTypeNormal, "UpdateThrottled", "Update",
							"Deployment %s waiting %s for UpdateWindow on node %s",
							d.Name, requeue.String(), node.Name)
					}
					if earliestRequeue == 0 || requeue < earliestRequeue {
						earliestRequeue = requeue
					}
				}
			}
			for _, plan := range manifests.BuildServices(buildCtx, rc) {
				servicePlans[plan.Name] = plan
			}
		}
		nodesStatus[node.Name] = ns
	}

	// Service apply --------------------------------------------
	serviceNames := make([]string, 0, len(servicePlans))
	for _, plan := range servicePlans {
		svc := plan.ToService(yanet.Namespace, owner)
		ensureLabel(&svc.ObjectMeta, manifests.LabelYanet, yanet.Name)
		ensureLabel(&svc.ObjectMeta, manifests.LabelComponent, plan.Selector[manifests.LabelComponent])
		if sErr := r.applyServiceV2(ctx, svc, autoSync, logger); sErr != nil {
			logger.Error(sErr, "service apply failed", "service", svc.Name)
			continue
		}
		serviceNames = append(serviceNames, svc.Name)
		desired.Services[svc.Name] = struct{}{}
	}
	sort.Strings(serviceNames)

	// 8. Orphan cleanup ----------------------------------------
	orphanCount, err := r.pruneOrphans(ctx, yanet, desired, autoSync, logger)
	if err != nil {
		logger.Error(err, "prune orphans failed")
	}
	yanetOrphansPruned.WithLabelValues(yanet.Name, yanet.Namespace).Add(float64(orphanCount))
	if orphanCount > 0 && r.Recorder != nil {
		r.Recorder.Eventf(yanet, nil, corev1.EventTypeNormal, "OrphanPruned", "Cleanup",
			"Pruned %d orphan resources no longer in desired set", orphanCount)
	}

	// 9. Status -------------------------------------------------
	yanet.Status.NodesStatus = nodesStatus
	yanet.Status.Services = serviceNames
	yanet.Status.Sync = aggregateSyncStatusV2(nodesStatus)
	yanet.Status.Pods = collectPodsV2(ctx, r.Client, yanet, logger)
	yanet.Status.Conditions = computeConditionsV2(yanet, missingOperators)

	// metrics: deployments out-of-sync counter
	outOfSyncCount := len(yanet.Status.Sync.OutOfSync) + len(yanet.Status.Sync.Error)
	yanetDeploymentsOutOfSync.WithLabelValues(yanet.Name, yanet.Namespace).Set(float64(outOfSyncCount))

	desiredStatus := yanet.Status
	if err := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
		fresh.Status = desiredStatus
	}); err != nil {
		logger.Error(err, "status update failed")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if syncWaiting {
		if earliestRequeue == 0 {
			earliestRequeue = updateWindow
		}
		return ctrl.Result{RequeueAfter: earliestRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// handleYanetV2Deletion runs cleanup on a v2 YanetV2 whose
// DeletionTimestamp is set, then removes the finalizer to allow the
// CR to be reaped. Cleanup is the same prune-with-empty-desired-set
// path as steady-state pruning, only here we pass autoSync=true so
// it actually deletes regardless of the spec's AutoSync flag.
func (r *YanetV2Reconciler) handleYanetV2Deletion(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
		// Nothing to do, GC will reap the CR.
		return ctrl.Result{}, nil
	}
	logger.Info("YanetV2 v2 is being deleted, running cleanup")
	if r.Recorder != nil {
		r.Recorder.Eventf(yanet, nil, corev1.EventTypeNormal, "Cleanup", "Finalize",
			"Running cleanup before deletion")
	}
	// Pass an empty desired set ⇒ everything labelled as ours
	// becomes an orphan and is deleted.
	if _, err := r.pruneOrphans(ctx, yanet, newDesiredSet(), true, logger); err != nil {
		// Bubble up: do not snip the finalizer when cleanup
		// failed — the next reconcile retries.
		logger.Error(err, "cleanup failed; finalizer kept for retry")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}
	controllerutil.RemoveFinalizer(yanet, yanetFinalizer)
	if err := r.Update(ctx, yanet); err != nil {
		// Ignore "not found" and "conflict" errors - object is already deleted or being modified
		if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// snapshotYanetConfigV2 reads the in-memory v2 YanetConfigV2 snapshot
// maintained by YanetConfigReconcilerV2, returning (Spec, true) when
// it is populated, or (zero, false) when the snapshot is empty.
//
// This mirrors the v1 design: the YanetV2 reconciler does NOT hit the
// API to read YanetConfigV2; it relies on a watcher-maintained snapshot
// in shared memory.
func (r *YanetV2Reconciler) snapshotYanetConfigV2() (yanetv2alpha1.YanetConfigSpec, bool) {
	if r.GlobalConfigV2 == nil {
		return yanetv2alpha1.YanetConfigSpec{}, false
	}
	r.GlobalConfigV2.Lock.Lock()
	defer r.GlobalConfigV2.Lock.Unlock()
	if len(r.GlobalConfigV2.Config.BoxTypes) == 0 {
		return yanetv2alpha1.YanetConfigSpec{}, false
	}
	return *r.GlobalConfigV2.Config.DeepCopy(), true
}

// listNodesForYanetV2 lists the nodes that match
// YanetV2.spec.nodeSelector. An empty selector matches all schedulable
// nodes.
func (r *YanetV2Reconciler) listNodesForYanetV2(ctx context.Context, yanet *yanetv2alpha1.YanetV2) ([]corev1.Node, error) {
	nodes := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodes, client.MatchingLabels(yanet.Spec.NodeSelector)); err != nil {
		return nil, err
	}
	out := make([]corev1.Node, 0, len(nodes.Items))
	for i := range nodes.Items {
		// Skip nodes marked unschedulable to avoid creating
		// Deployments that will never schedule.
		if nodes.Items[i].Spec.Unschedulable {
			continue
		}
		out = append(out, nodes.Items[i])
	}
	return out, nil
}

// readNumaFromNode reads the NFD label exposing the NUMA count on
// the node and returns 0 when absent (caller falls back to 1).
func readNumaFromNode(node *corev1.Node) int32 {
	v, ok := node.Labels[yanetv2alpha1.NFDNumaCountLabel]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	// Cap at int32 max to avoid overflow on absurd label values.
	const maxInt32 = 1<<31 - 1
	if err != nil || n < 0 || n > maxInt32 {
		return 0
	}
	return int32(n) //nolint:gosec // bounds-checked above
}

// applyInlineConfigMapsV2 creates/updates ConfigMaps for the inline
// configuration of the resolved component. ConfigMap names are stable
// (hash of content + deployment identity) so a content change yields
// a fresh ConfigMap and a Pod rollout.
//
// Returns the slice of ConfigMap names that should belong to the
// desired set so the prune helper does not delete them.
func (r *YanetV2Reconciler) applyInlineConfigMapsV2(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	buildCtx manifests.BuildContextV2,
	rc *helpers.ResolvedComponent,
	autoSync bool,
) ([]string, error) {
	cmaps := manifests.InlineConfigMaps(buildCtx, rc)
	if len(cmaps) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(cmaps))
	for name, content := range cmaps {
		names = append(names, name)
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: buildCtx.Namespace,
				Labels: map[string]string{
					manifests.LabelYanet:     buildCtx.YanetName,
					manifests.LabelComponent: rc.Name,
				},
			},
			Data: map[string]string{"config": content},
		}
		if !autoSync {
			// Even with autoSync off, ConfigMaps must exist for
			// the Pod to mount them; track desired names but do
			// not create when the user explicitly opted out.
			existing := &corev1.ConfigMap{}
			if err := r.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: buildCtx.Namespace}, existing); err == nil {
				continue
			} else if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("configmap get %s/%s: %w", buildCtx.Namespace, name, err)
			}
			// Missing ConfigMap and AutoSync=false: skip; the
			// Pod will fail until the user enables AutoSync.
			continue
		}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
			cm.Data = map[string]string{"config": content}
			ensureLabel(&cm.ObjectMeta, manifests.LabelYanet, buildCtx.YanetName)
			ensureLabel(&cm.ObjectMeta, manifests.LabelComponent, rc.Name)
			// R8: install the proper controller OwnerReference
			// using the runtime Scheme. This guarantees the
			// APIVersion/Kind are filled correctly even when
			// the input YanetV2's TypeMeta is empty (which it is
			// after a typed Get).
			if r.Scheme != nil {
				if serr := controllerutil.SetControllerReference(yanet, cm, r.Scheme); serr != nil {
					return serr
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("configmap %s/%s: %w", buildCtx.Namespace, name, err)
		}
	}
	return names, nil
}

// applyDeploymentV2 creates/updates a Deployment when AutoSync is on.
// When AutoSync is off, only reports the diff state for Status.
//
// Returns:
//   - state string: one of "synced", "sync-waiting", "out-of-sync
//     (missing)", or "error".
//   - requeue duration: when >0 the caller must re-queue the YanetV2
//     to re-attempt the throttled update.
func (r *YanetV2Reconciler) applyDeploymentV2(
	ctx context.Context,
	desired *appsv1.Deployment,
	autoSync bool,
	updateWindow time.Duration,
	nodeName string,
	logger logr.Logger,
) (string, time.Duration) {
	existing := &appsv1.Deployment{}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	getErr := r.Client.Get(ctx, key, existing)

	if apierrors.IsNotFound(getErr) {
		if !autoSync {
			return "out-of-sync (missing)", 0
		}
		if err := r.Client.Create(ctx, desired); err != nil {
			logger.Error(err, "Create failed", "deployment", desired.Name)
			return "error", 0
		}
		yanetDeploymentsCreatedTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
		return "synced", 0
	}
	if getErr != nil {
		logger.Error(getErr, "Get failed", "deployment", desired.Name)
		return "error", 0
	}

	if !autoSync {
		return "sync-waiting", 0
	}

	// UpdateWindow throttle (shared between v1 and v2 paths via
	// r.lastUpdateTS / r.lastUpdateHost).
	if rt := r.checkUpdateRequeue(logger, updateWindow, nodeName); rt > 0 {
		yanetUpdateThrottledTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
		return "sync-waiting", rt
	}

	// R10: handle 409 Conflict by re-fetching and re-applying the
	// desired spec. Without this, two operator replicas (now that
	// leader-election is on by default replicaCount may still be
	// >1) would race each other to the loser's exit code.
	updErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &appsv1.Deployment{}
		if gerr := r.Client.Get(ctx, key, fresh); gerr != nil {
			return gerr
		}
		fresh.Spec = desired.Spec
		// Merge labels/annotations: keep foreign keys (sidecar
		// injectors, webhooks), drop operator-owned keys that
		// disappeared from desired.
		mergeManagedMeta(&fresh.ObjectMeta, &desired.ObjectMeta)
		return r.Client.Update(ctx, fresh)
	})
	if updErr != nil {
		logger.Error(updErr, "Update failed", "deployment", desired.Name)
		return "error", 0
	}
	yanetDeploymentsUpdatedTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
	return "synced", 0
}

// applyServiceV2 mirrors applyDeploymentV2 for Services.
//
// R15: refuse to apply a Service that lacks Ports or Selector. Without
// this guard a builder bug that returns an empty Spec.Ports would,
// when applied to an existing Service, wipe all ports and break the
// load balancer. Likewise an empty Selector would orphan the Service
// from its Pods. Both conditions are surfaced via Recorder so the
// operator notices them in `kubectl describe`.
func (r *YanetV2Reconciler) applyServiceV2(
	ctx context.Context,
	desired *corev1.Service,
	autoSync bool,
	logger logr.Logger,
) error {
	if len(desired.Spec.Ports) == 0 {
		logger.Info("applyServiceV2: refusing to apply Service with empty Ports",
			"service", desired.Name, "namespace", desired.Namespace)
		if r.Recorder != nil {
			r.Recorder.Eventf(desired, nil, corev1.EventTypeWarning, "ServiceInvalid", "Apply",
				"refusing to apply Service %s/%s: Spec.Ports is empty",
				desired.Namespace, desired.Name)
		}
		return nil
	}
	if len(desired.Spec.Selector) == 0 {
		logger.Info("applyServiceV2: refusing to apply Service with empty Selector",
			"service", desired.Name, "namespace", desired.Namespace)
		if r.Recorder != nil {
			r.Recorder.Eventf(desired, nil, corev1.EventTypeWarning, "ServiceInvalid", "Apply",
				"refusing to apply Service %s/%s: Spec.Selector is empty",
				desired.Namespace, desired.Name)
		}
		return nil
	}

	existing := &corev1.Service{}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	getErr := r.Client.Get(ctx, key, existing)

	if apierrors.IsNotFound(getErr) {
		if !autoSync {
			return nil
		}
		return r.Client.Create(ctx, desired)
	}
	if getErr != nil {
		return getErr
	}
	if !autoSync {
		return nil
	}
	// R10: handle 409 Conflict by re-fetching and re-applying the
	// desired spec. ClusterIP/ClusterIPs are immutable so always
	// carry the existing values forward.
	updErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &corev1.Service{}
		if gerr := r.Client.Get(ctx, key, fresh); gerr != nil {
			return gerr
		}
		desiredCopy := desired.DeepCopy()
		desiredCopy.Spec.ClusterIP = fresh.Spec.ClusterIP
		desiredCopy.Spec.ClusterIPs = fresh.Spec.ClusterIPs
		desiredCopy.ResourceVersion = fresh.ResourceVersion
		// Preserve immutable fields that the API server would
		// reject changes for.
		fresh.Spec = desiredCopy.Spec
		mergeManagedMeta(&fresh.ObjectMeta, &desiredCopy.ObjectMeta)
		return r.Client.Update(ctx, fresh)
	})
	if updErr != nil {
		logger.Error(updErr, "Service update failed", "service", desired.Name)
		return updErr
	}
	return nil
}

// aggregateSyncStatusV2 buckets per-node deployment statuses into the
// CR-level Status.Sync slice form.
func aggregateSyncStatusV2(byNode map[string]yanetv2alpha1.NodeStatus) yanetv2alpha1.SyncStatus {
	var out yanetv2alpha1.SyncStatus
	for _, ns := range byNode {
		for name, state := range ns.Deployments {
			switch state {
			case "synced":
				out.Synced = append(out.Synced, name)
			case "sync-waiting":
				out.SyncWaiting = append(out.SyncWaiting, name)
			case "error":
				out.Error = append(out.Error, name)
			default:
				out.OutOfSync = append(out.OutOfSync, name)
			}
		}
	}
	sort.Strings(out.Synced)
	sort.Strings(out.SyncWaiting)
	sort.Strings(out.OutOfSync)
	sort.Strings(out.Error)
	return out
}

// mergeManagedKV merges desired into existing:
//   - keys in prevManaged but absent from desired: removed.
//   - keys in desired: written (overwrite or add).
//   - other keys in existing: kept (foreign — sidecars, webhooks).
//
// prevManaged is the key set the operator owned on the previous
// reconcile. When empty (first reconcile or pre-tracking resource),
// no key is removed — equivalent to a plain soft merge.
func mergeManagedKV(existing, desired map[string]string, prevManaged []string) map[string]string {
	if len(existing) == 0 && len(desired) == 0 {
		return nil
	}
	out := make(map[string]string, len(existing)+len(desired))
	for k, v := range existing {
		out[k] = v
	}
	for _, k := range prevManaged {
		if _, want := desired[k]; !want {
			delete(out, k)
		}
	}
	for k, v := range desired {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeManagedMeta updates fresh's labels and annotations from desired,
// preserving foreign keys and removing operator-owned keys that have
// been retracted (e.g. a label dropped from a patch). The previously
// owned key sets are read from fresh's tracking annotations and
// rewritten afterwards to reflect the current desired sets.
func mergeManagedMeta(fresh, desired *metav1.ObjectMeta) {
	prevLabels := parseManagedKeys(fresh.Annotations[manifests.AnnotationManagedLabels])
	prevAnnos := parseManagedKeys(fresh.Annotations[manifests.AnnotationManagedAnnotations])

	fresh.Labels = mergeManagedKV(fresh.Labels, desired.Labels, prevLabels)
	fresh.Annotations = mergeManagedKV(fresh.Annotations, desired.Annotations, prevAnnos)

	if len(desired.Labels) > 0 {
		if fresh.Annotations == nil {
			fresh.Annotations = make(map[string]string, 2)
		}
		fresh.Annotations[manifests.AnnotationManagedLabels] = serializeManagedKeys(desired.Labels)
	} else if fresh.Annotations != nil {
		delete(fresh.Annotations, manifests.AnnotationManagedLabels)
	}
	if len(desired.Annotations) > 0 {
		if fresh.Annotations == nil {
			fresh.Annotations = make(map[string]string, 2)
		}
		fresh.Annotations[manifests.AnnotationManagedAnnotations] = serializeManagedKeys(desired.Annotations)
	} else if fresh.Annotations != nil {
		delete(fresh.Annotations, manifests.AnnotationManagedAnnotations)
	}
	if len(fresh.Annotations) == 0 {
		fresh.Annotations = nil
	}
}

func parseManagedKeys(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// serializeManagedKeys returns a sorted, comma-separated key list.
// Sorting keeps the tracking annotation deterministic across
// reconciles and avoids spurious Update calls.
func serializeManagedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// ensureLabel sets a non-empty label on the given metadata, creating
// the labels map when nil. Empty values are silently ignored to avoid
// dropping label keys that downstream consumers rely on.
func ensureLabel(meta *metav1.ObjectMeta, key, value string) {
	if value == "" {
		return
	}
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels[key] = value
}
