package controller

import (
	"testing"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/manifests"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLegacyNodeClaimWaitsForObservedProducer(t *testing.T) {
	for _, mode := range []string{"active-set", "pending-pod", "unobserved-zero", "selector-mismatch", "stale-set-owner", "exact-set-pin"} {
		t.Run(mode, func(t *testing.T) {
			testContext := t.Context()
			incumbent, claimant := reviewYanet(), reviewYanet()
			incumbent.Namespace, incumbent.UID = "incumbent", "incumbent-uid"
			incumbent.Spec.NodeSelector = map[string]string{"pool": "old"}
			claimant.Spec.NodeSelector = map[string]string{"pool": "next"}
			a, b, c := reviewNode(), reviewNode(), reviewNode()
			a.Name, a.Labels = "node-a", map[string]string{"pool": "old", corev1.LabelHostname: "node-a", "zone": "legacy"}
			b.Name, b.Labels = "node-b", map[string]string{"pool": "next", corev1.LabelHostname: "node-a", "zone": "legacy"}
			c.Name, c.Labels = "node-c", map[string]string{"pool": "unrelated", corev1.LabelHostname: "node-c", "zone": "legacy"}
			current := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: incumbent.Namespace, UID: "current-uid",
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(incumbent, api.GroupVersion.WithKind("Yanet"))}},
				Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{manifests.LabelNode: a.Name}},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeName: a.Name}}}}
			replicas := int32(1)
			set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: incumbent.Namespace, UID: "old-uid", Generation: 2,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(current, appsv1.SchemeGroupVersion.WithKind("Deployment"))}},
				Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: current.Spec.Selector.DeepCopy(), Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: current.Spec.Selector.MatchLabels}, Spec: corev1.PodSpec{
						NodeSelector: map[string]string{corev1.LabelHostname: a.Name, "zone": "legacy"},
						Containers:   []corev1.Container{{Name: "dataplane", Image: "example.test/dataplane:v1"}},
					}}}, Status: appsv1.ReplicaSetStatus{ObservedGeneration: 2}}
			objects := []client.Object{incumbent, claimant, a, b, c, current, set}
			var pending *corev1.Pod
			switch mode {
			case "pending-pod", "unobserved-zero":
				replicas = 0
				if mode == "pending-pod" {
					pending = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: incumbent.Namespace,
						OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}},
						Spec: *set.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodPending}}
					objects = append(objects, pending)
				} else {
					set.Status.ObservedGeneration--
				}
			case "selector-mismatch":
				b.Labels["zone"] = "other"
			case "stale-set-owner":
				set.OwnerReferences[0].UID = "replaced-deployment"
			case "exact-set-pin":
				set.Spec.Template.Spec.NodeName = a.Name
			}
			r, snapshot := makeReconcilerEnv(t, objects...)
			snapshot.Config = minimalConfig()
			assertReconcile := func(wantNode string) {
				t.Helper()
				if _, err := reviewReconcile(testContext, r, claimant); err != nil {
					t.Fatal(err)
				}
				var deps appsv1.DeploymentList
				if err := r.List(testContext, &deps, client.InNamespace(claimant.Namespace), client.MatchingLabels{manifests.LabelComponent: "dataplane"}); err != nil {
					t.Fatal(err)
				}
				if wantNode == "" {
					if len(deps.Items) != 0 {
						t.Fatal("claimant applied before the old producer drained")
					}
				} else if len(deps.Items) != 1 || !manifests.HasExactNodePin(&deps.Items[0].Spec.Template.Spec, wantNode) {
					t.Fatalf("claimant did not converge on %s", wantNode)
				}
			}
			if mode == "selector-mismatch" || mode == "stale-set-owner" || mode == "exact-set-pin" {
				assertReconcile(b.Name)
				return
			}
			assertReconcile("")
			selectPool := func(pool string) {
				t.Helper()
				if err := r.Get(testContext, client.ObjectKeyFromObject(claimant), claimant); err != nil {
					t.Fatal(err)
				}
				claimant.Spec.NodeSelector = map[string]string{"pool": pool}
				if err := r.Update(testContext, claimant); err != nil {
					t.Fatal(err)
				}
			}
			selectPool("unrelated")
			assertReconcile(c.Name)
			if pending != nil {
				if err := r.Delete(testContext, pending); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.Get(testContext, client.ObjectKeyFromObject(set), set); err != nil {
					t.Fatal(err)
				}
				zero := int32(0)
				set.Spec.Replicas = &zero
				if err := r.Update(testContext, set); err != nil {
					t.Fatal(err)
				}
				set.Status.ObservedGeneration = set.Generation
				if err := r.Status().Update(testContext, set); err != nil {
					t.Fatal(err)
				}
			}
			selectPool("next")
			assertReconcile(b.Name)
			if err := r.Get(testContext, client.ObjectKeyFromObject(current), current); err != nil || !current.DeletionTimestamp.IsZero() {
				t.Fatalf("incumbent's exact-A Deployment was touched: %v", err)
			}
		})
	}
}

func TestLegacyNodeClaimFollowsBoundPodAfterHostnameChange(t *testing.T) {
	for _, mode := range []string{"owned", "updated-template", "terminal", "terminal-terminating", "losing-legacy-producer", "losing-updated-template", "stale-replicaset", "stale-deployment", "foreign-namespace", "not-controller"} {
		t.Run(mode, func(t *testing.T) {
			testContext := t.Context()
			incumbent, replacement := reviewYanet(), reviewYanet()
			incumbent.Namespace, incumbent.UID = "old", "old-yanet-uid"
			incumbent.Spec.NodeSelector = map[string]string{"pool": "old"}
			node := reviewNode()
			node.Name = "node-b"
			node.Labels = map[string]string{"pool": "edge", corev1.LabelHostname: "node-a"}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: incumbent.Namespace, UID: "deployment-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(incumbent, api.GroupVersion.WithKind("Yanet"))},
					Finalizers:      []string{"test.example/foreground"}},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{manifests.LabelNode: "node-a"}},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{corev1.LabelHostname: "node-a"}}},
				},
			}
			set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "legacy-set", Namespace: incumbent.Namespace, UID: "set-uid",
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "legacy-pod", Namespace: incumbent.Namespace, UID: "pod-uid",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))},
					Finalizers:      []string{"test.example/terminating"}},
				Spec:   corev1.PodSpec{NodeName: node.Name, NodeSelector: deployment.Spec.Template.Spec.NodeSelector},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			set.Spec.Template = *deployment.Spec.Template.DeepCopy()
			loser := mode == "losing-legacy-producer" || mode == "losing-updated-template"
			updated := mode == "updated-template" || mode == "losing-updated-template"
			if updated {
				deployment.Spec.Template.Spec.NodeSelector = nil
				deployment.Spec.Template.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-a"}}}}}}}}
			}
			switch mode {
			case "terminal", "terminal-terminating":
				pod.Status.Phase = corev1.PodSucceeded
			case "stale-replicaset":
				pod.OwnerReferences[0].UID = "obsolete-set"
			case "stale-deployment":
				set.OwnerReferences[0].UID = "obsolete-deployment"
			case "foreign-namespace":
				pod.Namespace = replacement.Namespace
			case "not-controller":
				pod.OwnerReferences[0].Controller = nil
			}
			objects := []client.Object{incumbent, replacement, node, deployment, set, pod}
			var neighbor, winner *appsv1.Deployment
			if loser {
				incumbent.Name = "z-loser"
				// Equal timestamps resolve by namespace before name.
				replacement.Namespace = "first-owner"
				incumbent.Spec.NodeSelector = nil // Selects A and B.
				deployment.OwnerReferences[0].Name = incumbent.Name
				a := reviewNode()
				a.Name, a.Labels = "node-a", map[string]string{"pool": "old", corev1.LabelHostname: "node-a"}
				neighbor = deployment.DeepCopy()
				neighbor.Name, neighbor.UID = "unrelated", "unrelated-uid"
				winner = deployment.DeepCopy()
				winner.Name, winner.Namespace, winner.UID = "winner", replacement.Namespace, "winner-uid"
				winner.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replacement, api.GroupVersion.WithKind("Yanet"))}
				winner.Spec.Selector.MatchLabels[manifests.LabelNode] = node.Name
				winner.Spec.Template.Spec = corev1.PodSpec{NodeName: node.Name}
				objects = append(objects, a, neighbor, winner)
			}
			r, snapshot := makeReconcilerEnv(t, objects...)
			snapshot.Config = minimalConfig()
			node.Labels[corev1.LabelHostname] = node.Name
			if err := r.Update(testContext, node); err != nil {
				t.Fatal(err)
			}
			if loser {
				if _, err := reviewReconcile(testContext, r, incumbent); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(deployment), deployment); err != nil {
					t.Fatal(err)
				}
				if deployment.DeletionTimestamp.IsZero() != updated {
					t.Fatalf("unexpected foreground deletion state for conflicting producer: %v", deployment.DeletionTimestamp)
				}
				for _, expected := range []*appsv1.Deployment{neighbor, winner} {
					actual := &appsv1.Deployment{}
					if err := r.Get(testContext, client.ObjectKeyFromObject(expected), actual); err != nil || !actual.DeletionTimestamp.IsZero() {
						t.Fatalf("non-conflicting Deployment %s was touched: %v", client.ObjectKeyFromObject(expected), err)
					}
				}
				return
			}
			if !updated {
				if err := r.Delete(testContext, deployment, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "terminal" {
				if err := r.Delete(testContext, pod); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reviewReconcile(testContext, r, replacement); err != nil {
				t.Fatal(err)
			}
			if mode == "owned" || mode == "updated-template" || mode == "terminal-terminating" {
				workloads := &appsv1.DeploymentList{}
				if err := r.List(testContext, workloads, client.InNamespace(replacement.Namespace)); err != nil {
					t.Fatal(err)
				}
				if len(workloads.Items) != 0 {
					t.Fatal("replacement acquired the Node while an incumbent Pod was still bound and terminating")
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(replacement), replacement); err != nil {
					t.Fatal(err)
				}
				if c := apimeta.FindStatusCondition(replacement.Status.Conditions, "Degraded"); c == nil || c.Reason != "NodeSelectionConflict" {
					t.Fatalf("expected node selection conflict, got %+v", replacement.Status.Conditions)
				}
				if err := r.Get(testContext, client.ObjectKeyFromObject(pod), pod); err != nil || pod.DeletionTimestamp.IsZero() {
					t.Fatalf("expected a terminating incumbent Pod: %v", err)
				}
				pod.Finalizers = nil
				if err := r.Update(testContext, pod); err != nil {
					t.Fatal(err)
				}
				if _, err := reviewReconcile(testContext, r, replacement); err != nil {
					t.Fatal(err)
				}
			}
			workload := &appsv1.DeploymentList{}
			if err := r.List(testContext, workload, client.InNamespace(replacement.Namespace), client.MatchingLabels{manifests.LabelComponent: "dataplane"}); err != nil {
				t.Fatal(err)
			}
			if len(workload.Items) != 1 || !manifests.HasExactNodePin(&workload.Items[0].Spec.Template.Spec, node.Name) {
				t.Fatal("replacement did not acquire the released Node")
			}
		})
	}
}

func TestLegacyNodeClaimDirectMatchWaitsForDrain(t *testing.T) {
	for _, mode := range []string{"alias", "terminating-alias", "canonical"} {
		t.Run(mode, func(t *testing.T) {
			testContext := t.Context()
			loser, winner := reviewYanet(), reviewYanet()
			loser.Namespace, loser.UID, loser.Spec.NodeSelector = "z-loser", "loser", nil
			winner.Namespace, winner.UID = "a-winner", "winner"
			a, b := reviewNode(), reviewNode()
			a.Name, b.Name = "node-a", "node-b"
			a.Labels = map[string]string{"pool": "old", corev1.LabelHostname: a.Name}
			logicalNode := a.Name
			if mode == "canonical" {
				logicalNode = b.Name
			}
			b.Labels[corev1.LabelHostname] = logicalNode
			legacy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "producer", Namespace: loser.Namespace, UID: "producer-uid",
					Finalizers: []string{"test.example/foreground"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(loser, api.GroupVersion.WithKind("Yanet"))}},
				Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{manifests.LabelNode: logicalNode}},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{corev1.LabelHostname: logicalNode}}}},
			}
			set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "previous-set", Namespace: loser.Namespace, UID: "set-uid",
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(legacy, appsv1.SchemeGroupVersion.WithKind("Deployment"))}},
				Spec: appsv1.ReplicaSetSpec{Template: *legacy.Spec.Template.DeepCopy()}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bound-pod", Namespace: loser.Namespace, UID: "pod-uid",
				Finalizers: []string{"test.example/terminating"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}},
				Spec: corev1.PodSpec{NodeName: b.Name}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			current := legacy.DeepCopy()
			current.Name, current.Namespace, current.UID = "current", winner.Namespace, "current-uid"
			current.Spec.Selector.MatchLabels[manifests.LabelNode] = b.Name
			current.Spec.Template.Spec = corev1.PodSpec{NodeName: b.Name}
			current.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(winner, api.GroupVersion.WithKind("Yanet"))}
			neighbor := legacy.DeepCopy()
			neighbor.Name, neighbor.UID = "independent-a", "independent-uid"
			neighbor.Spec.Selector.MatchLabels[manifests.LabelNode] = a.Name
			neighbor.Spec.Template.Spec = corev1.PodSpec{NodeName: a.Name}
			foreign := legacy.DeepCopy()
			foreign.Name, foreign.UID = "foreign", "foreign-uid"
			foreign.OwnerReferences[0].UID = "different-yanet-uid"
			objects := []client.Object{loser, winner, a, b, legacy, set, pod, current, neighbor, foreign}
			var neighborChildren []client.Object
			if mode == "alias" {
				// A second legacy producer can also reach B, but its Pod is on A.
				// Reachability must block acquisition, not authorize its deletion.
				neighbor.Spec.Template = *legacy.Spec.Template.DeepCopy()
				otherSet := set.DeepCopy()
				otherSet.Name, otherSet.UID = "neighbor-set", "neighbor-set-uid"
				otherSet.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(neighbor, appsv1.SchemeGroupVersion.WithKind("Deployment"))}
				otherPod := pod.DeepCopy()
				otherPod.Name, otherPod.UID, otherPod.Spec.NodeName = "neighbor-pod", "neighbor-pod-uid", a.Name
				otherPod.Finalizers = nil
				otherPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(otherSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
				neighborChildren = []client.Object{otherPod, otherSet}
				objects = append(objects, neighborChildren...)
			}
			r, snapshot := makeReconcilerEnv(t, objects...)
			snapshot.Config = minimalConfig()
			assertUnchanged := func(expected *appsv1.Deployment) {
				t.Helper()
				actual := &appsv1.Deployment{}
				if err := r.Get(testContext, client.ObjectKeyFromObject(expected), actual); err != nil {
					t.Fatal(err)
				}
				if actual.ResourceVersion != expected.ResourceVersion || !actual.DeletionTimestamp.IsZero() {
					t.Fatalf("unexpected write to %s", client.ObjectKeyFromObject(expected))
				}
			}
			for _, d := range []*appsv1.Deployment{current, neighbor, foreign} {
				if err := r.Get(testContext, client.ObjectKeyFromObject(d), d); err != nil {
					t.Fatal(err)
				}
			}
			assertWinnerWaiting := func() {
				t.Helper()
				if _, err := reviewReconcile(testContext, r, winner); err != nil {
					t.Fatal(err)
				}
				var workloads appsv1.DeploymentList
				if err := r.List(testContext, &workloads, client.InNamespace(winner.Namespace)); err != nil {
					t.Fatal(err)
				}
				if len(workloads.Items) != 1 {
					t.Fatalf("winner applied before drain completed: %d Deployments", len(workloads.Items))
				}
				assertUnchanged(current)
			}
			if mode == "terminating-alias" {
				if err := r.Delete(testContext, pod); err != nil {
					t.Fatal(err)
				}
			}
			assertWinnerWaiting()
			if _, err := reviewReconcile(testContext, r, loser); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(testContext, client.ObjectKeyFromObject(legacy), legacy); err != nil || legacy.DeletionTimestamp.IsZero() {
				t.Fatalf("loser did not begin foreground drain of the verified producer: %v", err)
			}
			assertUnchanged(neighbor)
			assertUnchanged(foreign)
			if mode != "terminating-alias" {
				if err := r.Delete(testContext, pod); err != nil {
					t.Fatal(err)
				}
			}
			assertWinnerWaiting()
			// Complete fake GC explicitly; Pod disappearance alone is not enough
			// while this foreground-deleting template still selects B.
			if err := r.Get(testContext, client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			pod.Finalizers = nil
			if err := r.Update(testContext, pod); err != nil {
				t.Fatal(err)
			}
			assertWinnerWaiting()
			if err := r.Delete(testContext, set); err != nil {
				t.Fatal(err)
			}
			legacy.Finalizers = nil
			if err := r.Update(testContext, legacy); err != nil {
				t.Fatal(err)
			}
			if mode == "alias" {
				assertWinnerWaiting()
				for _, child := range neighborChildren {
					if err := r.Delete(testContext, child); err != nil {
						t.Fatal(err)
					}
				}
				// An empty child inventory alone does not retire the live template.
				assertWinnerWaiting()
				b.Labels[corev1.LabelHostname] = b.Name
				if err := r.Update(testContext, b); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reviewReconcile(testContext, r, winner); err != nil {
				t.Fatal(err)
			}
			var workloads appsv1.DeploymentList
			if err := r.List(testContext, &workloads, client.InNamespace(winner.Namespace), client.MatchingLabels{manifests.LabelComponent: "dataplane"}); err != nil {
				t.Fatal(err)
			}
			if len(workloads.Items) != 1 || !manifests.HasExactNodePin(&workloads.Items[0].Spec.Template.Spec, b.Name) {
				t.Fatal("winner did not converge on B after drain")
			}
			assertUnchanged(neighbor)
			assertUnchanged(foreign)
		})
	}
}
