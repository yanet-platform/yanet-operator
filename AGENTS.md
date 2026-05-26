# AGENTS.md — yanet-operator Development Guide

This document provides context and guidelines for AI assistants working on this project.
It is the canonical entry point for any AI agent (Claude / Cursor / Aider / Roo / etc.):
the conventional filename `AGENTS.md` is recognized automatically. Read it BEFORE
running any build/test command.

## 🚨 ALWAYS use `make` + Docker. Never run `go build` / `go test` directly.

This rule is non-negotiable and applies to humans and AI agents alike:

- ✅ **Build:** `make docker-build` (produces `controller:latest`)
- ✅ **Unit tests:** `make test-docker-unit`
- ✅ **Race detector:** `make test-docker-race`
- ❌ **Do NOT run** `go build ./...`, `go test ./...`, `go vet`, etc. on the host —
  the host toolchain may not match `go.mod` / `Dockerfile`, and dependencies
  like envtest are not installed locally.

If you are an AI agent and your build/test request is denied with feedback like
`make docker-build`, that is the human pointing you back to this rule —
re-read this section, then use the Make target.

## 🎯 Project Overview

**yanet-operator** is a Kubernetes operator that manages YANET (Yet Another Network) deployments on worker nodes.

- **Language:** Go 1.26.2
- **Framework:** controller-runtime (Kubernetes operator framework)
- **Repository:** https://github.com/yanet-platform/yanet-operator
- **CRDs:**
  - `Yanet` (per-installation), `YanetConfig` (global)
  - Two **independent** CRD families in the same API group:
    - `yanets.yanet.yanet-platform.io` / `yanetconfigs.yanet.yanet-platform.io`
      — legacy v1alpha1 (kinds `Yanet` / `YanetConfig`).
    - `yanetsv2.yanet.yanet-platform.io` / `yanetconfigsv2.yanet.yanet-platform.io`
      — v2alpha1 (kinds `YanetV2` / `YanetConfigV2`).
    Each CRD is single-version; there is **no** API conversion, **no** storage
    version dispatch, and **no** Reconcile-time router. Two separate
    controllers (`YanetReconciler` and `YanetV2Reconciler`) handle the two
    kinds wholly independently (see "v1/v2 split" below).
- **Status:** Production-ready, v2alpha1 in active rollout with backward compatibility.

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
├── api/
│   ├── v1alpha1/                  # Legacy API (storage version)
│   │   ├── yanet_types.go
│   │   ├── yanetconfig_types.go
│   │   └── zz_generated.deepcopy.go  # Generated (DO NOT EDIT)
│   └── v2alpha1/                  # New API: components/patches/boxTypes model
│       ├── yanet_types.go         # Minimal Yanet CR (boxType + nodeSelector + overrides)
│       ├── yanetconfig_types.go   # Components palette, NamedPatch[], BoxType[]
│       ├── yanet_webhook.go       # admission.Validator[*Yanet] (immutable boxType, refs)
│       ├── yanetconfig_webhook.go # Validator (uniqueness, refs, strategic-merge dry-run)
│       ├── config_source.go       # Inline | HostPath | URL
│       └── zz_generated.deepcopy.go
├── cmd/main.go
├── internal/
│   ├── controller/                # Reconcilers
│   │   ├── yanet_controller.go    # Dispatches v2 → v1 by spec.boxType
│   │   ├── yanet_reconciler.go    # v1 path
│   │   ├── yanet_reconciler_v2.go # v2 path: resolve → build → patch → apply
│   │   ├── yanetconfig_controller.go    # v1 in-memory snapshot
│   │   ├── yanetconfig_controller_v2.go # v2 in-memory snapshot (mirrors v1)
│   │   ├── node_reconciler.go
│   │   ├── suite_test.go          # envtest + Ginkgo
│   │   └── *_test.go
│   ├── helpers/
│   │   ├── helpers.go, ptr.go, http_getters.go
│   │   ├── resolve_v2.go          # ResolveBoxComponent, EnabledComponentsForBox
│   │   └── *_test.go
│   ├── manifests/
│   │   ├── dataplane.go, controlplane.go, announcer.go, bird.go (v1)
│   │   ├── builder_v2.go          # v2 skeleton: NUMA fan-out, hugepages, ConfigSource
│   │   ├── patcher.go             # ApplyPatches via strategic merge
│   │   ├── service_v2.go          # 3 CP categories + operator Local
│   │   └── *_test.go
│   ├── events/recorder.go         # SA1019 wrapper for EventRecorder
│   └── names/const.go
├── deploy/charts/yanet-operator/  # Helm chart (NFD optional dep, yanetconfig-v2 template)
├── deploy/examples/v2alpha1-*.yaml
├── .github/workflows/test.yml
├── Makefile
└── .golangci.yml
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

// ✅ GOOD — DeepCopy under the lock
r.GlobalConfig.Lock.Lock()
r.GlobalConfig.Config = *config.Spec.DeepCopy()
r.GlobalConfig.Lock.Unlock()
```

### v1/v2 split (no dispatcher, no conversion)
Earlier versions kept v1 and v2 as two **versions** of the same CRD
(`yanets.yanet-platform.io`) with `v1alpha1` as the storage version. That
forced an in-process Reconcile dispatcher (gating the v2 branch on
`spec.boxType != ""`) and silently pruned v2-only fields whenever the API
server converted a v2 object down to the v1 storage schema — breaking the
admission webhook (it could no longer find `boxTypes` in any
`YanetConfig`).

The current model splits the two API surfaces into **separate CRDs**:
- v1: `yanets` + `yanetconfigs` (kinds `Yanet`/`YanetConfig`,
  `api/v1alpha1` Go package).
- v2: `yanetsv2` + `yanetconfigsv2` (kinds `YanetV2`/`YanetConfigV2`,
  `api/v2alpha1` Go package).

Each CRD has exactly one served+storage version, so the API server never
converts between them and never prunes fields. There is no Reconcile
dispatcher:
- `YanetReconciler` (`yanet_controller.go`) only watches `v1alpha1.Yanet`
  and handles Node events for AutoDiscovery.
- `YanetV2Reconciler` (`yanetv2_controller.go`) only watches
  `v2alpha1.YanetV2` plus Nodes/Pods filtered by the v2 ownership label.

Webhook paths and names are likewise disjoint:
- `vyanet.kb.io` / `vyanetconfig.kb.io` → `/validate-...-v1alpha1-yanet[config]`
- `vyanetv2.kb.io` / `vyanetconfigv2.kb.io` → `/validate-...-v2alpha1-yanet[config]v2`

**Migration note.** If a cluster already has v2 CRs created under the old
`yanets.yanet-platform.io/v2alpha1` versioned-CRD model, those objects do
not show up under the new `yanetsv2` CRD automatically — they belong to
the v1 CRD now. Either delete them before upgrading or re-create them
against the new CRD. v1 CRs are completely unaffected.

### envtest with multiple API versions
**Problem:** Ginkgo suites time out on "failed to wait for cache to be synced
for Kind *v2alpha1.Yanet" when only one API version is registered.
**Solution:** in [`suite_test.go`](internal/controller/suite_test.go) register
**both** versions before starting the manager:
```go
Expect(yanetv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
Expect(yanetv2alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
```
Wire `GlobalConfig` into `YanetReconciler` and `GlobalConfigV2` into
`YanetV2Reconciler` separately.

### Test failures in Docker
**Problem:** Integration tests fail with "etcd not found"
**Solution:** Integration tests require envtest, use `make test-integration` locally

### Coverage decrease
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

### Shared state (in-memory config snapshot)
```go
type MutexYanetConfigSpec struct {
    Config YanetConfigSpec
    Lock   sync.Mutex
}

// Always DeepCopy under the lock when reading or writing.
r.GlobalConfig.Lock.Lock()
config := *r.GlobalConfig.Config.DeepCopy()
r.GlobalConfig.Lock.Unlock()
```
The reconciler does **not** API-list YanetConfig on every cycle. A separate
`YanetConfigReconciler` (one per API version) owns the snapshot and the main
reconciler reads from it. Same pattern for v1 and v2.

### v1alpha1 — Deployment generation
- Factory functions: `DeploymentForDataplane`, `DeploymentForControlplane`, etc.
- Helpers in [`internal/manifests/helpers.go`](internal/manifests/helpers.go).

### v2alpha1 — three-tier model
1. **`YanetConfig.spec.components`** — palette of available components:
   five hardcoded slots (`controlplane`, `dataplane`, `bird`, `birdAdapter`,
   `announcer`) plus a dynamic `operators[]` array.
2. **`YanetConfig.spec.patches []NamedPatch`** — strategic-merge fragments of
   `appsv1.Deployment` stored as `runtime.RawExtension` (validated via dry-run
   `strategicpatch.StrategicMergePatch(skeleton, patch, appsv1.Deployment{})`
   in the webhook).
3. **`YanetConfig.spec.boxTypes []BoxType`** — named presets wiring components
   to ordered patch lists.

`Yanet` CRs reference a `boxType` by name; per-installation overrides are
restricted to per-container `image.{name,tag}` (under `containers.<name>`)
and `enabled` flags. The container key must match the rendered container
name — the component kind for hardcoded components, the declared
`OperatorContainer.name` for operators. No inline patches in `Yanet`.

Reconcile flow:
```
snapshot YanetConfig → resolve box components → build skeleton Deployments
→ ApplyPatches(deployment, patchNames, registry) → CreateOrUpdate
→ generate Services from components.<name>.port → status
```

### Controlplane NUMA fan-out
Controlplane gets one Deployment per NUMA domain on the node. NUMA count is
read from the NFD label `feature.node.kubernetes.io/cpu-numa_nodes_count`
(falls back to 1 when absent). Each instance listens on `port + numa_index`;
three Service categories are generated:
- `<yanet>-<nodehash>-numa{N}` — per-node Local (`internalTrafficPolicy=Local`).
- `<yanet>-controlplane-numa{N}-cluster` — cluster-wide round-robin per NUMA.
- `<yanet>-controlplane-all` — cluster-wide round-robin across all instances.

### Operator Services
When `OperatorSpec.Port > 0`, **one** cluster-wide `ClusterIP` Service is
generated, named after the operator, with `internalTrafficPolicy=Local` so
in-node callers reach the local pod.

### Webhook pattern (controller-runtime ≥ 0.23)
Use the generic typed validator:
```go
type MyValidator struct{ Client client.Client }

var _ admission.Validator[*MyKind] = &MyValidator{}

func SetupMyWebhook(mgr ctrl.Manager) error {
    return ctrl.NewWebhookManagedBy(mgr, &MyKind{}).
        WithValidator(&MyValidator{Client: mgr.GetClient()}).
        Complete()
}
```
Avoid module-level `webhookClient` globals — pass dependencies through the
validator struct.

### Controller pattern
- `YanetReconciler` — manages `Yanet` (both versions) and `Node` events.
- `YanetConfigReconciler` (v1) and `YanetConfigReconcilerV2` — keep the
  in-memory snapshots fresh.
- All reconcilers share `*MutexYanetConfigSpec` via pointer.

## 🔍 Known Limitations

### Acceptable (by design)
- ✅ `updateWindow` state in memory (not persistent across operator restarts)
- ✅ AutoDiscovery without retry / without caching (not priority)

### To be implemented (v2 deferred)
- [ ] Finalizers for graceful cleanup on Yanet delete
- [ ] `updateWindow` global throttling on the v2 path
- [ ] Formal `metav1.Condition` entries in `Yanet.Status` (currently only `Sync` buckets)
- [ ] Init-container generation for `ConfigSource.URL` (today: emptyDir + patch)
- [ ] JSON6902 (`jsonPatch`) — out of scope, only strategic merge is supported

### Done in v2 (was open in v1 era)
- ✅ Validation webhooks (`vyanet-v2.kb.io`, `vyanetconfig-v2.kb.io`)
- ✅ Watches: per-version `Yanet`, `Node` (with mapper), `Pod`
- ✅ Per-component `Service` generation (per-node Local + cluster-wide RR)

## 📚 Resources

- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [Kubebuilder Book](https://book.kubebuilder.io/)
- [Ginkgo Testing Framework](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [golangci-lint](https://golangci-lint.run/)

## 🚨 Critical Files (DO NOT EDIT)

- `api/v1alpha1/zz_generated.deepcopy.go`, `api/v2alpha1/zz_generated.deepcopy.go` — auto-generated
- `config/crd/bases/*.yaml` — generated by `controller-gen`
- `config/webhook/manifests.yaml` — generated from kubebuilder markers
- `deploy/charts/yanet-operator/crds/yanet.yaml` — built from `config/crd` via kustomize
- Regenerate with `make generate && make manifests && make helm-crds`

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

**Status:** Production-ready, v2alpha1 GA-track with backward compatibility for v1alpha1.
