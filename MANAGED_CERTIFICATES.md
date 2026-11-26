# Managed workload certificates and gateway policy

This opt-in integration requires cert-manager's `cert-manager.io/v1` CRDs.
Signing, approval and renewal remain cert-manager responsibilities. The YANET
manager creates `Certificate` resources, never signs CSR or reads TLS Secrets.
The Helm chart can install a pinned cert-manager dependency with
`cert-manager.enabled=true`; it is disabled by default to preserve existing
cluster-wide ownership. On a fresh cluster bootstrap that dependency first with
`manageCertificates=false` and `webhook.enabled=false`, then enable consumers
after its CRDs/controllers are ready. See the chart installation guide.
Enable the Helm value `manageCertificates: true` (manager flag
`--manage-certificates`) only after installing the CRDs. Installations without
`spec.certificates` manage their own credentials and authorization.

Every managed gateway uses persistent file-policy delivery and upstream reload.
This version requires a clean installation: stop the old workloads and retire
their offline startup definitions, then create a new installation with matching
runtime and exporter images. There is no policy mode switch, in-place migration
or checkpoint acknowledgment.

## Configuration

Add the following to `YanetConfig.spec`, using a released, digest-pinned operator
image containing `/manager credential-sync` and `/manager policy-sync` rather
than the illustrative digest:

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
are reserved; conflicting patches fail preflight. The component names
`credential-delivery` and `policy-delivery` are reserved for managed delivery.

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
authenticator fields. The certificate profiles own issuance, role-specific
mounts, durable PEM delivery and startup gates. The two sides agree on stable
`<mountPath>/current` paths; generated configuration contains paths, not private
key contents. Managed server profiles also configure file-policy paths and
polling through the environment, as described under [gateway policy](#gateway-policy).

For example, a route client's native TLS value is:

```yaml
ca_file: /etc/yanet2/pki/route/current/ca.crt
key_pairs:
  - type: file
    config:
      cert_file: /etc/yanet2/pki/route/current/tls.crt
      key_file: /etc/yanet2/pki/route/current/tls.key
```

For public native YANET2, use `gateways[].tls` for gateway-list clients or
`route_operator_tls` for BIRD route imports. `instances[].gateway_tls` is not a
public native field in v0.5.0 or the checked main revision; use that form only for
a client whose pinned schema explicitly supports it. Metrics and readiness
clients also need their own role-specific native TLS values.

Endpoint environment overrides are separate from these TLS values. In runtime
versions where `xcfg` treats provider configuration as an opaque `yaml.Node`,
variables targeting `key_pairs[].config.cert_file` or `key_file` do not configure
that payload. Render the complete native TLS configuration in the input file;
certificate mounting alone does not enable mutual TLS.

## Issuance, delivery and loading

For every selected node a separate, node-pinned `credential-delivery` Deployment
projects only its required leaf Secrets and an independently managed public
trust ConfigMap. It runs one exporter per role, without a service-account token,
with read-only source mounts and write access only to that role's host directory.
The root exporter drops capabilities and uses a read-only root filesystem.
Persistent files are root-owned; consumers currently need UID 0 to read them.
The delivery Deployment uses `Recreate`.

Leaf Secret projections are optional at the Pod-volume boundary so a missing
role's Secret does not prevent other exporters from starting. Each exporter still
validates its own material before publication and stays unready without a valid
installed generation. The independent X.509 trust ConfigMap remains required.

The TLS filesystem layout is:

```text
<hostPath>/<namespace>/<Yanet name>/<Yanet UID>/<Node UID>/<profile>/
  <generation digest>/{tls.crt,tls.key,ca.crt}
  current -> <generation digest>
```

Only the role directory is mounted read-only at its `mountPath` in a consumer.
The exporter pins one kubelet `..data` generation, validates key/cert match,
identity, DNS SANs, EKU, chain and validity, fsyncs complete files/directories and
atomically switches `current`. Directories are `0700`, files `0600`. Invalid or
unavailable replacement material leaves the previous generation intact.

Its readiness probe validates the installed generation, including the delivered
`ca.crt` and its agreement with the independent trust projection. The exporter
uses `--check --writer-readiness`: it holds the role's publication lock while
validating `current` and then syncing its parent directory. A busy lock or a
failed sync returns not ready. A later probe can confirm the same switch after
storage recovers; it does not roll back `current`. Consumer startup uses plain
`--check`, which validates the pinned generation without locking or syncing.

Creating or updating the TLS delivery template waits for Certificate Ready at its
current generation; an existing writer keeps its previous template while
issuance is pending. First consumer creation and spec updates also wait for a
current, ready TLS delivery Deployment. Explicit drain and cleanup do not wait
for issuance. Existing consumers are preserved during an issuance outage.
Gateway account and permission delivery is independent of these TLS waits.

`autoSync: false` does not create/update/delete Certificates or delivery workloads;
`stop: true` also freezes status/finalizer writes. cert-manager continues renewing
previously created Certificates independently. It may update their Secrets and an
already running exporter may deliver them while YANET reconciliation is paused.
Removing a role/node prunes its Certificate after delivery workloads drain.

Delivery logs expose the generation digest and errors, never PEM/key contents.
The installation's `CertificatesIssued` condition describes current issuance;
`CredentialsDelivered` describes TLS delivery readiness and local validation.
Neither proves an application loaded the new material. Application
load/connection fingerprints and remaining expiry require runtime monitoring.

## Recovery and rotation contract

- Checkpoint runtime consumers rather than the API-dependent delivery Deployments.
  Their managed credential and policy volumes are HostPaths, requiring neither an
  API server nor kubelet Secret cache on restart. Inline application configuration
  still requires its own supported checkpoint/materialization path.
- Keep immutable TLS generations until an explicit retention/secure-cleanup
  operation proves they are unused. Controller deletion does not erase local
  files. Configure cert-manager Secret ownership/retention separately; deleting
  a Certificate or Secret is not certificate revocation.
- An atomic TLS `current` switch does not make two separate file reads atomic. A
  consumer must pin a generation per load or retry and retain its last good pair.
  Test both startup and concurrent renewal with the actual consumer image.
- A key-pair reload only affects a new TLS handshake. Arrange controlled reconnect
  before expiry for established gRPC connections/streams. Independently loaded
  `ca_file` trust may require rebuilding credentials or restarting clients.
  For the example above, a native gateway `server.grpc.tls.max_connection_age`
  of `12h` leaves room for refresh and reconnect before expiry. gRPC applies age
  jitter and allows in-flight RPCs to drain; it is not a process restart timer.
- Deliver old+new public trust before switching issuers, then retire old trust
  after every consumer has migrated. Do not derive trust solely from the leaf
  Secret's `ca.crt`. The outage budget is limited by leaf/chain/trust validity;
  expired credentials never trigger automatic authentication disablement.

If a private key is missing or unusable, reissue the leaf through the configured
issuer and let the writer publish a new generation. Do not replace files inside
an immutable TLS generation.

Restrict issuer/approver policy to the permitted URI namespace,
node/installation/role, DNS SANs, EKU, lifetime and `isCA: false`. The operator's
ability to create Certificates is not sufficient authorization to issue an
arbitrary leaf. Provision issuer keys outside this controller, with separate
backup/rotation.

## Gateway policy

Each wired server profile must run a gateway containing upstream
[identity file reload](https://github.com/yanet-platform/yanet2/pull/3549),
[permission file reload](https://github.com/yanet-platform/yanet2/pull/3552)
and the `YANET_` environment overlay. Install the matching exporter and CRD
before creating the installation. Helm does not update an existing CRD from
`crds/` on upgrade.

The operator supplies a read-only mount at `/var/lib/yanet2/gateway-policy` and
injects the following gateway auth settings through environment variables:

```yaml
gateway:
  auth:
    mode: enabled
    identity_providers:
      - type: file
        path: /var/lib/yanet2/gateway-policy/identities.yaml
        refresh_interval: 5s
    permissions_path: /var/lib/yanet2/gateway-policy/permissions.yaml
    permissions_refresh_interval: 5s
```

Leave these managed fields out of runtime configuration, including deprecated
`auth.disabled`; configure the authenticators and TLS settings in the native
file. The indexed environment overlay does not remove additional identity
providers from a file.

Client profiles can declare `gatewayPermissions`: at most 128 full
`/package.Service/Method` patterns, with `*` allowed within service/method names.
Every client profile wired into the installation's `boxType` keeps its grants
even when disabled, scaled to zero, or omitted as a disabled native sidecar.
Only active workloads require issuance and delivery of client certificates.
To revoke a grant, edit `gatewayPermissions` or remove the role from box wiring.
An empty set writes an explicit empty policy, including after the last grant
is removed.

`additionalGatewayAccounts` adds at most 32 local logins to the UID-bound workload
accounts. Usernames are unique, at most 128 characters and match
`[A-Za-z_][A-Za-z0-9_.@-]*`; workload SPIFFE usernames are reserved for certificate
profiles. Issuance or trusted CA membership alone grants no access.

The node-pinned `policy-delivery` Deployment starts independently of cert-manager.
Its `policy-sync` writer reads a single `bundle.json` from a stable,
installation/Node-owned ConfigMap. The bundle is limited to 900 KiB; larger
updates are rejected before publication. The delivery Pod has no service-account
token and mounts no TLS private keys. Every five seconds it validates the complete
projection and atomically replaces each changed file, with file and directory
fsync, under:

```text
<hostPath>/<namespace>/<name>/<Yanet UID>/<Node UID>/.gateway-policy/
  identities.yaml
  permissions.yaml
  ssh-ca.pub       # optional
```

All gateway roles and NUMA instances on that Node read this directory. Changes
to accounts, grants or SSH trust preserve existing gateway and policy-delivery
Deployment specs and do not wait for certificate issuance or `updateWindow`.
The writer readiness probe compares local files with its current projection.
This confirms delivery, not gateway activation.

Identity and permission refreshes are independent. Mixed old/new data is possible
during updates, including after an interrupted publication. There is no policy
revision, multi-file transaction or rollback protection. Invalid input leaves
local files unchanged; a failed gateway reload retains that source's previous
in-memory data. Updates affect new RPCs, including calls on an existing
connection. Already open streams continue.

Kubelet projection, writer polling and gateway polling each add delay; there is
no guaranteed end-to-end deadline. Check authorization on each serving endpoint
when confirming a revocation.

The gateway's read-only `policy-sync --check` startup gate checks that local
identity and permission files are readable; the gateway validates their contents.
The last delivered files support startup without Kubernetes or a running writer,
even when the controller has newer undelivered data. Recovery uses these files;
restoring an older filesystem backup can restore older grants.

`autoSync: false` prevents controller policy writes and `stop: true` freezes
reconciliation. A running writer can still deliver an already-published bundle.
Disabling the installation drains workloads while preserving local files.
Drain and retire readers and offline startup definitions before retiring the
installation.

Clients using the gateway do not need policy reloads. Direct backend listeners
are outside gateway authorization and need their own access boundary.

### Human SSH certificate authentication

`sshTrustBundle` references a non-optional ConfigMap key in each installation
namespace. It contains bare OpenSSH ECDSA P-256 CA public keys, one per line;
blank lines and comments are allowed. Supply this ConfigMap independently or
through the chart's `extraManifests`. It is separate from the X.509 `trustBundle`.
The controller includes the validated public bytes in the policy bundle;
invalid or unavailable replacement input preserves the last delivered files.

Configure the native gateway alongside its X.509 authenticator from first start:

```yaml
authenticators:
  - type: sshcert
    config:
      ca_sources: [/var/lib/yanet2/gateway-policy/ssh-ca.pub]
      refresh_interval: 5m
```

The SSH authenticator derives the login from the certificate's first principal.
These are gateway accounts, not operating-system or Kubernetes roles. They also
apply to another enabled authenticator that resolves the same login.

SSH CA polling is independent of identity and permission polling (upstream
default: five minutes). Rotation to another valid, nonempty bundle is supported.
Removing `sshTrustBundle` removes the local file, but upstream retains an already
loaded CA on missing/empty input; this is **not a live trust revocation**.
Use account/grant removal to deny new RPCs. KRL distribution is a separate
native-authenticator policy.

When changing both grants and trust, use intermediate grants safe under both
trust sets and verify effective authorization before rotating the CA. Independent
reloads do not establish a common activation point. Removing an authenticator
requires a runtime configuration change and corresponding offline startup
configuration; a checkpoint still referencing a removed CA file can fail startup.

## Gateway listeners

For a managed controlplane server profile the reserved gRPC port is assigned to
`gateway.server.grpc.tls.endpoint` and the plaintext gRPC listener is disabled.
The native file must configure key pairs, authenticators and any HTTP listener.
Without that profile, the native file or explicit palette environment owns both
gRPC listener endpoints; configure the selected transport on `[::]:8080` for the
Service's Pod IP target. A runtime default bound to `[::1]:8080` is not reachable
through that Service.

Plaintext-only listener fragment:

```yaml
gateway:
  server:
    grpc:
      endpoint: '[::]:8080'
```

Externally provisioned TLS-only listener fragment:

```yaml
gateway:
  server:
    grpc:
      tls:
        endpoint: '[::]:8080'
        key_pairs:
          - type: file
            config:
              cert_file: /etc/yanet2/external-tls/tls.crt
              key_file: /etc/yanet2/external-tls/tls.key
```

Keep HTTP bind in the file when needed (`gateway.server.http.endpoint` for a
plaintext HTTP listener). Do not use `YANET_GATEWAY_SERVER_HTTP_ENDPOINT`:
the loader rejects ambiguity with the deprecated HTTP key. These listener
fragments do not configure authentication or change HTTP transport.

Clients must explicitly configure their supported TLS fields and bounded
reconnect behavior. With a managed BIRD adapter client profile, its route-import
endpoint targets the first active node-local controlplane gateway; configure
`route_operator_tls` accordingly. Callback listeners need their own transport and
access boundary.
