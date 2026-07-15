# Release Guide

This document describes the release process for yanet-operator.

## 🎯 Overview

Releases are fully automated via GitHub Actions. When you push a version tag, the CI/CD pipeline:

1. **Builds multi-platform Docker images** (amd64, arm64)
2. **Publishes to GHCR**
3. **Packages and publishes Helm chart** to GHCR OCI registry
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
# Set application version (without the 'v' prefix)
APP_VERSION="2.0.4"

# Create and push tag
git tag -a "v${APP_VERSION}" -m "Release v${APP_VERSION}"
git push origin "v${APP_VERSION}"
```

### 3. Monitor Release Pipeline

GitHub Actions will automatically:

1. **Build Docker images** — [`docker` job](.github/workflows/release.yml)
   - Platforms: `linux/amd64`, `linux/arm64`
   - Registries: GHCR
   - Tags: `v2.0.4`, `2.0.4`, `2.0`, `2`, `latest`

2. **Package Helm chart** — [`helm` job](.github/workflows/release.yml)
   - Syncs `appVersion` with git tag
   - Publishes to GHCR OCI

3. **Generate manifests** — [`manifests` job](.github/workflows/release.yml)
   - Creates `install.yaml` with all resources

4. **Create GitHub Release** — [`release` job](.github/workflows/release.yml)
   - Generates changelog from git commits
   - Attaches artifacts (Helm chart, install.yaml)
   - Publishes release notes

### 4. Verify Release

```bash
# Check Docker images
docker pull ghcr.io/yanet-platform/yanet-operator:2.0.4

# Verify multi-platform support
docker manifest inspect ghcr.io/yanet-platform/yanet-operator:2.0.4

# Check Helm chart
helm show chart oci://ghcr.io/yanet-platform/yanet-operator --version 0.1.8

# Test installation
kubectl apply -f https://github.com/yanet-platform/yanet-operator/releases/download/v2.0.4/install.yaml
```

## 📦 Release Artifacts

Each release includes:

### Docker Images

**GitHub Container Registry:**
- `ghcr.io/yanet-platform/yanet-operator:2.0.4`
- `ghcr.io/yanet-platform/yanet-operator:2.0`
- `ghcr.io/yanet-platform/yanet-operator:2`
- `ghcr.io/yanet-platform/yanet-operator:latest`

**Platforms:** `linux/amd64`, `linux/arm64`

### Helm Charts

**GitHub Container Registry:**
```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.1.8 \
  --namespace yanet-system \
  --create-namespace
```

### Kubernetes Manifests

**install.yaml** — Complete installation manifest:
```bash
kubectl apply -f https://github.com/yanet-platform/yanet-operator/releases/download/v2.0.4/install.yaml
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

- **Git tag:** `v2.0.4` (with `v` prefix)
- **Chart version:** `0.1.8` (in [`Chart.yaml`](deploy/charts/yanet-operator/Chart.yaml))
- **Chart appVersion:** `2.0.4` (auto-synced from git tag)
- **Docker image tag:** `2.0.4` (extracted from git tag)

The application and Helm chart use independent version sequences. Every release
must use a new value for both the git tag and the chart version because OCI chart
versions are immutable.

## 🛠️ Manual Release (Emergency)

If automated release fails, you can release manually:

### 1. Build and Push Docker Images

```bash
APP_VERSION="2.0.4"

# Build multi-platform image
docker buildx create --use
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag ghcr.io/yanet-platform/yanet-operator:${APP_VERSION} \
  --tag ghcr.io/yanet-platform/yanet-operator:latest \
  --push \
  .
```

### 2. Package and Push Helm Chart

```bash
APP_VERSION="2.0.4"
CHART_VERSION="0.1.8"

# Update appVersion in Chart.yaml
sed -i "s/^appVersion:.*/appVersion: \"${APP_VERSION}\"/" deploy/charts/yanet-operator/Chart.yaml

# Package chart
helm package deploy/charts/yanet-operator

# Push to GHCR
helm registry login ghcr.io
helm push yanet-operator-${CHART_VERSION}.tgz oci://ghcr.io/yanet-platform
```

### 3. Generate install.yaml

```bash
APP_VERSION="2.0.4"

make manifests
make build-installer IMG=ghcr.io/yanet-platform/yanet-operator:${APP_VERSION}
```

### 4. Create GitHub Release

```bash
APP_VERSION="2.0.4"
CHART_VERSION="0.1.8"

gh release create v${APP_VERSION} \
  --title "Release v${APP_VERSION}" \
  --notes "Manual release v${APP_VERSION}" \
  dist/install.yaml \
  yanet-operator-${CHART_VERSION}.tgz
```

## 🔍 Troubleshooting

### Release Pipeline Fails

**Check GitHub Actions logs:**
```bash
gh run list --workflow=release.yml
gh run view <run-id> --log
```

**Common issues:**

1. **GHCR authentication fails**
   - Ensure `packages: write` permission in workflow
   - Verify `GITHUB_TOKEN` has correct scopes

2. **Helm push fails**
   - Check chart version in `Chart.yaml`
   - Verify OCI registry credentials
   - Ensure chart version doesn't already exist

3. **Manifest generation fails**
   - Run `make manifests` locally to check for errors
   - Verify kustomize installation

### Version Mismatch

If `Chart.yaml` version doesn't match git tag:

```bash
# Update Chart.yaml manually
vim deploy/charts/yanet-operator/Chart.yaml

# Commit and re-tag
git add deploy/charts/yanet-operator/Chart.yaml
git commit -m "chore(release): bump chart version to 0.1.8"
git tag -d v2.0.4
git push origin :refs/tags/v2.0.4
git tag -a v2.0.4 -m "Release v2.0.4"
git push origin v2.0.4
```

### Rollback Release

To delete a release:

```bash
APP_VERSION="2.0.4"

# Delete GitHub release
gh release delete v${APP_VERSION} --yes

# Delete git tag
git tag -d v${APP_VERSION}
git push origin :refs/tags/v${APP_VERSION}

# Delete Docker images (manual via GHCR UI)
# Delete Helm charts (manual via registry UI)
```

## 📊 Release Metrics

Track release health:

- **GHCR downloads:** https://github.com/yanet-platform/yanet-operator/pkgs/container/yanet-operator
- **GitHub releases:** https://github.com/yanet-platform/yanet-operator/releases
- **Helm chart versions:** `helm search repo yanet-operator --versions`

## 🔐 Required Secrets

GitHub repository secrets:

| Secret | Description | Required For |
|--------|-------------|--------------|
| `GITHUB_TOKEN` | Auto-provided by GitHub | GHCR, releases |

## 📚 Related Documentation

- [README](README.md) — Project overview
- [Testing Guide](README_TESTS.md) — How to run tests
- [Validation Webhooks](README_WEBHOOKS.md) — Admission control
- [Prometheus Metrics](README_METRICS.md) — Monitoring
- [Architecture](ARCHITECTURE.md) — Design decisions

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
   docker manifest inspect ghcr.io/yanet-platform/yanet-operator:2.0.4
   ```

6. **Test Helm chart installation**
   ```bash
   helm install test oci://ghcr.io/yanet-platform/yanet-operator \
     --version 0.1.8 \
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
git commit -m "chore(release): bump chart version to 0.1.8"

# 2. Test
make test-docker && make lint-docker

# 3. Tag and push
git tag -a v2.0.4 -m "Release v2.0.4"
git push origin main v2.0.4

# 4. Monitor
gh run watch

# 5. Verify
docker pull ghcr.io/yanet-platform/yanet-operator:2.0.4
helm show chart oci://ghcr.io/yanet-platform/yanet-operator --version 0.1.8
```

---

**Last Updated:** 2026-07-15
**Workflow:** [`.github/workflows/release.yml`](.github/workflows/release.yml)
