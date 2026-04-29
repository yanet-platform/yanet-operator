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
	// yanetReconcileTotal counts total number of reconciliations per Yanet resource
	yanetReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanet_reconcile_total",
			Help: "Total number of reconciliations per Yanet resource",
		},
		[]string{"name", "namespace", "result"},
	)

	// yanetReconcileDuration tracks the duration of reconciliations
	yanetReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "yanet_reconcile_duration_seconds",
			Help:    "Duration of Yanet reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"name", "namespace"},
	)

	// yanetDeploymentsOutOfSync tracks number of deployments that are out of sync
	yanetDeploymentsOutOfSync = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_deployments_out_of_sync",
			Help: "Number of deployments that are out of sync per Yanet resource",
		},
		[]string{"name", "namespace"},
	)

	// yanetConfigReconcileTotal counts total number of reconciliations per YanetConfig resource
	yanetConfigReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yanetconfig_reconcile_total",
			Help: "Total number of reconciliations per YanetConfig resource",
		},
		[]string{"name", "namespace", "result"},
	)

	// yanetConfigReconcileDuration tracks the duration of YanetConfig reconciliations
	yanetConfigReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "yanetconfig_reconcile_duration_seconds",
			Help:    "Duration of YanetConfig reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"name", "namespace"},
	)

	// yanetResourcesTotal tracks total number of Yanet resources
	yanetResourcesTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_resources_total",
			Help: "Total number of Yanet resources",
		},
		[]string{"type"},
	)

	// yanetResourcesReady tracks number of ready Yanet resources
	yanetResourcesReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "yanet_resources_ready",
			Help: "Number of ready Yanet resources",
		},
		[]string{"type"},
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
	)
}
