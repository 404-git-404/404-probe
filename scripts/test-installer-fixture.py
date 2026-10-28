#!/usr/bin/env python3
"""Pure boundary regressions, safe without root or an installed namespace tool."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
import re

spec = importlib.util.spec_from_file_location("fixture", Path(__file__).with_name("installer-fixture.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class FixtureBoundaryTests(unittest.TestCase):
    def test_shell_parameter_operators_and_quotes_are_idempotent(self):
        for prefix in fixture.PREFIXES:
            for operator in (":-", "-", ":=", "=", ":+", "+", ":?", "?"):
                for quote in ("", "'", '"'):
                    source = '${VAR' + operator + quote + prefix + '/item' + quote + '}'
                    expected = source.replace(prefix, '/fixture/system' + prefix)
                    with self.subTest(prefix=prefix, operator=operator, quote=quote):
                        self.assertEqual(fixture.redirect(source, Path('/fixture')), expected)
                        self.assertEqual(fixture.redirect(expected, Path('/fixture')), expected)
            for source in ('X='+prefix+'/item', 'X="'+prefix+'/item"', "X='"+prefix+"/item'"):
                self.assertEqual(fixture.redirect(source, Path('/fixture')), source.replace(prefix, '/fixture/system'+prefix))
            for source in ('${temporary}'+prefix+'/item', '/fixture/system'+prefix+'/item', '/fixture/parent-'+prefix+'/item'):
                self.assertEqual(fixture.redirect(source, Path('/fixture')), source)

    def test_all_fixed_prefixes_including_both_uninstall_copies(self):
        source = Path(__file__).resolve().parent.parent.joinpath("install.sh").read_text(encoding="utf-8")
        self.assertEqual(source.count("remove_owned_tree /run/404-probe"), 2)
        rewritten = fixture.redirect(source, Path("/fixture"))
        remaining = re.sub(r'''/fixture/system[^\s'"<>;)]*''', "MAPPED", rewritten)
        for prefix in fixture.PREFIXES:
            self.assertFalse(prefix in remaining, "unmapped fixed prefix: " + prefix)
        self.assertEqual(rewritten.count("remove_owned_tree /fixture/system/run/404-probe"), 2)

    def test_positive_delete_and_forbidden_system_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            child = root / "canary"
            child.write_text("safe", encoding="utf-8")
            fixture.checked_path(root, child).unlink()
            self.assertFalse(child.exists())
            for path in ("/run/404-probe", "/etc/404-probe", "/var/lib/404-probe", "/var/lib/404-probe-updater", "/bin/rm", root / ".." / "escape", root):
                with self.subTest(path=path), self.assertRaises(ValueError):
                    fixture.checked_path(root, path)

    def test_parent_replacement_and_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            parent = root / "parent"
            parent.mkdir()
            self.assertEqual(fixture.checked_path(root, parent / "child"), parent / "child")
            parent.rmdir()
            try:
                parent.symlink_to(root, target_is_directory=True)
            except OSError as error:
                self.skipTest("host cannot create symlinks: " + str(error))
            with self.assertRaises(ValueError):
                fixture.checked_path(root, parent / "child")

    def test_every_audited_harness_uses_common_entry(self):
        scripts = Path(__file__).resolve().parent
        for name in ("install-sandbox", "install-oneclick", "install-oneclick-token", "install-password", "install-banner", "server-upgrade-transaction", "selector-order-existing", "migration-preflight", "migration-service-lifecycle", "release-rc"):
            self.assertIn("installer-fixture-entry.sh", (scripts / ("test-" + name + ".sh")).read_text(encoding="utf-8"))
        for name in ("uninstall_behavior", "security_rollback", "server_recovery", "download_behavior", "beta_channel"):
            source = (scripts.parent / "cmd/server" / ("installer_" + name + "_test.go")).read_text(encoding="utf-8")
            self.assertNotIn("exec.Command(bash,", source)
            self.assertIn("installerFixtureCommand(t, bash,", source)


if __name__ == "__main__":
    unittest.main()
