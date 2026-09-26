# Testing yanet-operator

## Strategy

- Test public behavior, persisted state and required absence of side effects.
- Use envtest for admission, API defaulting and storage semantics; fake clients
  cannot establish these contracts.
- Keep invalid fixtures valid except for the condition under test, and assert
  the intended failure rather than any error.
- For a bug fix, observe the regression fail before the fix and pass afterward.
  Verify new tests of existing behavior with a relevant temporary mutation.
- Avoid change-detector tests for source spelling, implementation order or a
  temporary migration. Mechanical renames use the surviving behavior suites.
- Keep tests deterministic and isolated. Failed API reads must not be treated as
  empty results in polling assertions. Avoid sleeps and uncontrolled external DNS.
- Coverage locates gaps; it does not prove assertion quality or runtime correctness.

See [Go Test Comments](https://go.dev/wiki/TestComments) and [AGENTS.md](AGENTS.md).

## Run through Make + Docker

Do not run `go test`, `go build` or `go vet` directly on the host. Make targets
use the pinned Go 1.26.2 image and install the required envtest assets.

```bash
make generate manifests helm-crds
make test-docker-unit          # API validators/types, helpers and manifests
make test-docker-integration   # Controller package, including envtest
make test-docker               # Full suite with coverage
make test-docker-race          # Full suite with race detector
make lint
make vet
make helm-lint
make test-packaging            # Rendered Helm/Kustomize contracts; requires Helm, Python 3 + PyYAML
make docker-build
```

## Existing suites

| Location | Behavior covered |
| --- | --- |
| `api/v1alpha1/*_test.go` | Palette/installation validation, config sources, NUMA and network shape |
| `internal/helpers/resolve_test.go` | Box wiring, effective images, overrides and selection |
| `internal/manifests/*_test.go` | Deployments, patches, Services, listeners, native sidecars and networks |
| `internal/controller/yanet_reconciler*_test.go` | Reconcile, ownership, errors, throttling, pruning and cleanup |
| `internal/controller/shared_services*_test.go` | Palette-owned Services, cutover and no-op convergence |
| `internal/controller/defaulting_envtest_test.go` | Real API defaults, dry-run normalization, no-op writes and patch removal |
| `internal/controller/yanet_*_e2e_test.go` | Manager-backed admission, autosync and status |
| `internal/controller/dataplane_networks_envtest_test.go` | Persisted network inheritance/replacement/explicit clear |
| `deploy/tests/webhooks/run.sh` | Existing Helm CI install/admission/reconcile smoke on kind |
| `deploy/tests/test_packaging.py` | Rendered TLS/Service/RBAC wiring, chart options and CI failure propagation |

`suite_test.go` registers only the component-based `v1alpha1` scheme and both
validators/controllers. Teardown waits for manager shutdown before stopping
envtest. Since envtest has no garbage collector or Deployment controller, its
cleanup helpers explicitly complete dependent teardown after ownership assertions.

The legacy single-node tests and fixtures are removed. The former component-based
tests now exercise `Yanet`/`YanetConfig` in `v1alpha1`.

## CI and limits

GitHub Actions runs unit/integration tests, lint, Docker build, chart lint/package
and the existing kind-based Helm smoke. Target-cluster qualification separately
covers device-plugin/CNI allocation, PF ownership, hugepages, runtime gateways,
restarts and packet forwarding. Local tests and Pod Ready do not establish those
hardware/application outcomes.
