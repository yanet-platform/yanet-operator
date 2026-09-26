# yanet-operator

[![GitHub Container Registry](https://img.shields.io/badge/GHCR-latest-blue?logo=github)](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)
[![Helm Chart](https://img.shields.io/badge/Helm-OCI-0f1689?logo=helm)](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)

Kubernetes operator for YANET2 workloads: component palettes, named strategic-merge
patches, box-type presets, multi-node installations and per-NUMA controlplanes.

**Operator 3.0.0 / chart 0.2.0** uses one API, `yanet.yanet-platform.io/v1alpha1`.
The former component-based API is exposed as `Yanet` and `YanetConfig`. The legacy
single-node implementation is removed. See the [breaking release notes](release-notes/v3.0.0.md)
before replacing an existing installation; no compatibility conversion is provided.

## Features

- Per-node Deployments selected through `nodeSelector`.
- Shared box-type Services with node-local traffic policy.
- Explicit NUMA fan-out and per-installation NUMA exclusions.
- Declarative native sidecars and standalone multi-container operators.
- Typed Multus attachments with matching device resource requests/limits.
- Admission validation, drift reporting, conditions, events and Prometheus metrics.
- Ownership-aware cleanup and guarded role/network transitions.

## Installation

Requires Kubernetes **1.33+** for named Service target ports on native sidecars.
After release publication, install into a cluster with the new CRDs:

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.2.0 \
  --namespace yanet-system \
  --create-namespace
```

Use `--set metrics.serviceMonitor.enabled=false` without Prometheus Operator.
Set the Grafana dashboard namespace or disable it with
`--set grafana.dashboards.enabled=false` when no Grafana sidecar consumes it.

The [chart README](deploy/charts/yanet-operator/README.md) describes all values.
Helm does not upgrade/delete CRDs in `crds/`; an existing incompatible installation
requires the coordinated [replacement procedure](release-notes/v3.0.0.md).

## API

| Kind | Resource | Scope |
| --- | --- | --- |
| `YanetConfig` | `yanetconfigs.yanet.yanet-platform.io` | Cluster; singleton `config` |
| `Yanet` | `yanets.yanet.yanet-platform.io` | Namespaced |

`YanetConfig.spec` contains three layers:

1. `components`: available controlplane/dataplane/birdAdapter, ordered dataplane
   `sidecars[]` and standalone `operators[].containers[]`.
2. `patches`: named strategic-merge Deployment fragments.
3. `boxTypes`: named presets selecting components and their ordered patches.

An installation selects a box type and nodes, with narrow image, enablement, NUMA
and network overrides. General resources, annotations and extra mounts belong in
the palette's patches.

### Configure the palette

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: YanetConfig
metadata:
  name: config
spec:
  components:
    controlplane:
      image: {name: controlplane, tag: example}
      numa: 2
      config:
        hostPath: /etc/yanet2
        args: [-c, "/etc/yanet2/controlplane.d/numa{numa}.yaml"]
    dataplane:
      image: {name: dataplane, tag: example}
      config:
        hostPath: /etc/yanet2
        args: [/etc/yanet2/dataplane.yaml]
  boxTypes:
    - name: release
      components:
        controlplane: {}
        dataplane: {}
```

This illustrates the schema, not a hardware-qualified configuration. Use the
[full example](deploy/examples/v1alpha1-yanetconfig-full.yaml) for sidecars,
resource/mount patches and typed networks. Replace example images with compatible
releases and prepare host configuration before enabling workloads.
Helm can manage this singleton through `yanetconfig.spec`; it is omitted by default.

### Select an installation

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: worker-1
  namespace: yanet
spec:
  boxType: release
  nodeSelector:
    kubernetes.io/hostname: worker-1
  autoSync: true
```

`boxType` is immutable. `autoSync` defaults to false (report-only).
`enabled: false` with `autoSync: true` scales Deployments to zero; `stop: true` on
the palette freezes reconciliation, including finalizer cleanup, without draining Pods.
Each node has one installation owner; an existing workload owner takes precedence
over a new overlapping selector.

## Runtime contract

All runtime Pods use private networking; `hostNetwork: true` and nonzero
`hostPort` are rejected after patches. Sidecar index `i` reserves ports
`8080+2*i` / `8081+2*i` before selection/enablement. Standalone Pods use 8080/8081.
Shared Services `yanet-<boxType>-<role>[-numa<N>]` expose 8080/8081 with
`internalTrafficPolicy: Local`; they remain present for disabled roles.

Managed HostPath config receives runtime bind/advertise and named NUMA gateway
environment after patches. Inline ConfigMap data stays opaque and receives no
automatic environment. Optional `config.mountPath` selects the container directory
(default `/etc/yanet2`); inline data is mounted as `<mountPath>/config`.
Only `{numa}` in controlplane arguments is substituted; all other arguments are literal.

Typed `components.dataplane.networks` couples existing NADs to device resources.
An installation list replaces the palette, `[]` clears it, and omission/null
inherits. The operator does not provision NICs or NADs. See the
[network configuration](deploy/charts/yanet-operator/README.md).

Runtime probe readiness and full packet forwarding require target-cluster
qualification. Pod Ready and operator conditions alone do not establish them.

## Development

Use Make + Docker as required by [AGENTS.md](AGENTS.md):

```bash
make generate manifests helm-crds
make fmt
make test-docker
make test-docker-race
make lint vet
make helm-lint
make docker-build
```

## Documentation

- [Architecture](ARCHITECTURE.md) and [YANET2 runtime topology](YANET2_ARCH.md)
- [Testing](README_TESTS.md)
- [Admission webhooks](README_WEBHOOKS.md)
- [Prometheus metrics](README_METRICS.md)
- [Release process](README_RELEASES.md)
- [Contributing](CONTRIBUTING.md)

## License

Copyright 2023-2026 YANDEX LLC. Licensed under the [Apache License 2.0](LICENSE).
