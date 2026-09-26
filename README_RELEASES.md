# Release guide

Application releases follow [Semantic Versioning](https://semver.org/).
Incompatible API changes require a major release. Helm chart versions are
independent and must also be new for each release.

The next release is **operator 3.0.0 / chart 0.2.0**. Its API reset and replacement
procedure are documented in [release notes](release-notes/v3.0.0.md). Release
preparation in a pull request does not publish images or qualify a live cluster.

## Prepare

1. Update `deploy/charts/yanet-operator/Chart.yaml`: `version` is the chart version,
   `appVersion` is the operator image version without `v`.
2. Write compatibility notes in `release-notes/v<application-version>.md`.
3. Regenerate API artifacts and run the relevant checks through Make/Docker:

   ```bash
   make generate
   make manifests
   make helm-crds
   make fmt
   make test-docker
   make test-docker-race
   make lint
   make vet
   make helm-lint
   make docker-build
   ```

4. Inspect the final diff and generated schemas. Complete review and merge the
   release preparation before tagging. Record separate target-cluster results.

## Publish

After the reviewed commit is merged and its checks pass:

```bash
git fetch origin main --tags
git tag -a v3.0.0 origin/main -m "Release v3.0.0"
git push origin v3.0.0
```

Verify that `origin/main` is the intended release revision before tagging.
Never move/reuse an existing release tag or OCI chart version.

`.github/workflows/release.yml` is the sole owner of stable tagged publication:

1. Build/publish `linux/amd64` and `linux/arm64` images to GHCR.
2. Package/publish the chart to GHCR OCI, with `appVersion` set from the tag.
3. Generate `install.yaml` with the matching image.
4. Create the GitHub Release with chart/manifests, commit changelog and the
   version-specific compatibility notes.

The regular Helm workflow lints, packages and tests PR/main charts; it uploads a
workflow artifact and does not publish the stable chart version. The regular
Docker workflow builds development/PR images; version tags belong to the release
workflow so a single-platform build cannot overwrite the multi-platform release.

## Verify artifacts

```bash
gh run list --workflow=release.yml
gh release view v3.0.0
docker manifest inspect ghcr.io/yanet-platform/yanet-operator:3.0.0
helm show chart oci://ghcr.io/yanet-platform/yanet-operator --version 0.2.0
```

Check both image platforms, the source revision/digest, chart `appVersion` and
the attached `install.yaml`. Do not infer publication from a successful local
build or from a merged PR.

For a new cluster:

```bash
helm install yanet-operator \
  oci://ghcr.io/yanet-platform/yanet-operator \
  --version 0.2.0 \
  --namespace yanet-system \
  --create-namespace
```

If Prometheus Operator is absent, set `metrics.serviceMonitor.enabled=false`.
Configure the Grafana namespace or disable dashboard creation as appropriate.
Existing installations must follow the [replacement notes](release-notes/v3.0.0.md)
instead of applying this command over incompatible CRDs.

### Standalone installer prerequisites

`install.yaml` (also `make deploy`) requires **cert-manager v1 with its webhook and
CA injector running before applying the installer**. It creates a namespaced
self-signed Issuer and serving Certificate, mounts the resulting TLS Secret and
connects both validating webhooks to the operator Service. It does not install
cert-manager. The Kubernetes API server must be able to reach the webhook Service.

On a clean cluster with cert-manager already installed:

```bash
kubectl apply -f install.yaml
kubectl wait -n yanet-operator-system --for=condition=Ready \
  certificate/yanet-operator-serving-cert --timeout=120s
kubectl rollout status -n yanet-operator-system \
  deployment/yanet-operator-controller-manager --timeout=120s
kubectl get validatingwebhookconfiguration \
  yanet-operator-validating-webhook-configuration -o yaml
```

Verify both `clientConfig.caBundle` fields are populated before creating
`YanetConfig` or `Yanet` objects: certificate readiness and Deployment rollout
alone do not prove that asynchronous CA injection has completed. The installer
uses fail-closed admission. Unlike the default Helm certgen hooks, cert-manager
renews certificates and maintains the CA bundle. Do not enable the commented CRD
conversion patches: this release has a single API version and no conversion server.

## Failed publication

Inspect the failed Actions job with `gh run view <run-id> --log-failed`.
Fix the cause and determine which artifacts were already published before
retrying. If source changes are needed, use a new application/chart version;
released versions are immutable. Do not delete/re-tag a public release as a
version-mismatch workaround.

Reference: [Helm CRD best practices](https://helm.sh/docs/v3/chart_best_practices/custom_resource_definitions/).
