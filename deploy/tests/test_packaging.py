"""Render real deployment artifacts and validate their cross-resource contracts."""

import json
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


def bound_rules(docs, account, namespace):
    """Resolve chart RBAC bindings for a Pod's actual service-account identity."""
    identity = f"system:serviceaccount:{namespace}:{account}"
    groups = {"system:serviceaccounts", f"system:serviceaccounts:{namespace}", "system:authenticated"}
    rules = []
    for binding in docs:
        if binding["kind"] not in ("RoleBinding", "ClusterRoleBinding"):
            continue
        binding_namespace = binding["metadata"].get("namespace", namespace)
        matches = any(
            (subject["kind"] == "ServiceAccount" and subject["name"] == account
             and subject.get("namespace", binding_namespace) == namespace)
            or (subject["kind"] == "User" and subject["name"] == identity)
            or (subject["kind"] == "Group" and subject["name"] in groups)
            for subject in binding.get("subjects", [])
        )
        if not matches:
            continue
        reference = binding["roleRef"]
        roles = [
            doc for doc in docs
            if doc["kind"] == reference["kind"] and doc["metadata"]["name"] == reference["name"]
            and (doc["kind"] == "ClusterRole"
                 or doc["metadata"].get("namespace", namespace) == binding_namespace)
        ]
        assert len(roles) == 1, f"unresolved role reference: {reference}"
        rules.extend(roles[0].get("rules", []))
    return rules


def secret_rules(rules):
    return [
        rule for rule in rules
        if {"", "*"}.intersection(rule.get("apiGroups", []))
        and any(resource in ("secrets", "*") or resource.startswith("secrets/")
                for resource in rule.get("resources", []))
        and rule.get("verbs")
    ]


class PackagingTest(unittest.TestCase):
    def test_argocd_runtime_resources_with_fail_closed_admission(self):
        docs = chart(
            "webhook.enabled=true", "webhook.failurePolicy=Fail",
            "webhook.certManager.enabled=true", "runtimeResources.argoCD=true",
            "yanetconfig.spec.updateWindow=600", "yanets.example.boxType=release",
            "yanets.example.enabled=false",
        )
        palette = one(docs, "YanetConfig")
        installation = one(docs, "Yanet")
        self.assertEqual(palette["metadata"]["name"], "config")
        self.assertNotIn("namespace", palette["metadata"])
        self.assertEqual(palette["spec"], {"updateWindow": 600})
        self.assertEqual(installation["metadata"]["name"], "example")
        self.assertEqual(installation["metadata"]["namespace"], "custom-system")
        self.assertEqual(installation["spec"], {"boxType": "release", "enabled": False})
        webhook = one(docs, "ValidatingWebhookConfiguration")
        self.assertEqual({entry["failurePolicy"] for entry in webhook["webhooks"]}, {"Fail"})

        def wave(resource):
            return int(resource["metadata"].get("annotations", {}).get("argocd.argoproj.io/sync-wave", "0"))

        for prerequisite in (one(docs, "Deployment"), one(docs, "Certificate"), webhook):
            self.assertLess(wave(prerequisite), wave(palette))
        self.assertLess(wave(palette), wave(installation))

    def test_fail_closed_runtime_resources_require_argocd_and_cert_manager(self):
        webhook = ("webhook.enabled=true", "webhook.failurePolicy=Fail")
        for resource in ("yanetconfig.spec.updateWindow=600", "yanets.example.boxType=release"):
            with self.subTest(resource=resource):
                with self.assertRaisesRegex(AssertionError, "require an ordered Argo CD sync"):
                    chart(*webhook, resource, "webhook.certManager.enabled=true")
                with self.assertRaisesRegex(AssertionError, "require webhook.certManager.enabled"):
                    chart(*webhook, resource, "runtimeResources.argoCD=true")

        docs = chart(*webhook)
        self.assertFalse({doc["kind"] for doc in docs} & {"YanetConfig", "Yanet"})

    def test_gateway_ssh_trust_and_human_accounts(self):
        values = {
            "manageCertificates": True,
            "yanetconfig": {"spec": {"certificates": {
                "sshTrustBundle": {"name": "gateway-ssh-trust", "key": "ssh-ca.pub"},
                "additionalGatewayAccounts": [
                    {"username": "root", "permissions": ["/*/*"]},
                    {"username": "alice", "permissions": ["/example.v1.Service/Read"]},
                ],
            }}},
            "extraManifests": {"sshTrust": {
                "apiVersion": "v1", "kind": "ConfigMap",
                "metadata": {"name": "gateway-ssh-trust", "namespace": "custom-system"},
                "data": {"ssh-ca.pub": "public-ssh-ca-fixture\n"},
            }},
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "values.yaml"
            source.write_text(yaml.safe_dump(values))
            docs = render(shlex.split(os.environ.get("HELM", "helm")) + [
                "template", "test", "deploy/charts/yanet-operator", "--namespace", "custom-system", "-f", str(source),
            ])
        certificates = one(docs, "YanetConfig")["spec"]["certificates"]
        self.assertEqual(certificates["additionalGatewayAccounts"], [
            {"username": "root", "permissions": ["/*/*"]},
            {"username": "alice", "permissions": ["/example.v1.Service/Read"]},
        ])
        reference = certificates["sshTrustBundle"]
        trust = next(doc for doc in docs if doc["kind"] == "ConfigMap" and doc["metadata"]["name"] == reference["name"])
        self.assertEqual(trust["metadata"]["namespace"], "custom-system")
        self.assertEqual(trust["data"][reference["key"]], "public-ssh-ca-fixture\n")
        self.assertIn("--manage-certificates", pod(docs)["containers"][0]["args"])

    def test_bundled_cert_manager(self):
        docs = chart("cert-manager.enabled=true", "webhook.enabled=false")
        deployments = {doc["metadata"]["name"] for doc in docs if doc["kind"] == "Deployment"}
        self.assertTrue({
            "test-cert-manager", "test-cert-manager-cainjector", "test-cert-manager-webhook",
        } <= deployments, deployments)
        crds = {doc["metadata"]["name"] for doc in docs if doc["kind"] == "CustomResourceDefinition"}
        self.assertTrue({
            "certificates.cert-manager.io", "certificaterequests.cert-manager.io",
            "issuers.cert-manager.io", "clusterissuers.cert-manager.io",
        } <= crds, crds)

    def test_external_cert_manager(self):
        docs = chart("cert-manager.enabled=false", "manageCertificates=true")
        self.assertIn("--manage-certificates", pod(docs)["containers"][0]["args"])
        self.assertFalse(any(doc["kind"] == "CustomResourceDefinition" for doc in docs))
        self.assertFalse(any("cert-manager" in doc["metadata"]["name"] for doc in docs if doc["kind"] == "Deployment"))

    def test_workload_certificate_opt_in(self):
        for enabled in (False, True):
            with self.subTest(enabled=enabled):
                docs = chart(f"manageCertificates={str(enabled).lower()}")
                args = pod(docs)["containers"][0]["args"]
                self.assertEqual("--manage-certificates" in args, enabled)
                role = next(d for d in docs if d["kind"] == "ClusterRole" and d["metadata"]["name"] == "manager-role")
                certificate_rules = [r for r in role["rules"] if "cert-manager.io" in r["apiGroups"]]
                self.assertEqual(bool(certificate_rules), enabled)
                for rule in certificate_rules:
                    self.assertEqual(rule["resources"], ["certificates"])
                self.assertFalse(any("secrets" in r["resources"] for r in role["rules"]))

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

    def test_manager_secret_permissions_are_isolated_from_certgen(self):
        for provider, webhook_values in [
            ("disabled", ["webhook.enabled=false"]),
            ("certgen", []),
            ("cert-manager", ["webhook.certManager.enabled=true"]),
        ]:
            for account_values in [
                [],
                ["serviceAccount.name=custom"],
                ["serviceAccount.name=existing", "serviceAccount.create=false"],
                ["serviceAccount.name=", "serviceAccount.create=true"],
                ["serviceAccount.name=", "serviceAccount.create=false"],
            ]:
                for managed in (False, True):
                    with self.subTest(provider=provider, account=account_values, managed=managed):
                        docs = chart(*webhook_values, *account_values,
                                     f"manageCertificates={str(managed).lower()}")
                        namespace = one(docs, "Deployment")["metadata"]["namespace"]
                        account = pod(docs)["serviceAccountName"]
                        rules = bound_rules(docs, account, namespace)
                        self.assertTrue(any("leases" in rule.get("resources", []) for rule in rules))
                        self.assertEqual(secret_rules(rules), [], f"manager {account} can access Secrets")

                        jobs = [doc for doc in docs if doc["kind"] == "Job"]
                        if provider != "certgen":
                            self.assertEqual(jobs, [])
                            continue
                        self.assertEqual(len(jobs), 2)
                        secret_name = next(volume["secret"]["secretName"]
                                           for volume in pod(docs)["volumes"] if "secret" in volume)
                        commands = set()
                        for job in jobs:
                            job_namespace = job["metadata"]["namespace"]
                            workload = job["spec"]["template"]["spec"]
                            certgen_account = workload["serviceAccountName"]
                            self.assertNotEqual(certgen_account, account)
                            self.assertEqual(job_namespace, namespace)
                            self.assertTrue(any(doc["kind"] == "ServiceAccount"
                                                and doc["metadata"]["name"] == certgen_account
                                                and doc["metadata"]["namespace"] == namespace
                                                for doc in docs))
                            access = secret_rules(bound_rules(docs, certgen_account, namespace))
                            self.assertEqual(len(access), 1)
                            self.assertEqual(access[0]["apiGroups"], [""])
                            self.assertEqual(access[0]["resources"], ["secrets"])
                            self.assertEqual(set(access[0]["verbs"]), {"get", "create", "patch", "update"})
                            self.assertTrue(not access[0].get("resourceNames")
                                            or secret_name in access[0]["resourceNames"])
                            args = workload["containers"][0]["args"]
                            commands.add(args[0])
                            self.assertIn(f"--namespace={namespace}", args)
                            self.assertIn(f"--secret-name={secret_name}", args)
                            phase = "pre" if args[0] == "create" else "post"
                            self.assertEqual(set(job["metadata"]["annotations"]["helm.sh/hook"].split(",")),
                                             {f"{phase}-install", f"{phase}-upgrade"})
                        self.assertEqual(commands, {"create", "patch"})

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


class ReleasePipelineTest(unittest.TestCase):
    def test_chart_publication_does_not_overwrite_image(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
        step = next(s for s in workflow["jobs"]["helm"]["steps"] if s["name"] == "Push Helm chart to GitHub Container Registry")
        expressions = {
            "github.repository_owner": "yanet-platform",
            "env.CHART_NAME": "yanet-operator",
            "env.HELM_REPO": workflow["env"].get("HELM_REPO", ""),
            "steps.chart_version.outputs.version": "3.0.2",
        }
        command = step["run"]
        for key, value in expressions.items():
            command = command.replace("${{ " + key + " }}", value)
        command = command.replace("${{ github.repository_owner }}", "yanet-platform")
        with tempfile.TemporaryDirectory() as directory:
            # The destination passed to Helm is the publication contract; no
            # registry write belongs in this deterministic regression test.
            executable = Path(directory) / "helm"
            executable.write_text("#!/usr/bin/env python3\nimport json, sys\nprint(json.dumps(sys.argv[1:]))\n")
            executable.chmod(0o755)
            result = subprocess.run(
                ["bash", "-eo", "pipefail", "-c", command], cwd=directory,
                env={**os.environ, "PATH": directory + ":" + os.environ["PATH"]},
                capture_output=True, text=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout), [
                "push", "yanet-operator-3.0.2.tgz", "oci://ghcr.io/yanet-platform/charts",
            ], "the chart would overwrite the Docker image's 3.0.2 tag")

    def test_chart_and_image_versions_follow_release(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
        steps = workflow["jobs"]["helm"]["steps"]
        with tempfile.TemporaryDirectory() as directory:
            chart_path = Path(directory) / "deploy/charts/yanet-operator/Chart.yaml"
            chart_path.parent.mkdir(parents=True)
            chart_path.write_text('apiVersion: v2\nname: yanet-operator\nversion: 0.2.1\nappVersion: "3.0.1"\n')
            output = Path(directory) / "output"
            for step in steps:
                if step.get("id") not in {"release_version", "chart_version"}:
                    continue
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", step["run"]], cwd=directory,
                    env={**os.environ, "GITHUB_REF": "refs/tags/v3.0.2", "RELEASE_VERSION": "3.0.2", "GITHUB_OUTPUT": str(output)},
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
            metadata = yaml.safe_load(chart_path.read_text())
            self.assertEqual(metadata["version"], "3.0.2")
            self.assertEqual(metadata["appVersion"], "3.0.2")
            self.assertEqual(output.read_text().strip(), "version=3.0.2")


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
