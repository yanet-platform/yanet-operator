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

	"github.com/go-logr/logr"
	yanetv2alpha1 "github.com/yanet-platform/yanet-operator/api/v2alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type sharedServiceKeyV2 struct {
	namespace string
	name      string
}

type sharedServiceScopeV2 struct {
	namespace string
	boxType   string
}

// reconcileSharedServicesV2 owns the namespace/box-type Services from the
// cluster-scoped singleton config. Individual YanetV2 objects only label their
// Pods for selection and report the shared Service names in status.
func (r *YanetConfigReconcilerV2) reconcileSharedServicesV2(
	ctx context.Context,
	config *yanetv2alpha1.YanetConfigV2,
	logger logr.Logger,
) error {
	installations := &yanetv2alpha1.YanetV2List{}
	if err := r.Client.List(ctx, installations); err != nil {
		return fmt.Errorf("list YanetV2 objects for shared Services: %w", err)
	}
	nodes := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodes); err != nil {
		return fmt.Errorf("list Nodes for shared Services: %w", err)
	}

	owner := *metav1.NewControllerRef(config, yanetv2alpha1.GroupVersion.WithKind("YanetConfigV2"))
	desired := make(map[sharedServiceKeyV2]*corev1.Service)
	blocked := make(map[sharedServiceKeyV2]struct{})
	protectedScopes := make(map[sharedServiceScopeV2]struct{})
	scopes := make(map[sharedServiceScopeV2]struct{})
	var planErrs []error
	for index := range installations.Items {
		yanet := &installations.Items[index]
		if !yanet.DeletionTimestamp.IsZero() {
			continue
		}
		scope := sharedServiceScopeV2{namespace: yanet.Namespace, boxType: yanet.Spec.BoxType}
		scopes[scope] = struct{}{}
		installationFailed := false
		refs, err := helpers.EnabledComponentsForBox(&config.Spec, yanet.Spec.BoxType)
		if err != nil {
			planErrs = append(planErrs, fmt.Errorf("YanetV2 %s/%s: %w", yanet.Namespace, yanet.Name, err))
			protectedScopes[scope] = struct{}{}
			continue
		}
		numaCount := int32(1)
		for nodeIndex := range nodes.Items {
			node := &nodes.Items[nodeIndex]
			if !labels.SelectorFromSet(yanet.Spec.NodeSelector).Matches(labels.Set(node.Labels)) {
				continue
			}
			if nodeNuma := readNumaFromNode(node); nodeNuma > numaCount {
				numaCount = nodeNuma
			}
		}
		buildCtx := manifests.BuildContextV2{
			Namespace: yanet.Namespace,
			BoxType:   yanet.Spec.BoxType,
			NumaCount: numaCount,
		}
		for _, ref := range refs {
			component, resolveErr := helpers.ResolveBoxServiceComponent(
				&config.Spec,
				yanet.Spec.BoxType,
				ref.Kind,
				ref.OperatorName,
			)
			if resolveErr != nil {
				installationFailed = true
				planErrs = append(planErrs, fmt.Errorf(
					"resolve %s for YanetV2 %s/%s: %w",
					ref.Kind,
					yanet.Namespace,
					yanet.Name,
					resolveErr,
				))
				continue
			}
			if component == nil {
				continue
			}
			for _, plan := range manifests.BuildServices(buildCtx, component) {
				if validateErr := plan.Validate(); validateErr != nil {
					installationFailed = true
					planErrs = append(planErrs, fmt.Errorf(
						"validate shared Service %s/%s: %w",
						yanet.Namespace,
						plan.Name,
						validateErr,
					))
					continue
				}
				service := plan.ToService(yanet.Namespace, owner)
				key := sharedServiceKeyV2{namespace: service.Namespace, name: service.Name}
				if _, ambiguous := blocked[key]; ambiguous {
					installationFailed = true
					continue
				}
				if previous, duplicate := desired[key]; duplicate &&
					!apiequality.Semantic.DeepEqual(previous.Spec, service.Spec) {
					installationFailed = true
					delete(desired, key)
					blocked[key] = struct{}{}
					planErrs = append(planErrs, fmt.Errorf(
						"YanetV2 objects generate conflicting shared Service plans named %s/%s",
						service.Namespace,
						service.Name,
					))
					continue
				}
				desired[key] = service
			}
		}
		if installationFailed {
			protectedScopes[scope] = struct{}{}
		}
	}
	// A shared selector cutover affects every installation in a namespace/box,
	// even an autoSync=false installation or a no-longer-selected old node.
	// Preserve routing and protect pruning while that scope has old producers.
	migrationBlocked, migrationErrs := blockedSharedServicePlacementScopesV2(ctx, r.Client, &config.Spec, scopes)
	planErrs = append(planErrs, migrationErrs...)
	for scope := range migrationBlocked {
		protectedScopes[scope] = struct{}{}
	}
	keys := make([]sharedServiceKeyV2, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].namespace == keys[j].namespace {
			return keys[i].name < keys[j].name
		}
		return keys[i].namespace < keys[j].namespace
	})
	for _, key := range keys {
		scope := sharedServiceScopeV2{namespace: key.namespace, boxType: desired[key].Labels[manifests.LabelBoxType]}
		if _, blocked := migrationBlocked[scope]; blocked {
			continue
		}
		if r.sharedServicesStoppedV2() {
			return nil
		}
		if err := r.applySharedServiceV2(ctx, desired[key]); err != nil {
			planErrs = append(planErrs, fmt.Errorf("apply shared Service %s/%s: %w", key.namespace, key.name, err))
			protectedScopes[sharedServiceScopeV2{
				namespace: key.namespace,
				boxType:   desired[key].Labels[manifests.LabelBoxType],
			}] = struct{}{}
		}
	}
	services := &corev1.ServiceList{}
	if err := r.Client.List(ctx, services, client.MatchingLabels{
		manifests.LabelSharedService: "true",
	}); err != nil {
		planErrs = append(planErrs, fmt.Errorf("list shared Services for pruning: %w", err))
		return errors.Join(planErrs...)
	}
	for index := range services.Items {
		service := &services.Items[index]
		key := sharedServiceKeyV2{namespace: service.Namespace, name: service.Name}
		scope := sharedServiceScopeV2{
			namespace: service.Namespace,
			boxType:   service.Labels[manifests.LabelBoxType],
		}
		_, protected := protectedScopes[scope]
		_, ambiguous := blocked[key]
		if _, keep := desired[key]; keep || protected || ambiguous || !controlledByV2(service, &owner) {
			continue
		}
		if r.sharedServicesStoppedV2() {
			return nil
		}
		logger.Info("deleting orphan shared Service", "namespace", service.Namespace, "service", service.Name)
		if err := r.Client.Delete(ctx, service, client.Preconditions{
			UID: &service.UID, ResourceVersion: &service.ResourceVersion,
		}); err != nil && !apierrors.IsNotFound(err) {
			planErrs = append(planErrs, fmt.Errorf("delete shared Service %s/%s: %w", service.Namespace, service.Name, err))
		}
	}
	return errors.Join(planErrs...)
}

// blockedSharedServicePlacementScopesV2 is independent of workload reconciliation:
// publishing a new snapshot must still allow explicit scale-to-zero to drain it.
func blockedSharedServicePlacementScopesV2(
	ctx context.Context, reader client.Reader, config *yanetv2alpha1.YanetConfigSpec, scopes map[sharedServiceScopeV2]struct{},
) (map[sharedServiceScopeV2]struct{}, []error) {
	blocked := make(map[sharedServiceScopeV2]struct{})
	var errs []error
	ordered := make([]sharedServiceScopeV2, 0, len(scopes))
	for scope := range scopes {
		ordered = append(ordered, scope)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].namespace+"/"+ordered[i].boxType < ordered[j].namespace+"/"+ordered[j].boxType
	})
	type inventory struct {
		producers []placementProducerV2
		err       error
	}
	byNamespace := make(map[string]inventory)
	for _, scope := range ordered {
		box, err := helpers.FindBoxType(config, scope.boxType)
		if err != nil || len(box.Operators) == 0 {
			// Resolution errors are already protected/reported by Service planning.
			continue
		}
		live, loaded := byNamespace[scope.namespace]
		if !loaded {
			live.producers, live.err = listPlacementProducersV2(ctx, reader, scope.namespace)
			byNamespace[scope.namespace] = live
		}
		if live.err != nil {
			blocked[scope] = struct{}{}
			errs = append(errs, live.err)
			continue
		}
		var names []string
		for name := range box.Operators {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, producer := range live.producers {
			if producer.template.Labels[manifests.LabelBoxType] != scope.boxType {
				continue
			}
			for _, name := range names {
				if incompatibleOperatorPlacementV2(producer.template.Labels, name, box.Operators[name].Placement == yanetv2alpha1.OperatorPlacementDataplane) {
					blocked[scope] = struct{}{}
					errs = append(errs, fmt.Errorf("shared Service placement migration in %s/%s for %q requires drain of old %s %s; preserving existing Services until all installations in this scope have drained incompatible producers",
						scope.namespace, scope.boxType, name, producer.kind, producer.name))
					break
				}
			}
			if _, failed := blocked[scope]; failed {
				break
			}
		}
	}
	return blocked, errs
}

// A newer snapshot can publish stop while an API read or retry is in flight.
// Recheck before writes without holding the snapshot mutex across API calls.
func (r *YanetConfigReconcilerV2) sharedServicesStoppedV2() bool {
	if r.GlobalConfigV2 == nil {
		return false
	}
	r.GlobalConfigV2.Lock.Lock()
	defer r.GlobalConfigV2.Lock.Unlock()
	return r.GlobalConfigV2.Config.Stop
}

func (r *YanetConfigReconcilerV2) applySharedServiceV2(
	ctx context.Context,
	desired *corev1.Service,
) error {
	if len(desired.Spec.Ports) == 0 || len(desired.Spec.Selector) == 0 {
		return fmt.Errorf("refusing to apply invalid shared Service %s/%s", desired.Namespace, desired.Name)
	}
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	existing := &corev1.Service{}
	err := r.Client.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		if r.sharedServicesStoppedV2() {
			return nil
		}
		return r.Client.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := validateServiceOwnership(existing, desired); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &corev1.Service{}
		if getErr := r.Client.Get(ctx, key, fresh); getErr != nil {
			return getErr
		}
		if ownershipErr := validateServiceOwnership(fresh, desired); ownershipErr != nil {
			return ownershipErr
		}
		candidate, changed := desiredSharedServiceUpdateV2(fresh, desired)
		if !changed || r.sharedServicesStoppedV2() {
			return nil
		}
		return r.Client.Update(ctx, candidate)
	})
}

func desiredSharedServiceUpdateV2(existing, desired *corev1.Service) (*corev1.Service, bool) {
	existingNormalized := existing.DeepCopy()
	desiredNormalized := desired.DeepCopy()
	clientgoscheme.Scheme.Default(existingNormalized)
	clientgoscheme.Scheme.Default(desiredNormalized)
	desiredNormalized.Spec.ClusterIP = existingNormalized.Spec.ClusterIP
	desiredNormalized.Spec.ClusterIPs = append([]string(nil), existingNormalized.Spec.ClusterIPs...)
	desiredNormalized.Spec.IPFamilies = append([]corev1.IPFamily(nil), existingNormalized.Spec.IPFamilies...)
	if existingNormalized.Spec.IPFamilyPolicy != nil {
		policy := *existingNormalized.Spec.IPFamilyPolicy
		desiredNormalized.Spec.IPFamilyPolicy = &policy
	}
	candidate := existing.DeepCopy()
	candidate.Spec = desiredNormalized.Spec
	candidate.OwnerReferences = append([]metav1.OwnerReference(nil), desiredNormalized.OwnerReferences...)
	mergeManagedMeta(&candidate.ObjectMeta, &desiredNormalized.ObjectMeta)
	delete(candidate.Labels, manifests.LabelYanet)
	changed := !apiequality.Semantic.DeepEqual(existingNormalized.Spec, candidate.Spec) ||
		!apiequality.Semantic.DeepEqual(existing.Labels, candidate.Labels) ||
		!apiequality.Semantic.DeepEqual(existing.Annotations, candidate.Annotations) ||
		!apiequality.Semantic.DeepEqual(existing.OwnerReferences, candidate.OwnerReferences)
	return candidate, changed
}

func controlledByV2(object metav1.Object, owner *metav1.OwnerReference) bool {
	existing := metav1.GetControllerOf(object)
	if existing == nil {
		return false
	}
	if owner.UID != "" && existing.UID != owner.UID {
		return false
	}
	return existing.APIVersion == owner.APIVersion &&
		existing.Kind == owner.Kind &&
		existing.Name == owner.Name
}
