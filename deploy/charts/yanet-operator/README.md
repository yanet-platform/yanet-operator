# Yanet Operator Helm Chart

Kubernetes operator for managing YANET (Yet Another Network) deployments on worker nodes.

Requires Kubernetes 1.33+ for named Service target ports on native sidecars.

## Installation

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.1.13 \
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

The v2 configuration is supplied through `yanetconfigV2.spec`. The chart
creates the cluster-scoped `YanetConfigV2` singleton with the fixed name
`config`. Existing clusters with the older namespaced CRD must export the
configuration and recreate the CRD before upgrading because Kubernetes cannot
change a CRD's scope in place. Recreate `config` manually, or let Helm create it
by setting `yanetconfigV2`; the chart does not adopt an existing object.
Rename a legacy `birdAdapter` container override key to the rendered name
`bird-adapter`.

Chart 0.1.12 changes the v2 workload schema. Declare native sidecars as the ordered
atomic `components.dataplane.sidecars[]` list, one container per entry; select them
through box-type sidecar maps and override them through installation
`components.dataplane.sidecars.<name>`. Standalone groups remain in `operators[]`;
announcer is an ordinary operator. Remove old placement/host-network fields and
intermediate endpoint patches. Coordinate specs, CRDs and controller; this is not
an automatic conversion. All v2 Pods require private networking and reject hostPort.
The v1 API and controller remain unchanged.

Sidecar index `i` reserves `8080+2*i` / `8081+2*i` even when disabled or unselected.
External Service ports stay 8080/8081. `listeners: []` disables the Service, not the
slot or host-config env. Metrics requires explicit `[http]`. Managed HostPath
configs receive runtime bind, advertise and complete named NUMA gateway env after
patches; ConfigMap content stays opaque. Deploy compatible runtime images and
prepare host gateway identities/TLS before enabling the new profile.

Chart 0.1.13 adds optional `config.mountPath` for every v2 configuration source.
It selects an absolute container directory and defaults to `/etc/yanet2`.
HostPath source directories are unchanged; inline data appears as `config` in the
chosen directory. Supply matching `config.args` explicitly. Patches remain available
for additional inputs, sockets and permissions; no component name implies a mount.
The full example uses a generated ConfigMap to select netconfig's Netplan mode and
mounts only the host `/etc/netplan/00-interfaces.yaml` file as its network input.

With webhooks enabled, chart-managed `yanetconfigV2` requires
`webhook.failurePolicy: Ignore`. Helm
creates normal resources before the post-install/post-upgrade webhook CA job;
with `Fail`, manage the singleton separately after the webhook is ready.

Before enabling the native v2 BIRD sidecar during an upgrade, stop the old
operator and delete its standalone v2 BIRD Deployments. Both variants own the
node-local `/run/bird` control-socket directory and must not overlap.

Drain workloads before changing networking or moving a role between standalone
and dataplane. Use installation `enabled: false` and `autoSync: true`, then wait for
observed scale-down and terminated Pods. `stop: true` only pauses reconciliation.
Preflight retains producer and shared-Service cutover guards using live
Deployments, ReplicaSets and Pods. It no longer allocates node-wide ports.

In `yanetconfigV2.spec.components`, each image's `registry` and `prefix`
independently inherit `spec.images` when omitted; `""` explicitly clears that
part. These fields are not installation container overrides. Controlplane
`config.args` accepts `{numa}`; literal YAML paths retain the legacy NUMA suffix.
Set `yanetconfigV2.spec.components.controlplane.numa` explicitly for multi-NUMA
hosts before upgrading. It defaults to 1 and does not depend on node labels.
Per-installation `disabledNuma` excludes domains without renumbering the rest.

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
