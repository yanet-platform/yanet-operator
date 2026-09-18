# yanet-operator

[![GitHub Container Registry](https://img.shields.io/badge/GHCR-latest-blue?logo=github)](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)
[![Helm Chart](https://img.shields.io/badge/Helm-OCI-0f1689?logo=helm)](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)

Kubernetes operator for managing YANET (Yet Another Network) deployments on worker nodes.

> **🚀 v2alpha1 API Available!** Component-based API (boxTypes + strategic-merge patches) with multi-node support via `nodeSelector` and per-NUMA controlplane fan-out. See [YANET2_ARCH.md](YANET2_ARCH.md) for architecture details.

## Features

- 🔄 **Automated Deployment Management** — Creates and updates Deployments based on Yanet CRD specs
- 🎯 **Per-Node Deployment** — One deployment per worker node with node affinity
- 🔍 **Auto-Discovery** — Automatically discovers and manages Yanet instances
- 📊 **Status Conditions** — Ready, Synced, and Progressing conditions
- 🧹 **Graceful Cleanup** — Finalizers ensure proper resource cleanup
- 📝 **Kubernetes Events** — Audit trail for all operator actions
- ✅ **Validation Webhooks** — Admission control for Yanet and YanetConfig resources
- 📈 **Prometheus Metrics** — Reconciliation metrics and resource monitoring
- 🧪 **93.2% Test Coverage** — Comprehensive unit and integration tests
- 🚀 **CI/CD Ready** — GitHub Actions for testing and publishing

## Quick Start

### Installation via Helm (Recommended)

```bash
# Install from GitHub Container Registry
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.1.5 \
  --namespace yanet-system \
  --create-namespace
```

### Installation via kubectl

```bash
# Apply CRDs and operator deployment
kubectl apply -f https://github.com/yanet-platform/yanet-operator/releases/latest/download/install.yaml
```

## API Versions

v1 and v2 live in the same API group (`yanet.yanet-platform.io`) but as
**separate CRDs** with disjoint controllers and webhooks. There is no
storage-version dispatch and no in-process conversion.

### v1alpha1 — `yanets` / `yanetconfigs` (legacy, in production)
Single-node management with the `nodename` field; per-installation
component spec embedded directly in the `Yanet` CR.

### v2alpha1 — `yanetsv2` / `yanetconfigsv2` ✨
Component palette + named strategic-merge patches + named `boxTypes`
live in the cluster-scoped `YanetConfigV2` singleton named `config`. The
`YanetV2` CR is minimal: pick a `boxType`,
select nodes via `nodeSelector`, optionally override per-container
`image.{name,tag}`, workload/native-sidecar `enabled`, and controlplane
`disabledNuma`. Standalone groups use `components.operators[].containers[]`.
Dataplane sidecars use the ordered, atomic `components.dataplane.sidecars[]` list:
one `SidecarSpec` (`name`, `image`, `config`, `listeners`) per native container.
Box types select sidecars by name; installation overrides use
`components.dataplane.sidecars.<name>`. BIRD, neighbour-sidecar and netconfig are
ordinary entries with declarative mounts and permissions. See the
[full example](deploy/examples/v2alpha1-yanetconfig-full.yaml); replace illustrative
image tags with tested releases before deployment.

Runtime Kubernetes probes and blocking KNI startup hooks are intentionally absent:
dataplane must start to create KNI. Application readiness belongs to announcer and
the YANET gRPC readiness APIs; operator consumption of this readiness is deferred.
Neighbour-sidecar exposes its own `Ready/Watch`, without gateway registration.
Omitted listeners default to `[grpc]` for every role; HTTP-only roles explicitly
declare `[http]`. `listeners: []` suppresses the Service, not the reserved slot or
host-config bind/gateway env.

Shared Services
are unconditional for service-backed roles and are named
`yanet-<boxType>-<component>[-numa<N>]` within each namespace. They expose
stable gRPC/HTTP ports `8080/8081`. All v2 Pods use private networking; final
`hostNetwork: true` and nonzero `hostPort` are rejected. Sidecar index `i` reserves
`8080+2*i` and `8081+2*i` before selection/enablement filtering. Append preserves
existing indices; reorder/insertion/removal changes the Pod template. For a managed
HostPath config after patches, the operator injects runtime bind env, Service FQDN
advertise and complete named NUMA gateway overrides. Inline/ConfigMap data stays
opaque and receives no automatic env. Compatible gateway runtimes must support
`YANET_KUBERNETES_GATEWAYS`; preparing their images and host configs is a rollout
prerequisite. See [the runtime contract](ARCHITECTURE.md#listener-endpoint-configuration).
Per-NUMA controlplane
fan-out is configured by `YanetConfigV2.spec.components.controlplane.numa`
(default 1). Set it explicitly for multi-NUMA hosts before upgrading; node
labels do not determine the fan-out. `disabledNuma` excludes physical domains
without renumbering the remaining instances. Each node can belong to only
one `YanetV2`; overlapping selectors are resolved in favour of the existing
workload owner (or the oldest CR before workloads exist).

> **Scope migration:** Kubernetes does not permit changing an installed CRD
> from namespaced to cluster-scoped. Before upgrading a cluster that already
> has the older namespaced `YanetConfigV2` CRD, export its spec, remove and
> reinstall that CRD, then recreate the configuration manually as cluster-scoped
> `metadata.name: config` or let Helm create it through `yanetconfigV2` values.
> Rename any
> `spec.components.birdAdapter.containers.birdAdapter` override key to the
> rendered container name `bird-adapter`.
> Delete old per-installation v2 Services before enabling the shared-Service
> model; the operator deliberately does not take over resources with another owner.
> Before enabling the native BIRD sidecar, stop the old operator and delete its
> standalone v2 BIRD Deployments. The old and new BIRD processes share the
> node-local `/run/bird` control-socket directory and must not overlap.
>
> **v2 schema change in chart 0.1.12:** replace fixed sidecar maps with the ordered
> list; move colocated operator declarations into it and announcer into ordinary
> operators. Remove legacy placement, host-network fields and endpoint patches.
> Coordinate CRD, controller and spec updates; there is no automatic conversion.
> Drain existing workloads before changing networking or moving a role between
> standalone and dataplane. Preflight retains Deployment/ReplicaSet/Pod producer
> guards and shared-Service cutover guards. `enabled: false` with `autoSync: true`
> drains; `stop: true` only freezes reconciliation and does not stop Pods.
> v1 resources and controllers are unchanged.

Palette images may override `registry` and `prefix` independently: omission
inherits `spec.images`, while `""` clears that part. Installation container
overrides still only support `name`, `tag`, and native-sidecar `enabled`.
Controlplane `config.args` supports `{numa}` for the physical NUMA index;
literal `.yaml`/`.yml` arguments without it retain the legacy `-<index>` suffix.

See [YANET2_ARCH.md](YANET2_ARCH.md) for the full design and
[`deploy/examples/v2alpha1-*.yaml`](deploy/examples/) for runnable
samples.

## Usage

### Create a Yanet instance

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: yanet-worker-01
spec:
  nodeName: worker-01
  type: release
  dataplane:
    enable: true
    image: yanetplatform/yanet-dataplane
    tag: latest
  controlplane:
    enable: true
    image: yanetplatform/yanet-controlplane
    tag: latest
```

```bash
kubectl apply -f yanet-instance.yaml
```

### Configure global settings

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: YanetConfig
metadata:
  name: yanet-config
spec:
  updateWindow: 300
  autoDiscovery:
    enable: true
    namespace: default
```

## Documentation

- 📖 [Testing Guide](README_TESTS.md) — How to run tests and contribute
- ✅ [Validation Webhooks](README_WEBHOOKS.md) — Admission control and validation rules
- 📈 [Prometheus Metrics](README_METRICS.md) — Monitoring and observability
- 🚀 [Release Guide](README_RELEASES.md) — How to create and publish releases
- 🏗️ [Architecture](ARCHITECTURE.md) — Design decisions and roadmap
- 🤖 [AI Development Guide](AGENTS.md) — Guidelines for AI assistants

## Development

### Prerequisites

- Go 1.26.2+
- Docker
- kubectl
- Helm 3+
- Kind (for local testing)

## Getting Started

You'll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.

**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Build and Run Locally

```bash
# Run tests
make test

# Run tests with race detector
make test-race

# Run tests in Docker (no local Go required)
make test-docker

# Build binary
make build

# Run locally (against current kubeconfig context)
make run
```

### Build and Push Docker Image

```bash
# Build image
make docker-build IMG=ghcr.io/yanet-platform/yanet-operator:v0.1.5

# Push to GHCR
make docker-push IMG=ghcr.io/yanet-platform/yanet-operator:v0.1.5
```

### Deploy to Cluster

```bash
# Install CRDs
make install

# Deploy operator
make deploy IMG=ghcr.io/yanet-platform/yanet-operator:v0.1.5

# Create sample resources
kubectl apply -f config/samples/
```

### Update Helm Chart

```bash
# 1. Update CRDs (if API changed)
make manifests
./bin/kustomize build config/crd > deploy/charts/yanet-operator/crds/yanet.yaml

# 2. Update RBAC (if permissions changed)
make manifests
./bin/kustomize build config/rbac/ | sed 's/system/{{ .Values.namespace }}/g' > deploy/charts/yanet-operator/templates/rbac.yaml

# 3. Update version in Chart.yaml
# Edit deploy/charts/yanet-operator/Chart.yaml

# 4. Test chart locally
helm lint deploy/charts/yanet-operator
helm template test deploy/charts/yanet-operator

# 5. Create git tag to trigger publishing
git tag v0.1.5
git push origin v0.1.5
```

**Note:** GitHub Actions will automatically build and publish Docker images and Helm charts when you push a version tag.

### Uninstall CRDs
To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller
UnDeploy the controller from the cluster:

```sh
make undeploy
```

### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.

### Test It Out
1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make run
```

**NOTE:** You can also run this in one step by running: `make install run`

### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make help` for more information on all potential `make` targets

## CI/CD

The project uses GitHub Actions for automated testing and publishing:

- **Tests** — Run on every push and PR
- **Docker Images** — Published to GHCR on version tags
- **Helm Charts** — Published to GHCR OCI registry on version tags

See [GITHUB_ACTIONS_DOCKER.md](GITHUB_ACTIONS_DOCKER.md) for setup instructions.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## Resources

- [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)
- [YANET Platform](https://github.com/yanet-platform)
- [GHCR Repository](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)

## License

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
