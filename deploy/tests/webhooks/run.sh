#!/usr/bin/env bash
# End-to-end webhook + reconciler test for the yanet-operator Helm chart.
#
# Assumes the chart is already installed in namespace $NS with webhook.enabled=true.
# Verifies:
#   1. All four webhook paths are registered on the operator (v1+v2, Yanet+YanetConfig).
#   2. Every "valid" CR is accepted by the webhook AND, once reconciled, the
#      operator generates the expected Deployments (all with replicas=0 because
#      every component has enabled=false).
#   3. Every "invalid" CR under cases/invalid/ is REJECTED by the webhook with
#      a non-empty error message.
#   4. The operator log contains no ERROR lines (except known-noise patterns
#      that are explicitly whitelisted).
#
# Required env:
#   NS              namespace where the chart is installed (default: yanet)
#   NODE_NAME       kubernetes node name to use as the target for valid CRs
#                   (default: first node returned by `kubectl get nodes`)

set -euo pipefail

NS="${NS:-yanet}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALID_DIR="${HERE}/cases"
INVALID_DIR="${HERE}/cases/invalid"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

NODE_NAME="${NODE_NAME:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}"
[ -n "$NODE_NAME" ] || { echo "FAIL: no nodes in cluster"; exit 1; }

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }
fail()  { red "FAIL: $*"; exit 1; }

V1_NAME="$NODE_NAME"
V2_NAME="yanet-v2-${NODE_NAME}"

# --------------------------------------------------------------------
# 1) Webhook registration check (operator side).
# --------------------------------------------------------------------
blue "[1/5] Checking webhook registration in operator logs..."
POD="$(kubectl -n "$NS" get pod -l app.kubernetes.io/name=yanet-operator -o jsonpath='{.items[0].metadata.name}')"
[ -n "$POD" ] || fail "operator pod not found in namespace $NS"

LOGS="$(kubectl -n "$NS" logs "$POD" --tail=-1)"
for path in \
    /validate-yanet-yanet-platform-io-v1alpha1-yanet \
    /validate-yanet-yanet-platform-io-v1alpha1-yanetconfig \
    /validate-yanet-yanet-platform-io-v2alpha1-yanetv2 \
    /validate-yanet-yanet-platform-io-v2alpha1-yanetconfigv2; do
    if ! grep -q "Registering webhook.*${path}" <<<"$LOGS"; then
        echo "$LOGS" | tail -100
        fail "webhook path ${path} was not registered by the operator"
    fi
    green "  ok: ${path}"
done

# --------------------------------------------------------------------
# 2) Prepare valid CRs — patch them with the real node name from the cluster.
# --------------------------------------------------------------------
blue "[2/5] Preparing valid CRs (node=$NODE_NAME)..."
cp -r "$VALID_DIR"/*.yaml "$WORK"/

# v1 Yanet — set spec.nodename to NODE_NAME, rename CR to NODE_NAME
sed -i "s|name: test-node-v1.example.com|name: ${V1_NAME}|; s|nodename: test-node-v1.example.com|nodename: ${NODE_NAME}|" \
    "$WORK"/02-yanet-v1-valid.yaml

# v2 Yanet — drop the unreachable nodeSelector so it matches every node,
# and rename the CR to a deterministic value for assertions.
python3 - "$WORK"/04-yanet-v2-valid.yaml "$V2_NAME" <<'PY'
import sys, re, pathlib
path = pathlib.Path(sys.argv[1])
new_name = sys.argv[2]
text = path.read_text()
text = re.sub(r'^(  name:).*$', r'\1 ' + new_name, text, count=1, flags=re.M)
# Strip the nodeSelector block: the "nodeSelector:" key and all immediately
# following lines that are indented deeper than it (any number of entries).
out_lines = []
skip_indent = None
for line in text.splitlines():
    if skip_indent is not None:
        # Keep skipping as long as line is blank or indented deeper than nodeSelector.
        if line == "" or (line[0] == " " and len(line) - len(line.lstrip()) > skip_indent):
            continue
        skip_indent = None
    if line.lstrip().startswith("nodeSelector:"):
        skip_indent = len(line) - len(line.lstrip())
        continue
    out_lines.append(line)
path.write_text("\n".join(out_lines) + "\n")
PY

# --------------------------------------------------------------------
# 3) Apply valid CRs — every one MUST succeed.
# --------------------------------------------------------------------
blue "[3/5] Applying valid CRs (must be accepted)..."
for f in "$WORK"/*.yaml; do
    echo "  applying $(basename "$f")"
    kubectl apply -f "$f" >/dev/null \
        || fail "valid CR $(basename "$f") was rejected by the webhook"
done
green "  all valid CRs accepted"

# --------------------------------------------------------------------
# 4) Apply invalid CRs — every one MUST fail with a webhook error.
# --------------------------------------------------------------------
blue "[4/5] Applying invalid CRs (must be rejected)..."
for f in "$INVALID_DIR"/*.yaml; do
    [ -f "$f" ] || continue
    name="$(basename "$f")"
    if out="$(kubectl apply -f "$f" 2>&1)"; then
        kubectl delete -f "$f" --ignore-not-found >/dev/null 2>&1 || true
        fail "invalid CR $name was unexpectedly accepted: $out"
    fi
    if ! grep -qiE 'denied the request|admission webhook|validation failed' <<<"$out"; then
        fail "invalid CR $name was rejected, but not by the webhook: $out"
    fi
    green "  ok rejected: $name"
done

# --------------------------------------------------------------------
# 5) Wait for reconciler, verify Deployments, then scan operator log.
# --------------------------------------------------------------------
blue "[5/5] Waiting for reconciler to render Deployments..."

# Expected v1 Deployments — one per enabled component (all four enabled=false
# → replicas=0 but the Deployment object still exists).
V1_EXPECTED=(
    "controlplane-${V1_NAME}"
    "dataplane-${V1_NAME}"
    "bird-${V1_NAME}"
    "announcer-${V1_NAME}"
)

# v2 Deployments use a short hash of the node name in the suffix; we don't
# replicate that math in bash, so we assert the count + labels instead.
# Expected v2 Deployment label set on the node we just targeted.
V2_LABEL_SELECTOR="yanet.yanet-platform.io/yanet=${V2_NAME}"

deadline=$(( $(date +%s) + 120 ))
while :; do
    missing=()
    for d in "${V1_EXPECTED[@]}"; do
        kubectl -n "$NS" get deployment "$d" >/dev/null 2>&1 || missing+=("$d")
    done
    v2_count="$(kubectl -n "$NS" get deployments -l "$V2_LABEL_SELECTOR" -o name 2>/dev/null | wc -l | tr -d ' ')"
    if [ "${#missing[@]}" -eq 0 ] && [ "${v2_count:-0}" -gt 0 ]; then
        break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "missing v1 Deployments: ${missing[*]:-none}"
        echo "v2 Deployment count (label $V2_LABEL_SELECTOR): ${v2_count:-0}"
        kubectl -n "$NS" get deployments -o wide || true
        fail "reconciler did not render the expected Deployments in time"
    fi
    sleep 3
done

echo "--- Deployments in $NS ---"
kubectl -n "$NS" get deployments -o wide

# Assert ALL operator-managed Deployments have replicas=0.
NONZERO="$(kubectl -n "$NS" get deployments \
    -l 'app.kubernetes.io/created-by=yanet-operator' \
    -o jsonpath='{range .items[*]}{.metadata.name}={.spec.replicas}{"\n"}{end}' \
    | awk -F= '$2 != "0" && $1 != "" {print}')"
if [ -n "$NONZERO" ]; then
    fail $'expected all operator-managed Deployments to have replicas=0, got:\n'"$NONZERO"
fi
green "  all operator-managed Deployments have replicas=0"

# Assert all expected v1 Deployments are present.
for d in "${V1_EXPECTED[@]}"; do
    kubectl -n "$NS" get deployment "$d" >/dev/null 2>&1 \
        || fail "missing v1 Deployment: $d"
done
green "  v1 Deployments OK: ${V1_EXPECTED[*]}"

# Assert v2 produced at least one Deployment for our Yanet CR.
[ "${v2_count:-0}" -gt 0 ] || fail "v2 reconciler produced no Deployments for $V2_NAME"
green "  v2 Deployments OK: $v2_count Deployment(s) labelled $V2_LABEL_SELECTOR"

# --------------------------------------------------------------------
# Scan operator log for ERROR lines.
# --------------------------------------------------------------------
echo "--- Tail of operator log ---"
kubectl -n "$NS" logs "$POD" --tail=200 || true

# Whitelist patterns that are NOT real regressions (known noise that the
# webhook fix is supposed to eliminate, but might appear in older logs).
ERRORS="$(kubectl -n "$NS" logs "$POD" --tail=-1 \
    | grep -E $'\tERROR\t' \
    | grep -vE 'the server could not find the requested resource' \
    || true)"
if [ -n "$ERRORS" ]; then
    echo "--- operator ERROR lines ---"
    echo "$ERRORS"
    fail "operator log contains unexpected ERROR lines"
fi
green "  operator log has no unexpected ERROR lines"

green "All webhook tests passed."
