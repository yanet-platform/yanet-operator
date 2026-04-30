# Testing yanet-operator

## 📊 Current Status

- **Coverage:** 93.2% overall (94.0% helpers, 92.4% manifests, 36.3% controller)
- **Unit tests:** 46 tests, all passing
- **Integration tests:** 4 scenarios
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
│   ├── helpers_test.go         # 7 tests (NEW: GetLabeledNodes, DeploymentDiff edge cases, GetNodes)
│   └── http_getters_test.go    # 5 tests (HTTP client)
├── manifests/
│   ├── builder.go              # NEW: DeploymentBuilder (280 lines)
│   ├── dataplane_test.go       # 3 tests
│   ├── controlplane_test.go    # 4 tests
│   ├── announcer_test.go       # 3 tests
│   ├── bird_test.go            # 4 tests
│   └── helpers_test.go         # 6 tests
└── controller/
    ├── yanet_reconciler_test.go         # NEW: 2 tests (checkUpdateRequeue)
    ├── yanet_conditions_test.go         # NEW: 7 tests (computeConditions)
    ├── node_deletion_test.go            # NEW: 4 tests (handleNodeDeletion)
    └── yanet_controller_integration_test.go  # 4 scenarios
```

## 🎯 Coverage Goals

| Package | Current | Target | Status |
|---------|---------|--------|--------|
| manifests | 92.4% | 90%+ | ✅ |
| helpers | 94.0% | 70%+ | ✅ |
| controller | 36.3% (unit tests added) | 70%+ | 🔄 |

## 🚀 Next Steps

### Priority 1: Validation Webhooks
- Create webhook for Yanet CRD
- Create webhook for YanetConfig CRD
- Add webhook tests

## 📚 References

- [Kubebuilder Testing](https://book.kubebuilder.io/cronjob-tutorial/writing-tests.html)
- [Ginkgo/Gomega](https://onsi.github.io/ginkgo/)
- [Controller-runtime Testing](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
