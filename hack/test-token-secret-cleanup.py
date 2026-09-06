"""Check the opt-in token Secret cleanup contract (requires Helm and PyYAML)."""

from pathlib import Path
import subprocess
import unittest

import yaml


CHART = Path(__file__).resolve().parents[1] / "charts/private-llm-sync-agent"


def render(*values):
    command = ["helm", "template", "test", str(CHART), "--include-crds"]
    for value in values:
        command.extend(["--set", value])
    return [doc for doc in yaml.safe_load_all(subprocess.check_output(command)) if doc]


def resource(documents, kind, name):
    return next(doc for doc in documents
                if doc["kind"] == kind and doc["metadata"]["name"] == name)


class TokenSecretCleanupTest(unittest.TestCase):
    def test_agent_upgrade_preserves_deployment_identity(self):
        documents = render()
        deployment = resource(documents, "Deployment", "test")
        pod = deployment["spec"]["template"]["spec"]
        self.assertEqual(pod["serviceAccountName"], "test")
        self.assertEqual(deployment["spec"]["selector"]["matchLabels"], {
            "app.kubernetes.io/name": "kcp-api-syncagent",
            "app.kubernetes.io/instance": "llm-agent",
        })
        self.assertEqual(pod["containers"][0]["image"],
                         "ghcr.io/kcp-dev/api-syncagent:v0.7.0")
        binding = next(doc for doc in documents if doc["kind"] == "RoleBinding"
                       and doc["metadata"]["name"].endswith(":leaderelection"))
        self.assertEqual(binding["roleRef"]["kind"], "Role")
        resource(documents, "Role", binding["roleRef"]["name"])
        self.assertEqual(binding["subjects"][0]["name"], pod["serviceAccountName"])

    def test_cleanup_is_opt_in_and_scoped_to_token_copies(self):
        default = render()
        enabled = render("publishedResources.tokenSecretCleanup=true")
        name = "publish-llm-apitokenrequests"
        original = resource(default, "PublishedResource", name)
        changed = resource(enabled, "PublishedResource", name)
        related = changed["spec"]["related"]
        self.assertEqual(len(related), 1)
        self.assertEqual(related[0]["identifier"], "token-secret")
        self.assertEqual(related[0]["origin"], "service")
        self.assertIs(related[0].pop("cleanup"), True)
        self.assertEqual(changed, original)
        name = "publish-llm-llminstances"
        self.assertEqual(resource(default, "PublishedResource", name),
                         resource(enabled, "PublishedResource", name))

    def test_bundled_schema_accepts_cleanup(self):
        documents = render("publishedResources.tokenSecretCleanup=true")
        crd = resource(documents, "CustomResourceDefinition",
                       "publishedresources.syncagent.kcp.io")
        version = next(v for v in crd["spec"]["versions"] if v["name"] == "v1alpha1")
        spec = version["schema"]["openAPIV3Schema"]["properties"]["spec"]
        cleanup = spec["properties"]["related"]["items"]["properties"]["cleanup"]
        self.assertEqual(cleanup["type"], "boolean")


if __name__ == "__main__":
    unittest.main()
