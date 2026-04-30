# yanet-operator — Architecture

This document describes the **operator's** internal architecture: how
[`api/v1alpha1`](api/v1alpha1) and [`api/v2alpha1`](api/v2alpha1) coexist, how
controllers dispatch between them, how manifests are generated, and how the
operator interacts with Kubernetes.

For the **target deployment topology** of yanet2 itself (modules, operators,
agents, NUMA, BIRD), see [`YANET2_ARCH.md`](YANET2_ARCH.md). For day-to-day
contributor rules (testing, linting, common pitfalls), see
[`AGENTS.md`](AGENTS.md).

---

## 1. Two API Surfaces in Parallel (four separate CRDs)

v1 and v2 are exposed as **four independent CRDs** sharing the API group
`yanet.yanet-platform.io`. Each CRD has exactly one served+storage version,
so the API server never converts between them and never prunes fields.

| Surface    | CRD                                  | Kind            | Role                                                      |
|------------|--------------------------------------|-----------------|-----------------------------------------------------------|
| `v1alpha1` | `yanets.yanet-platform.io`           | `Yanet`         | Legacy single-node CR (backward-compatible, untouched).   |
| `v1alpha1` | `yanetconfigs.yanet-platform.io`     | `YanetConfig`   | Legacy global config + AutoDiscovery.                     |
| `v2alpha1` | `yanetsv2.yanet-platform.io`        | `YanetV2`       | Component-based CR (boxType + nodeSelector + overrides).  |
| `v2alpha1` | `yanetconfigsv2.yanet-platform.io`   | `YanetConfigV2` | Component palette + named patches + `boxTypes` registry.  |

The operator does not migrate v1 CRs to v2; the two surfaces are handled by
two independent controllers (see below) and reconcile fully independently.

### Why not a single CRD with two versions

An earlier iteration kept v1 and v2 as two **versions** of the same CRD
(`yanets.yanet-platform.io`) with `v1alpha1` as the storage version. Two
fatal problems followed:

1. The API server silently pruned v2-only fields whenever it converted a v2
   object down to the v1 storage schema. The v2 admission webhook then could
   not see `boxTypes` in any stored `YanetConfig` and rejected valid CRs.
2. The reconciler needed an in-process dispatcher gating the v2 branch on
   `spec.boxType != ""` (because `client.Get(*v2alpha1.Yanet)` on a v1 CR
   would succeed via conversion with all v2-only fields zero).

The current four-CRD model (above) eliminates both failure modes. Two
independent controllers handle the two surfaces:

- [`YanetReconciler`](internal/controller/yanet_controller.go) — only watches
  `v1alpha1.Yanet` plus Nodes for AutoDiscovery.
- [`YanetV2Reconciler`](internal/controller/yanetv2_controller.go) — only
  watches `v2alpha1.YanetV2` plus Nodes/Pods filtered by the v2 ownership
  label.

There is no shared Reconcile entry point, no `spec.boxType` probe, no
storage-version conversion. Webhook paths (`/validate-...-v1alpha1-yanet`,
`/validate-...-v2alpha1-yanetv2`, etc.) and names (`vyanet.kb.io`,
`vyanetv2.kb.io`, etc.) are disjoint as well.

> **Migration note.** If a cluster already has v2 CRs created under the
> previous `yanets.yanet-platform.io/v2alpha1` versioned-CRD model, those
> objects belong to the v1 CRD now and are invisible under the new
> `yanetsv2` CRD. Either delete them before upgrading or re-create them as
> `kind: YanetV2`. v1 CRs are completely unaffected.

---

## 2. v1alpha1 — flat, per-installation

### CR shape

`Yanet` (v1alpha1) describes one installation pinned to a single node:

```yaml
spec:
  nodeName: node-1
  type: release
  autoSync: true
  controlplane: { enable: true, image: yanet-controlplane }
  dataplane:    { enable: true, image: yanet-dataplane, ... }
  bird:         { enable: true, image: yanet-bird, tag: 2.0.12 }
  announcer:    { enable: true, image: yanet-announcer }
```

`YanetConfig` (v1alpha1) holds shared cluster-wide settings: image registry,
named annotations, named post-start hooks, named init containers, resources,
tolerations, etc. The Yanet CR references these by name through fields like
`additionalOpts.annotations: [telegraf]`, `enabledOpts.{cp,dp}.resources`, etc.

### Reconcile flow (v1)

```
Yanet CR change ─┐
Node change   ───┼─►  YanetReconciler.Reconcile
GlobalConfig ────┘        │
                          ▼
                 reconcilerYanet(yanet, config)
                          │
                          ├─ DeploymentForControlplane
                          ├─ DeploymentForDataplane
                          ├─ DeploymentForBird
                          ├─ DeploymentForAnnouncer
                          ▼
                 CreateOrUpdate per component
                          ▼
                  Status.Sync buckets
```

Generators live in [`internal/manifests/`](internal/manifests/) (`dataplane.go`,
`controlplane.go`, `bird.go`, `announcer.go`).

---

## 3. v2alpha1 — three-tier composable model

The v2alpha1 design separates three independent axes of configuration:

```
┌─────────────────────────────────────────────────────────────────┐
│ YanetConfig (cluster-wide, in-memory snapshot)                  │
│                                                                 │
│  spec.components       — palette of available components        │
│    ├─ controlplane     — 5 hardcoded slots                      │
│    ├─ dataplane                                                 │
│    ├─ bird                                                      │
│    ├─ birdAdapter                                               │
│    ├─ announcer                                                 │
│    └─ operators[]      — dynamic, by name                       │
│                                                                 │
│  spec.patches []NamedPatch                                      │
│    └─ raw strategic-merge fragments of appsv1.Deployment        │
│       (validated via dry-run StrategicMergePatch)               │
│                                                                 │
│  spec.boxTypes []BoxType                                        │
│    └─ named presets wiring components → ordered patch lists     │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ referenced by
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│ Yanet (per-installation, minimal)                               │
│                                                                 │
│  spec.boxType: <name>           # required, immutable           │
│  spec.nodeSelector: {...}                                       │
│  spec.enabled: true             # default                       │
│  spec.autoSync: false           # default                       │
│  spec.components:               # narrow typed overrides only   │
│    controlplane:                                                │
│      containers:                                                │
│        controlplane: { tag: v2.1.5-hotfix }                     │
│    operators:                                                   │
│      antiddos: { enabled: false }                               │
└─────────────────────────────────────────────────────────────────┘
```

Per-installation customisation is intentionally narrow: only per-container
`image.{name,tag}` (under `containers.<name>`) and `enabled` are accepted on
the Yanet CR. Everything broader (annotations, postStart, hostIPC, privileged,
resources) belongs in a NamedPatch.

The container key inside `containers` must match the rendered container name
— for the 5 hardcoded components it equals the component kind itself
(`controlplane`, `dataplane`, `bird`, `birdAdapter`, `announcer`); for
operators it equals the `OperatorContainer.name` declared in YanetConfig
(which is mandatory).

### CR shape

`YanetConfig` (v2alpha1) — see [`api/v2alpha1/yanetconfig_types.go`](api/v2alpha1/yanetconfig_types.go):

```yaml
spec:
  stop: false
  updateWindow: 0
  images: { registry: ..., prefix: ..., pullPolicy: IfNotPresent }
  components:
    controlplane: { image: {...}, port: 8080, portRange: 4, numa: 2 }
    dataplane:    { image: {...}, port: 8081, hugepages: { size: 1Gi, count: 8 } }
    bird:         { image: {...}, port: 179 }
    birdAdapter:  { image: {...} }
    announcer:    { image: {...}, port: 9090 }
    operators:
      - name: antiddos
        port: 9001
        containers:
          - { name: operator, image: {...} }
          - { name: agent,    image: {...}, hostIPC: true }
  patches:
    - name: telegraf
      patch:
        spec: { template: { metadata: { annotations: { telegraf...: "8080" } } } }
    - name: cp-resources-release
      patch: ...
  boxTypes:
    - name: release
      components:
        controlplane: { patches: [telegraf, cp-resources-release] }
        dataplane:    { patches: [telegraf, dp-privileged] }
        bird:         { patches: [telegraf] }
      operators:
        antiddos:     { patches: [telegraf] }
```

`Yanet` (v2alpha1) — see [`api/v2alpha1/yanet_types.go`](api/v2alpha1/yanet_types.go):

```yaml
spec:
  boxType: release
  nodeSelector: { role: yanet-edge }
  autoSync: true
  components:
    controlplane:
      containers:
        controlplane: { tag: v2.1.5-hotfix }
```

### Reconcile flow (v2)

```
Yanet CR change ────┐
Node change      ───┼─►  YanetReconciler.Reconcile
Pod change       ───┤        │
YanetConfigV2    ───┘        │  (read-only via in-memory snapshot)
snapshot                     ▼
                    reconcileYanetV2(yanet)
                             │
                             ├─ snapshot YanetConfigV2 (DeepCopy under lock)
                             ├─ FindBoxType(spec.boxType)
                             ├─ EnabledComponentsForBox  → []ComponentRef
                             ├─ listNodes(nodeSelector)
                             │
                             │  for each node:
                             │    BuildContextV2 (NUMA from NFD label)
                             │    for each ComponentRef:
                             │      ResolveBoxComponent → ResolvedComponent
                             │      InlineConfigMaps   → CreateOrUpdate ConfigMaps
                             │      BuildDeployments   → []Deployment skeletons
                             │      ApplyPatches       → strategic merge in order
                             │      applyDeploymentV2  → CreateOrUpdate (or status only)
                             │      BuildServices      → ServicePlan[]
                             │
                             ├─ deduplicate ServicePlans across nodes
                             ├─ applyServiceV2 each
                             └─ Status.Sync buckets + per-node summaries
```

Key files:
- [`internal/helpers/resolve_v2.go`](internal/helpers/resolve_v2.go) — `ResolveBoxComponent`, `EnabledComponentsForBox`, `FindBoxType`, `FindOperator`, `ShortNodeKey`.
- [`internal/manifests/builder_v2.go`](internal/manifests/builder_v2.go) — `BuildDeployments`, `InlineConfigMaps`, hugepages, ConfigSource branches.
- [`internal/manifests/patcher.go`](internal/manifests/patcher.go) — `PatchRegistry`, `ApplyPatches` via `strategicpatch.StrategicMergePatch`.
- [`internal/manifests/service_v2.go`](internal/manifests/service_v2.go) — `ServicePlan`, `BuildServices`, `ToService`.
- [`internal/controller/yanet_reconciler_v2.go`](internal/controller/yanet_reconciler_v2.go) — orchestration.
- [`internal/controller/yanetconfig_controller_v2.go`](internal/controller/yanetconfig_controller_v2.go) — snapshot watcher.

---

## 4. Controlplane NUMA fan-out

`controlplane.numa` (or NFD label `feature.node.kubernetes.io/cpu-numa_nodes_count`)
controls how many controlplane Deployments are generated **per node**:

```
Node has 2 NUMA domains  ⇒  2 Deployments:
  yanet-<nodehash>-controlplane-numa0 listening on port + 0
  yanet-<nodehash>-controlplane-numa1 listening on port + 1
```

Three Service categories are generated:

| Service                                     | Selector                                | InternalTrafficPolicy |
|---------------------------------------------|-----------------------------------------|-----------------------|
| `<yanet>-<nodehash>-numa{N}` (per-node)     | `app=cp, numa=N, node=<host>`           | `Local`               |
| `<yanet>-controlplane-numa{N}-cluster` (RR) | `app=cp, numa=N`                        | (none)                |
| `<yanet>-controlplane-all` (RR)             | `app=cp`                                | (none)                |

Per-node Local services are how an in-node caller (e.g. a pod scheduled to the
same NUMA domain) reaches the local controlplane instance. Cluster-wide RR
services are entry points for unaware clients.

### Operator services

When `OperatorSpec.Port > 0`, **one** cluster-wide ClusterIP Service is
generated, named after the operator (`route-operator`, `antiddos`, …), with
`internalTrafficPolicy=Local` so callers on the same node hit the local pod
without leaving the host.

Other components (`bird`, `birdAdapter`, `announcer`, `dataplane`) each get
one simple cluster-wide ClusterIP. `bird` and `birdAdapter` are Local
(they share a `/run/bird` hostPath socket); the others are not.

---

## 5. ConfigSource — three variants

All five hardcoded components and every operator container can supply a
`ConfigSource` ([`api/v2alpha1/config_source.go`](api/v2alpha1/config_source.go)):

| Variant   | What the builder does                                            |
|-----------|------------------------------------------------------------------|
| `inline`  | Generates a hash-named `ConfigMap`, mounts it read-only at the per-component default directory (`/etc/yanet2` or `/etc/bird`). |
| `hostPath`| Mounts the HOST directory read-only at the per-component default path. The component binary reads its config file from inside that directory using its own default name. |
| `url`     | Creates an `emptyDir`; an init-container is expected to populate it (today via a patch; see deferred items). |

The webhook enforces that exactly one variant is set.

### `FileName` — optional config file selector

When `ConfigSource.fileName` is non-empty, the builder:

1. Passes `--config=<mountDir>/<fileName>` as a command-line argument to the
   container so the binary knows which file to read.
2. For `inline` configs: remaps the `"config"` key in the `ConfigMapVolumeSource`
   via `Items[{key:"config", path:<fileName>}]` so the file appears at
   `<mountDir>/<fileName>` inside the Pod (the mounted directory still
   contains only that one file).
3. For `hostPath` and `url` configs: the directory is mounted as-is;
   `--config=...` tells the process which file to open.

Example:

```yaml
controlplane:
  config:
    hostPath: /etc/yanet2
    fileName: controlplane.conf   # → --config=/etc/yanet2/controlplane.conf
```

### Default `pullPolicy`

When `spec.images.pullPolicy` is empty in `YanetConfigV2`, the reconciler
defaults to `IfNotPresent`. An explicit value (`Always`, `Never`) overrides
the default for all generated containers.

---

## 6. Patches — strategic merge, ordered

`spec.patches []NamedPatch` is a registry of strategic-merge fragments of
`appsv1.Deployment`. Each `BoxComponent.patches []string` references them by
name; patches are applied **in the listed order** by `ApplyPatches`.

Why strategic merge:
- Container/volume merge by name (`patchMergeKey`).
- Annotation/label maps merge by key (additive).
- Same algorithm Kubernetes uses for `kubectl apply`.

Validation:
- Webhook enforces uniqueness of names and existence of all references.
- Webhook **dry-runs** every patch via `strategicpatch.StrategicMergePatch(empty Deployment, patch, appsv1.Deployment{})` so a typo (e.g. `templete:` instead of `template:`) is caught at admit time.

JSON6902 (`jsonPatch`) is intentionally not supported. Service / ConfigMap
patching is not supported either — those are generated entirely from the
component definition (`port`, `Hugepages`, `ConfigSource`).

---

## 7. In-memory config snapshot

The reconciler does **not** call `client.List(YanetConfig)` on every cycle.
A separate `YanetConfigReconciler` (one per API version) maintains an
in-memory snapshot under a mutex:

```go
type MutexYanetConfigSpec struct {
    Config YanetConfigSpec
    Lock   sync.Mutex
}
```

`YanetReconciler` reads from it under the same lock with a DeepCopy. This
mirrors the v1 design and avoids every reconcile cycle paying the API-server
round-trip cost.

[`cmd/main.go`](cmd/main.go) constructs both snapshots and wires them into the
respective reconcilers and config watchers.

---

## 8. Watches and event mapping

[`internal/controller/yanet_controller.go`](internal/controller/yanet_controller.go) sets up:

| Watch source                | Mapper                       | Purpose                                                     |
|-----------------------------|------------------------------|-------------------------------------------------------------|
| `v1alpha1.Yanet`            | direct                       | Legacy CRs.                                                 |
| `v2alpha1.Yanet`            | direct                       | New CRs.                                                    |
| `corev1.Node`               | `mapNodeToV2Yanets` + v1     | Trigger reconcile when a node is added/removed/labelled.    |
| `corev1.Pod`                | `mapPodToYanet`              | Update Yanet status when managed pods change phase.         |

Node/Pod events are fanned out to all Yanet CRs whose `nodeSelector` matches.

---

## 9. Webhooks

Two validating webhooks, both using the controller-runtime ≥ 0.23 generic
typed validator:

```go
type MyValidator struct{ Client client.Client }
var _ admission.Validator[*MyKind] = &MyValidator{}
```

| Webhook                | Endpoint                                                  | Validates                                                                                               |
|------------------------|-----------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| `vyanet-v2.kb.io`      | `/validate-yanet-yanet-platform-io-v2alpha1-yanet`        | `boxType` required, `boxType` immutable on update, `boxType` exists in some YanetConfig, operator overrides reference declared operators, per-container override keys match rendered container names. Degrades to a warning when no YanetConfig is reachable (bootstrap case). |
| `vyanetconfig-v2.kb.io`| `/validate-yanet-yanet-platform-io-v2alpha1-yanetconfig`  | Uniqueness of patch / operator / boxType names; cross-references; required `controlplane` + `dataplane` per box; **dry-run** of every NamedPatch. |

Validators take dependencies via struct fields (no module-level globals).

---

## 10. `enabled` and `autoSync` (v2)

v2 separates two orthogonal axes that v1 used to conflate. Both are
`*bool` on [`YanetSpec`](api/v2alpha1/yanet_types.go:37); v1 has only the
per-component `Enable` flag and a non-pointer `AutoSync bool`.

### 10.1. `spec.autoSync` — "may the operator touch managed objects?"

Defaults to `false`. Controls whether the reconciler **applies** drift or
only **reports** it. Symmetric for every managed object kind:

| `autoSync` | Deployment exists | Deployment missing | Service exists | Service missing | Inline ConfigMap exists | Inline ConfigMap missing | Orphans       |
|------------|-------------------|--------------------|----------------|-----------------|-------------------------|--------------------------|---------------|
| `true`     | `Update`          | `Create`           | `Update`       | `Create`        | `Update`                | `Create`                 | deleted       |
| `false`    | no apply          | no apply           | no apply       | no apply        | no apply                | no apply                 | only counted  |

In `false` mode the user can hand-edit any managed object (replicas,
foreign labels, `externalTrafficPolicy`, ConfigMap contents, …) and the
operator will not fight back; drift is surfaced under
`status.sync.outofsync` / `status.sync.syncwaiting` instead. Covered by
[`TestReconcileV2_AutoSyncOff_PreservesHandEditsOnExistingResources`](internal/controller/yanet_reconciler_v2_test.go:399)
and [`TestPruneOrphans_AutoSyncFalse_DoesNotDelete`](internal/controller/yanet_reconciler_v2_hardening_test.go:273).

### 10.2. `spec.enabled` — "should pods actually run?"

Defaults to `true`. This is a **scale-to-zero switch**, not a reconcile
pause. The reconciler still renders every Deployment / Service /
ConfigMap (so the user can inspect generated specs and patches still
take effect), but forces `spec.replicas=0` on every Deployment after
patches have been applied — overriding any per-component
`components.<name>.enabled` and any patch-set `replicas` value.

To "freeze" the operator's view of a CR (keep existing objects exactly
as they are, hand edits and all) use `spec.autoSync=false` from §10.1
instead.

Compatibility matrix:

| `enabled` | `autoSync` | Effect                                                                                                                          |
|-----------|------------|---------------------------------------------------------------------------------------------------------------------------------|
| `true`    | `true`     | Steady-state: full reconcile, replicas governed by per-component overrides and patches.                                         |
| `true`    | `false`    | Report-only: the operator never touches managed objects; hand edits preserved; drift in `status.sync`.                          |
| `false`   | `true`     | Active scale-to-zero: reconciler keeps applying desired specs but with `replicas=0` on every Deployment.                        |
| `false`   | `false`    | Passive scale-to-zero: reconciler reports drift only; existing Deployments retain whatever replicas they had before.            |

Covered by
[`TestReconcileV2_Disabled_ScalesToZero`](internal/controller/yanet_reconciler_v2_test.go:103).

### 10.3. Per-component `components.<name>.enabled`

Independent from §10.2. Lives inside the boxType components mapping
(via `YanetConfigV2.spec.boxTypes[].components.<name>.enabled` or as a
per-installation override on `Yanet.spec.components.<name>.enabled`)
and is consumed by
[`replicasFor`](internal/manifests/builder_v2.go:321): `Enabled=false`
on a single component yields `replicas=0` for just that Deployment.

If `spec.enabled=false` is also set on the CR, the CR-level switch
wins — every Deployment is scaled to zero regardless of per-component
state.

---

## 11. Build / test surface

| Make target              | What it does                                                                 |
|--------------------------|------------------------------------------------------------------------------|
| `make generate`          | DeepCopy generation via `controller-gen object`.                             |
| `make manifests`         | CRDs + webhook configs via `controller-gen rbac/crd/webhook`.                |
| `make helm-crds`         | Build `deploy/charts/yanet-operator/crds/yanet.yaml` via kustomize.          |
| `make fmt` / `make vet`  | gofmt / go vet (in Docker).                                                  |
| `make lint`              | golangci-lint (in Docker).                                                   |
| `make test-unit`         | Unit tests in `internal/helpers`, `internal/manifests`.                      |
| `make test`              | Full suite: API webhooks, helpers, manifests, controller envtest (Ginkgo).   |
| `make test-race`         | Same with `-race`.                                                           |

envtest registers **both** API versions in
[`internal/controller/suite_test.go`](internal/controller/suite_test.go); a
common pitfall is missing one of them — the cache sync then times out for
`*v2alpha1.Yanet` after 60 seconds.

---

## 12. Hardening (DONE) and remaining out-of-scope

### Done in the H1–H6 hardening pass (sprint 2026-05-07)

See [`YANET2_HARDENING_PLAN/00-STATUS.md`](YANET2_HARDENING_PLAN/00-STATUS.md)
for the full list.

- **Finalizer** `yanet.yanet-platform.io/finalizer` is now installed and
  removed by the v2 reconciler (mirrors the v1 path). Cleanup on
  `DeletionTimestamp` runs `pruneOrphans` with an empty desired set
  ([`yanet_reconciler_v2.go:64`](internal/controller/yanet_reconciler_v2.go:64)).
- **Orphan cleanup**: every reconcile cycle lists Deployment / Service /
  ConfigMap labelled `yanet.yanet-platform.io/yanet=<name>` and deletes
  anything that is no longer in the desired set
  ([`prune_v2.go:1`](internal/controller/prune_v2.go:1)). When `autoSync=false`
  the helper only reports the count instead of deleting.
- **Global `updateWindow` throttling** is now exercised on the v2 path:
  `applyDeploymentV2` calls the existing `checkUpdateRequeue` before any
  drift `Update`, sharing `lastUpdateTS / lastUpdateHost` with v1
  ([`yanet_reconciler_v2.go:419`](internal/controller/yanet_reconciler_v2.go:419)).
- **`metav1.Condition`** for Available / Progressing / Degraded / Ready,
  plus `Status.Pods` aggregation grouped by phase
  ([`yanet_conditions_v2.go:1`](internal/controller/yanet_conditions_v2.go:1)).
- **v2 webhook** rejects negative `spec.updateWindow`
  ([`yanetconfig_webhook.go:78`](api/v2alpha1/yanetconfig_webhook.go:78)).
- **Metrics**: `yanet_orphans_pruned_total`,
  `yanet_v2_deployments_created_total`,
  `yanet_v2_deployments_updated_total`,
  `yanet_v2_update_throttled_total`
  ([`metrics.go:1`](internal/controller/metrics.go:1)) plus a v2 Grafana
  dashboard
  ([`yanet-operator-v2.json`](deploy/charts/yanet-operator/dashboards/yanet-operator-v2.json:1)).

### Still deferred / out of scope

- **`ConfigSource.URL`** end-to-end (operator fetching the URL into a
  ConfigMap). Today: `emptyDir` + a user-supplied patch attaches the init
  container. See `H7` in the hardening plan.
- **AutoDiscovery** in v2 — kept v1-only by design.
- **JSON6902** patches — only strategic merge is supported.
- **Patches on Service / ConfigMap** — generation is hardcoded from the
  component definition.

---

## 13. Where to read next

- [`YANET2_ARCH.md`](YANET2_ARCH.md) — what yanet2 looks like on a node (modules, agents, BIRD, NUMA).
- [`AGENTS.md`](AGENTS.md) — contributor guide (testing, linting, common pitfalls).
- [`README_WEBHOOKS.md`](README_WEBHOOKS.md) — webhook setup and certificates.
- [`README_TESTS.md`](README_TESTS.md) — testing details.
- [`README_METRICS.md`](README_METRICS.md) — Prometheus metrics surface.
- [`README_RELEASES.md`](README_RELEASES.md) — release process.
- [`deploy/examples/v2alpha1-*.yaml`](deploy/examples/) — full v2alpha1 sample manifests.
