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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
)

// handleNodeDeletion handles cleanup when a worker node is deleted
// It finds and deletes the corresponding Yanet CRD
func (r *YanetReconciler) handleNodeDeletion(ctx context.Context, nodeName string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// List all Yanet resources
	u := &unstructured.UnstructuredList{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Kind:    "Yanet",
		Version: "v1alpha1",
		Group:   "yanet.yanet-platform.io",
	})

	err := r.Client.List(ctx, u)
	if err != nil {
		logger.Error(err, "Failed to list Yanet resources for node deletion cleanup")
		return ctrl.Result{}, err
	}

	// Find Yanet resource for this node
	for _, obj := range u.Items {
		yanet := &yanetv1alpha1.Yanet{}
		err = r.Client.Get(ctx, client.ObjectKey{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}, yanet)

		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			logger.Error(err, "Failed to get Yanet resource")
			continue
		}

		// Check if this Yanet is for the deleted node
		if yanet.Spec.NodeName == nodeName {
			logger.Info("Found Yanet for deleted node, deleting it",
				"yanet", yanet.Name,
				"namespace", yanet.Namespace,
				"node", nodeName)

			r.Recorder.Event(yanet, v1.EventTypeNormal, "NodeDeleted",
				fmt.Sprintf("Worker node %s deleted, cleaning up Yanet resource", nodeName))

			err = r.Client.Delete(ctx, yanet)
			if err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete Yanet resource for deleted node")
				r.Recorder.Event(yanet, v1.EventTypeWarning, "CleanupFailed",
					fmt.Sprintf("Failed to delete Yanet for deleted node: %v", err))
				return ctrl.Result{}, err
			}

			logger.Info("Successfully deleted Yanet for deleted node",
				"yanet", yanet.Name,
				"namespace", yanet.Namespace,
				"node", nodeName)
			return ctrl.Result{}, nil
		}
	}

	logger.Info("No Yanet resource found for deleted node", "node", nodeName)
	return ctrl.Result{}, nil
}
