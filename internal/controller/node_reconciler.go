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
)

// Reconcile logic for Node object
func (r *YanetReconciler) reconcilerNode(ctx context.Context, config *yanetv1alpha1.YanetConfigSpec, node *v1.Node) (ctrl.Result, error) {
	for label := range node.Labels {
		if label == "node-role.kubernetes.io/control-plane" {
			r.Log.Info(fmt.Sprintf("AutoDiscovery: found new Node with name: %s but it is controlplane, skip it.", node.Name))
			return ctrl.Result{}, nil
		}
	}
	r.Log.Info(fmt.Sprintf("AutoDiscovery: found new Node with name: %s", node.Name))
	r.Log.Info(fmt.Sprintf("AutoDiscovery: try find existing Yanet object for Node: %s", node.Name))

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
			r.Log.Info("Autodiscovery: there are not Yanet objects in this cluster.")
		} else {
			r.Log.Error(err, "AutoDiscovery: error while list Yanet objects, you must to create first object manually")
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
				r.Log.Info(fmt.Sprintf(
					`AutoDiscovery: Yanet resource not found in cluster for NamespacedName: %s.
					Ignoring since object must be deleted`,
					nn,
				))
			} else {
				r.Log.Error(err, fmt.Sprintf("AutoDiscovery: failed to get Yanet object with NamespacedName: %s.", nn))
				return ctrl.Result{}, err
			}
		}
		if yanet.Spec.NodeName == node.Name {
			r.Log.Info(fmt.Sprintf("AutoDiscovery: Yanet object for NamespacedName: %s already exist.", nn))
			return ctrl.Result{}, nil
		}
	}

	r.Log.Info(fmt.Sprintf("AutoDiscovery: try to create Yanet object for Node: %s", node.Name))
	dataplane := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Dataplane,
	}
	controlplane := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Controlplane,
	}
	announcer := &yanetv1alpha1.Dep{
		Image: config.AutoDiscovery.Images.Announcer,
	}
	bird := &yanetv1alpha1.DepWithTag{
		Image: config.AutoDiscovery.Images.Bird,
	}

	err, version := helpers.HttpGet(fmt.Sprintf("%s/%s", config.AutoDiscovery.ConfigsUri, node.Name))
	if err != nil {
		r.Log.Error(err, fmt.Sprintf("AutoDiscovery: can not get version for Node: %s, use latest", node.Name))
		version = "latest"
	}
	err, arch := helpers.HttpGet(fmt.Sprintf("%s/%s", config.AutoDiscovery.ArchUri, node.Name))
	if err != nil {
		r.Log.Error(err, fmt.Sprintf("AutoDiscovery: can not get arch for Node: %s, use corei7", node.Name))
		arch = "corei7"
	}
	err, t := helpers.HttpGet(fmt.Sprintf("%s/%s", config.AutoDiscovery.TypeUri, node.Name))
	if err != nil {
		r.Log.Error(err, fmt.Sprintf("AutoDiscovery: can not get arch for Node: %s, use release", node.Name))
		t = "release"
	}
	newyanet := &yanetv1alpha1.Yanet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: config.AutoDiscovery.Namespace,
		},
		Spec: yanetv1alpha1.YanetSpec{
			Tag:          version,
			Arch:         arch,
			Type:         t,
			NodeName:     node.Name,
			Registry:     config.AutoDiscovery.Registry,
			Dataplane:    *dataplane,
			Controlplane: *controlplane,
			Announcer:    *announcer,
			Bird:         *bird,
		},
	}
	r.Log.Info(fmt.Sprintf("AutoDiscovery: create new Yanet object for Node: %s", node.Name))
	err = r.Client.Create(ctx, newyanet)
	if err != nil {
		r.Log.Error(err, fmt.Sprintf("AutoDiscovery: can not create new Yanet object for Node: %s", node.Name))
	}
	return ctrl.Result{}, nil
}
