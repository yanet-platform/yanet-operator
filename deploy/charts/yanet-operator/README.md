# Yanet Operator Helm Chart

Kubernetes operator for managing YANET (Yet Another Network) deployments on worker nodes.

Requires Kubernetes 1.33+ for named Service target ports on native sidecars.

## Installation

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.2.0 \
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

Optionally manage the cluster-scoped `YanetConfig/config` singleton through
`yanetconfig.spec`. The default `{}` omits it. This minimal palette illustrates
the component schema; supply qualified images, host configs and resource patches
before enabling workloads:

```yaml
yanetconfig:
  spec:
    components:
      controlplane:
        image: {name: controlplane, tag: example}
      dataplane:
        image: {name: dataplane, tag: example}
    boxTypes:
      - name: release
        components:
          controlplane: {}
          dataplane: {}
```

Chart 0.2.0 / operator 3.0.0 uses only `yanet.yanet-platform.io/v1alpha1`, kinds
`Yanet` and `YanetConfig`. The former component-based API has these short names;
the legacy single-node implementation is removed. Existing installations require
a clean CRD replacement and recreated resources; `helm upgrade` does not update
CRDs or convert old objects. See the [release notes](../../../release-notes/v3.0.0.md).

Declare native sidecars as the ordered
atomic `components.dataplane.sidecars[]` list, one container per entry; select them
through box-type sidecar maps and override them through installation
`components.dataplane.sidecars.<name>`. Standalone groups remain in `operators[]`;
announcer is an ordinary operator. Remove old placement/host-network fields and
intermediate endpoint patches. All Pods require private networking and reject hostPort.

Sidecar index `i` reserves `8080+2*i` / `8081+2*i` even when disabled or unselected.
External Service ports stay 8080/8081. `listeners: []` disables the Service, not the
slot or host-config env. Metrics requires explicit `[http]`. Managed HostPath
configs receive runtime bind, advertise and complete named NUMA gateway env after
patches; ConfigMap content stays opaque. Deploy compatible runtime images and
prepare host gateway identities/TLS before enabling the new profile.

Optional `config.mountPath` is available for every configuration source.
It selects an absolute container directory and defaults to `/etc/yanet2`.
HostPath source directories are unchanged; inline data appears as `config` in the
chosen directory. Supply matching `config.args` explicitly. Patches remain available
for additional inputs, sockets and permissions; no component name implies a mount.
The full example uses a generated ConfigMap to select netconfig's Netplan mode and
mounts only the host `/etc/netplan/00-interfaces.yaml` file as its network input.

With webhooks enabled, chart-managed `yanetconfig` requires
`webhook.failurePolicy: Ignore`. Helm
creates normal resources before the post-install/post-upgrade webhook CA job;
with `Fail`, manage the singleton separately after the webhook is ready.

Drain workloads before changing networking or moving a role between standalone
and dataplane. Use installation `enabled: false` and `autoSync: true`, then wait for
observed scale-down and terminated Pods. `stop: true` only pauses reconciliation.
Preflight retains producer and shared-Service cutover guards using live
Deployments, ReplicaSets and Pods. It no longer allocates node-wide ports.

In `yanetconfig.spec.components`, each image's `registry` and `prefix`
independently inherit `spec.images` when omitted; `""` explicitly clears that
part. These fields are not installation container overrides. Controlplane
`config.args` substitutes `{numa}` only; all other arguments remain literal.
Set `yanetconfig.spec.components.controlplane.numa` explicitly for multi-NUMA
hosts before upgrading. It defaults to 1 and does not depend on node labels.
Per-installation `disabledNuma` excludes domains without renumbering the rest.

There is no `autoDiscovery`, `config.url`, or container-level `hostIPC` field.
Use explicit Deployment patches for config downloaders, Pod IPC and agent
shared-memory mounts.

### Device-backed Multus attachments

Declare the default ordered list in `yanetconfig.spec.components.dataplane.networks`:

```yaml
yanetconfig:
  spec:
    components:
      dataplane:
        networks:
          - name: yanet-pf
            interface: eth2
            resourceName: yanet-platform.io/pf
          - name: yanet-pf
            interface: eth4
            resourceName: yanet-platform.io/pf
```

This is a fragment of the palette, not a complete installation. Each entry adds
one Multus attachment and one device request/limit to the primary dataplane
container. The two entries above produce two attachments of the same NAD and
`yanet-platform.io/pf: "2"`. An omitted `namespace` uses the installation's
namespace. Interface names must be unique, at most 15 characters, and cannot be
`eth0` or `lo`.

An installation can replace the complete list:

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: custom-node
  namespace: yanet
spec:
  boxType: firewall
  nodeSelector:
    kubernetes.io/hostname: worker-1
  components:
    dataplane:
      networks:
        - name: custom-pf
          namespace: yanet
          interface: eth2
          resourceName: yanet-platform.io/custom_pf
```

Omitted/null `networks` inherits the palette; `networks: []` removes the typed
attachments and their reservations. This override applies to every node matched
by the installation. Requests are explicit integer counts, not an automatic
"all devices on this node" selection.

NADs and device-plugin pools remain externally managed. `resourceName` must equal
the referenced NAD's resource annotation. The operator does not discover NICs,
configure PF/VF drivers, or read node-local device-plugin JSON. A standalone
device-plugin DaemonSet and common NAD can be deployed with `extraManifests`.

When adopting typed networks, remove the Multus annotation and corresponding
device quantities from dataplane/sidecar patches. Conflicts are rejected before
workload changes, including stale default-pool reservations after an override.
CPU, memory, hugepages and unrelated device-resource patches are retained. If
typed networks are omitted everywhere, existing patch-based networking remains
supported.

Before changing live PF attachments, disable the installation with
`Yanet.spec.enabled: false` and wait for its Pods to terminate. Update the host
configuration, device-plugin selection and attachment list together, then enable
the installation. This does not resolve the separate dataplane/controlplane
shared-memory startup-lifecycle requirement.

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
