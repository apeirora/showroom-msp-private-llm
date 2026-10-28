#!/usr/bin/env python3
"""Verify policy delivery and immutable rollout bindings in rendered/package charts."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/private-llm-operator"
POLICY = {"cutover": "2026-10-28T16:00:00Z", "entries": [
    {"uid": "7d249eb7-e422-4225-a7f1-6edb0a2a9e24", "namespace": "tenant-a", "name": "model", "mode": "legacy", "slug": "legacy-a"}]}


def render(chart, values):
    with tempfile.TemporaryDirectory() as tmp:
        f = Path(tmp) / "values.json"
        f.write_text(json.dumps(values))
        return subprocess.run(["helm", "template", "policy-check", str(chart), "--namespace", "private-llm", "-f", str(f)], capture_output=True, text=True, check=False)


def objects(result):
    if result.returncode != 0:
        raise RuntimeError(result.stderr)
    return [o for o in yaml.safe_load_all(result.stdout) if o]


def values(policy):
    return {"slugPolicy": policy, "ocm": {"enabled": False}, "traefik": {"enabled": False}}


class PolicyDelivery(unittest.TestCase):
    def check_policy(self, result, expected):
        docs = objects(result)
        dep = next(o for o in docs if o["kind"] == "Deployment" and o["metadata"]["name"] == "private-llm-controller-manager")
        pod = dep["spec"]["template"]
        manager = next(c for c in pod["spec"]["containers"] if c["name"] == "manager")
        self.assertEqual(dep["spec"]["replicas"], 1)
        self.assertEqual(dep["spec"]["strategy"]["type"], "Recreate")
        self.assertIsNone(dep["spec"]["strategy"].get("rollingUpdate"))
        self.assertIn("--slug-policy-file=/etc/private-llm-slugs/policy.json", manager["args"])
        mount = next(m for m in manager["volumeMounts"] if m["name"] == "slug-policy")
        self.assertEqual(mount, {"name": "slug-policy", "mountPath": "/etc/private-llm-slugs", "readOnly": True})
        volume = next(v for v in pod["spec"]["volumes"] if v["name"] == mount["name"])
        cm = next(o for o in docs if o["kind"] == "ConfigMap" and o["metadata"]["name"] == volume["configMap"]["name"])
        self.assertEqual(cm["metadata"]["namespace"], dep["metadata"]["namespace"])
        self.assertEqual(json.loads(cm["data"]["policy.json"]), expected)
        return pod["metadata"]["annotations"]["checksum/slug-policy"]

    def test_exact_policy_reaches_manager(self):
        self.check_policy(render(CHART, values(POLICY)), POLICY)

    def test_policy_change_requires_new_pod(self):
        changed = copy.deepcopy(POLICY)
        changed["entries"][0]["slug"] = "reviewed-b"
        self.assertNotEqual(self.check_policy(render(CHART, values(POLICY)), POLICY), self.check_policy(render(CHART, values(changed)), changed))

    def test_schema_rejects_mistyped_or_incomplete_identity(self):
        bad = []
        for key, value in [("mode", "adopt"), ("uid", ""), ("unknown", "typo")]:
            policy = copy.deepcopy(POLICY)
            policy["entries"][0][key] = value
            bad.append(policy)
        policy = copy.deepcopy(POLICY); del policy["entries"][0]["slug"]; bad.append(policy)
        policy = copy.deepcopy(POLICY); policy["entries"][0]["mode"] = "derive"; bad.append(policy)
        policy = copy.deepcopy(POLICY); policy["entries"] = "not-a-list"; bad.append(policy)
        for policy in bad:
            with self.subTest(policy=policy):
                result = render(CHART, values(policy))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("slugPolicy", result.stderr)

    def test_derive_entry_has_no_legacy_slug(self):
        policy = copy.deepcopy(POLICY)
        policy["entries"][0]["mode"] = "derive"
        del policy["entries"][0]["slug"]
        self.check_policy(render(CHART, values(policy)), policy)

    def test_ocm_forwards_exact_policy(self):
        docs = objects(render(ROOT / "charts/private-llm-operator-ocm", {"operator": {"slugPolicy": POLICY}}))
        deployer = next(o for o in docs if o["kind"] == "FluxDeployer")
        self.assertEqual(deployer["spec"]["helmReleaseTemplate"]["values"]["slugPolicy"], POLICY)

    def test_packaged_chart_keeps_policy_mount_and_schema(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = subprocess.run(["helm", "package", str(CHART), "--destination", tmp], capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            archives = list(Path(tmp).glob("*.tgz")); self.assertEqual(len(archives), 1)
            self.check_policy(render(archives[0], values(POLICY)), POLICY)
            invalid = copy.deepcopy(POLICY); invalid["entries"][0]["mode"] = "adopt"
            self.assertNotEqual(render(archives[0], values(invalid)).returncode, 0)


if __name__ == "__main__":
    unittest.main()
