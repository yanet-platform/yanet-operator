# FOR AI: yanet-operator Development Guide

This document provides context and guidelines for AI assistants working on this project.

## 🎯 Project Overview

**yanet-operator** is a Kubernetes operator that manages YANET (Yet Another Network) deployments on worker nodes.

- **Language:** Go 1.26.2
- **Framework:** controller-runtime (Kubernetes operator framework)
- **Repository:** https://github.com/yanet-platform/yanet-operator
- **CRDs:** Yanet (per-node), YanetConfig (global)
- **Test Coverage:** 87.6% (manifests: 97.4%, helpers: 65.7%)
- **Status:** Production-ready with comprehensive features

## 📋 Critical Rules

### 1. Code Comments
- ✅ **ALL comments MUST be in English only**
- ✅ Use generic examples in tests: `test-node`, `docker.io/test`, etc.
- ❌ NO references to internal hostnames or registries in tests
- ✅ **Use structured logging** — key-value pairs, not fmt.Sprintf

### 2. Testing Requirements
- ✅ **ALL new code MUST have tests**
- ✅ **Target coverage: 70%+** (current: 87.6%)
- ✅ **Run tests through Docker** (no local dependencies)
- ✅ **Use race detector** for concurrency testing

### 3. Test Workflow
When adding new functionality:
1. Write tests FIRST (TDD approach)
2. Add test to appropriate `*_test.go` file
3. Update `Makefile` if needed (new test targets)
4. Ensure GitHub Actions workflow covers it
5. Run `make test-docker-unit` to verify
6. Check coverage: `go tool cover -func=cover.out`

### 4. Concurrency Rules
- ✅ **Use `DeepCopy()` for shared state**
- ✅ **Always hold mutex when accessing shared data**
- ❌ **NEVER save references to data protected by mutex**
- ✅ **Run `make test-docker-race` to detect data races**

## 🏗️ Project Structure

```
yanet-operator/
├── api/v1alpha1/              # CRD definitions
│   ├── yanet_types.go         # Yanet CRD (per-node)
│   ├── yanetconfig_types.go   # YanetConfig CRD (global)
│   └── zz_generated.deepcopy.go  # Generated (DO NOT EDIT)
├── cmd/
│   └── main.go                # Entry point
├── internal/
│   ├── controller/            # Reconcilers
│   │   ├── yanet_controller.go
│   │   ├── yanet_reconciler.go
│   │   ├── yanetconfig_controller.go
│   │   ├── node_reconciler.go
│   │   ├── suite_test.go      # Test setup
│   │   └── *_test.go          # Integration tests
│   ├── helpers/               # Utilities
│   │   ├── helpers.go
│   │   ├── http_getters.go
│   │   └── *_test.go          # Unit tests
│   ├── manifests/             # Deployment generators
│   │   ├── dataplane.go
│   │   ├── controlplane.go
│   │   ├── announcer.go
│   │   ├── bird.go
│   │   ├── helpers.go
│   │   └── *_test.go          # Unit tests (97.4% coverage!)
│   └── names/                 # Constants
│       └── const.go
├── .github/workflows/         # CI/CD
│   └── test.yml               # GitHub Actions
├── Makefile                   # Build and test targets
├── .golangci.yml              # Linter config
└── README_TESTS.md            # Testing documentation
```

## 🧪 Testing Guidelines

### Test File Naming
- Unit tests: `<package>_test.go` in same directory
- Integration tests: `<controller>_integration_test.go`
- Test package: `package <name>` (same as source) or `package <name>_test` (black-box)

### Test Structure

**Unit test (table-driven):**
```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {
            name:     "description",
            input:    "input",
            expected: "expected",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := MyFunction(tt.input)
            if result != tt.expected {
                t.Errorf("got %v, want %v", result, tt.expected)
            }
        })
    }
}
```

**Integration test (Ginkgo/Gomega):**
```go
var _ = Describe("MyController", func() {
    Context("When doing something", func() {
        It("Should work correctly", func() {
            obj := &MyObject{}
            Expect(k8sClient.Create(ctx, obj)).Should(Succeed())
        })
    })
})
```

### Running Tests

**Docker-based (recommended):**
```bash
make test-docker-unit      # Unit tests only
make test-docker-race      # With race detector
```

**Local (requires Go 1.26.2 and envtest):**
```bash
make test-unit             # Unit tests
make test-integration      # Integration tests
make test                  # All tests
make test-race             # With race detector
```

### Adding New Tests

1. Create `*_test.go` file in same directory as source
2. Write table-driven tests
3. Run locally: `make test-docker-unit`
4. Check coverage: `go tool cover -func=cover.out`
5. Ensure coverage doesn't decrease

## 🔧 Makefile Targets

### Testing
- `make test` — all tests with coverage
- `make test-race` — with race detector
- `make test-unit` — unit tests only
- `make test-integration` — integration tests only
- `make test-docker-unit` — unit tests in Docker
- `make test-docker-race` — race detector in Docker

### Development
- `make fmt` — format code
- `make vet` — run go vet
- `make lint` — run golangci-lint
- `make build` — build binary
- `make generate` — generate DeepCopy methods
- `make manifests` — generate CRDs

## 🤖 GitHub Actions

### Workflow: `.github/workflows/test.yml`

**Triggers:**
- Push to: `main`, `master`, `develop`
- Pull requests to these branches

**Jobs:**
1. **test** — unit + integration tests with race detector
2. **lint** — golangci-lint
3. **build** — build operator binary

**Important:**
- Go version from `go.mod` (via `go-version-file`)
- Synchronized with `Dockerfile`
- Codecov integration for coverage tracking

### Adding New Workflow Steps

When adding new test targets to Makefile:
1. Add corresponding step in `.github/workflows/test.yml`
2. Ensure it uses Docker or has all dependencies
3. Test locally first

## 🐛 Common Issues

### Data Races
**Problem:** Shared state accessed without mutex
**Solution:** 
```go
// ❌ BAD
r.GlobalConfig.Config = config.Spec

// ✅ GOOD
r.GlobalConfig.Lock.Lock()
r.GlobalConfig.Config = *config.Spec.DeepCopy()
r.GlobalConfig.Lock.Unlock()
```

### Test Failures in Docker
**Problem:** Integration tests fail with "etcd not found"
**Solution:** Integration tests require envtest, use `make test-integration` locally

### Coverage Decrease
**Problem:** New code without tests
**Solution:** Write tests before committing, run `make test-docker-unit`

## 📝 Code Review Checklist

Before submitting PR:
- [ ] All comments in English
- [ ] Tests added for new code
- [ ] `make test-docker-unit` passes
- [ ] `make test-docker-race` passes (no data races)
- [ ] `make lint` passes
- [ ] `make fmt` applied
- [ ] Coverage >= 70% (check with `go tool cover`)
- [ ] No references to internal hostnames/registries in tests
- [ ] GitHub Actions workflow updated if needed

## 🎯 Architecture Patterns

### Shared State
```go
type MutexYanetConfigSpec struct {
    Config YanetConfigSpec
    Lock   sync.Mutex
}

// Always use DeepCopy when reading/writing
r.GlobalConfig.Lock.Lock()
config := *r.GlobalConfig.Config.DeepCopy()
r.GlobalConfig.Lock.Unlock()
```

### Deployment Generation
- Factory functions: `DeploymentForDataplane()`, `DeploymentForControlplane()`, etc.
- Helpers in `internal/manifests/helpers.go`
- All functions have 97.4% test coverage

### Controller Pattern
- `YanetReconciler` — manages Yanet resources and Nodes
- `YanetConfigReconciler` — manages global configuration
- Both share `GlobalConfig` via pointer

## 🔍 Known Limitations

### Acceptable (by design)
- ✅ UpdateWindow state in memory (not persistent)
- ✅ AutoDiscovery without retry (not priority)
- ✅ No caching for AutoDiscovery (not priority)

### To Be Implemented (future)
- [ ] Validation webhooks
- [ ] Finalizers for graceful cleanup
- [ ] Metrics for monitoring
- [ ] Events for auditing
- [ ] Status conditions

## 📚 Resources

- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [Kubebuilder Book](https://book.kubebuilder.io/)
- [Ginkgo Testing Framework](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [golangci-lint](https://golangci-lint.run/)

## 🚨 Critical Files (DO NOT EDIT)

- `api/v1alpha1/zz_generated.deepcopy.go` — auto-generated
- `config/crd/bases/*.yaml` — generated by controller-gen
- Run `make generate` and `make manifests` to regenerate

## 💡 Tips for AI Assistants

1. **Always check existing tests** before writing new ones
2. **Follow table-driven test pattern** for consistency
3. **Use Docker for testing** to avoid environment issues
4. **Check coverage** after adding tests
5. **Update documentation** when adding new features
6. **Keep comments in English** — this is non-negotiable
7. **Test with race detector** — data races are critical bugs
8. **Reference line numbers** when discussing code issues

## 🎓 Learning from This Project

### Good Practices Implemented
- ✅ Comprehensive test suite (87.6% coverage)
- ✅ Docker-based testing (reproducible)
- ✅ GitHub Actions CI/CD
- ✅ Table-driven tests
- ✅ Race detector in CI
- ✅ Clear separation of concerns

### Lessons Learned
- Data races are subtle — always use DeepCopy for shared state
- Docker-based tests eliminate "works on my machine" issues
- High test coverage (97.4% for manifests) catches bugs early
- Integration tests need envtest setup
- Comments in English improve collaboration

---

**Last Updated:** 2026-04-29
**Test Coverage:** 87.6%
**Status:** Production-ready with comprehensive test suite
