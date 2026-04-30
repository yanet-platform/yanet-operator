# Prometheus Metrics for yanet-operator

## Overview

yanet-operator exposes Prometheus metrics for monitoring reconciliation performance and resource state.

## Available Metrics

### Yanet Controller Metrics

#### `yanet_reconcile_total`
**Type:** Counter  
**Labels:** `name`, `namespace`, `result`  
**Description:** Total number of reconciliations per Yanet resource

**Example:**
```promql
# Total reconciliations for yanet-node1
yanet_reconcile_total{name="yanet-node1",namespace="default",result="success"}

# Error rate
rate(yanet_reconcile_total{result="error"}[5m])
```

#### `yanet_reconcile_duration_seconds`
**Type:** Histogram  
**Labels:** `name`, `namespace`  
**Description:** Duration of Yanet reconciliations in seconds

**Example:**
```promql
# 95th percentile reconciliation time
histogram_quantile(0.95, rate(yanet_reconcile_duration_seconds_bucket[5m]))

# Average reconciliation time
rate(yanet_reconcile_duration_seconds_sum[5m]) / rate(yanet_reconcile_duration_seconds_count[5m])
```

#### `yanet_deployments_out_of_sync`
**Type:** Gauge  
**Labels:** `name`, `namespace`  
**Description:** Number of deployments that are out of sync per Yanet resource

**Example:**
```promql
# Resources with out-of-sync deployments
yanet_deployments_out_of_sync > 0

# Total out-of-sync deployments across all resources
sum(yanet_deployments_out_of_sync)
```

### YanetConfig Controller Metrics

#### `yanetconfig_reconcile_total`
**Type:** Counter  
**Labels:** `name`, `namespace`, `result`  
**Description:** Total number of reconciliations per YanetConfig resource

**Example:**
```promql
# Total config reconciliations
yanetconfig_reconcile_total{name="global-config",namespace="default"}
```

#### `yanetconfig_reconcile_duration_seconds`
**Type:** Histogram  
**Labels:** `name`, `namespace`  
**Description:** Duration of YanetConfig reconciliations in seconds

**Example:**
```promql
# Config reconciliation latency
histogram_quantile(0.99, rate(yanetconfig_reconcile_duration_seconds_bucket[5m]))
```

### Resource Metrics

#### `yanet_resources_total`
**Type:** Gauge  
**Labels:** `type`  
**Description:** Total number of Yanet resources

**Example:**
```promql
# Total Yanet resources by type
yanet_resources_total{type="release"}
yanet_resources_total{type="balancer"}
```

#### `yanet_resources_ready`
**Type:** Gauge  
**Labels:** `type`  
**Description:** Number of ready Yanet resources

**Example:**
```promql
# Ready vs total resources
yanet_resources_ready / yanet_resources_total
```

## Installation

### Enable Metrics in Helm Chart

Metrics are enabled by default:

```yaml
# values.yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    interval: 30s
    release: kube-prometheus-stack  # Must match your Prometheus Operator release
```

### Disable Metrics

To disable metrics collection:

```yaml
metrics:
  enabled: false
```

### Custom Prometheus Release

If your Prometheus Operator uses a different release name:

```yaml
metrics:
  serviceMonitor:
    release: my-prometheus-release
```

## ServiceMonitor

When metrics are enabled, a ServiceMonitor resource is created for Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: yanet-operator
  labels:
    release: kube-prometheus-stack
spec:
  endpoints:
  - port: metrics
    interval: 30s
  selector:
    matchLabels:
      app.kubernetes.io/name: yanet-operator
```

## Grafana Dashboard

### Installation

The Helm chart includes a pre-built Grafana dashboard that can be automatically deployed as a ConfigMap:

```yaml
# values.yaml
grafana:
  dashboards:
    enabled: true
    namespace: kube-mon  # Namespace where Grafana is installed
    labels:
      grafana_dashboard: "1"  # Label used by Grafana sidecar for discovery
```

The dashboard will be automatically discovered by Grafana if you're using the [Grafana sidecar](https://github.com/grafana/helm-charts/tree/main/charts/grafana#sidecar-for-dashboards) with the following configuration:

```yaml
# Grafana Helm values
sidecar:
  dashboards:
    enabled: true
    label: grafana_dashboard
```

### Dashboard Features

The included dashboard provides:

- **Reconciliation Rate**: Success and error rates for Yanet and YanetConfig resources
- **Reconciliation Latency**: P50, P95, P99 percentiles for reconciliation duration
- **Deployments Out of Sync**: Gauge showing resources with synchronization issues
- **Resource Metrics**: Total and ready resources by type
- **Success Rate**: Overall success rate trends

### Manual Installation

If you prefer to import the dashboard manually:

1. Get the dashboard JSON:
```bash
kubectl get configmap yanet-operator-dashboard -n kube-mon -o jsonpath='{.data.yanet-operator\.json}' > dashboard.json
```

2. Import in Grafana UI: **Dashboards → Import → Upload JSON file**

### Custom Queries

You can create custom panels using these example queries:

#### Reconciliation Performance

```promql
# Reconciliation rate
rate(yanet_reconcile_total[5m])

# Error rate
rate(yanet_reconcile_total{result="error"}[5m])

# Success rate
rate(yanet_reconcile_total{result="success"}[5m]) / rate(yanet_reconcile_total[5m])
```

#### Latency Monitoring

```promql
# P50 latency
histogram_quantile(0.50, rate(yanet_reconcile_duration_seconds_bucket[5m]))

# P95 latency
histogram_quantile(0.95, rate(yanet_reconcile_duration_seconds_bucket[5m]))

# P99 latency
histogram_quantile(0.99, rate(yanet_reconcile_duration_seconds_bucket[5m]))
```

#### Resource Health

```promql
# Out-of-sync deployments
sum(yanet_deployments_out_of_sync) by (name, namespace)

# Resources with issues
count(yanet_deployments_out_of_sync > 0)
```

## Alerting Rules

### Example PrometheusRule

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: yanet-operator-alerts
spec:
  groups:
  - name: yanet-operator
    interval: 30s
    rules:
    - alert: YanetHighErrorRate
      expr: |
        rate(yanet_reconcile_total{result="error"}[5m]) > 0.1
      for: 5m
      labels:
        severity: warning
      annotations:
        summary: "High error rate in Yanet reconciliation"
        description: "Yanet {{ $labels.name }} in {{ $labels.namespace }} has error rate {{ $value }}"
    
    - alert: YanetDeploymentsOutOfSync
      expr: |
        yanet_deployments_out_of_sync > 0
      for: 10m
      labels:
        severity: warning
      annotations:
        summary: "Yanet deployments out of sync"
        description: "Yanet {{ $labels.name }} has {{ $value }} deployments out of sync"
    
    - alert: YanetHighReconciliationLatency
      expr: |
        histogram_quantile(0.95, rate(yanet_reconcile_duration_seconds_bucket[5m])) > 30
      for: 5m
      labels:
        severity: warning
      annotations:
        summary: "High reconciliation latency"
        description: "P95 reconciliation latency is {{ $value }}s"
```

## Troubleshooting

### Metrics not appearing in Prometheus

1. Check that ServiceMonitor is created:
```bash
kubectl get servicemonitor -n yanet-operator
```

2. Check that the `release` label matches your Prometheus Operator:
```bash
kubectl get servicemonitor yanet-operator -n yanet-operator -o yaml | grep release
```

3. Check Prometheus Operator logs:
```bash
kubectl logs -n monitoring -l app.kubernetes.io/name=prometheus-operator
```

4. Verify metrics endpoint is accessible:
```bash
kubectl port-forward -n yanet-operator svc/yanet-operator-metrics 8080:8080
curl http://localhost:8080/metrics
```

### ServiceMonitor not picked up by Prometheus

Ensure your Prometheus Operator is configured to watch the namespace:

```yaml
# Prometheus CR
spec:
  serviceMonitorNamespaceSelector: {}  # Watch all namespaces
  # OR
  serviceMonitorNamespaceSelector:
    matchLabels:
      monitoring: enabled
```

## Architecture

```
┌─────────────────────┐
│  Yanet Controller   │
│                     │
│  - Reconcile        │──┐
│  - Update Status    │  │
└─────────────────────┘  │
                         │ Record Metrics
┌─────────────────────┐  │
│ YanetConfig         │  │
│ Controller          │──┤
│                     │  │
└─────────────────────┘  │
                         ▼
                  ┌──────────────┐
                  │   Metrics    │
                  │   Registry   │
                  └──────┬───────┘
                         │
                         │ HTTP /metrics
                         ▼
                  ┌──────────────┐
                  │  Prometheus  │
                  │   Scraper    │
                  └──────┬───────┘
                         │
                         ▼
                  ┌──────────────┐
                  │   Grafana    │
                  └──────────────┘
```

## Files

- [`internal/controller/metrics.go`](internal/controller/metrics.go) — Metrics definitions
- [`internal/controller/yanet_controller.go`](internal/controller/yanet_controller.go) — Yanet metrics recording
- [`internal/controller/yanetconfig_controller.go`](internal/controller/yanetconfig_controller.go) — YanetConfig metrics recording
- [`deploy/charts/yanet-operator/templates/metrics-service.yaml`](deploy/charts/yanet-operator/templates/metrics-service.yaml) — Metrics Service
- [`deploy/charts/yanet-operator/templates/servicemonitor.yaml`](deploy/charts/yanet-operator/templates/servicemonitor.yaml) — ServiceMonitor for Prometheus Operator
- [`deploy/charts/yanet-operator/templates/grafana-dashboard.yaml`](deploy/charts/yanet-operator/templates/grafana-dashboard.yaml) — Grafana Dashboard ConfigMap
- [`deploy/charts/yanet-operator/dashboards/yanet-operator.json`](deploy/charts/yanet-operator/dashboards/yanet-operator.json) — Grafana Dashboard JSON
