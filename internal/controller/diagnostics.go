package controller

import (
	"context"
	"fmt"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func planDiagnostics(ctx manifests.BuildContext, images map[string]string, workloads []renderedWorkload) (*appsv1.DaemonSet, error) {
	controlplaneImages := map[string]struct{}{}
	var image string
	for _, workload := range workloads {
		if !servingControlplane(workload) {
			continue
		}
		cp := controlplaneImage(workload.deployment)
		controlplaneImages[cp] = struct{}{}
		if candidate := images[cp]; cp != "" && candidate != "" {
			image = candidate
		}
	}
	if image == "" {
		return nil, nil
	}
	if len(controlplaneImages) != 1 {
		return nil, fmt.Errorf("diagnostics CLI requires the same controlplane image across enabled NUMA domains")
	}
	return manifests.BuildDiagnostics(ctx, image)
}

func servingControlplane(workload renderedWorkload) bool {
	return workload.component.Kind == helpers.KindControlplane && workload.component.Enabled &&
		workload.deployment.Spec.Replicas != nil && *workload.deployment.Spec.Replicas > 0
}

func controlplaneImage(d *appsv1.Deployment) string {
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "controlplane" {
			return c.Image
		}
	}
	return ""
}

// Keep the previous CLI while the gateway's actual rollout lags the rendered
// image (for example during a throttled update or while old pods still serve).
func (r *YanetReconciler) diagnosticsControlplanesReady(ctx context.Context, workloads []renderedWorkload) (bool, error) {
	for _, workload := range workloads {
		if !servingControlplane(workload) {
			continue
		}
		want := workload.deployment
		var live appsv1.Deployment
		if err := r.liveReader().Get(ctx, client.ObjectKeyFromObject(want), &live); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if err := validateDeploymentOwnership(&live, want); err != nil {
			return false, err
		}
		replicas := *want.Spec.Replicas
		if !live.DeletionTimestamp.IsZero() || controlplaneImage(&live) != controlplaneImage(want) ||
			live.Status.ObservedGeneration < live.Generation || live.Status.UpdatedReplicas != replicas ||
			live.Status.ReadyReplicas != replicas || live.Status.Replicas != replicas {
			return false, nil
		}
	}
	return true, nil
}

func (r *YanetReconciler) applyDiagnostics(ctx context.Context, y *api.Yanet, desired *appsv1.DaemonSet, autoSync bool) (string, error) {
	current := &appsv1.DaemonSet{}
	err := r.liveReader().Get(ctx, client.ObjectKeyFromObject(desired), current)
	if err != nil && !apierrors.IsNotFound(err) {
		return "error", err
	}
	if err == nil && !controlledByYanet(current, y) {
		return "error", fmt.Errorf("diagnostics DaemonSet %s has a different controller owner", current.Name)
	}
	if apierrors.IsNotFound(err) {
		if !autoSync {
			return "out-of-sync (missing)", nil
		}
		if err := r.checkGlobalStop(); err != nil {
			return "error", err
		}
		if err := r.Create(ctx, desired.DeepCopy()); err != nil {
			return "error", err
		}
		return "synced", nil
	}
	if !current.DeletionTimestamp.IsZero() {
		return "sync-waiting", nil
	}
	if !apiequality.Semantic.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
		return "error", fmt.Errorf("diagnostics DaemonSet %s has an unexpected selector", current.Name)
	}
	next := current.DeepCopy()
	next.Spec = *desired.Spec.DeepCopy()
	if err := r.checkGlobalStop(); err != nil {
		return "error", err
	}
	// Normalize Kubernetes defaults and admission mutations before comparing;
	// a raw comparison would send an update on every reconcile.
	if err := r.Update(ctx, next, client.DryRunAll); err != nil {
		return "error", err
	}
	if apiequality.Semantic.DeepEqual(current.Spec, next.Spec) {
		return "synced", nil
	}
	if !autoSync {
		return "out-of-sync", nil
	}
	if err := r.checkGlobalStop(); err != nil {
		return "error", err
	}
	actual := current.DeepCopy()
	actual.Spec = *desired.Spec.DeepCopy()
	if err := r.Update(ctx, actual); err != nil {
		return "error", err
	}
	return "synced", nil
}

func (r *YanetReconciler) pruneDiagnostics(ctx context.Context, y *api.Yanet, keep map[string]struct{}, autoSync bool, result *pruneResult) error {
	sets := &appsv1.DaemonSetList{}
	if err := r.liveReader().List(ctx, sets, client.InNamespace(y.Namespace)); err != nil {
		return err
	}
	for i := range sets.Items {
		set := &sets.Items[i]
		if !controlledByYanet(set, y) {
			continue
		}
		if _, ok := keep[set.Name]; ok {
			continue
		}
		if !autoSync {
			result.RetainedDaemonSets = append(result.RetainedDaemonSets, *set)
			continue
		}
		if !set.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.checkGlobalStop(); err != nil {
			return err
		}
		if err := r.Delete(ctx, set, client.PropagationPolicy(metav1.DeletePropagationForeground),
			client.Preconditions{UID: &set.UID, ResourceVersion: &set.ResourceVersion}); err != nil {
			if !isNotFoundOrGone(err) {
				return err
			}
			continue
		}
		result.Deleted++
	}
	return nil
}
