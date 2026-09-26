# Validation webhooks

The operator registers two validators for `yanet.yanet-platform.io/v1alpha1`:

| Kind | Webhook | Service path |
| --- | --- | --- |
| `Yanet` | `vyanet.kb.io` | `/validate-yanet-yanet-platform-io-v1alpha1-yanet` |
| `YanetConfig` | `vyanetconfig.kb.io` | `/validate-yanet-yanet-platform-io-v1alpha1-yanetconfig` |

Both handle CREATE and UPDATE. Delete admission does not prevent cleanup.

## Yanet

- Requires a valid `boxType`; it cannot change after creation.
- Validates installation/component names and typed overrides.
- When the cluster-wide palette is available, checks box-type references,
  component wiring, container names and effective NUMA/network overrides.
- When the palette is absent, returns a bootstrap warning; the reconciler waits
  for configuration before applying workloads.

```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: worker-1
  namespace: yanet
spec:
  boxType: release
  nodeSelector:
    kubernetes.io/hostname: worker-1
  autoSync: true
```

## YanetConfig

The resource is cluster-scoped and must be named `config`. Validation covers:

- Nonnegative, bounded `updateWindow`.
- Unique patch, box-type, role and container names.
- Required controlplane/dataplane wiring and valid component/patch references.
- Image and configuration-source shape, hugepage quantities and NUMA settings.
- Ordered atomic sidecars, typed network attachments and private networking.
- Strategic-merge patch dry-runs and effective configuration constraints.

See the [full palette example](deploy/examples/v1alpha1-yanetconfig-full.yaml)
and [architecture](ARCHITECTURE.md).

## Helm installation

Webhooks are enabled by default. The certificate generation hook creates a TLS
Secret; the post-install/post-upgrade hook patches the CA bundle into the
ValidatingWebhookConfiguration.

```yaml
webhook:
  enabled: true
  port: 9443
  failurePolicy: Ignore
```

Chart-managed `yanetconfig` requires `failurePolicy: Ignore` because Helm applies
normal resources before the post-install CA job. To use `Fail`, manage
`YanetConfig/config` separately after the webhook is ready. Rejected requests
still fail under `Ignore`; that policy only permits requests when the webhook
cannot be reached.

Setting `webhook.enabled: false` disables the server flag, webhook configuration,
certificate mounts and certificate jobs. Structural CRD validation still applies.

Set `webhook.certManager.enabled: true` to use an already installed cert-manager v1
with its CA injector instead of certgen hooks. The chart creates a namespaced
self-signed Issuer and Certificate and annotates the webhook configuration for CA
injection. cert-manager owns certificate renewal. Wait for Certificate readiness,
operator rollout and populated CA bundles before relying on admission; `Ignore`
also permits requests during asynchronous issuance/injection. Switching certificate
providers on an existing release requires a coordinated certificate/Secret handover;
it is not an automatic migration of an existing certgen Secret.

The standalone Kustomize/release installer always uses cert-manager; see
[installer prerequisites](README_RELEASES.md#standalone-installer-prerequisites).

## Troubleshooting

Inspect the webhook Service, operator logs, TLS Secret and CA configuration in
the namespace where the chart is installed:

```bash
kubectl get svc,secret -n yanet-system
kubectl logs -n yanet-system deployment/yanet-operator
kubectl get validatingwebhookconfiguration yanet-operator-validating-webhook-configuration -o yaml
```

A connection error is not evidence of a validation rejection. Check endpoints,
certificates and `caBundle` before investigating admission rules.

## Tests and sources

- `api/v1alpha1/yanet_webhook.go` and `yanetconfig_webhook.go`: typed validators.
- `internal/controller/webhook_test.go` and `yanet_webhook_e2e_test.go`: real
  envtest admission requests through the configured Service paths.
- `deploy/tests/webhooks/run.sh`: existing Helm CI admission and reconcile smoke.
- `config/webhook/manifests.yaml`: generated from API markers.
- `deploy/charts/yanet-operator/templates/webhook-configuration.yaml`: Helm routes.
