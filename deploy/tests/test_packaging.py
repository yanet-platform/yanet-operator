"""Render real deployment artifacts and validate their cross-resource contracts."""

import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


def render(command):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=90)
    if result.returncode:
        raise AssertionError(result.stderr)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def chart(*values):
    command = shlex.split(os.environ.get("HELM", "helm"))
    command += ["template", "test", "deploy/charts/yanet-operator", "--namespace", "custom-system"]
    for value in values:
        command += ["--set", value]
    return render(command)


def one(docs, kind):
    matches = [doc for doc in docs if doc["kind"] == kind]
    assert len(matches) == 1, f"expected one {kind}, got {len(matches)}"
    return matches[0]


def pod(docs):
    return one(docs, "Deployment")["spec"]["template"]["spec"]


class PackagingTest(unittest.TestCase):
    def assert_certificate_wiring(self, docs):
        certificate = one(docs, "Certificate")
        issuer = one(docs, "Issuer")
        self.assertEqual(certificate["spec"]["issuerRef"]["name"], issuer["metadata"]["name"])
        self.assertEqual(certificate["metadata"]["namespace"], issuer["metadata"]["namespace"])
        webhook = one(docs, "ValidatingWebhookConfiguration")
        self.assertEqual(
            webhook["metadata"]["annotations"]["cert-manager.io/inject-ca-from"],
            f'{certificate["metadata"]["namespace"]}/{certificate["metadata"]["name"]}',
        )
        workload = pod(docs)
        manager = next(c for c in workload["containers"] if c["command"] == ["/manager"])
        mount = next(m for m in manager["volumeMounts"] if m["mountPath"] == "/tmp/k8s-webhook-server/serving-certs")
        volume = next(v for v in workload["volumes"] if v["name"] == mount["name"])
        self.assertEqual(volume["secret"]["secretName"], certificate["spec"]["secretName"])
        self.assertEqual(len(webhook["webhooks"]), 2)
        for hook in webhook["webhooks"]:
            reference = hook["clientConfig"]["service"]
            service = next(d for d in docs if d["kind"] == "Service" and d["metadata"]["name"] == reference["name"])
            self.assertEqual(reference["namespace"], service["metadata"]["namespace"])
            self.assertIn(f'{reference["name"]}.{reference["namespace"]}.svc', certificate["spec"]["dnsNames"])
            labels = one(docs, "Deployment")["spec"]["template"]["metadata"]["labels"]
            self.assertTrue(service["spec"]["selector"].items() <= labels.items())
            self.assertEqual(service["spec"]["ports"][0]["port"], reference.get("port", 443))
            target = service["spec"]["ports"][0]["targetPort"]
            port_field = "containerPort" if isinstance(target, int) else "name"
            self.assertIn(target, [p[port_field] for p in manager["ports"]])

    def test_installer_tls(self):
        docs = render([os.environ["KUSTOMIZE"], "build", "config/default"])
        self.assert_certificate_wiring(docs)
        self.assertEqual(one(docs, "Namespace")["metadata"]["name"], "yanet-operator-system")

    def test_cert_manager(self):
        docs = chart("webhook.certManager.enabled=true", "fullnameOverride=custom-operator")
        self.assert_certificate_wiring(docs)
        self.assertFalse(any(d["kind"] == "Job" for d in docs))

    def test_service_accounts(self):
        for values, expected, created in [
            ([], "controller-manager", True),
            (["serviceAccount.name=custom"], "custom", True),
            (["serviceAccount.name=existing", "serviceAccount.create=false"], "existing", False),
            (["serviceAccount.name=", "serviceAccount.create=true"], "yanet-operator", True),
            (["serviceAccount.name=", "serviceAccount.create=false"], "default", False),
        ]:
            with self.subTest(values=values):
                docs = chart(*values)
                self.assertEqual(pod(docs)["serviceAccountName"], expected)
                accounts = [d["metadata"]["name"] for d in docs if d["kind"] == "ServiceAccount" and "helm.sh/hook" not in d["metadata"].get("annotations", {})]
                self.assertEqual(accounts, [expected] if created else [])
                for binding in docs:
                    if binding["kind"] in ("RoleBinding", "ClusterRoleBinding") and "certgen" not in binding["metadata"]["name"]:
                        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": expected, "namespace": "custom-system"}])

    def test_custom_port(self):
        docs = chart("webhook.port=10443")
        manager = pod(docs)["containers"][0]
        self.assertIn("--webhook-port=10443", manager["args"])
        self.assertEqual(manager["ports"][0]["containerPort"], 10443)

    def test_webhook_disabled(self):
        docs = chart("webhook.enabled=false", "webhook.certManager.enabled=true")
        self.assertFalse(any(d["kind"] in ("Certificate", "Issuer", "Job", "ValidatingWebhookConfiguration") for d in docs))
        self.assertIn("--webhook-enabled=false", pod(docs)["containers"][0]["args"])
        self.assertNotIn("volumes", pod(docs))


class ReviewPipelineTest(unittest.TestCase):
    def test_analysis_failures_propagate(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ai-code-review.yml").read_text())
        steps = workflow["jobs"]["ai-review"]["steps"]
        for step_name, failed_tool in [
            ("Run static analysis", "staticcheck"),
            ("Run static analysis", "gosec"),
            ("Run static analysis", "go"),
            ("Race detector check", "make"),
        ]:
            with self.subTest(tool=failed_tool), tempfile.TemporaryDirectory() as directory:
                for tool in ("go", "staticcheck", "gosec", "make"):
                    script = Path(directory) / tool
                    # Tool installation succeeds; the analysis itself fails.
                    script.write_text('#!/bin/sh\n[ "$1" = install ] && exit 0\nexit ' + ("17" if tool == failed_tool else "0") + "\n")
                    script.chmod(0o755)
                step = next(s for s in steps if s["name"] == step_name)
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", step["run"]],
                    cwd=directory, env={**os.environ, "PATH": directory + ":" + os.environ["PATH"]},
                    capture_output=True, text=True, timeout=10,
                )
                self.assertNotEqual(result.returncode, 0, f"{failed_tool} failure was reported as success")

    def test_final_gate(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ai-code-review.yml").read_text())
        steps = workflow["jobs"]["ai-review"]["steps"]
        gate = next(s for s in steps if s["name"] == "Enforce analysis results")
        for static, race, success in [
            ("success", "success", True),
            ("failure", "success", False),
            ("success", "failure", False),
            ("failure", "failure", False),
            ("skipped", "skipped", True),
        ]:
            with self.subTest(static=static, race=race):
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", gate["run"]],
                    env={**os.environ, "STATIC_OUTCOME": static, "RACE_OUTCOME": race},
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode == 0, success, result.stdout)


if __name__ == "__main__":
    unittest.main()
