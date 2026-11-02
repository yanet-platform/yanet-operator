# Managed workload certificates

This opt-in integration requires cert-manager's `cert-manager.io/v1` CRDs.
Signing, approval and renewal remain cert-manager responsibilities. The YANET
manager creates `Certificate` resources, never signs CSR or reads TLS Secrets.
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
  hostPath: /var/lib/yanet/credentials
  exporterImage: example.test/yanet-operator@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  duration: 87600h
  renewBefore: 480h
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

The initial profile requests `87600h` (3650 days, ten 365-day years), with renewal
starting `480h` (20 days) before expiry. Confirm the issuer policy permits that
lifetime and the issuing CA chain remains valid for the intended period.

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

## Issuance, delivery and loading

For every selected node a separate, node-pinned `credential-delivery` Deployment
projects only its required leaf Secrets and an independently managed public
trust ConfigMap. It runs one exporter per role, without a service-account token,
with read-only source mounts and write access only to that role's host directory.
The root exporter drops capabilities and uses a read-only root filesystem.
Persistent files are root-owned; consumers currently need UID 0 to read them.
The delivery Deployment uses `Recreate`, preventing overlapping writers during
policy changes.

The filesystem layout is:

```text
<hostPath>/<namespace>/<Yanet name>/<Yanet UID>/<Node UID>/<profile>/
  <generation digest>/{tls.crt,tls.key,ca.crt}
  current -> <generation digest>
```

Only the role directory is mounted read-only at its `mountPath` in a consumer.
The exporter pins one kubelet `..data` generation, validates key/cert match,
identity, DNS SANs, EKU, chain and validity, fsyncs complete files/directories and
atomically switches `current`. Directories are `0700`, files `0600`. Invalid or
unavailable replacement material leaves the previous generation intact. Its
readiness probe validates the installed generation, not just Secret existence.
First consumer creation and spec updates wait for both Certificate Ready at its
current generation and a current, ready delivery Deployment. Explicit drain and
cleanup do not wait. Existing consumers are preserved during an issuance outage.

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

For a managed controlplane server profile the reserved gRPC port is assigned to
`gateway.server.grpc.tls.endpoint` and the plaintext gRPC listener is disabled.
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
