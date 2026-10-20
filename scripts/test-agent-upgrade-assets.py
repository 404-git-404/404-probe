#!/usr/bin/env python3
"""Run with python3 scripts/test-agent-upgrade-assets.py; no root/network/Go needed."""

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("assets", Path(__file__).with_name("agent-upgrade-assets.py"))
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)


class ReleasePreflightTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.metadata = {
            "schema_version": 2, "version": "v1.0.3", "commit": "a" * 40,
            "compatibility": {"upgrade_protocol": 2, "min_server_version": "v1.0.1"},
            "assets": [],
        }
        for name, (_, arch) in assets.BINARIES.items():
            (self.root / name).write_bytes(b"\x7fELF" + name.encode())
            self.metadata["assets"].append({"name": name, "goos": "linux", "goarch": arch,
                                           "sha256": self.digest(name)})
        (self.root / "install.sh").write_bytes(b"#!/bin/bash\n")
        self.manifest()
        self.settings = {}
        self.run = patch.object(assets.subprocess, "run", side_effect=self.inspect).start()
        self.addCleanup(patch.stopall)

    def digest(self, name):
        return hashlib.sha256((self.root / name).read_bytes()).hexdigest()

    def manifest(self):
        (self.root / "RELEASE-METADATA.json").write_bytes(json.dumps(self.metadata).encode())
        (self.root / "SHA256SUMS").write_bytes("".join(
            self.digest(name) + "  " + name + "\n" for name in sorted(assets.PAYLOADS)).encode())

    def inspect(self, args, **kwargs):
        role, arch = assets.BINARIES[Path(args[-1]).name]
        settings = {"GOOS": "linux", "GOARCH": arch, "vcs.revision": "a" * 40,
                    "vcs.modified": "false", **self.settings}
        output = "\tpath\t404-probe/cmd/" + role + "\n" + "".join(
            "\tbuild\t" + key + "=" + value + "\n" for key, value in settings.items())
        return subprocess.CompletedProcess(args, 0, stdout=output)

    def validate(self):
        return assets.validate_release(self.root, "v1.0.3", "go")

    def test_complete_release_checks_all_four_binaries(self):
        self.assertEqual(self.validate(), "a" * 40)
        self.assertEqual(self.run.call_count, 4)

    def test_stable_version_boundaries_and_order(self):
        for version in ("1.0.3", "v01.0.3", "v1.0.3-rc.1", "v1.0.3;id", "v1.0.18446744073709551616"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                assets.stable_version(version)
        self.assertEqual(assets.stable_version("v1.10.0"), (1, 10, 0))
        for source, target in (("v1.0.3", "v1.0.3"), ("v1.0.4", "v1.0.3"), ("v0.9.3", "v1.0.4")):
            with self.subTest(source=source, target=target), self.assertRaises(ValueError):
                assets.validate_pair(source, target, self.root, self.root, "go")

    def test_future_pair_passes_each_selected_version_and_directory(self):
        with patch.object(assets, "validate_release", side_effect=["a" * 40, "b" * 40]) as validate:
            self.assertEqual(assets.validate_pair("v1.0.4", "v1.0.5", "/source", "/target", "go"),
                             ("a" * 40, "b" * 40))
        self.assertEqual([call.args for call in validate.call_args_list],
                         [("/source", "v1.0.4", "go"), ("/target", "v1.0.5", "go")])

    def test_missing_asset_rejected_before_inspection(self):
        for name in assets.PAYLOADS | {"SHA256SUMS"}:
            path = self.root / name
            data = path.read_bytes()
            path.unlink()
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "missing regular"):
                self.validate()
            path.write_bytes(data)
        self.run.assert_not_called()

    def test_tampering_and_manifest_path_injection(self):
        (self.root / "install.sh").write_bytes(b"changed\n")
        with self.assertRaisesRegex(ValueError, "SHA256SUMS mismatch"):
            self.validate()
        self.manifest()
        path = self.root / "SHA256SUMS"
        path.write_bytes(path.read_bytes() + b"a" * 64 + b"  ../escape\n")
        with self.assertRaisesRegex(ValueError, "invalid SHA256SUMS"):
            self.validate()

    def test_duplicate_checksums_and_missing_coverage(self):
        path = self.root / "SHA256SUMS"
        original = path.read_bytes()
        path.write_bytes(original + original.splitlines(keepends=True)[0])
        with self.assertRaisesRegex(ValueError, "duplicate SHA256SUMS"):
            self.validate()
        path.write_bytes(b"".join(original.splitlines(keepends=True)[1:]))
        with self.assertRaisesRegex(ValueError, "exactly the six"):
            self.validate()

    def test_rehashed_wrong_metadata(self):
        original = copy.deepcopy(self.metadata)
        for key, value in (("version", "v1.0.4"), ("commit", "invalid"), ("schema_version", 1),
                           ("assets", []), ("assets", original["assets"][:3] + original["assets"][:1]),
                           ("compatibility", {"upgrade_protocol": 1, "min_server_version": "v1.0.1"}),
                           ("compatibility", {"upgrade_protocol": 2, "min_server_version": "v1.0.4"})):
            self.metadata = {**copy.deepcopy(original), key: value}
            self.manifest()
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.validate()

    def test_rehashed_metadata_hash_and_arch_mismatch(self):
        original = copy.deepcopy(self.metadata)
        for key, value in (("sha256", "0" * 64), ("goarch", "386")):
            self.metadata = copy.deepcopy(original)
            self.metadata["assets"][0][key] = value
            self.manifest()
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "metadata asset/hash mismatch"):
                self.validate()

    def test_rehashed_binary_revision_dirty_or_platform(self):
        for key, value in (("vcs.revision", "b" * 40), ("vcs.modified", "true"),
                           ("GOARCH", "386"), ("GOOS", "windows")):
            self.settings = {key: value}
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "binary .* mismatch"):
                self.validate()

    def test_linked_version_when_present(self):
        self.settings = {"-ldflags": json.dumps("-s -w -X 404-probe/internal/buildinfo.Version=v1.0.3")}
        self.validate()
        self.settings["-ldflags"] = "-X 404-probe/internal/buildinfo.Version=v1.0.4"
        with self.assertRaisesRegex(ValueError, "linked version mismatch"):
            self.validate()

    def test_crlf_is_rejected_even_with_valid_checksum(self):
        (self.root / "install.sh").write_bytes(b"#!/bin/bash\r\n")
        self.manifest()
        with self.assertRaisesRegex(ValueError, "contains CR"):
            self.validate()

    def test_duplicate_json_keys(self):
        with self.assertRaisesRegex(ValueError, "duplicate metadata key"):
            json.loads('{"version":"v1.0.3","version":"v1.0.4"}', object_pairs_hook=assets.unique_object)


if __name__ == "__main__":
    unittest.main(verbosity=2)
