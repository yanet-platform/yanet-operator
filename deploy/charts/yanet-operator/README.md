# Yanet Operator Helm Chart

Kubernetes operator for managing YANET (Yet Another Network) deployments on worker nodes.

## Installation

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.1.6 \
  --namespace yanet-system \
  --create-namespace
```

## Configuration

### Metrics and Monitoring

Enable Prometheus metrics collection:

```yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    interval: 30s
    release: kube-prometheus-stack
```

### Grafana Dashboard

Enable automatic Grafana dashboard deployment:

```yaml
grafana:
  dashboards:
    enabled: true
    namespace: kube-mon  # Namespace where Grafana is installed
    labels:
      grafana_dashboard: "1"  # Label for Grafana sidecar discovery
```

The dashboard will be automatically discovered if you're using Grafana with sidecar enabled:

```yaml
# Grafana Helm values
sidecar:
  dashboards:
    enabled: true
    label: grafana_dashboard
```

### Webhooks

Enable validation webhooks:

```yaml
webhook:
  enabled: true
  port: 9443
  certManager:
    enabled: false  # Set to true if using cert-manager
```

### YanetConfig

Configure global YanetConfig resource:

```yaml
yanetconfig:
  spec:
    autodiscovery:
      enable: false
      namespace: yanet
      registry: dockerhub.io
    stop: false
```

## Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator replicas | `2` |
| `image.repository` | Operator image repository | `ghcr.io/yanet-platform/yanet-operator` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `metrics.enabled` | Enable Prometheus metrics | `true` |
| `metrics.serviceMonitor.enabled` | Create ServiceMonitor | `true` |
| `metrics.serviceMonitor.interval` | Scrape interval | `30s` |
| `metrics.serviceMonitor.release` | Prometheus Operator release label | `kube-prometheus-stack` |
| `grafana.dashboards.enabled` | Deploy Grafana dashboard | `true` |
| `grafana.dashboards.namespace` | Dashboard ConfigMap namespace | `kube-mon` |
| `grafana.dashboards.labels` | Labels for dashboard discovery | `{"grafana_dashboard": "1"}` |
| `webhook.enabled` | Enable validation webhooks | `true` |
| `webhook.port` | Webhook server port | `9443` |
| `webhook.certManager.enabled` | Use cert-manager for certificates | `false` |
| `resources.limits.cpu` | CPU limit | `4` |
| `resources.limits.memory` | Memory limit | `4Gi` |
| `resources.requests.cpu` | CPU request | `2` |
| `resources.requests.memory` | Memory request | `2Gi` |

## Examples

### Minimal Installation

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --namespace yanet-system \
  --create-namespace
```

### With Custom Namespace for Grafana

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --namespace yanet-system \
  --create-namespace \
  --set grafana.dashboards.namespace=monitoring
```

### Disable Metrics and Dashboard

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --namespace yanet-system \
  --create-namespace \
  --set metrics.enabled=false \
  --set grafana.dashboards.enabled=false
```

### With cert-manager

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --namespace yanet-system \
  --create-namespace \
  --set webhook.certManager.enabled=true
```

## Upgrading

```bash
helm upgrade yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --namespace yanet-system
```

## Uninstalling

```bash
helm uninstall yanet-operator --namespace yanet-system
```

## Documentation

- [Prometheus Metrics](../../README_METRICS.md)
- [Validation Webhooks](../../README_WEBHOOKS.md)
- [Testing Guide](../../README_TESTS.md)
- [Architecture](../../ARCHITECTURE.md)
