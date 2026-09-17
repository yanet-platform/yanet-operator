# Testing yanet-operator

## Test Strategy

- Check public behavior, persisted state, and required absence of side effects.
- Use envtest where correctness depends on admission, API defaulting, or storage semantics.
- Keep error fixtures valid apart from the condition under test; assert the intended error.
- For regressions, observe the expected failure on the original code, then success after the fix.
  For tests of existing correct behavior, temporarily introduce a relevant fault and verify failure.
- Control time and dependencies rather than adding sleeps, permanent skips, or external DNS requests.
- CI runs unit/integration tests, lint, Docker builds, and Helm installation tests.

## 🧪 Running Tests

### ⚠️ IMPORTANT: Always use `make`

**All tests MUST be run through `make` + Docker** to avoid dependency on local environment.
This guarantees:
- Correct Go version (1.26.2 from `go.mod`)
- All dependencies installed
- Consistent environment across all machines
- CI/CD compatibility without changes

### Docker-based (REQUIRED)
```bash
make test-docker-unit          # Unit tests only (helpers + manifests)
make test-docker-integration   # Integration + E2E tests (controller package)
make test-docker-race          # All tests with race detector
make test-docker               # All tests with coverage
make lint                      # Linter (also via Docker)
```

### ❌ DO NOT run directly
```bash
# ❌ BAD - depends on local Go installation
go test ./...
go build ./...

# ✅ GOOD - through make + Docker
make test-docker-unit
make docker-build
```

## 📁 Test Structure

```
internal/
├── helpers/
│   ├── helpers_test.go         # GetLabeledNodes, DeploymentDiff edge cases, GetNodes
│   ├── http_getters_test.go    # HTTP client tests
│   └── resolve_v2_test.go      # v2: FindBoxType, ResolveBoxComponent, EnabledComponentsForBox
├── manifests/
│   ├── builder_v2_test.go      # v2: BuildDeployments, NUMA fan-out, patches
│   ├── service_v2_test.go      # v2: BuildServices, ServicePlan
│   ├── patcher_test.go         # ApplyPatches, PatchRegistry
│   ├── dataplane_test.go       # v1: DeploymentForDataplane
│   ├── controlplane_test.go    # v1: DeploymentForControlplane
│   ├── announcer_test.go       # v1: DeploymentForAnnouncer
│   ├── bird_test.go            # v1: DeploymentForBird
│   └── helpers_test.go         # Labels, Tolerations, Volumes
└── controller/
    ├── yanet_reconciler_v2_test.go          # v2: reconcileYanetV2 (basic)
    ├── yanet_reconciler_v2_extended_test.go # v2: edge cases, throttling, orphans (11 tests)
    ├── yanet_reconciler_v2_deletion_test.go # v2: deletion, ConfigMaps
    ├── yanet_reconciler_v2_h9_test.go       # v2: H9 edge cases
    ├── yanet_reconciler_v2_hardening_test.go # v2: hardening scenarios
    ├── yanet_reconciler_test.go             # v1: checkUpdateRequeue
    ├── yanet_conditions_test.go             # computeConditions
    ├── yanet_conditions_v2_test.go          # v2 conditions
    ├── node_deletion_test.go                # handleNodeDeletion
    ├── yanet_controller_integration_test.go # Ginkgo integration tests (v1)
    ├── cleanup_regression_test.go          # Public Reconcile: cleanup retries/ownership, throttle convergence
    ├── defaulting_v2_envtest_test.go        # Real API defaults, no-op writes, autosync, patch removal
    ├── operator_examples_v2_test.go        # Admission/rendered runtime contract, overrides, read-only scopes
    ├── yanet_webhook_e2e_test.go            # E2E: webhook validation (v1 + v2)
    ├── yanet_autosync_e2e_test.go           # E2E: autoSync behavior (v1 + v2)
    ├── yanet_status_e2e_test.go             # E2E: status reporting (v1 + v2)
    ├── e2e_helpers_test.go                  # Helper functions for E2E tests
    └── suite_test.go                        # Test suite setup
```

### E2E Test Helpers

**e2e_helpers_test.go** provides utility functions for reliable E2E testing:

```go
// countDeployments - returns count and any API error, for polling assertions
func countDeployments(ctx context.Context, ns string) (int, error)

// ensureNamespace - creates namespace if it doesn't exist
func ensureNamespace(ctx context.Context, ns string)

// cleanupDeployments, cleanupServices, cleanupYanetV1/V2 - cleanup helpers
```

**Usage example** (failed API reads must not count as zero Deployments):
```go
Eventually(func() (int, error) {
    return countDeployments(ctx, namespace)
}, 5*time.Second).Should(Equal(0))
```

## 🎯 Coverage Goals

| Package | Current | Target | Status |
|---------|---------|--------|--------|
| manifests | 90.9% | 90%+ | ✅ |
| helpers | 86.4% | 70%+ | ✅ |
| controller | 85.4% | 70%+ | ✅ |

Measured with Go 1.26.2 / Kubernetes 1.35.0 envtest on 2026-09-17. Use coverage
to locate untested paths, not as proof that assertions catch regressions.

## 🚀 Test Suites

### Controller Unit Tests

**yanet_reconciler_v2_extended_test.go (11 tests):**
- Edge cases: empty nodeSelector, config not loaded, boxType not found, global stop
- UpdateWindow throttling: same node, different nodes, expired window
- Orphan cleanup: multiple types, foreign labels, empty desired set, autoSync=false

**yanet_reconciler_v2_deletion_test.go:**
- Deletion handling: no finalizer, with finalizer, foreign resources
- ConfigMap management: no inline, autoSync true/false, updates

**cleanup_regression_test.go:**
- Public Reconcile retains finalizers while owned Deployments remain, even with missing/changed labels.
- Cleanup errors propagate; a successful retry finishes deletion without touching another owner's resources.
- A throttled image update leaves the old workload intact and converges after the window expires, without sleeps.
- The former pending throttling suite and its duplicated fixtures were removed.

### API-server Tests — Docker + envtest

**defaulting_v2_envtest_test.go:**
- Public reconcilers use an independent API server: fake clients cannot establish server defaulting.
- Observe both persisted update requests and resource versions; an unnecessary PUT can leave the version unchanged.
- Check no-op/throttle/autosync behavior, real image/field changes, patch removal, and dry-run failures.

The manager-backed Ginkgo suites below wait for manager shutdown before stopping
envtest. Deployment-count assertions propagate API list errors instead of
treating a failed observation as an empty namespace.

**yanet_webhook_e2e_test.go:**
- V1 API: YanetConfig/Yanet validation (negative updateWindow, empty nodeName, invalid type)
- V2 API: YanetConfigV2/YanetV2 validation (duplicate patches, port overlap, unknown boxType)
- V2 API: Immutability (boxType cannot be changed), patch reference validation
- Verifies: admission webhooks reject invalid CRs, cross-references validated

**yanet_autosync_e2e_test.go:**
- V1 API: autoSync=false/true behavior, toggling autoSync
- V2 API: autoSync=false/true behavior, toggling autoSync, manual edits preserved, patches applied
- Verifies: read-only mode works, manual interventions preserved, state transitions

**yanet_status_e2e_test.go:**
- V1 API: Status.Sync populated with Synced/Disabled deployments
- V2 API: Status.Sync, Status.NodesStatus, Status.Services tracking
- V2 API: Multi-node tracking, status updates on autoSync toggle
- Verifies: status fields populated correctly, per-node tracking, sync state accuracy

## 🧪 Running Specific Tests

**⚠️ All tests MUST run via Docker + make:**

```bash
# Unit tests only (helpers + manifests)
make test-docker-unit

# Integration + E2E tests (controller package, includes all e2e)
make test-docker-integration

# All tests with coverage
make test-docker

# With race detector
make test-docker-race

# Coverage report (after make test-docker)
go tool cover -html=cover.out
```

**E2E tests are part of integration suite** and run automatically via:
- `make test-docker-integration` (controller package tests)
- `make test-docker` (all tests)
- GitHub Actions workflow (`.github/workflows/test.yml`)

## 🐛 Debugging Tests

**⚠️ Use Docker for consistency:**

```bash
# Verbose output for integration tests
make test-docker-integration

# With race detector
make test-docker-race

# Check coverage for specific file (after make test-docker)
go tool cover -func=cover.out | grep yanet_reconciler_v2.go

# Focused helper test through the same Docker toolchain:
make --eval 'check-http: ; $(call docker-go,go test -count=1 ./internal/helpers -run TestHttpGet)' check-http
```

## 📚 References

- [Kubebuilder Testing](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html)
- [Ginkgo/Gomega](https://onsi.github.io/ginkgo/)
- [Controller-runtime Testing](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
- [Fake-client limitations](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client/fake)
- [Test behavior, not implementation](https://testing.googleblog.com/2013/08/testing-on-toilet-test-behavior-not.html)
- [Idempotent reconciliation](https://book.kubebuilder.io/reference/good-practices)
- [AGENTS.md](AGENTS.md) - Development guidelines (MUST READ)
