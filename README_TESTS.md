# Testing yanet-operator

## 📊 Current Status

- **Coverage:** ~90% overall (89.2% helpers, 92.1% manifests, ~36% controller)
- **Unit tests:** 60+ tests, all passing
- **Integration tests:** 4 Ginkgo scenarios
- **CI/CD:** GitHub Actions (tests + Docker + Helm)

## 🧪 Running Tests

### Quick Start (Docker-based, recommended)
```bash
make test-unit              # Unit tests only
make test-docker            # All tests in Docker
make test-docker-race       # With race detector
```

### Local (requires Go 1.26.2+)
```bash
make test                   # All tests with coverage
make test-race              # With race detector
make lint                   # Code quality checks
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
    ├── yanet_reconciler_v2_test.go          # v2: reconcileYanetV2
    ├── yanet_reconciler_v2_h9_test.go       # v2: H9 edge cases
    ├── yanet_reconciler_v2_hardening_test.go # v2: hardening scenarios
    ├── yanet_reconciler_test.go             # v1: checkUpdateRequeue
    ├── yanet_conditions_test.go             # computeConditions
    ├── yanet_conditions_v2_test.go          # v2 conditions
    ├── node_deletion_test.go                # handleNodeDeletion
    └── yanet_controller_integration_test.go # Ginkgo integration tests
```

## 🎯 Coverage Goals

| Package | Current | Target | Status |
|---------|---------|--------|--------|
| manifests | 92.1% | 90%+ | ✅ |
| helpers | 89.2% | 70%+ | ✅ |
| controller | ~36% (v1 tests, v2 tests growing) | 70%+ | 🔄 |

## 🚀 Next Steps

### Priority 1: Controller Coverage
- Add more unit tests for reconciler logic
- Increase controller package coverage to 70%+

### Priority 2: E2E Testing
- Add end-to-end tests with kind cluster
- Test webhook validation end-to-end

## 📚 References

- [Kubebuilder Testing](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html)
- [Ginkgo/Gomega](https://onsi.github.io/ginkgo/)
- [Controller-runtime Testing](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
