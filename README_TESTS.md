# Testing yanet-operator

## 📊 Current Status

- **Coverage:** ~90% overall (89.2% helpers, 91.3% manifests, 55.7% controller)
- **Unit tests:** 80+ tests, all passing
- **Integration tests:** 10+ Ginkgo scenarios (4 existing + 6 new E2E tests)
- **E2E tests:** 4 test suites (Throttling, Webhook, AutoSync, Status) - all via Docker
- **CI/CD:** GitHub Actions (tests + Docker + Helm)

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
    ├── yanet_reconciler_v2_deletion_test.go # v2: deletion, ConfigMaps (9 tests)
    ├── yanet_reconciler_v2_h9_test.go       # v2: H9 edge cases
    ├── yanet_reconciler_v2_hardening_test.go # v2: hardening scenarios
    ├── yanet_reconciler_test.go             # v1: checkUpdateRequeue
    ├── yanet_conditions_test.go             # computeConditions
    ├── yanet_conditions_v2_test.go          # v2 conditions
    ├── node_deletion_test.go                # handleNodeDeletion
    ├── yanet_controller_integration_test.go # Ginkgo integration tests (v1)
    ├── yanet_throttling_e2e_test.go         # E2E: throttling (v1 + v2)
    ├── yanet_webhook_e2e_test.go            # E2E: webhook validation (v1 + v2)
    ├── yanet_autosync_e2e_test.go           # E2E: autoSync behavior (v1 + v2)
    └── yanet_status_e2e_test.go             # E2E: status reporting (v1 + v2)
```

## 🎯 Coverage Goals

| Package | Current | Target | Status |
|---------|---------|--------|--------|
| manifests | 91.3% | 90%+ | ✅ |
| helpers | 89.2% | 70%+ | ✅ |
| controller | 55.7% | 70%+ | 🔄 In Progress |

### Controller Package Details

| Function | Coverage | Status |
|----------|----------|--------|
| `applyInlineConfigMapsV2` | 84.0% | ✅ |
| `reconcileYanetV2` | 77.1% | ✅ |
| `pruneOrphans` | 73.6% | ✅ |
| `handleYanetV2Deletion` | 61.5% | 🔄 |

## 🚀 Test Suites

### Unit Tests (20 tests)

**yanet_reconciler_v2_extended_test.go (11 tests):**
- Edge cases: empty nodeSelector, config not loaded, boxType not found, global stop
- UpdateWindow throttling: same node, different nodes, expired window
- Orphan cleanup: multiple types, foreign labels, empty desired set, autoSync=false

**yanet_reconciler_v2_deletion_test.go (9 tests):**
- Deletion handling: no finalizer, with finalizer, cleanup errors, foreign resources
- ConfigMap management: no inline, autoSync true/false, updates

### E2E Tests (4 test suites, 15+ scenarios) - All via Docker + envtest

**yanet_throttling_e2e_test.go:**
- V1 API: UpdateWindow throttling with `enabled=false`
- V2 API: UpdateWindow throttling with `enabled=false`
- Verifies: replicas=0 when disabled, config changes propagate, throttling works

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

# For local debugging (requires Go 1.26.2 + envtest):
KUBEBUILDER_ASSETS=$(setup-envtest use 1.35.0 -p path) \
  go test ./internal/controller/... -run TestHandleYanetV2Deletion_WithFinalizer -v
```

## 📚 References

- [Kubebuilder Testing](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html)
- [Ginkgo/Gomega](https://onsi.github.io/ginkgo/)
- [Controller-runtime Testing](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
- [AGENTS.md](AGENTS.md) - Development guidelines (MUST READ)
