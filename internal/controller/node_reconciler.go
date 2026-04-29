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

	yanetv1alpha1 "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	helpers "github.com/yanet-platform/yanet-operator/internal/helpers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconcile logic for Node object
func (r *YanetReconciler) reconcilerNode(ctx context.Context, config *yanetv1alpha1.YanetConfigSpec, node *v1.Node) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	for label := range node.Labels {
		if label == "node-role.kubernetes.io/control-plane" {
			logger.Info("AutoDiscovery: found control-plane node, skipping", "node", node.Name)
			return ctrl.Result{}, nil
		}
	}
	logger.Info("AutoDiscovery: found new worker node", "node", node.Name)
	logger.Info("AutoDiscovery: looking for existing Yanet object", "node", node.Name)

	yanet := &yanetv1alpha1.Yanet{}
	u := &unstructured.UnstructuredList{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Kind:    "Yanet",
		Version: "v1alpha1",
		Group:   "yanet.yanet-platform.io",
	})
	err := r.Client.List(ctx, u, client.InNamespace(config.AutoDiscovery.Namespace))
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Autodiscovery: there are not Yanet objects in this cluster.")
		} else {
			logger.Error(err, "AutoDiscovery: error while list Yanet objects, you must to create first object manually")
			return ctrl.Result{}, err
		}
	}

	for _, obj := range u.Items {
		nn := types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}
		err = r.Client.Get(ctx, nn, yanet)
		if err != nil {
			if errors.IsNotFound(err) {
				logger.Info("AutoDiscovery: Yanet resource not found, ignoring since object must be deleted",
					"namespacedName", nn)
			} else {
				logger.Error(err, fmt.Sprintf("AutoDiscovery: failed to get Yanet object with NamespacedName: %s.", nn))
				return ctrl.Result{}, err
			}
		}
		if yanet.Spec.NodeName == node.Name {
			logger.Info("AutoDiscovery: Yanet object already exists", "namespacedName", nn, "node", node.Name)
			return ctrl.Result{}, nil
		}
	}

	logger.Info("AutoDiscovery: creating new Yanet object", "node", node.Name)
	dataplane := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Dataplane,
	}
	controlplane := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Controlplane,
	}
	announcer := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Announcer,
	}
	bird := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Bird,
	}

	version, err := helpers.HttpGet(fmt.Sprintf("%s/%s", config.AutoDiscovery.ConfigsUri, node.Name))
	if err != nil {
		logger.Error(err, "AutoDiscovery: cannot get version, using latest", "node", node.Name)
		version = "latest"
	}
	t, err := helpers.HttpGet(fmt.Sprintf("%s/%s", config.AutoDiscovery.TypeUri, node.Name))
	if err != nil {
		logger.Error(err, "AutoDiscovery: cannot get type, using release", "node", node.Name)
		t = "release"
	}
	newyanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: config.AutoDiscovery.Namespace,
		},
		Spec: yanetv1alpha1.YanetSpec{
			Tag:          version,
			Type:         t,
			NodeName:     node.Name,
			Registry:     config.AutoDiscovery.Registry,
			Dataplane:    *dataplane,
			Controlplane: *controlplane,
			Announcer:    *announcer,
			Bird:         *bird,
		},
	}
	logger.Info("AutoDiscovery: creating new Yanet object", "node", node.Name, "version", version, "type", t)
	err = r.Client.Create(ctx, newyanet)
	if err != nil {
		logger.Error(err, "AutoDiscovery: failed to create new Yanet object", "node", node.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
