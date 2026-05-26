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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// yanetReconcileTotal counts total number of reconciliations per YanetV2 resource
	yanetReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_reconcile_total",
			Help: "Total number of reconciliations per YanetV2 resource",
		},
		[]string{"name", "namespace", "result"},
	)

	// yanetReconcileDuration tracks the duration of reconciliations
	yanetReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "yanet_reconcile_duration_seconds",
			Help:    "Duration of YanetV2 reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"name", "namespace"},
	)

	// yanetDeploymentsOutOfSync tracks number of deployments that are out of sync
	yanetDeploymentsOutOfSync = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_deployments_out_of_sync",
			Help: "Number of deployments that are out of sync per YanetV2 resource",
		},
		[]string{"name", "namespace"},
	)

	// yanetConfigReconcileTotal counts total number of reconciliations per YanetConfigV2 resource
	yanetConfigReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanetconfig_reconcile_total",
			Help: "Total number of reconciliations per YanetConfigV2 resource",
		},
		[]string{"name", "namespace", "result"},
	)

	// yanetConfigReconcileDuration tracks the duration of YanetConfigV2 reconciliations
	yanetConfigReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "yanetconfig_reconcile_duration_seconds",
			Help:    "Duration of YanetConfigV2 reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"name", "namespace"},
	)

	// yanetResourcesTotal tracks total number of YanetV2 resources
	yanetResourcesTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_resources_total",
			Help: "Total number of YanetV2 resources",
		},
		[]string{"type"},
	)

	// yanetResourcesReady tracks number of ready YanetV2 resources
	yanetResourcesReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_resources_ready",
			Help: "Number of ready YanetV2 resources",
		},
		[]string{"type"},
	)

	// yanetOrphansPruned counts orphan resources deleted by the v2
	// pruner. It is incremented per resource (Deployment, Service
	// or ConfigMap) deleted in a reconcile cycle.
	yanetOrphansPruned = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_orphans_pruned_total",
			Help: "Number of orphan resources pruned by the v2 reconciler",
		},
		[]string{"name", "namespace"},
	)

	// yanetDeploymentsCreatedTotal counts Deployments created by
	// the v2 reconciler.
	yanetDeploymentsCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_v2_deployments_created_total",
			Help: "Number of Deployments created by the v2 reconciler",
		},
		[]string{"deployment", "namespace"},
	)

	// yanetDeploymentsUpdatedTotal counts Deployments updated by
	// the v2 reconciler.
	yanetDeploymentsUpdatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_v2_deployments_updated_total",
			Help: "Number of Deployments updated by the v2 reconciler",
		},
		[]string{"deployment", "namespace"},
	)

	// yanetUpdateThrottledTotal counts how many times the v2
	// reconciler bailed on a Deployment update because of the
	// global UpdateWindow throttle.
	yanetUpdateThrottledTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_v2_update_throttled_total",
			Help: "Number of Deployment updates throttled by spec.updateWindow",
		},
		[]string{"deployment", "namespace"},
	)
)

func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(
		yanetReconcileTotal,
		yanetReconcileDuration,
		yanetDeploymentsOutOfSync,
		yanetConfigReconcileTotal,
		yanetConfigReconcileDuration,
		yanetResourcesTotal,
		yanetResourcesReady,
		yanetOrphansPruned,
		yanetDeploymentsCreatedTotal,
		yanetDeploymentsUpdatedTotal,
		yanetUpdateThrottledTotal,
	)
}
