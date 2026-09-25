# AGENTS.md — yanet-operator Development Guide

Read this file before changing code or running builds/tests.

## Build and test through Make + Docker

Use the Go version pinned in `go.mod`, `Dockerfile` and `Makefile` (1.26.2).
Do not run `go build`, `go test` or `go vet` directly on the host.

```bash
make generate                 # DeepCopy generation
make manifests                # CRDs, admission configuration and RBAC
make helm-crds                # CRD bundle distributed by Helm
make fmt
make test-docker-unit          # API, helpers and manifests
make test-docker               # Full suite, including envtest
make test-docker-race          # Full suite with race detection
make lint
make vet
make helm-lint
make docker-build
```

Generated tool installation may require the pinned Go toolchain too. Keep tool
installation and build execution in Docker when the host toolchain differs.
Integration tests require envtest assets; the Docker targets install them.

## API and project structure

The operator manages **YANET2 runtime workloads** through one Kubernetes API,
`yanet.yanet-platform.io/v1alpha1`:

- `Yanet` / `yanets`: namespaced installation, `boxType`, `nodeSelector`, overrides.
- `YanetConfig` / `yanetconfigs`: cluster-scoped singleton named `config`, with
  the component palette, named patches and box types.

The legacy single-node API and implementation have been removed. There is no
compatibility dispatcher, alias package or conversion webhook. API version and
runtime generation are different concepts: `/etc/yanet2` and YANET2 image names
must not be renamed when changing the operator API.

| Path | Responsibility |
| --- | --- |
| `api/v1alpha1/` | Types, validation, typed configuration/network attachments |
| `cmd/main.go` | Scheme, manager, two controllers and two validators |
| `internal/controller/yanet_controller.go` | Installation watch/reconcile entry point |
| `internal/controller/yanet_reconciler.go` | Preflight, apply, status and cleanup |
| `internal/controller/yanetconfig_controller.go` | Shared palette snapshot |
| `internal/controller/shared_services.go` | Shared Service lifecycle |
| `internal/helpers/resolve.go` | Resolve palette, box wiring and overrides |
| `internal/manifests/` | Build, compose, patch and validate Kubernetes resources |
| `internal/events/recorder.go` | EventRecorder wrapper |
| `deploy/charts/yanet-operator/` | Helm chart, `yanetconfig` values and dashboard |
| `deploy/examples/v1alpha1-*.yaml` | Component-based examples |
| `deploy/tests/webhooks/` | Existing Helm admission/reconcile smoke harness |

## Code and tests

- Read the implementation, callers and existing tests before changing behavior.
- Make the smallest cohesive change; avoid speculative abstractions and unrelated cleanup.
- Comments, commits and public documentation must be in English. Use generic
  hosts/registries in tests, never internal infrastructure names.
- Use structured key-value logging and propagate actionable errors.
- Keep ownership, cancellation, side effects and concurrency invariants explicit.
- Test observable public behavior and realistic failures; do not add tests of
  private methods, source spelling, a temporary migration or implementation order.
- For mechanical renames/removals, run the surviving behavior tests and relevant
  generation/build checks instead of adding change-detector tests.
- When adding a regression test, observe its expected failure before the fix and
  success afterward. For already-correct behavior, verify sensitivity with a
  relevant temporary mutation. Never weaken assertions just to obtain green tests.
- Use small fixtures, table-driven tests where useful, faithful fakes and real
  envtest when correctness depends on admission/defaulting/storage semantics.
- Check returned errors in polling assertions. A failed List must not become an
  apparently empty set. Avoid sleeps and uncontrolled network dependencies.
- Follow existing `testing`, Ginkgo/Gomega and fake-client patterns; do not add
  another assertion framework. Use `t.Helper`/`GinkgoHelper` for setup helpers.
- Use coverage to find untested paths, not as proof of correctness. Run focused
  tests first, then affected suites, race detection and required build/lint checks.
- Report commands, failures and verification gaps. envtest has no Deployment
  controller or garbage collector and does not establish hardware/runtime behavior.

References: [Go Test Comments](https://go.dev/wiki/TestComments),
[Kubernetes CRDs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/),
[Helm CRD lifecycle](https://helm.sh/docs/v3/chart_best_practices/custom_resource_definitions/).

## Architecture invariants

### Snapshot and ownership

Both reconcilers share one `*MutexYanetConfigSpec`. Read and publish deep copies
under its mutex; never keep references to mutable protected data. Config watch
mapping refreshes the snapshot before enqueueing installations. Serialized reads
and publication prevent an older read from replacing a newer stop flag.

An installation owns its Deployments/ConfigMaps. `YanetConfig/config` owns shared
Services. Ownership checks include the controller reference, kind, group and UID;
labels alone do not establish ownership. Cleanup waits for foreground Deployment
deletion before releasing the installation finalizer and node claim.

`stop: true` freezes writes, including deletion and finalizer changes.
`autoSync: false` reports workload drift without applying it.
`enabled: false` with `autoSync: true` scales the installation's Deployments to zero.
Shared Services have a separate lifecycle and remain available for box-type roles.

### Three-tier configuration

1. `YanetConfig.spec.components`: controlplane, dataplane, optional birdAdapter,
   ordered atomic dataplane `sidecars[]`, and standalone `operators[].containers[]`.
2. `spec.patches`: named strategic-merge Deployment fragments, validated by dry-run.
3. `spec.boxTypes`: named presets connecting components and ordered patch lists.

An installation selects an immutable `boxType`. Overrides are limited to
container image name/tag, workload/sidecar enablement, controlplane `disabledNuma`
and dataplane `networks`. Networks omitted/null inherit, a list replaces the
palette and `[]` clears it. General annotations/resources belong in patches.

### Rendering and networking

- Render all node/component plans and validate producer transitions before writes.
- Controlplane NUMA count is explicit (default 1); `{numa}` in arguments uses the
  physical index. Disabled domains do not renumber surviving domains.
- All workloads use private networking. Reject final `hostNetwork: true` and
  nonzero `hostPort`, including values introduced by patches.
- Sidecar index `i` reserves `8080+2*i` / `8081+2*i` before selection/enablement.
  Standalone Pods use 8080/8081; shared Service ports stay 8080/8081.
- Omitted listeners default to grpc; HTTP-only requires `[http]`; `[]` suppresses
  the Service, not the reserved slot.
- Only managed HostPath config enables runtime bind/advertise/named NUMA gateway
  environment after patches. ConfigMap contents stay opaque. Do not validate
  application configuration addresses/ports in the operator.
- Typed networks couple externally managed NADs to matching resource quantities.
  Reject patches conflicting with the managed Multus annotation/reservations.
- Preserve role-migration drain and shared-Service cutover guards.

### Admission and generated files

Use typed `admission.Validator[*Kind]` implementations with dependencies in struct
fields and register them through `WithValidator`. Avoid module-level clients.
Register the single API scheme before starting the manager in envtest.

Do not manually edit these generated artifacts:

- `api/v1alpha1/zz_generated.deepcopy.go`
- `config/crd/bases/*.yaml`
- `config/rbac/role.yaml`
- `config/webhook/manifests.yaml`
- `deploy/charts/yanet-operator/crds/yanet.yaml`

Regenerate with `make generate && make manifests && make helm-crds`. Keep the
handwritten Helm RBAC/webhook templates aligned with generated resources.

## Release

See `README_RELEASES.md` and `release-notes/v3.0.0.md`. Application and chart
versions are independent. `release.yml` owns stable publication; PR chart builds
only produce artifacts. A CRD reset requires a coordinated clean installation,
not an implicit Helm upgrade or automatic adoption of old objects.
