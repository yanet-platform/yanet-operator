# Release Guide

This document describes the release process for yanet-operator.

## 🎯 Overview

Releases are fully automated via GitHub Actions. When you push a version tag, the CI/CD pipeline:

1. **Builds multi-platform Docker images** (amd64, arm64)
2. **Publishes to Docker Hub and GHCR**
3. **Packages and publishes Helm chart** to OCI registries
4. **Generates installation manifests** (`install.yaml`)
5. **Creates GitHub Release** with artifacts and release notes

## 📋 Release Checklist

### 1. Prepare Release

- [ ] Ensure all tests pass: `make test-docker`
- [ ] Run linter: `make lint-docker`
- [ ] Update [`Chart.yaml`](deploy/charts/yanet-operator/Chart.yaml) version
- [ ] Update documentation if needed
- [ ] Commit all changes

### 2. Create Release Tag

```bash
# Set version (without 'v' prefix in variables)
VERSION="0.1.7"

# Create and push tag
git tag -a "v${VERSION}" -m "Release v${VERSION}"
git push origin "v${VERSION}"
```

### 3. Monitor Release Pipeline

GitHub Actions will automatically:

1. **Build Docker images** — [`docker` job](.github/workflows/release.yml)
   - Platforms: `linux/amd64`, `linux/arm64`
   - Registries: Docker Hub, GHCR
   - Tags: `v0.1.7`, `0.1.7`, `0.1`, `0`, `latest`

2. **Package Helm chart** — [`helm` job](.github/workflows/release.yml)
   - Syncs `appVersion` with git tag
   - Publishes to Docker Hub OCI and GHCR

3. **Generate manifests** — [`manifests` job](.github/workflows/release.yml)
   - Creates `install.yaml` with all resources

4. **Create GitHub Release** — [`release` job](.github/workflows/release.yml)
   - Generates changelog from git commits
   - Attaches artifacts (Helm chart, install.yaml)
   - Publishes release notes

### 4. Verify Release

```bash
# Check Docker images
docker pull yanetplatform/yanet-operator:0.1.7
docker pull ghcr.io/yanet-platform/yanet-operator:0.1.7

# Verify multi-platform support
docker manifest inspect yanetplatform/yanet-operator:0.1.7

# Check Helm chart
helm show chart oci://registry-1.docker.io/yanetplatform/yanet-operator --version 0.1.7

# Test installation
kubectl apply -f https://github.com/yanet-platform/yanet-operator/releases/download/v0.1.7/install.yaml
```

## 📦 Release Artifacts

Each release includes:

### Docker Images

**Docker Hub:**
- `yanetplatform/yanet-operator:0.1.7` (version)
- `yanetplatform/yanet-operator:0.1` (minor)
- `yanetplatform/yanet-operator:0` (major)
- `yanetplatform/yanet-operator:latest`

**GitHub Container Registry:**
- `ghcr.io/yanet-platform/yanet-operator:0.1.7`
- `ghcr.io/yanet-platform/yanet-operator:0.1`
- `ghcr.io/yanet-platform/yanet-operator:0`
- `ghcr.io/yanet-platform/yanet-operator:latest`

**Platforms:** `linux/amd64`, `linux/arm64`

### Helm Charts

**Docker Hub OCI:**
```bash
helm install yanet-operator \
  oci://registry-1.docker.io/yanetplatform/yanet-operator \
  --version 0.1.7 \
  --namespace yanet-system \
  --create-namespace
```

**GitHub Container Registry:**
```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.1.7 \
  --namespace yanet-system \
  --create-namespace
```

### Kubernetes Manifests

**install.yaml** — Complete installation manifest:
```bash
kubectl apply -f https://github.com/yanet-platform/yanet-operator/releases/download/v0.1.7/install.yaml
```

Contains:
- Custom Resource Definitions (CRDs)
- Namespace
- ServiceAccount, Role, RoleBinding
- Deployment
- Service (metrics, webhooks)
- ValidatingWebhookConfiguration

## 🔄 Versioning Strategy

We follow [Semantic Versioning](https://semver.org/):

- **MAJOR** (0.x.x) — Incompatible API changes
- **MINOR** (x.1.x) — New features, backward compatible
- **PATCH** (x.x.7) — Bug fixes, backward compatible

### Version Synchronization

- **Git tag:** `v0.1.7` (with 'v' prefix)
- **Chart version:** `0.1.7` (in [`Chart.yaml`](deploy/charts/yanet-operator/Chart.yaml))
- **Chart appVersion:** `0.1.7` (auto-synced from git tag)
- **Docker image tag:** `0.1.7` (extracted from git tag)

## 🛠️ Manual Release (Emergency)

If automated release fails, you can release manually:

### 1. Build and Push Docker Images

```bash
VERSION="0.1.7"

# Build multi-platform image
docker buildx create --use
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag yanetplatform/yanet-operator:${VERSION} \
  --tag yanetplatform/yanet-operator:latest \
  --tag ghcr.io/yanet-platform/yanet-operator:${VERSION} \
  --tag ghcr.io/yanet-platform/yanet-operator:latest \
  --push \
  .
```

### 2. Package and Push Helm Chart

```bash
# Update appVersion in Chart.yaml
sed -i "s/^appVersion:.*/appVersion: \"${VERSION}\"/" deploy/charts/yanet-operator/Chart.yaml

# Package chart
helm package deploy/charts/yanet-operator

# Push to Docker Hub
helm registry login registry-1.docker.io
helm push yanet-operator-${VERSION}.tgz oci://registry-1.docker.io/yanetplatform

# Push to GHCR
helm registry login ghcr.io
helm push yanet-operator-${VERSION}.tgz oci://ghcr.io/yanet-platform
```

### 3. Generate install.yaml

```bash
make manifests
make build-installer IMG=yanetplatform/yanet-operator:${VERSION}
```

### 4. Create GitHub Release

```bash
gh release create v${VERSION} \
  --title "Release v${VERSION}" \
  --notes "Manual release v${VERSION}" \
  dist/install.yaml \
  yanet-operator-${VERSION}.tgz
```

## 🔍 Troubleshooting

### Release Pipeline Fails

**Check GitHub Actions logs:**
```bash
gh run list --workflow=release.yml
gh run view <run-id> --log
```

**Common issues:**

1. **Docker Hub authentication fails**
   - Verify `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets
   - Check token permissions (read, write, delete)

2. **GHCR authentication fails**
   - Ensure `packages: write` permission in workflow
   - Verify `GITHUB_TOKEN` has correct scopes

3. **Helm push fails**
   - Check chart version in `Chart.yaml`
   - Verify OCI registry credentials
   - Ensure chart version doesn't already exist

4. **Manifest generation fails**
   - Run `make manifests` locally to check for errors
   - Verify kustomize installation

### Version Mismatch

If `Chart.yaml` version doesn't match git tag:

```bash
# Update Chart.yaml manually
vim deploy/charts/yanet-operator/Chart.yaml

# Commit and re-tag
git add deploy/charts/yanet-operator/Chart.yaml
git commit -m "Update chart version to 0.1.7"
git tag -d v0.1.7
git push origin :refs/tags/v0.1.7
git tag -a v0.1.7 -m "Release v0.1.7"
git push origin v0.1.7
```

### Rollback Release

To delete a release:

```bash
VERSION="0.1.7"

# Delete GitHub release
gh release delete v${VERSION} --yes

# Delete git tag
git tag -d v${VERSION}
git push origin :refs/tags/v${VERSION}

# Delete Docker images (manual via Docker Hub/GHCR UI)
# Delete Helm charts (manual via registry UI)
```

## 📊 Release Metrics

Track release health:

- **Docker Hub pulls:** https://hub.docker.com/r/yanetplatform/yanet-operator
- **GHCR downloads:** https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator
- **GitHub releases:** https://github.com/yanet-platform/yanet-operator/releases
- **Helm chart versions:** `helm search repo yanet-operator --versions`

## 🔐 Required Secrets

GitHub repository secrets:

| Secret | Description | Required For |
|--------|-------------|--------------|
| `DOCKERHUB_USERNAME` | Docker Hub username | Docker image publishing |
| `DOCKERHUB_TOKEN` | Docker Hub access token | Docker image publishing |
| `GITHUB_TOKEN` | Auto-provided by GitHub | GHCR, releases |

**Setup Docker Hub token:**
1. Go to https://hub.docker.com/settings/security
2. Create new access token with read/write/delete permissions
3. Add to GitHub: Settings → Secrets → Actions → New repository secret

## 📚 Related Documentation

- [README](README.md) — Project overview
- [Testing Guide](README_TESTS.md) — How to run tests
- [Validation Webhooks](README_WEBHOOKS.md) — Admission control
- [Prometheus Metrics](README_METRICS.md) — Monitoring
- [Architecture Analysis](ARCHITECTURE_ANALYSIS.md) — Design decisions

## 🎓 Best Practices

1. **Always test before releasing**
   ```bash
   make test-docker
   make lint-docker
   ```

2. **Update Chart.yaml version first**
   - Commit version bump separately
   - Tag after version is committed

3. **Use semantic versioning**
   - Breaking changes → major version
   - New features → minor version
   - Bug fixes → patch version

4. **Write meaningful release notes**
   - Highlight breaking changes
   - List new features
   - Document bug fixes

5. **Verify multi-platform images**
   ```bash
   docker manifest inspect yanetplatform/yanet-operator:0.1.7
   ```

6. **Test Helm chart installation**
   ```bash
   helm install test oci://registry-1.docker.io/yanetplatform/yanet-operator \
     --version 0.1.7 \
     --namespace test \
     --create-namespace \
     --dry-run
   ```

## 🚀 Quick Release

For experienced maintainers:

```bash
# 1. Update version
vim deploy/charts/yanet-operator/Chart.yaml
git add deploy/charts/yanet-operator/Chart.yaml
git commit -m "Bump version to 0.1.7"

# 2. Test
make test-docker && make lint-docker

# 3. Tag and push
git tag -a v0.1.7 -m "Release v0.1.7"
git push origin main v0.1.7

# 4. Monitor
gh run watch

# 5. Verify
docker pull yanetplatform/yanet-operator:0.1.7
helm show chart oci://registry-1.docker.io/yanetplatform/yanet-operator --version 0.1.7
```

---

**Last Updated:** 2026-04-30  
**Workflow:** [`.github/workflows/release.yml`](.github/workflows/release.yml)
