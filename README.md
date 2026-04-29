# yanet-operator

[![Docker Hub](https://img.shields.io/docker/v/yanetplatform/yanet-operator?label=Docker%20Hub&logo=docker)](https://hub.docker.com/r/yanetplatform/yanet-operator)
[![GitHub Container Registry](https://img.shields.io/badge/GHCR-latest-blue?logo=github)](https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator)
[![Helm Chart](https://img.shields.io/badge/Helm-OCI-0f1689?logo=helm)](https://hub.docker.com/r/yanetplatform/yanet-operator)

Kubernetes operator for managing YANET (Yet Another Network) deployments on worker nodes.

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
# Install from Docker Hub OCI registry
helm install yanet-operator \
  oci://registry-1.docker.io/yanetplatform/yanet-operator \
  --version 0.1.5 \
  --namespace yanet-system \
  --create-namespace

# Or install from GitHub Container Registry
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
- 🏗️ [Architecture Analysis](ARCHITECTURE_ANALYSIS.md) — Design decisions and roadmap
- 🤖 [AI Development Guide](FOR_AI.md) — Guidelines for AI assistants

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
make docker-build IMG=yanetplatform/yanet-operator:v0.1.5

# Push to Docker Hub
make docker-push IMG=yanetplatform/yanet-operator:v0.1.5
```

### Deploy to Cluster

```bash
# Install CRDs
make install

# Deploy operator
make deploy IMG=yanetplatform/yanet-operator:v0.1.5

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
- **Docker Images** — Published to Docker Hub and GHCR on version tags
- **Helm Charts** — Published to OCI registries on version tags

See [GITHUB_ACTIONS_DOCKER.md](GITHUB_ACTIONS_DOCKER.md) for setup instructions.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

## Resources

- [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)
- [YANET Platform](https://github.com/yanet-platform)
- [Docker Hub Repository](https://hub.docker.com/r/yanetplatform/yanet-operator)

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

