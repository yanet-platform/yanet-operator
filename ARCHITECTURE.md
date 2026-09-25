# yanet-operator architecture

The operator manages YANET2 runtime workloads through a single Kubernetes API.
For the runtime process topology, see [YANET2_ARCH.md](YANET2_ARCH.md).

## API identity

Both CRDs serve/store only `yanet.yanet-platform.io/v1alpha1`:

| Kind | CRD | Scope |
| --- | --- | --- |
| `Yanet` | `yanets.yanet.yanet-platform.io` | Namespaced |
| `YanetConfig` | `yanetconfigs.yanet.yanet-platform.io` | Cluster; name `config` |

Types and validators live in `api/v1alpha1`. There is no legacy implementation,
version dispatcher or conversion webhook. Release 3.0.0 reuses the short API
names for the component-based model; see [replacement notes](release-notes/v3.0.0.md).
The YANET2 runtime name, image paths and `/etc/yanet2` configuration are independent
of the operator API version.

## Three-tier composable model

`YanetConfig.spec` separates:

1. `components`: palette of controlplane, dataplane, optional birdAdapter,
   ordered atomic dataplane `sidecars[]` and standalone `operators[].containers[]`.
2. `patches`: named strategic-merge `appsv1.Deployment` fragments.
3. `boxTypes`: named wiring presets with ordered patch references.

Every sidecar entry describes exactly one native container (`name`, `image`,
`config`, `listeners`). Box types select sidecars by name. BIRD, neighbour-sidecar,
netconfig and announcer are ordinary declared roles, not implicit name-based
dependencies. Shared mounts and permissions are explicit palette patches.

`Yanet.spec` selects an immutable `boxType` and `nodeSelector`. Its overrides are
limited to image name/tag per rendered container, enablement, controlplane
`disabledNuma` and the complete dataplane `networks` list. Sidecar overrides live
under `components.dataplane.sidecars.<name>`; standalone container overrides use
`components.operators.<name>.containers.<container>`.

Palette images independently inherit `registry`/`prefix` from `spec.images`.
Explicit empty strings clear a segment; installation overrides do not expose
these fields. The default image pull policy is `IfNotPresent`.

## Controllers and snapshot

`cmd/main.go` registers one scheme and two reconcilers:

- `YanetReconciler` manages installation Deployments, inline ConfigMaps and status.
- `YanetConfigReconciler` publishes the palette snapshot and owns shared Services.

They share one mutex-protected `MutexYanetConfigSpec`. Readers use deep copies;
config refresh serializes the API read with snapshot publication. The config
watch mapper refreshes before enqueueing installations, so they do not render
from an old palette after a change. A failed refresh clears the snapshot under
the publication lock.

| Watch | Mapping |
| --- | --- |
| `Yanet` | Installation reconcile; also enqueue the config singleton |
| `YanetConfig` | Refresh snapshot, then enqueue all installations |
| `Node` | Installations whose `nodeSelector` matches |
| `Pod` | Owning installation from the managed label, including label-removal updates |
| Owned Deployments/ConfigMaps | Installation owner |
| Owned Services | Config singleton owner |

## Reconcile and ownership

```text
snapshot/config stop check → finalizer lifecycle → box/override resolution
→ matching nodes and ownership claims → render every workload
→ patch/compose/validate all plans and producer transitions
→ apply inline ConfigMaps and Deployments (or report drift)
→ preflight shared Service names → prune orphans → aggregate status

YanetConfig reconcile → aggregate namespace × boxType roles
→ create/update shared Services → prune obsolete shared Services
```

One installation may own a node. The incumbent workload owner wins; before any
workload exists, the oldest CR wins, with namespace/name as a stable tie-breaker.
A deleting installation retains its claim until cleanup finishes.

Owner references include the API kind/group, controller bit and UID. Matching
labels or a reused name do not authorize adoption/deletion. Finalizer cleanup
requests foreground Deployment deletion and waits for its completion before
releasing the node claim. Label drift does not hide owned resources from cleanup.

`YanetConfig.spec.stop: true` freezes writes, including status, finalizers and
deletion. It does not stop running Pods. Startup reads the persisted singleton
before writes when the snapshot has not yet loaded, preserving this stop contract.

### Enablement and drift

| `enabled` | `autoSync` | Workload behavior |
| --- | --- | --- |
| true | true | Apply desired workload state |
| true | false | Report drift; preserve existing workload edits |
| false | true | Apply rendered workloads with replicas forced to zero |
| false | false | Report drift; existing replicas remain unchanged |

Defaults are `enabled=true`, `autoSync=false`. Component enablement affects that
component; installation disablement overrides every Deployment's replicas after
patching. Shared Services have a separate palette-owned lifecycle and remain
available for declared box-type roles.

Apparent Deployment drift is normalized through a real API-server dry-run update
before deciding whether to persist an update or consume the global `updateWindow`.
This also occurs in report-only mode, without a persisted write. Conflict retries
re-read ownership. Removed patch fields return to server defaults. Shared Service
read-before-write uses the direct API reader because the informer cache can miss
a Service created earlier in the same reconcile.

## NUMA and configuration sources

`components.controlplane.numa` explicitly sets the number of physical NUMA
domains (default 1). Each selected node gets one controlplane Deployment per
enabled domain. `disabledNuma` excludes domains without renumbering the survivors.
An installation's non-nil exclusion list replaces the palette; an empty list
re-enables all domains.

`config.args` replaces only `{numa}`, for example
`/etc/yanet2/controlplane.d/numa{numa}.yaml`. The host must supply corresponding
configs with matching physical instance identities. No filename suffix is inferred.

`ConfigSource` has exactly two alternatives:

- `hostPath`: existing host directory mounted read-only.
- `inline`: opaque data delivered through a generated ConfigMap.

`mountPath` optionally selects the absolute container directory; the default is
`/etc/yanet2`. Inline data appears in `<mountPath>/config`. Arguments are literal
except for the controlplane NUMA placeholder. Extra files, sockets and download
init containers use explicit patches; there is no URL placeholder API.

## Listener endpoint configuration

Every final Pod uses private networking. `hostNetwork: true` and nonzero
`hostPort` are rejected after patches, including init containers.

- Standalone workloads listen on 8080/8081.
- Dataplane sidecar index `i` reserves `8080+2*i` / `8081+2*i` in the full ordered
  palette before enablement/selection. Disabling a sidecar does not compact slots.
- Omitted listeners default to `[grpc]`; HTTP-only roles explicitly select `[http]`.
  `[]` suppresses the Service, not the slot or managed host-config environment.
- Shared Services `yanet-<boxType>-<role>[-numa<N>]` expose 8080/8081 using named
  target ports and `internalTrafficPolicy: Local`. Selectors contain the box/role
  and physical NUMA identity, not the installation or node name.

Only a managed HostPath config remaining after patches enables automatic runtime
bind/Service-FQDN advertise and the complete named NUMA gateway environment.
Inline or externally patched ConfigMaps are opaque and get no automatic network
environment. Managed environment values override conflicting patches. Runtime
images must support `YANET_KUBERNETES_GATEWAYS`; the operator does not inspect
application configuration addresses, ports or TLS content.

Before moving a role between standalone and dataplane, drain its current
producers. Deployment/ReplicaSet/Pod guards prevent overlapping producers, and
shared-Service cutover waits for the scope to drain. `stop` is not a substitute
for `enabled:false` with `autoSync:true` and observed Pod termination.

## Device-backed network attachments

`components.dataplane.networks` declares an ordered list of existing NADs and
matching extended resources. Each entry adds one Multus attachment and one
request/limit to the primary dataplane container. Repeated NAD/resource entries
sum their quantities; interface names must be unique.

Per-installation omission/null inherits the palette; a list replaces it; `[]`
clears it. Patches must not also set the managed Multus annotation or reservation
quantities, including stale quantities from the replaced default network list.
The operator does not discover/provision NICs, manage device-plugin JSON or create
NADs. Device allocation and hardware behavior require target-cluster checks.

## Patches and generated artifacts

`ApplyPatches` applies named strategic-merge Deployment fragments in order.
Validators check references and dry-run fragments; the renderer validates the
complete effective Pod, identities, ports, sidecars and networks before writes.
The dataplane/controlplane intrinsic security and shared-memory baseline is built
by `internal/manifests/builder.go`; optional settings remain explicit patches.

API definitions drive `controller-gen` DeepCopy, CRDs, RBAC and admission
configuration. `make helm-crds` bundles CRDs with kustomize. Helm's handwritten
RBAC and webhook templates must match those generated contracts.

See [webhook documentation](README_WEBHOOKS.md), [testing](README_TESTS.md),
[metrics](README_METRICS.md) and [release process](README_RELEASES.md).
