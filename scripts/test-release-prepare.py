#!/usr/bin/env python3
"""Check README retargeting in release_prepare, including a window that starts at the previous release."""

import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

import release_prepare

ROOT = Path(__file__).resolve().parents[1]


def tag_exists(tag):
    return subprocess.run(["git", "rev-parse", "--verify", "--quiet", f"refs/tags/{tag}^{{commit}}"],
                          cwd=ROOT, stdout=subprocess.DEVNULL).returncode == 0


class RetargetReadmeLines(unittest.TestCase):
    def test_protected_lines_keep_their_version(self):
        generated = "> **本版支持从 v1.3.1 升级**。详情见 [v1.4.0 发布说明](RELEASE_NOTES_v1.4.0.md)。"
        legacy = "详情见 [v1.3.1 发布说明](RELEASE_NOTES_v1.3.1.md)。"
        text = "\n".join(["> v1.3.1 Stable 摘要 发布说明", generated, legacy, "无关的 v1.3.1 文字"])
        result = release_prepare.retarget_readme_lines(
            text, "v1.3.1", "v1.4.0", ("本版支持从 v1.3.1 升级",)).split("\n")
        self.assertEqual(result[0], "> v1.3.1 Stable 摘要 发布说明")
        self.assertEqual(result[1], generated)
        self.assertEqual(result[2], "详情见 [v1.4.0 发布说明](RELEASE_NOTES_v1.4.0.md)。")
        self.assertEqual(result[3], "无关的 v1.3.1 文字")
        # Without protection the generated line would be rewritten to the wrong start.
        unprotected = release_prepare.retarget_readme_lines(text, "v1.3.1", "v1.4.0").split("\n")
        self.assertIn("本版支持从 v1.4.0 升级", unprotected[1])


@unittest.skipUnless(tag_exists("v1.3.1"), "tag v1.3.1 is required")
class PrepareFromPreviousRelease(unittest.TestCase):
    def test_window_starting_at_previous_release_keeps_start_in_readme(self):
        archive = subprocess.run(["git", "archive", "v1.3.1"], cwd=ROOT, check=True,
                                 stdout=subprocess.PIPE).stdout
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
                tar.extractall(root, filter="data")
            # The v1.3.1 tree predates the registry entry for itself.
            (root / "scripts/r31-versions.json").write_bytes((ROOT / "scripts/r31-versions.json").read_bytes())
            release_prepare.prepare(root, "v1.4.0", "v1.3.1", None)
            readme = (root / "README.md").read_text(encoding="utf-8").split("\n")
        upgrade = [line for line in readme if line.startswith("> **本版支持从 ")]
        self.assertEqual(len(upgrade), 1)
        self.assertIn("本版支持从 v1.3.1 升级", upgrade[0])
        self.assertNotIn("本版支持从 v1.4.0", upgrade[0])
        self.assertIn("[v1.4.0 发布说明](RELEASE_NOTES_v1.4.0.md)", upgrade[0])
        self.assertTrue(any("现有 Server 升级（支持 v1.3.1）" in line for line in readme))
        agent_rule = [line for line in readme if line.startswith("> **Agent 升级规则不变")]
        self.assertEqual(len(agent_rule), 1)
        self.assertIn("[v1.4.0 发布说明](RELEASE_NOTES_v1.4.0.md)", agent_rule[0])


@unittest.skipUnless(tag_exists("v1.4.0"), "tag v1.4.0 is required")
class PrepareFromPreparedRelease(unittest.TestCase):
    def test_second_preparation_accepts_readme_generated_by_the_previous_one(self):
        archive = subprocess.run(["git", "archive", "v1.4.0"], cwd=ROOT, check=True,
                                 stdout=subprocess.PIPE).stdout
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
                tar.extractall(root, filter="data")
            (root / "scripts/r31-versions.json").write_bytes((ROOT / "scripts/r31-versions.json").read_bytes())
            release_prepare.prepare(root, "v1.4.1", "v1.4.0", None)
            readme = (root / "README.md").read_text(encoding="utf-8").split("\n")
        upgrade = [line for line in readme if line.startswith("> **本版支持从 ")]
        self.assertEqual(len(upgrade), 1)
        self.assertIn("本版支持从 v1.4.0 升级", upgrade[0])
        self.assertIn("数据库 schema 保持 46", upgrade[0])  # the v1.4.0 tree is still at schema 46
        self.assertTrue(any("现有 Server 升级（支持 v1.4.0）" in line for line in readme))
        self.assertTrue(any("the current v1.4.1 Server supports upgrades from v1.4.0" in line for line in readme))


if __name__ == "__main__":
    unittest.main()
