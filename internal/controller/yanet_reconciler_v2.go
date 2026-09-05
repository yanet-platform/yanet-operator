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
	"errors"
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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
		previousStatus := fresh.Status.DeepCopy()
		mutate(fresh)
		if apiequality.Semantic.DeepEqual(previousStatus, &fresh.Status) {
			yanet.Status = fresh.Status
			yanet.ResourceVersion = fresh.ResourceVersion
			return nil
		}
		if err := r.checkGlobalStopV2(); err != nil {
			return err
		}
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
//  1. Fetch the cluster-wide YanetConfigV2 snapshot and honour spec.stop
//     before performing any writes.
//  2. Manage the finalizer (add on first reconcile, run cleanup +
//     remove on DeletionTimestamp).
//  3. Bail out with a requeue when the config snapshot is empty, then honour
//     spec.enabled.
//  4. List the nodes matched by YanetV2.spec.nodeSelector.
//  5. Build a PatchRegistry once for the whole reconcile.
//  6. For each node × component slot in the boxType:
//     resolve → build deployments → apply patches → CreateOrUpdate.
//     Inline ConfigMaps are applied first so the Pod can roll them in.
//     The global UpdateWindow throttles cross-node Deployment updates.
//  7. Preflight shared Service plans for status reporting. The
//     YanetConfigV2 controller owns their lifecycle.
//  8. Prune orphan Deployments / ConfigMaps owned by this YanetV2 but
//     no longer in the desired set.
//  9. Aggregate Pods, compute conditions and write Status.
func (r *YanetV2Reconciler) reconcileYanetV2(ctx context.Context, yanet *yanetv2alpha1.YanetV2) (result ctrl.Result, reconcileErr error) {
	defer func() {
		if errors.Is(reconcileErr, errGlobalStopV2) || r.checkGlobalStopV2() != nil {
			result, reconcileErr = ctrl.Result{}, nil
		}
	}()
	logger := log.FromContext(ctx).WithValues("yanet", yanet.Name, "namespace", yanet.Namespace)

	// Global stop is a strict freeze, including finalizer and deletion writes.
	cfgSpec, configLoaded := r.snapshotYanetConfigV2()
	if !configLoaded {
		// The config controller may not have populated the in-memory snapshot
		// yet after manager startup. Read the singleton once before any write so
		// a persisted global stop cannot be bypassed during that window.
		persisted := &yanetv2alpha1.YanetConfigV2{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: yanetv2alpha1.YanetConfigName}, persisted); err == nil {
			cfgSpec = *persisted.Spec.DeepCopy()
			configLoaded = true
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("read YanetConfigV2 before reconcile: %w", err)
		}
	}
	if configLoaded && cfgSpec.Stop {
		logger.Info("YanetConfigV2.spec.stop is true, skipping reconcile")
		return ctrl.Result{}, nil
	}

	// Finalizer / deletion handling ----------------------------
	if !yanet.DeletionTimestamp.IsZero() {
		return r.handleYanetV2Deletion(ctx, yanet, logger)
	}
	if !controllerutil.ContainsFinalizer(yanet, yanetFinalizer) {
		controllerutil.AddFinalizer(yanet, yanetFinalizer)
		if err := r.checkGlobalStopV2(); err != nil {
			return ctrl.Result{}, err
		}
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
	autoSync := helpers.BoolValue(yanet.Spec.AutoSync, false)

	if !configLoaded {
		logger.Info("YanetConfigV2 v2 snapshot is empty; requeue")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
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

	// Resolve the selected box before validating per-installation overrides and
	// surface a distinct "BoxTypeNotFound" reason on the status (the
	// downstream EnabledComponentsForBox would otherwise conflate
	// missing boxType with a malformed one under "BoxTypeInvalid").
	box, err := helpers.FindBoxType(&cfg.Spec, yanet.Spec.BoxType)
	if err != nil {
		logger.Error(err, "boxType resolution failed")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
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
	if overrideErr := yanetv2alpha1.ValidateEffectiveYanetComponentOverrides(
		yanet.Spec.Components,
		&cfg.Spec.Components,
		box,
	); overrideErr != nil {
		logger.Error(overrideErr, "component override validation failed")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
			r.Recorder.Eventf(
				yanet,
				nil,
				corev1.EventTypeWarning,
				"OverridesInvalid",
				"Reconcile",
				"component overrides are invalid: %v",
				overrideErr,
			)
		}
		if uerr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "OverridesInvalid", overrideErr.Error())
		}); uerr != nil {
			logger.Info("status update failed (continuing)", "error", uerr)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	enabled, err := helpers.EnabledComponentsForBox(&cfg.Spec, yanet.Spec.BoxType)
	if err != nil {
		logger.Error(err, "could not enumerate boxType components")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
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
	if conflictErr := r.validateExclusiveNodesV2(ctx, yanet, nodes); conflictErr != nil {
		logger.Error(conflictErr, "node selection conflict")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
			r.Recorder.Eventf(yanet, nil, corev1.EventTypeWarning, "NodeSelectionConflict", "Reconcile", "%v", conflictErr)
		}
		statusErr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "NodeSelectionConflict", conflictErr.Error())
		})
		var cleanupErr error
		var nodeConflict *nodeSelectionConflictV2
		if autoSync && errors.As(conflictErr, &nodeConflict) {
			cleanupErr = r.pruneConflictingDeploymentsV2(ctx, yanet, nodeConflict.cleanupNodeNames, logger)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, errors.Join(statusErr, cleanupErr)
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

	updateWindow := time.Duration(cfg.Spec.UpdateWindow) * time.Second
	pullPolicy := cfg.Spec.Images.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	servicePlans, listenerAssignments, preflightErr := r.preflightResourcesV2(
		ctx, &cfg.Spec, yanet, nodes, enabled, installationEnabled, pullPolicy, owner, registry,
	)
	if preflightErr != nil {
		logger.Error(preflightErr, "resource preflight failed")
		if r.Recorder != nil && r.checkGlobalStopV2() == nil {
			r.Recorder.Eventf(yanet, nil, corev1.EventTypeWarning, "ResourcePreflightFailed", "Reconcile", "%v", preflightErr)
		}
		statusErr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			setConditionsV2Degraded(fresh, "ResourcePreflightFailed", preflightErr.Error())
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, errors.Join(preflightErr, statusErr)
	}
	var reconcileErrs []error
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
		buildCtx := manifests.BuildContextV2{
			YanetName:   yanet.Name,
			Namespace:   yanet.Namespace,
			BoxType:     yanet.Spec.BoxType,
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
				reconcileErrs = append(reconcileErrs, rerr)
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
				reconcileErrs = append(reconcileErrs, cmErr)
				continue
			}
			for _, n := range cmNames {
				desired.ConfigMaps[n] = struct{}{}
			}

			deployments, berr := manifests.BuildDeployments(buildCtx, rc)
			if berr != nil {
				logger.Error(berr, "build failed", "component", rc.Name)
				reconcileErrs = append(reconcileErrs, berr)
				continue
			}
			for _, d := range deployments {
				identity := manifests.CaptureWorkloadIdentity(d)
				if perr := manifests.ApplyPatches(d, rc.Patches, registry); perr != nil {
					logger.Error(perr, "patch failed", "component", rc.Name, "deployment", d.Name)
					reconcileErrs = append(reconcileErrs, perr)
					continue
				}
				manifests.RestoreWorkloadIdentity(d, identity)
				if nameErr := manifests.ValidatePodContainerNames(d); nameErr != nil {
					logger.Error(nameErr, "container name validation failed", "component", rc.Name, "deployment", d.Name)
					reconcileErrs = append(reconcileErrs, nameErr)
					continue
				}
				if listenerErr := manifests.ConfigureListeners(d, rc, listenerAssignments[node.Name][d.Name]); listenerErr != nil {
					logger.Error(listenerErr, "listener configuration failed", "component", rc.Name, "deployment", d.Name)
					reconcileErrs = append(reconcileErrs, listenerErr)
					continue
				}
				normalizeDeploymentReplicas(d, rc.Enabled, installationEnabled)
				state, requeue, applyErr := r.applyDeploymentV2(ctx, d, autoSync, updateWindow, node.Name, logger)
				ns.Deployments[d.Name] = state
				desired.Deployments[d.Name] = struct{}{}
				if applyErr != nil {
					reconcileErrs = append(reconcileErrs, applyErr)
				}
				if state == "sync-waiting" {
					syncWaiting = true
				}
				if requeue > 0 {
					if r.Recorder != nil && r.checkGlobalStopV2() == nil {
						r.Recorder.Eventf(yanet, nil, corev1.EventTypeNormal, "UpdateThrottled", "Update",
							"Deployment %s waiting %s for UpdateWindow on node %s",
							d.Name, requeue.String(), node.Name)
					}
					if earliestRequeue == 0 || requeue < earliestRequeue {
						earliestRequeue = requeue
					}
				}
			}
		}
		nodesStatus[node.Name] = ns
	}

	// Shared Services are reconciled by YanetConfigReconcilerV2. Keep the
	// expected names in this installation's status.
	serviceNames := make([]string, 0, len(servicePlans))
	for name := range servicePlans {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	pods, podErr := collectPodsV2(ctx, r.Client, yanet)
	if podErr != nil {
		reconcileErrs = append(reconcileErrs, podErr)
		// Preserve the last observation when the API could not be read.
		pods = yanet.Status.Pods
	}
	if len(reconcileErrs) > 0 {
		reconcileErr := errors.Join(reconcileErrs...)
		yanet.Status.NodesStatus = nodesStatus
		yanet.Status.Services = serviceNames
		yanet.Status.Sync = aggregateSyncStatusV2(nodesStatus)
		yanet.Status.Pods = pods
		setConditionsV2Degraded(yanet, "ReconcileFailed", reconcileErr.Error())
		desiredStatus := yanet.Status
		statusErr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			fresh.Status = desiredStatus
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, errors.Join(reconcileErr, statusErr)
	}

	// 8. Orphan cleanup ----------------------------------------
	orphanCount, err := r.pruneOrphans(ctx, yanet, desired, autoSync, logger)
	if err != nil {
		logger.Error(err, "prune orphans failed")
		pruneErr := fmt.Errorf("prune orphans: %w", err)
		yanet.Status.NodesStatus = nodesStatus
		yanet.Status.Services = serviceNames
		yanet.Status.Sync = aggregateSyncStatusV2(nodesStatus)
		yanet.Status.Pods = pods
		setConditionsV2Degraded(yanet, "ReconcileFailed", pruneErr.Error())
		desiredStatus := yanet.Status
		statusErr := r.updateStatusV2(ctx, yanet, func(fresh *yanetv2alpha1.YanetV2) {
			fresh.Status = desiredStatus
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, errors.Join(pruneErr, statusErr)
	}
	yanetOrphansPruned.WithLabelValues(yanet.Name, yanet.Namespace).Add(float64(orphanCount))
	if orphanCount > 0 && r.Recorder != nil && r.checkGlobalStopV2() == nil {
		r.Recorder.Eventf(yanet, nil, corev1.EventTypeNormal, "OrphanPruned", "Cleanup",
			"Pruned %d orphan resources no longer in desired set", orphanCount)
	}

	// 9. Status -------------------------------------------------
	yanet.Status.NodesStatus = nodesStatus
	yanet.Status.Services = serviceNames
	yanet.Status.Sync = aggregateSyncStatusV2(nodesStatus)
	yanet.Status.Pods = pods
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

// preflightResourcesV2 resolves and validates every effective component
// before any resource is changed. This prevents a conflicting Service name
// from being applied for one component while another component advertises the
// same name with a different selector or port set.
func (r *YanetV2Reconciler) preflightResourcesV2(
	ctx context.Context,
	cfg *yanetv2alpha1.YanetConfigSpec,
	yanet *yanetv2alpha1.YanetV2,
	nodes []corev1.Node,
	enabled []helpers.ComponentRef,
	installationEnabled bool,
	pullPolicy corev1.PullPolicy,
	owner metav1.OwnerReference,
	registry manifests.PatchRegistry,
) (
	plans map[string]manifests.ServicePlan,
	assignments map[string]listenerPortAssignmentsV2,
	preflightErr error,
) {
	plans = make(map[string]manifests.ServicePlan)
	assignments = make(map[string]listenerPortAssignmentsV2, len(nodes))
	workloadsByNode := make(map[string][]renderedWorkloadV2, len(nodes))
	collided := make(map[string]struct{})
	var preflightErrs []error
	addServicePlans := func(buildCtx manifests.BuildContextV2, component *helpers.ResolvedComponent, location string) {
		for _, plan := range manifests.BuildServices(buildCtx, component) {
			if err := plan.Validate(); err != nil {
				preflightErrs = append(preflightErrs, fmt.Errorf("validate Service for %s %s: %w", component.Name, location, err))
				continue
			}
			if _, conflict := collided[plan.Name]; conflict {
				continue
			}
			if existing, duplicate := plans[plan.Name]; duplicate &&
				!apiequality.Semantic.DeepEqual(existing, plan) {
				delete(plans, plan.Name)
				collided[plan.Name] = struct{}{}
				preflightErrs = append(preflightErrs,
					fmt.Errorf("components generate conflicting Service plans named %q", plan.Name))
				continue
			}
			plans[plan.Name] = plan
		}
	}

	for i := range nodes {
		node := &nodes[i]
		var workloads []renderedWorkloadV2
		buildCtx := manifests.BuildContextV2{
			YanetName:   yanet.Name,
			Namespace:   yanet.Namespace,
			BoxType:     yanet.Spec.BoxType,
			NodeName:    node.Name,
			NumaCount:   readNumaFromNode(node),
			PullPolicy:  pullPolicy,
			PullSecrets: cfg.Images.PullSecrets,
			OwnerRef:    owner,
		}
		for _, ref := range enabled {
			rc, err := helpers.ResolveBoxComponent(cfg, &yanet.Spec, ref.Kind, ref.OperatorName)
			if err != nil {
				preflightErrs = append(preflightErrs, fmt.Errorf("resolve %s on node %s: %w", ref.Kind, node.Name, err))
				continue
			}
			if rc == nil {
				continue
			}
			deployments, err := manifests.BuildDeployments(buildCtx, rc)
			if err != nil {
				preflightErrs = append(preflightErrs, fmt.Errorf("build %s on node %s: %w", rc.Name, node.Name, err))
				continue
			}
			if ref.Kind == helpers.KindControlplane && rc.Enabled && installationEnabled && len(deployments) == 0 {
				preflightErrs = append(preflightErrs,
					fmt.Errorf("controlplane has no enabled NUMA domain on node %s", node.Name))
				continue
			}
			for _, deployment := range deployments {
				identity := manifests.CaptureWorkloadIdentity(deployment)
				if err := manifests.ApplyPatches(deployment, rc.Patches, registry); err != nil {
					preflightErrs = append(preflightErrs,
						fmt.Errorf("patch %s on node %s: %w", deployment.Name, node.Name, err))
					continue
				}
				manifests.RestoreWorkloadIdentity(deployment, identity)
				if err := manifests.ValidatePodContainerNames(deployment); err != nil {
					preflightErrs = append(preflightErrs,
						fmt.Errorf("validate %s on node %s: %w", deployment.Name, node.Name, err))
					continue
				}
				normalizeDeploymentReplicas(deployment, rc.Enabled, installationEnabled)
				workloads = append(workloads, renderedWorkloadV2{deployment: deployment, component: rc})
			}
			addServicePlans(buildCtx, rc, "on node "+node.Name)
		}
		nodeAssignments, allocationErr := allocateHostNetworkPortsV2(workloads, cfg.HostNetworkPortRange)
		if allocationErr != nil {
			preflightErrs = append(preflightErrs, fmt.Errorf("node %s: %w", node.Name, allocationErr))
			continue
		}
		assignments[node.Name] = nodeAssignments
		workloadsByNode[node.Name] = workloads
		hostPorts := make(map[hostPortKey]hostPortOwnerV2)
		for _, workload := range workloads {
			if err := reserveHostPorts(hostPorts, workload.deployment); err != nil {
				preflightErrs = append(preflightErrs, fmt.Errorf("node %s: %w", node.Name, err))
			}
		}
	}
	if len(nodes) == 0 {
		buildCtx := manifests.BuildContextV2{
			Namespace: yanet.Namespace,
			BoxType:   yanet.Spec.BoxType,
			NumaCount: 1,
		}
		for _, ref := range enabled {
			component, err := helpers.ResolveBoxComponent(cfg, &yanet.Spec, ref.Kind, ref.OperatorName)
			if err != nil {
				preflightErrs = append(preflightErrs, fmt.Errorf("resolve %s without matched nodes: %w", ref.Kind, err))
				continue
			}
			if component != nil {
				addServicePlans(buildCtx, component, "without matched nodes")
			}
		}
	}
	if len(preflightErrs) > 0 {
		return nil, nil, errors.Join(preflightErrs...)
	}
	if err := r.validateLiveHostPortsV2(ctx, yanet, nodes, workloadsByNode); err != nil {
		return nil, nil, err
	}
	return plans, assignments, nil
}

//+kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch

// validateLiveHostPortsV2 prevents desired-only allocation from handing a port
// to another Deployment before its previous user stops. This is deliberately a
// preflight refusal, not a rollout orchestrator: stop the conflicting workloads
// before migrating ports. Recreate only serializes Pods of the SAME Deployment.
func (r *YanetV2Reconciler) validateLiveHostPortsV2(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	nodes []corev1.Node,
	workloadsByNode map[string][]renderedWorkloadV2,
) error {
	hasPorts := false
	for _, workloads := range workloadsByNode {
		for _, workload := range workloads {
			if !deploymentReplicasAreZero(workload.deployment) && len(podHostPortsV2(&workload.deployment.Spec.Template.Spec)) > 0 {
				hasPorts = true
			}
		}
	}
	if !hasPorts {
		return nil
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments); err != nil {
		return fmt.Errorf("list live Deployments before host-port migration: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return fmt.Errorf("list live Pods before host-port migration: %w", err)
	}
	replicaSets := &appsv1.ReplicaSetList{}
	if err := r.List(ctx, replicaSets); err != nil {
		return fmt.Errorf("list live ReplicaSets before host-port migration (read access is required): %w", err)
	}
	// Cached List order is unspecified. Keep the first reported collision
	// stable so an unchanged conflict does not churn status messages.
	nodes = append([]corev1.Node(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	key := func(object metav1.Object) string { return object.GetNamespace() + "/" + object.GetName() }
	sort.Slice(deployments.Items, func(i, j int) bool { return key(&deployments.Items[i]) < key(&deployments.Items[j]) })
	sort.Slice(replicaSets.Items, func(i, j int) bool { return key(&replicaSets.Items[i]) < key(&replicaSets.Items[j]) })
	sort.Slice(pods.Items, func(i, j int) bool { return key(&pods.Items[i]) < key(&pods.Items[j]) })
	byKey := make(map[client.ObjectKey]*appsv1.Deployment, len(deployments.Items))
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		byKey[client.ObjectKeyFromObject(deployment)] = deployment
	}
	rsByKey := make(map[client.ObjectKey]*appsv1.ReplicaSet, len(replicaSets.Items))
	for i := range replicaSets.Items {
		rs := &replicaSets.Items[i]
		rsByKey[client.ObjectKeyFromObject(rs)] = rs
	}
	for i := range nodes {
		node := &nodes[i]
		for _, workload := range workloadsByNode[node.Name] {
			desired := workload.deployment
			if deploymentReplicasAreZero(desired) {
				continue
			}
			desiredPorts := podHostPortsV2(&desired.Spec.Template.Spec)
			if len(desiredPorts) == 0 {
				continue
			}
			existing := byKey[client.ObjectKeyFromObject(desired)]
			recreate := existing != nil && desired.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType &&
				validateDeploymentOwnership(existing, desired) == nil
			sameDeployment := func(rs *appsv1.ReplicaSet) bool {
				if !recreate || rs.Namespace != existing.Namespace {
					return false
				}
				owner := metav1.GetControllerOf(rs)
				return owner != nil && owner.APIVersion == appsv1.SchemeGroupVersion.String() && owner.Kind == "Deployment" &&
					owner.Name == existing.Name && owner.UID == existing.UID
			}
			for j := range deployments.Items {
				live := &deployments.Items[j]
				if recreate && live == existing {
					continue
				}
				if deploymentReplicasAreZero(live) && live.Status.Replicas == 0 && live.Status.ObservedGeneration >= live.Generation {
					continue
				}
				placementMatches := podMayUseNodeV2(&live.Spec.Template.Spec, node)
				if controlledByYanetV2(live, yanet) && live.Labels[manifests.LabelNode] != "" {
					placementMatches = live.Labels[manifests.LabelNode] == node.Name
				}
				if placementMatches {
					if port, conflict := overlappingHostPortV2(desiredPorts, podHostPortsV2(&live.Spec.Template.Spec)); conflict {
						return hostPortMigrationErrorV2(node.Name, desired, "Deployment", live, port)
					}
				}
			}
			// The Deployment template may already have changed while its old
			// ReplicaSet can still create Pods. Pod inspection alone misses that
			// gap; a scaled-down ReplicaSet with remaining replicas also reserves.
			for j := range replicaSets.Items {
				rs := &replicaSets.Items[j]
				if sameDeployment(rs) || rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0 && rs.Status.Replicas == 0 {
					continue
				}
				placementMatches := podMayUseNodeV2(&rs.Spec.Template.Spec, node)
				if owner := metav1.GetControllerOf(rs); owner != nil && owner.Kind == "Deployment" && owner.APIVersion == appsv1.SchemeGroupVersion.String() {
					if parent := byKey[client.ObjectKey{Namespace: rs.Namespace, Name: owner.Name}]; parent != nil && parent.UID == owner.UID &&
						controlledByYanetV2(parent, yanet) && parent.Labels[manifests.LabelNode] != "" {
						placementMatches = parent.Labels[manifests.LabelNode] == node.Name
					}
				}
				if placementMatches {
					if port, conflict := overlappingHostPortV2(desiredPorts, podHostPortsV2(&rs.Spec.Template.Spec)); conflict {
						return hostPortMigrationErrorV2(node.Name, desired, "ReplicaSet", rs, port)
					}
				}
			}
			for j := range pods.Items {
				pod := &pods.Items[j]
				if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || !podMayUseNodeV2(&pod.Spec, node) {
					continue
				}
				port, conflict := overlappingHostPortV2(desiredPorts, podHostPortsV2(&pod.Spec))
				if !conflict {
					continue
				}
				podOwner := metav1.GetControllerOf(pod)
				if recreate && pod.Namespace == existing.Namespace && podOwner != nil &&
					podOwner.APIVersion == appsv1.SchemeGroupVersion.String() && podOwner.Kind == "ReplicaSet" {
					rsKey := client.ObjectKey{Namespace: pod.Namespace, Name: podOwner.Name}
					if rs := rsByKey[rsKey]; rs != nil && rs.UID == podOwner.UID && sameDeployment(rs) {
						continue
					}
				}
				return hostPortMigrationErrorV2(node.Name, desired, "Pod", pod, port)
			}
		}
	}
	return nil
}

func hostPortMigrationErrorV2(node string, desired *appsv1.Deployment, kind string, live client.Object, port hostPortKey) error {
	return fmt.Errorf("unsafe host-port migration on node %s: Deployment %s/%s would use %s port %d still reserved by %s %s/%s; stop the old workloads and wait for their Pods to terminate before applying this port migration",
		node, desired.Namespace, desired.Name, port.protocol, port.port, kind, live.GetNamespace(), live.GetName())
}

func podMayUseNodeV2(pod *corev1.PodSpec, node *corev1.Node) bool {
	if pod.NodeName != "" {
		return pod.NodeName == node.Name
	}
	// Ignoring affinity is conservative: do not guess that an unbound workload
	// cannot use the node. Managed workloads have an exact operator-owned pin.
	return labels.SelectorFromSet(pod.NodeSelector).Matches(labels.Set(node.Labels))
}

func podHostPortsV2(pod *corev1.PodSpec) []hostPortKey {
	var ports []hostPortKey
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for _, container := range containers {
			for _, port := range container.Ports {
				protocol := port.Protocol
				if protocol == "" {
					protocol = corev1.ProtocolTCP
				}
				// HostIP is deliberately not used to infer disjoint listeners.
				if pod.HostNetwork && port.ContainerPort > 0 {
					ports = append(ports, hostPortKey{port: port.ContainerPort, protocol: protocol})
				}
				if port.HostPort > 0 {
					ports = append(ports, hostPortKey{port: port.HostPort, protocol: protocol})
				}
			}
		}
	}
	return ports
}

func overlappingHostPortV2(desired, live []hostPortKey) (hostPortKey, bool) {
	for _, target := range desired {
		for _, occupied := range live {
			if target == occupied {
				return target, true
			}
		}
	}
	return hostPortKey{}, false
}

func normalizeDeploymentReplicas(deployment *appsv1.Deployment, componentEnabled, installationEnabled bool) {
	if componentEnabled && installationEnabled {
		return
	}
	zero := int32(0)
	deployment.Spec.Replicas = &zero
}

type hostPortKey struct {
	port     int32
	protocol corev1.Protocol
}

func reserveHostPorts(
	reserved map[hostPortKey]hostPortOwnerV2,
	deployment *appsv1.Deployment,
) error {
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		return nil
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 1 &&
		deployment.Spec.Template.Labels[manifests.LabelComponent] == string(helpers.KindDataplane) {
		return fmt.Errorf(
			"deployment %s is a node-pinned dataplane workload with %d replicas",
			deployment.Name, *deployment.Spec.Replicas,
		)
	}
	if err := validateIntraPodHostPortsV2(deployment, nil); err != nil {
		return err
	}
	if !deployment.Spec.Template.Spec.HostNetwork {
		return nil
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 1 {
		return fmt.Errorf(
			"deployment %s uses hostNetwork with %d replicas pinned to one node",
			deployment.Name, *deployment.Spec.Replicas,
		)
	}
	reserve := func(containers []corev1.Container) error {
		for i := range containers {
			container := &containers[i]
			owner := hostPortOwnerV2{deployment: deployment.Name, container: container.Name}
			for j := range container.Ports {
				if err := reserveHostPortV2(reserved, owner, &container.Ports[j]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := reserve(deployment.Spec.Template.Spec.Containers); err != nil {
		return err
	}
	return reserve(deployment.Spec.Template.Spec.InitContainers)
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
	if r.Recorder != nil && r.checkGlobalStopV2() == nil {
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
	// A successful Delete only starts garbage collection. Keep the node claim
	// until foreground deletion has also terminated the ReplicaSets and Pods.
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(yanet.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	for i := range deployments.Items {
		if controlledByYanetV2(&deployments.Items[i], yanet) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}
	controllerutil.RemoveFinalizer(yanet, yanetFinalizer)
	if err := r.checkGlobalStopV2(); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Update(ctx, yanet); err != nil {
		// A conflict must retry; it does not mean our finalizer was removed.
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

var errGlobalStopV2 = errors.New("YanetConfigV2.spec.stop is true")

// checkGlobalStopV2 rechecks the kill switch immediately before a write, not
// only when rendering begins. Do not hold the snapshot lock across API calls:
// the config watcher must remain able to publish a stop while a call is blocked.
// An API request already in flight cannot be withdrawn by this guard.
func (r *YanetV2Reconciler) checkGlobalStopV2() error {
	if r.GlobalConfigV2 == nil {
		return nil
	}
	r.GlobalConfigV2.Lock.Lock()
	defer r.GlobalConfigV2.Lock.Unlock()
	if r.GlobalConfigV2.Config.Stop {
		return errGlobalStopV2
	}
	return nil
}

// snapshotYanetConfigV2 reads the in-memory v2 YanetConfigV2 snapshot
// maintained by YanetConfigReconcilerV2, returning (Spec, true) when
// it is populated, or (zero, false) when the snapshot is empty.
//
// Steady-state rendering relies on this watcher-maintained snapshot. Before
// the snapshot is populated, reconcileYanetV2 also checks the singleton in the
// API so a persisted stop is honored during startup.
func (r *YanetV2Reconciler) snapshotYanetConfigV2() (yanetv2alpha1.YanetConfigSpec, bool) {
	if r.GlobalConfigV2 == nil {
		return yanetv2alpha1.YanetConfigSpec{}, false
	}
	r.GlobalConfigV2.Lock.Lock()
	defer r.GlobalConfigV2.Lock.Unlock()
	if len(r.GlobalConfigV2.Config.BoxTypes) == 0 && !r.GlobalConfigV2.Config.Stop {
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

// validateExclusiveNodesV2 enforces the host-resource invariant that one node
// belongs to at most one YanetV2. Services may be shared by box type, but two
// installations cannot safely share DPDK devices, hugepages, BIRD sockets, or
// a host-network listener range.
type nodeSelectionConflictV2 struct {
	messages         []string
	cleanupNodeNames map[string]struct{}
}

func (e *nodeSelectionConflictV2) Error() string {
	return strings.Join(e.messages, "; ")
}

func (r *YanetV2Reconciler) validateExclusiveNodesV2(
	ctx context.Context,
	yanet *yanetv2alpha1.YanetV2,
	nodes []corev1.Node,
) error {
	if len(nodes) == 0 {
		return nil
	}
	installations := &yanetv2alpha1.YanetV2List{}
	if err := r.Client.List(ctx, installations); err != nil {
		return fmt.Errorf("list YanetV2 objects for node exclusivity: %w", err)
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.Client.List(ctx, deployments); err != nil {
		return fmt.Errorf("list Deployments for node exclusivity: %w", err)
	}
	hasWorkload := func(installation *yanetv2alpha1.YanetV2, nodeName string) bool {
		for deploymentIndex := range deployments.Items {
			deployment := &deployments.Items[deploymentIndex]
			if deployment.Namespace != installation.Namespace || deployment.Labels[manifests.LabelNode] != nodeName {
				continue
			}
			if controlledByYanetV2(deployment, installation) {
				return true
			}
		}
		return false
	}
	var conflicts []string
	cleanupNodes := make(map[string]struct{})
	for i := range installations.Items {
		other := &installations.Items[i]
		if other.Namespace == yanet.Namespace && other.Name == yanet.Name {
			continue
		}
		for nodeIndex := range nodes {
			node := &nodes[nodeIndex]
			otherHasWorkload := hasWorkload(other, node.Name)
			// A selector update does not terminate the existing Pods. The
			// incumbent retains its claim until its old workloads are removed.
			if !otherHasWorkload && !labels.SelectorFromSet(other.Spec.NodeSelector).Matches(labels.Set(node.Labels)) {
				continue
			}
			currentHasWorkload := hasWorkload(yanet, node.Name)
			currentWins := currentHasWorkload && !otherHasWorkload
			if currentHasWorkload == otherHasWorkload {
				currentWins = yanetV2Precedes(yanet, other)
			}
			// A deleting installation remains authoritative until its object
			// and finalizer are gone, so its host-network Pods cannot overlap a
			// replacement while cleanup is still in progress.
			if currentWins && other.DeletionTimestamp.IsZero() {
				continue
			}
			conflicts = append(conflicts, fmt.Sprintf(
				"node %s is also selected by YanetV2 %s/%s",
				node.Name,
				other.Namespace,
				other.Name,
			))
			if currentHasWorkload && !currentWins {
				cleanupNodes[node.Name] = struct{}{}
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)
	return &nodeSelectionConflictV2{messages: conflicts, cleanupNodeNames: cleanupNodes}
}

func yanetV2Precedes(left, right *yanetv2alpha1.YanetV2) bool {
	leftCreated := left.CreationTimestamp.Time
	rightCreated := right.CreationTimestamp.Time
	if !leftCreated.Equal(rightCreated) {
		return leftCreated.Before(rightCreated)
	}
	leftKey := left.Namespace + "/" + left.Name
	rightKey := right.Namespace + "/" + right.Name
	return leftKey < rightKey
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
			if cm.ResourceVersion != "" && !controlledByYanetV2(cm, yanet) {
				return fmt.Errorf("ConfigMap %s/%s already exists without the desired controller owner", cm.Namespace, cm.Name)
			}
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
			return r.checkGlobalStopV2()
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
// Returns the sync state, an optional throttle duration, and any Kubernetes API
// error. Unchanged Deployments are not written and do not consume the global
// UpdateWindow.
func (r *YanetV2Reconciler) applyDeploymentV2(
	ctx context.Context,
	desired *appsv1.Deployment,
	autoSync bool,
	updateWindow time.Duration,
	nodeName string,
	logger logr.Logger,
) (string, time.Duration, error) {
	existing := &appsv1.Deployment{}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	getErr := r.Client.Get(ctx, key, existing)

	if apierrors.IsNotFound(getErr) {
		if !autoSync {
			return "out-of-sync (missing)", 0, nil
		}
		mergeManagedMeta(&desired.ObjectMeta, desired.ObjectMeta.DeepCopy())
		if err := r.checkGlobalStopV2(); err != nil {
			return "sync-waiting", 0, err
		}
		if err := r.Client.Create(ctx, desired); err != nil {
			logger.Error(err, "Create failed", "deployment", desired.Name)
			return "error", 0, fmt.Errorf("create Deployment %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		yanetDeploymentsCreatedTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
		return "synced", 0, nil
	}
	if getErr != nil {
		logger.Error(getErr, "Get failed", "deployment", desired.Name)
		return "error", 0, fmt.Errorf("get Deployment %s/%s: %w", desired.Namespace, desired.Name, getErr)
	}
	if err := validateDeploymentOwnership(existing, desired); err != nil {
		return "error", 0, err
	}

	if _, changed := r.desiredDeploymentUpdate(existing, desired); !changed {
		return "synced", 0, nil
	}
	if !autoSync {
		return "out-of-sync", 0, nil
	}

	// UpdateWindow throttle is local to the v2 controller.
	if rt := r.checkUpdateRequeue(logger, updateWindow, nodeName); rt > 0 {
		yanetUpdateThrottledTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
		return "sync-waiting", rt, nil
	}

	// R10: handle 409 Conflict by re-fetching and re-applying the
	// desired spec. Without this, two operator replicas (now that
	// leader-election is on by default replicaCount may still be
	// >1) would race each other to the loser's exit code.
	updated := false
	updErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &appsv1.Deployment{}
		if gerr := r.Client.Get(ctx, key, fresh); gerr != nil {
			return gerr
		}
		if ownershipErr := validateDeploymentOwnership(fresh, desired); ownershipErr != nil {
			return ownershipErr
		}
		candidate, changed := r.desiredDeploymentUpdate(fresh, desired)
		if !changed {
			return nil
		}
		if err := r.checkGlobalStopV2(); err != nil {
			return err
		}
		if err := r.Client.Update(ctx, candidate); err != nil {
			return err
		}
		updated = true
		return nil
	})
	if updErr != nil {
		logger.Error(updErr, "Update failed", "deployment", desired.Name)
		return "error", 0, fmt.Errorf("update Deployment %s/%s: %w", desired.Namespace, desired.Name, updErr)
	}
	if updated {
		yanetDeploymentsUpdatedTotal.WithLabelValues(desired.Name, desired.Namespace).Inc()
	}
	return "synced", 0, nil
}

func (r *YanetV2Reconciler) desiredDeploymentUpdate(
	existing, desired *appsv1.Deployment,
) (*appsv1.Deployment, bool) {
	existingNormalized := existing.DeepCopy()
	desiredNormalized := desired.DeepCopy()
	clientgoscheme.Scheme.Default(existingNormalized)
	clientgoscheme.Scheme.Default(desiredNormalized)
	candidate := existing.DeepCopy()
	candidate.Spec = desiredNormalized.Spec
	candidate.OwnerReferences = append([]metav1.OwnerReference(nil), desiredNormalized.OwnerReferences...)
	// Keep foreign metadata while removing keys previously managed by the
	// operator that disappeared from the desired object.
	mergeManagedMeta(&candidate.ObjectMeta, &desiredNormalized.ObjectMeta)
	changed := !apiequality.Semantic.DeepEqual(existingNormalized.Spec, candidate.Spec) ||
		!apiequality.Semantic.DeepEqual(existing.OwnerReferences, candidate.OwnerReferences) ||
		!apiequality.Semantic.DeepEqual(existing.Labels, candidate.Labels) ||
		!apiequality.Semantic.DeepEqual(existing.Annotations, candidate.Annotations)
	return candidate, changed
}

func validateDeploymentOwnership(existing, desired *appsv1.Deployment) error {
	desiredOwner := metav1.GetControllerOf(desired)
	if desiredOwner == nil {
		return fmt.Errorf("desired Deployment %s/%s has no controller owner", desired.Namespace, desired.Name)
	}
	existingOwner := metav1.GetControllerOf(existing)
	if existingOwner == nil {
		return fmt.Errorf("Deployment %s/%s already exists without the desired controller owner", existing.Namespace, existing.Name)
	}
	if desiredOwner.UID != "" && existingOwner.UID != desiredOwner.UID {
		return fmt.Errorf("Deployment %s/%s is controlled by another resource instance", existing.Namespace, existing.Name)
	}
	if existingOwner.APIVersion != desiredOwner.APIVersion ||
		existingOwner.Kind != desiredOwner.Kind || existingOwner.Name != desiredOwner.Name {
		return fmt.Errorf("Deployment %s/%s is controlled by another resource", existing.Namespace, existing.Name)
	}
	return nil
}

func validateServiceOwnership(existing, desired *corev1.Service) error {
	desiredOwner := metav1.GetControllerOf(desired)
	if desiredOwner == nil {
		return fmt.Errorf("desired Service %s/%s has no controller owner", desired.Namespace, desired.Name)
	}
	existingOwner := metav1.GetControllerOf(existing)
	if existingOwner == nil {
		return fmt.Errorf("Service %s/%s already exists without the desired controller owner", existing.Namespace, existing.Name)
	}
	if desiredOwner.UID != "" && existingOwner.UID != desiredOwner.UID {
		return fmt.Errorf("Service %s/%s is controlled by another resource instance", existing.Namespace, existing.Name)
	}
	if existingOwner.APIVersion != desiredOwner.APIVersion ||
		existingOwner.Kind != desiredOwner.Kind || existingOwner.Name != desiredOwner.Name {
		return fmt.Errorf("Service %s/%s is controlled by another resource", existing.Namespace, existing.Name)
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
