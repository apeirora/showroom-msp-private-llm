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
    def outputs(self, tag):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "outputs"
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "GITHUB_REF_NAME": tag,
                     "GITHUB_REPOSITORY_OWNER": "ApeiroRA", "GITHUB_OUTPUT": str(output)},
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

    def rewrite(self, text, version):
        with tempfile.TemporaryDirectory() as tmp:
            values = Path(tmp) / "values.yaml"
            values.write_text(text)
            proc = subprocess.run(
                ["python3", str(SET_IMAGE), str(values), version],
                capture_output=True, text=True, check=False,
            )
            return proc, values.read_text()

    def test_exact_root_tag_updated_preserving_other_content(self):
        for version in ["2.17.1", "2.17.1-rc.1", "3.0.0-0"]:
            with self.subTest(version=version):
                proc, after = self.rewrite(self.values, version)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(
                    after, self.values.replace("  tag: '2.17.0'", f'  tag: "{version}"'),
                )

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
        ]
        for index, text in enumerate(cases):
            with self.subTest(case=index):
                proc, after = self.rewrite(text, "2.17.1-rc.1")
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, text)


if __name__ == "__main__":
    unittest.main()
