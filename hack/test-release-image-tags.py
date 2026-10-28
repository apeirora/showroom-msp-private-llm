#!/usr/bin/env python3
"""Exercise release-channel outputs without Docker, credentials or publication."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release-image-tags.sh")
SET_IMAGE = Path(__file__).with_name("set-release-image.py")


class ReleaseImageTags(unittest.TestCase):
    def outputs(self, tag, owner="ApeiroRA"):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "outputs"
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "GITHUB_REF_NAME": tag,
                     "GITHUB_REPOSITORY_OWNER": owner, "GITHUB_OUTPUT": str(output)},
                capture_output=True, text=True, check=False,
            )
            return proc, output.read_text() if output.exists() else ""

    def test_stable_versions_publish_latest(self):
        for tag in ["v2.17.1", "2.17.1", "v3.0.0", "0.1.0"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn("publish_latest=true\n", output)
                self.assertIn("ghcr.io/apeirora/private-llm-controller:latest\n", output)
                self.assertIn("version=" + tag.removeprefix("v") + "\n", output)

    def test_prereleases_never_publish_latest(self):
        for tag in ["v2.17.1-rc.1", "2.17.1-rc.794", "v3.0.0-beta.2", "v2.18.0-dev", "v2.18.0-0"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn("publish_latest=false\n", output)
                self.assertNotIn(":latest", output)
                self.assertIn("ghcr.io/apeirora/private-llm-controller:" + tag.removeprefix("v") + "\n", output)

    def test_invalid_or_unrepresentable_tags_fail_before_output(self):
        for tag in ["latest", "v2.17", "v2.17.1+build.1", "v2.17.1-", "v02.17.1", "v2.17.1-rc.01", "v2.17.1\npublish_latest=true"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(output, "")


class PackagedImageVersion(unittest.TestCase):
    repository = "ghcr.io/apeirora/private-llm-controller"
    values = """PUBLIC_HOST: localhost
image:
  repository: ghcr.io/apeirora/private-llm-controller
  tag: '2.17.0' # operator only
  pullPolicy: IfNotPresent
portalIntegration:
  image:
    repository: nginx
    tag: '1.25'
"""

    def rewrite(self, text, version, repository=None):
        if repository is None:
            repository = self.repository
        with tempfile.TemporaryDirectory() as tmp:
            values = Path(tmp) / "values.yaml"
            values.write_text(text)
            proc = subprocess.run(
                ["python3", str(SET_IMAGE), str(values), version, "--repository", repository],
                capture_output=True, text=True, check=False,
            )
            return proc, values.read_text()

    def test_exact_root_image_updated_preserving_other_content(self):
        for version in ["2.17.1", "2.17.1-rc.1", "3.0.0-0"]:
            with self.subTest(version=version):
                proc, after = self.rewrite(self.values, version)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(
                    after, self.values.replace(
                        "  repository: " + self.repository,
                        '  repository: "' + self.repository + '"',
                    ).replace("  tag: '2.17.0'", f'  tag: "{version}"'),
                )

    def test_fork_image_matches_release_tags_and_preserves_nested_image(self):
        for tag in ["v2.17.1", "v2.17.1-rc.1"]:
            with self.subTest(tag=tag):
                proc, output = ReleaseImageTags().outputs(tag, "Fork-Team")
                self.assertEqual(proc.returncode, 0, proc.stderr)
                image = next(line for line in output.splitlines() if line.startswith("ghcr.io/"))
                repository, version = image.rsplit(":", 1)
                self.assertEqual(repository, "ghcr.io/fork-team/private-llm-controller")
                proc, after = self.rewrite(self.values, version, repository)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn('  repository: "' + repository + '"\n', after)
                self.assertIn('  tag: "' + version + '" # operator only\n', after)
                self.assertNotIn(self.repository, after)
                self.assertIn("    repository: nginx\n    tag: '1.25'\n", after)

    def test_quoted_repository_comments_are_preserved(self):
        for quote in ["'", '"']:
            with self.subTest(quote=quote):
                text = self.values.replace(
                    self.repository, quote + self.repository + quote + " # operator repository",
                )
                fork = "ghcr.io/fork-team/private-llm-controller"
                proc, after = self.rewrite(text, "2.17.1-rc.1", fork)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn('  repository: "' + fork + '" # operator repository\n', after)

    def test_invalid_release_repository_does_not_modify_values(self):
        for repository in [
            "", "ghcr.io/Fork-Team/private-llm-controller", "ghcr.io/-owner/private-llm-controller",
            "ghcr.io/owner-/private-llm-controller", "ghcr.io/" + "a" * 40 + "/private-llm-controller",
            "docker.io/owner/private-llm-controller", "https://ghcr.io/owner/private-llm-controller",
            "ghcr.io/owner/other-image", "ghcr.io/owner/private-llm-controller:2.17.1",
            "ghcr.io/owner/private-llm-controller@sha256:" + "a" * 64,
            "ghcr.io/owner/private-llm-controller\nother: injected",
        ]:
            with self.subTest(repository=repository):
                proc, after = self.rewrite(self.values, "2.17.1-rc.1", repository)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, self.values)

    def test_invalid_release_version_does_not_modify_values(self):
        for version in ["v2.17.1", "2.17.1+build.1", "2.17.1-rc.01", "latest", "2.17.1\nother: injected"]:
            with self.subTest(version=version):
                proc, after = self.rewrite(self.values, version)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, self.values)

    def test_schema_drift_fails_without_modifying_values(self):
        cases = [
            self.values.replace("image:\n", "containerImage:\n", 1),
            self.values + "image:\n  tag: 2.17.0\n",
            self.values.replace("  tag: '2.17.0'", "  tag: 2.17.0\n  tag: '2.17.0'"),
            self.values.replace("  tag: '2.17.0'", "    tag: '2.17.0'"),
            self.values.replace("image:\n", "image: {}\n", 1),
            self.values.replace("image:\n", "'image':\n", 1),
            self.values.replace("  tag: '2.17.0'", "  tag: latest"),
            self.values.replace("  tag: '2.17.0'", "\ttag: '2.17.0'"),
            self.values.replace("  repository:", "  'repository':"),
            self.values.replace("  repository:", "    repository:"),
            self.values.replace("  repository: " + self.repository, "  repository: {}"),
            self.values.replace("  repository: " + self.repository, "  repository: &image " + self.repository),
            self.values.replace("  repository: " + self.repository, "  repository: *image"),
            self.values.replace("  repository: " + self.repository, "  repository: [" + self.repository + "]"),
            self.values.replace(self.repository, self.repository + "#not-a-comment"),
            self.values.replace("'2.17.0' # operator only", "2.17.0#not-a-comment"),
            self.values.replace("  repository: " + self.repository, "  repository: " + self.repository + "\n  repository: " + self.repository),
            self.values.replace("  repository: " + self.repository, "  repository: " + self.repository + "\n  'repository': " + self.repository),
        ]
        for index, text in enumerate(cases):
            with self.subTest(case=index):
                proc, after = self.rewrite(text, "2.17.1-rc.1")
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, text)


if __name__ == "__main__":
    unittest.main()
