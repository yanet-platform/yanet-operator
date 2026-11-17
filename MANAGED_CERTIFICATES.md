# Managed workload certificates

This opt-in integration requires cert-manager's `cert-manager.io/v1` CRDs.
Signing, approval and renewal remain cert-manager responsibilities. The YANET
manager creates `Certificate` resources, never signs CSR or reads TLS Secrets.
The Helm chart can install a pinned cert-manager dependency with
`cert-manager.enabled=true`; it is disabled by default to preserve existing
cluster-wide ownership. On a fresh cluster bootstrap that dependency first with
`manageCertificates=false` and `webhook.enabled=false`, then enable consumers
after its CRDs/controllers are ready. See the chart installation guide.
Enable the Helm value `manageCertificates: true` (manager flag
`--manage-certificates`) only after installing the CRDs. Existing installations
without `spec.certificates` retain their rendering and lifecycle.

## Configuration

Add the following to `YanetConfig.spec`, using a released, digest-pinned operator
image containing `/manager credential-sync` rather than the illustrative digest:

```yaml
certificates:
  issuerRef: {name: yanet-workloads, kind: Issuer}
  trustDomain: yanet.example.test
  trustBundle: {name: yanet-workload-trust, key: ca.crt}
  # Optional, independent OpenSSH CA public keys; never X.509 PEM or private keys.
  sshTrustBundle: {name: yanet-gateway-ssh-trust, key: ssh-ca.pub}
  additionalGatewayAccounts:
    - username: root
      permissions: ['/*/*']
    - username: alice
      permissions: ['/controlplane.ynpb.v1.AuthService/IntrospectToken']
  hostPath: /var/lib/yanet/credentials
  exporterImage: example.test/yanet-operator@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  duration: 72h
  renewBefore: 48h
  profiles:
    - name: gateway
      component: controlplane
      container: controlplane
      mountPath: /etc/yanet2/pki/gateway
      usage: server
      dnsNames:
        - '{node}'
        - 'yanet-{boxType}-controlplane-numa0.{namespace}.svc.cluster.local'
        - 'yanet-{boxType}-controlplane-numa1.{namespace}.svc.cluster.local'
    - name: acl
      component: acl
      container: acl
      mountPath: /etc/yanet2/pki/acl
      usage: client
      # Illustrative grant; supply the complete least-privilege RPC set for the role.
      gatewayPermissions:
        - /controlplane.ynpb.v1.Gateway/Register
```

This example requests a three-day leaf (`72h`) and renewal with two days
remaining (`48h`), giving approximately daily renewal when the issuer honors the
requested lifetime. Confirm the actual Certificate status `notBefore`, `notAfter`
and `renewalTime`; this is not an exact wall-clock schedule. The issuing CA chain
must remain valid throughout the leaf lifetime.

Each referenced logical component/container must be declared in the palette.
Profiles are rendered only for selected effective workloads, including enabled
native sidecars. An operator with multiple containers can assign different
profiles to each. A role is shared across that node's NUMA fan-out. Mount names
are reserved; conflicting patches fail preflight. The exporter component name
`credential-delivery` is reserved while certificates are enabled.

Names and URI identities include the installation and Node UIDs, not Pod UIDs.
Reconcile/restart does not request a new identity; replacing either object does.
The exact URI is:

```text
spiffe://<trustDomain>/ns/<namespace>/installation/<Yanet UID>/node/<Node UID>/role/<profile name>
```

Certificates have an empty CommonName, exactly one URI SAN, the declared server
DNS SANs (clients have none), role-specific EKU, ECDSA P-256 keys,
`rotationPolicy: Always`, explicit renewal timing and `revisionHistoryLimit: 1`.
Verify issuer and all clients support these settings before enabling them.
This is a URI naming contract, not SPIRE/node attestation.

## Native application configuration

The host configuration generator owns the application's native TLS and
authentication fields. The certificate profiles own issuance, role-specific
mounts, durable PEM delivery and startup gates. The two sides agree on stable
`<mountPath>/current` paths; generated configuration contains paths, not private
key contents.

For example, a route client's native TLS value is:

```yaml
ca_file: /etc/yanet2/pki/route/current/ca.crt
key_pairs:
  - type: file
    config:
      cert_file: /etc/yanet2/pki/route/current/tls.crt
      key_file: /etc/yanet2/pki/route/current/tls.key
```

Place that value under the component's supported `gateways[].tls`,
`instances[].gateway_tls` or `route_operator_tls` field, as appropriate. Metrics
and readiness clients also need their own role-specific native TLS values. A
gateway server needs native TLS listener and authentication settings, including
the generated identity and permission files described below.

Endpoint environment overrides are separate from these TLS values. In runtime
versions where `xcfg` treats provider configuration as an opaque `yaml.Node`,
variables targeting `key_pairs[].config.cert_file` or `key_file` do not configure
that payload. Render the complete native TLS configuration in the input file;
certificate mounting alone does not enable mutual TLS. This workflow requires
neither an application-configuration renderer in the manager nor a Pod patch to
rewrite configuration files at startup.

## Issuance, delivery and loading

For every selected node a separate, node-pinned `credential-delivery` Deployment
projects only its required leaf Secrets and an independently managed public
trust ConfigMap. It runs one exporter per role, without a service-account token,
with read-only source mounts and write access only to that role's host directory.
The root exporter drops capabilities and uses a read-only root filesystem.
Persistent files are root-owned; consumers currently need UID 0 to read them.
The delivery Deployment uses `Recreate`, preventing overlapping writers during
policy changes.
Leaf Secret projections are optional at the Pod-volume boundary so a missing
role's Secret does not prevent other exporters from starting. Each exporter still
validates its own material before publication and stays unready without a valid
installed generation. Independent public trust ConfigMaps remain required.

The filesystem layout is:

```text
<hostPath>/<namespace>/<Yanet name>/<Yanet UID>/<Node UID>/<profile>/
  <generation digest>/{tls.crt,tls.key,ca.crt}
  <server generation digest>/{identities.yaml,permissions.yaml,policy.json}
  current -> <generation digest>
```

Only the role directory is mounted read-only at its `mountPath` in a consumer.
The exporter pins one kubelet `..data` generation, validates key/cert match,
identity, DNS SANs, EKU, chain and validity, fsyncs complete files/directories and
atomically switches `current`. Directories are `0700`, files `0600`. Invalid or
unavailable replacement material leaves the previous generation intact. Its
readiness probe validates the installed generation, including the delivered
`ca.crt` and its agreement with the independent trust projection.
Creating or updating the delivery template waits for Certificate Ready at its
current generation; an existing writer keeps its previous template and policy
while issuance is pending. First consumer creation and spec updates also wait for
a current, ready delivery Deployment. Explicit drain and cleanup do not wait for
issuance. Certificate errors do not prevent scaling an existing owned delivery
Deployment to zero. Existing consumers are preserved during an issuance outage.

`autoSync: false` does not create/update/delete Certificates or delivery workloads;
`stop: true` also freezes status/finalizer writes. cert-manager continues renewing
previously created Certificates independently. It may update their Secrets and an
already running exporter may deliver them while YANET reconciliation is paused.
Removing a role/node prunes its Certificate after delivery workloads drain.

Delivery logs expose the generation digest and errors, never PEM/key contents.
The installation's `CertificatesIssued` condition describes current issuance;
`CredentialsDelivered` describes the current delivery Deployment's readiness and
local validation. Neither proves an application loaded the new material. Application
load/connection fingerprints and remaining expiry require runtime monitoring.

## Recovery and rotation contract

- Checkpoint only runtime consumers, not the API-dependent delivery Deployment.
  Consumers' managed credential volumes are HostPaths, requiring neither an API
  server nor kubelet Secret cache on restart. Existing inline application config
  still requires its own supported checkpoint/materialization path.
- Keep immutable generations until an explicit retention/secure-cleanup operation
  proves they are unused. Controller deletion does not erase node-local recovery
  credentials. Configure cert-manager Secret ownership/retention separately;
  deleting a Certificate or Secret is not certificate revocation.
- An atomic `current` switch does not make two separate file reads atomic. A
  consumer must pin a generation per load or retry and retain its last good pair.
  Test both startup and concurrent renewal with the actual consumer image.
- A key-pair reload only affects a new TLS handshake. Arrange and verify controlled
  reconnect before expiry for established gRPC connections/streams. Independently
  loaded `ca_file` trust may require rebuilding credentials or restarting clients.
  For the example above, a native gateway `server.grpc.tls.max_connection_age`
  of `12h` leaves room for file refresh and reconnect before expiry. gRPC applies
  age jitter and allows in-flight RPCs to drain; it is not a process restart timer.
  Verify consumer recovery under load: planned transport rotation must not be
  mistaken for lost dataplane state or require manual stream recovery.
- Deliver old+new public trust before switching issuers, then retire old trust
  after every consumer has migrated. Do not derive trust solely from the leaf
  Secret's `ca.crt`. An outage budget is limited by leaf/chain/trust validity;
  expired credentials never trigger automatic authentication disablement.

Before issuance, restrict issuer/approver policy to the permitted URI namespace,
node/installation/role, DNS SANs, EKU, lifetime and `isCA: false`. Disable any
built-in auto-approver that would bypass that policy. The operator's ability to
create Certificates is not sufficient authorization to issue an arbitrary leaf.
Provision issuer keys outside this controller, with separate backup/rotation.

Authentication mode, RPC permissions and account mapping are application policy.
Client profiles can declare `gatewayPermissions`: at most 128 full
`/package.Service/Method` patterns, with `*` allowed within service/method names.
Only effective node workloads receive accounts. Every server-role exporter writes
their exact installation/Node UID-bound URI principals to `identities.yaml` and
their explicit grants to `permissions.yaml` alongside the PEMs in `current`.
An empty set writes an explicit empty policy, including after the last grant is
removed. No account is implied by certificate issuance alone.

Configure the gateway's file identity provider and permission store to read
`<server mountPath>/current/identities.yaml` and `permissions.yaml`. The operator
does not enable authentication or infer role permissions. Policy changes create
a new durable generation and change the gateway Pod template; a read-only
`credential-sync --check` init container rejects stale policy before startup.
It uses only cached public trust and the role directory, so the gate also works
without the API. Normal leaf rotation does not change the Pod template.

### Policy handoff and upgrade from v3.1.0

v3.1.0 was published on 2026-11-17. Its exact-policy startup gates are not
forward-compatible with a different account policy. Upgrading only the manager
does not change the digest-pinned gate images or saved checkpoints. This change
requires an operator and `certificates.exporterImage` containing the revision-aware
protocol; it does not retroactively change v3.1.0 images.

The required per-installation transition is:

1. Keep the existing gateway accounts, permissions, effective role membership and
   SSH CA policy unchanged. Upgrade the operator and exporter image. An attempted
   policy change against existing legacy workloads is rejected before application.
   The new writer may add revision-1 metadata to the **same** authorization set;
   the old exact-policy gate still accepts those files during this bridge.
2. Wait for all desired revision-aware delivery/gateway Deployments to finish
   updating and become ready at their current generation. Refresh every supported
   offline runtime checkpoint, including every NUMA gateway, with the new gates
   and images. Kubernetes readiness cannot establish that external checkpoints
   were refreshed.
3. Only after refreshing those checkpoints, acknowledge that installation UID:

   ```bash
   INSTALLATION_UID=$(kubectl -n <namespace> get yanet <name> -o jsonpath='{.metadata.uid}')
   kubectl -n <namespace> annotate yanet <name> \
     yanet.yanet-platform.io/policy-checkpoints-ready="${INSTALLATION_UID}/1" --overwrite
   ```

   The controller additionally verifies ownership, desired revision-aware
   templates and current-generation readiness. An early acknowledgment cannot
   bypass that observation. Inspect the controller-owned
   `yanet.yanet-platform.io/policy-state` annotation: `activated` must be `true`
   before changing policy. Do not edit, delete or restore an older policy-state
   annotation. Fresh installations use the same ready/checkpoint acknowledgment
   path before their first policy change; they have no legacy bridge to perform.
4. After activation, pre-protocol checkpoints are **not supported**. Retire them
   rather than restoring old grants to make their exact-policy gates pass. A
   recreated Yanet has a new UID, private credential directory and acknowledgment
   scope; copying another installation's state/acknowledgment is rejected.

Policy advancement is blocked until this transition completes, including when
the new configuration arrives before the rollout/checkpoint acknowledgment.
Drain and deletion retain their independent lifecycle.

Within the supported protocol, policy revisions are monotonically allocated on
the Yanet with optimistic concurrency. The installation UID is the epoch.
Authoritative config/Yanet UID, generation and spec reads fence stale or
incomparable sources, including config recreation; resourceVersions are never
interpreted as numbers. A→B→A→B has four distinct revisions. Leaf/issuer renewal
alone does not allocate a new policy revision or restart gateways.

The allocator also commits to the exact SSH ConfigMap bytes through
`--ssh-trust-sha256` on writers, readiness checks and gateway gates (`none` when
SSH trust is absent). A lagging kubelet projection cannot occupy the new revision:
delivery stays unready and retains the previous generation until the expected
bytes arrive, then retries the same allocated revision. A retained older gate
validates the installed newer policy's SSH commitment rather than requiring its
historical CA. External referenced SSH ConfigMap create/update/delete events
enqueue installations in that ConfigMap's namespace; unrelated ConfigMaps do not
trigger an installation inventory.

`policy.json`, authorization files, optional SSH trust and PEMs are published in
one immutable generation. A gateway gate accepts only its own epoch and at least
its requested revision. At an equal revision the policy must match exactly; at a
newer revision it validates and loads the **current** policy, not its historical
grants. Retained older supported templates therefore recover after a writer
advances, even if delivery status stalls or the consumer Update is forbidden.
New templates cannot start on an older publication. Removed accounts/SSH trust
never fall back to historical files. Exporter readiness requires its exact
desired revision, rather than adopting another publication as successful delivery.
This compatibility concerns authorization changes within the same role's URI,
DNS SAN and EKU identity. It does not relax those checks: replacing an installation
or Node UID, or changing the role/trust-domain/SAN contract, requires its own
consumer/checkpoint identity migration and remains fail closed.

Writers serialize their check-and-switch with a local nonblocking lock and reject
older revisions, conflicting equal revisions, epoch mismatches and damaged
installed policy metadata. The lock is released on failed writes; a failed switch
does not select an incomplete generation. These are trusted administrative paths,
not a defense against a root administrator replacing the complete credential
directory and its history. Restoring arbitrary historical filesystem snapshots
or editing controller-owned revision state is not a supported rollback.
Unversioned CLI/API callers retain exact-policy checking and cannot overwrite a
versioned publication.

The native gateway still reads authorization at startup. This protocol prevents
revoked grants from returning on **restart**; it does not immediately revoke an
already-running process's established RPCs. Apply the documented native reload,
restart and connection-draining policy separately.

### Human SSH certificate authentication

`certificates.sshTrustBundle` optionally references a non-optional ConfigMap key
in each installation namespace. It contains bare OpenSSH ECDSA P-256 CA public
keys, one per line; blank lines and comments are allowed. Use the chart's
`extraManifests` to supply this ConfigMap, or manage it independently. It is
separate from the X.509 `trustBundle` and must not be added to `ca.crt`.

Only server-role exporters receive this projection. They validate the complete
bundle and persist `current/ssh-ca.pub` atomically with the server credentials
and policy. Invalid or unavailable replacement trust preserves the previous
generation. CA-only changes create a new generation without rotating the leaf.
Gateway startup checks use the persisted file, so API-unavailable recovery does
not need a ConfigMap projection. Configure the native gateway alongside x509:

```yaml
authenticators:
  - type: sshcert
    config:
      ca_sources: [/etc/yanet2/pki/gateway/current/ssh-ca.pub]
      refresh_interval: 5m
```

`certificates.additionalGatewayAccounts` is an optional list of at most 32
explicit local accounts, default empty. Each record has `username` and
`permissions`; usernames are unique, at most 128 characters and match
`[A-Za-z_][A-Za-z0-9_.@-]*`. Workload SPIFFE usernames are reserved for the
effective certificate profiles. Local accounts are merged with those workload
accounts rather than replacing them. The example grants root all RPCs and alice
only introspection; supply a reviewed explicit read-only RPC allowlist for an
operational reader. Issuance or trusted CA membership alone grants no access.

The native SSH authenticator derives the login from the certificate's first
principal. These are gateway accounts, not operating-system or Kubernetes roles.
They also apply to another enabled authenticator that resolves the same login.
Removing or changing a human account changes the durable policy and gateway Pod
template. Existing file identity/permission providers load at startup; refreshing
CA trust does not reload account permissions. Disabling SSH authentication must
also remove its native authenticator configuration. Revocation/KRL distribution
is a separate native-authenticator policy, not implied by CA delivery.

For a managed controlplane server profile the reserved gRPC port is assigned to
`gateway.server.grpc.tls.endpoint` and the plaintext gRPC listener is disabled.
Without that profile, the native host file or explicit palette environment owns
both listener endpoints; configure the selected transport on `[::]:8080`. External
TLS does not require a managed certificate profile, and no plaintext listener is
injected merely because that profile is absent.
The host file must configure native TLS key pairs, authentication and the HTTP
listener. Clients must explicitly configure their supported `gateway_tls` or
`gateways[].tls` fields and bounded reconnect behavior. Callback listeners need
their own transport/access boundary; mounting certificates does not secure them.
With a managed BIRD adapter client profile, its route-import endpoint targets
the first active node-local controlplane gateway rather than the plaintext route
callback Service. Configure `route_operator_tls` in its host file accordingly.

Do not add anonymous registration to bypass missing credentials. Verify transport
boundaries, reload/reconnect, issuer failure and API-unavailable cold start on
the matched runtime images before a production rollout.
