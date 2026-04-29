# Validation Webhooks for yanet-operator

## Overview

yanet-operator uses Kubernetes Admission Webhooks to validate `Yanet` and `YanetConfig` resources before creation or update.

## Yanet Validation

### Validation Rules

1. **spec.nodename** — required field, cannot be empty
2. **spec.nodename** — immutable (cannot be changed after creation)
3. **spec.type** — must be either `release` or `balancer`

### Examples

**Valid Yanet:**
```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: yanet-node1
spec:
  nodename: node1.example.com
  type: release
```

**Invalid Yanet (empty nodename):**
```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: yanet-node1
spec:
  nodename: ""  # ❌ Error: spec.nodename cannot be empty
  type: release
```

**Invalid Yanet (wrong type):**
```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: Yanet
metadata:
  name: yanet-node1
spec:
  nodename: node1.example.com
  type: custom  # ❌ Error: spec.type must be either 'release' or 'balancer'
```

**Attempt to change nodename:**
```bash
# Create
kubectl apply -f yanet.yaml  # ✅ OK

# Try to change nodename
# ❌ Error: spec.nodename is immutable
```

## YanetConfig Validation

### Validation Rules

1. **spec.updatewindow** — must be >= 0

### Warnings

Webhook may issue warnings (non-blocking):

1. If `spec.stop = true` — "Stop is enabled - operator will not reconcile resources"
2. If `spec.autodiscovery.enable = true` but `typeuri` is not set
3. If `spec.autodiscovery.enable = true` but `namespace` is not set

### Examples

**Valid YanetConfig:**
```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: YanetConfig
metadata:
  name: global-config
spec:
  updatewindow: 60
  stop: false
```

**Invalid YanetConfig:**
```yaml
apiVersion: yanet.yanet-platform.io/v1alpha1
kind: YanetConfig
metadata:
  name: global-config
spec:
  updatewindow: -10  # ❌ Error: spec.updatewindow must be >= 0
```

## Installation via Helm

Webhook is enabled by default in Helm chart:

```yaml
# values.yaml
webhook:
  enabled: true
  port: 9443
  certManager:
    enabled: false  # Use cert-manager for certificate generation
  certGen:
    image:
      repository: registry.k8s.io/ingress-nginx/kube-webhook-certgen
      tag: v1.5.2
```

### Certificate Generation

By default, `kube-webhook-certgen` is used for automatic certificate generation:

1. **Pre-install/Pre-upgrade hook** — creates TLS certificates in Secret
2. **Post-install/Post-upgrade hook** — updates CA bundle in ValidatingWebhookConfiguration

### Using cert-manager

If you have cert-manager installed:

```yaml
webhook:
  enabled: true
  certManager:
    enabled: true
```

## Disabling Webhook

To disable webhook:

```yaml
webhook:
  enabled: false
```

## Troubleshooting

### Webhook not working

1. Check that webhook service is available:
```bash
kubectl get svc -n yanet-operator yanet-operator-webhook-service
```

2. Check that certificates are created:
```bash
kubectl get secret -n yanet-operator yanet-operator-webhook-certs
```

3. Check ValidatingWebhookConfiguration:
```bash
kubectl get validatingwebhookconfiguration yanet-operator-validating-webhook-configuration -o yaml
```

4. Check operator logs:
```bash
kubectl logs -n yanet-operator deployment/yanet-operator
```

### Error "connection refused"

Ensure that:
- Deployment is running and Ready
- Service is configured correctly
- Certificates are valid

### Error "x509: certificate signed by unknown authority"

CA bundle in ValidatingWebhookConfiguration is not updated. Run post-upgrade hook manually:

```bash
kubectl delete job -n yanet-operator yanet-operator-webhook-update-ca
helm upgrade yanet-operator ./deploy/charts/yanet-operator
```

## Architecture

```
┌─────────────────┐
│  kubectl apply  │
└────────┬────────┘
         │
         ▼
┌─────────────────────────┐
│  Kubernetes API Server  │
└────────┬────────────────┘
         │
         │ Admission Request
         ▼
┌─────────────────────────────────┐
│  ValidatingWebhookConfiguration │
└────────┬────────────────────────┘
         │
         ▼
┌──────────────────────────┐
│  yanet-operator webhook  │
│  (port 9443)             │
└────────┬─────────────────┘
         │
         │ Validate
         ▼
┌──────────────────┐
│  Yanet/          │
│  YanetConfig     │
│  Validator       │
└──────────────────┘
```

## Files

- [`api/v1alpha1/yanet_webhook.go`](api/v1alpha1/yanet_webhook.go) — Yanet validation
- [`api/v1alpha1/yanetconfig_webhook.go`](api/v1alpha1/yanetconfig_webhook.go) — YanetConfig validation
- [`deploy/charts/yanet-operator/templates/webhook-service.yaml`](deploy/charts/yanet-operator/templates/webhook-service.yaml) — Service for webhook
- [`deploy/charts/yanet-operator/templates/webhook-cert-jobs.yaml`](deploy/charts/yanet-operator/templates/webhook-cert-jobs.yaml) — Jobs for certificate generation
- [`deploy/charts/yanet-operator/templates/webhook-configuration.yaml`](deploy/charts/yanet-operator/templates/webhook-configuration.yaml) — ValidatingWebhookConfiguration
- [`config/webhook/manifests.yaml`](config/webhook/manifests.yaml) — Generated webhook manifest
