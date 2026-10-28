#!/usr/bin/env python3
"""Boundary regressions; the Linux startup case also exercises actual bwrap."""
import importlib.util
from pathlib import Path, PurePosixPath
import tempfile
import unittest
import re
import shutil
import subprocess
import sys
import json
import os
import io

spec = importlib.util.spec_from_file_location("fixture", Path(__file__).with_name("installer-fixture.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class FixtureBoundaryTests(unittest.TestCase):
    def test_outer_audit_exempts_only_exact_canary_process_and_children(self):
        proof = '10 execve("/usr/bin/bash", ["bash", "-c", ' + json.dumps(fixture.CANARY) + '], 0x0) = 0\n'
        proof += '10 clone(child_stack=NULL, flags=SIGCHLD) = 11\n'
        proof += '11 mkdir("/etc/404-probe", 0777) = -1 EROFS (Read-only file system)\n'
        workload = '20 newfstatat(AT_FDCWD, "/etc/404-probe/config", 0x0, 0) = -1 ENOENT\n'
        audit = fixture.audit_workload(proof + workload)
        self.assertEqual(audit['canary_processes'], [10, 11])
        self.assertEqual(audit['expected_canary_access'], ['/etc/404-probe'])
        self.assertEqual(audit['original_project_access'], ['/etc/404-probe/config'])
        self.assertEqual(fixture.audit_workload(io.StringIO(proof + workload)), audit)
        fake = '30 execve("/usr/bin/bash", ["bash", "-c", "echo fake canary"], 0x0) = 0\n'
        fake += '30 openat(AT_FDCWD, "/run/404-probe/socket", O_RDONLY) = -1 ENOENT\n'
        self.assertEqual(fixture.audit_workload(fake)['original_project_access'], ['/run/404-probe/socket'])
        self.assertEqual(fixture.audit_workload('openat("/var/lib/404-probe/db", O_RDONLY) = -1 ENOENT')['original_project_access'], ['/var/lib/404-probe/db'])

    @unittest.skipUnless(sys.platform == 'linux' and all(shutil.which(tool) for tool in ('bwrap', 'strace', 'bash')), 'requires actual Linux bwrap and strace')
    def test_linux_in_place_proof_executes_go_style_and_shell_bodies_without_nested_bwrap(self):
        repo = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory(prefix='single-layer-', dir=repo.parent) as directory:
            root = Path(directory)
            (root / 'shell-harness.sh').write_text('source /work/source/scripts/installer-fixture-entry.sh\nprintf shell-body > /work/fixture/shell-body\n', encoding='utf-8')
            body = '''set -Eeuo pipefail
mkdir /work/fixture/child
python3 -B /work/source/scripts/installer-fixture.py --root /work/fixture/child -- bash -c 'printf go-body > /work/fixture/child/go-body; chmod 755 /work/fixture/child/go-body; pwd > /work/fixture/child/pwd'
bash /work/fixture/shell-harness.sh
'''
            result = subprocess.run([sys.executable, '-B', str(repo / 'scripts/installer-fixture.py'), '--root', str(root), '--', 'bash', '-c', body], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((root / 'child/go-body').read_text(), 'go-body')
            self.assertEqual((root / 'child/pwd').read_text().strip(), '/work/fixture/child')
            self.assertEqual((root / 'child/go-body').stat().st_mode & 0o777, 0o755)
            self.assertEqual((root / 'shell-body').read_text(), 'shell-body')
            self.assertTrue(json.loads((root / 'child/in-place-proof.json').read_text())['verified'])
            self.assertFalse((root / 'child/canary.trace').exists())
            trace = (root / 'filesystem.trace').read_text()
            self.assertNotRegex(trace, r'execve\("[^"\n]*/(?:bwrap|strace)"')
            audit = json.loads((root / 'filesystem-audit.json').read_text())
            self.assertEqual(audit['original_project_access'], [])
            self.assertTrue(audit['expected_canary_access'])

    @unittest.skipUnless(sys.platform == 'linux' and shutil.which('bash'), 'requires Linux bash')
    def test_linux_forged_flags_skip_without_executing_either_body(self):
        repo = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory(prefix='forged-fixture-') as directory:
            root = Path(directory)
            marker = root / 'must-not-execute'
            env = dict(os.environ, INSTALLER_FIXTURE_ACTIVE=str(root), INSTALLER_FIXTURE_ROOT=str(root))
            command = [sys.executable, '-B', str(repo / 'scripts/installer-fixture.py'), '--root', str(root), '--', 'bash', '-c', 'touch ' + str(marker)]
            result = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 77, result.stdout + result.stderr)
            self.assertFalse(marker.exists())
            script = root / 'fake-shell.sh'
            script.write_text('source "' + str(repo / 'scripts/installer-fixture-entry.sh') + '"\ntouch "' + str(marker) + '"\n', encoding='utf-8')
            result = subprocess.run(['bash', str(script)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 77, result.stdout + result.stderr)
            self.assertFalse(marker.exists())
            self.assertFalse((root / 'canary.trace').exists())

    @unittest.skipUnless(sys.platform == 'linux' and all(shutil.which(tool) for tool in ('bwrap', 'strace', 'bash')), 'requires actual Linux bwrap and strace')
    def test_linux_outside_fixture_still_launches_single_namespace(self):
        repo = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory(prefix='outside-fixture-', dir=repo.parent) as directory:
            root = Path(directory)
            env = {key: value for key, value in os.environ.items() if not key.startswith('INSTALLER_FIXTURE_')}
            result = subprocess.run([sys.executable, '-B', str(repo / 'scripts/installer-fixture.py'), '--root', str(root), '--', 'bash', '-c', 'readlink /proc/self/ns/mnt > /work/fixture/mount-namespace'], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertNotEqual((root / 'mount-namespace').read_text().strip(), os.readlink('/proc/self/ns/mnt'))
            self.assertTrue(json.loads((root / 'canary-audit.json').read_text())['expected_attempts_seen'])
            self.assertEqual(json.loads((root / 'filesystem-audit.json').read_text())['mode'], 'new-namespace')

    def test_mount_targets_are_independent_of_hidden_host_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            command = fixture.sandbox(root, ['true'])
            self.assertIn('--tmpfs', command)
            self.assertEqual(command[command.index('--tmpfs') + 1], '/')
            self.assertIn('/work/source', command)
            self.assertIn('/work/fixture', command)
            self.assertEqual(command[command.index('--remount-ro') + 1], '/')
            for path in ('/etc', '/run', '/var/lib', '/var/log', '/usr/local', '/root', '/home'):
                index = command.index(path)
                self.assertEqual(command[index - 2:index], ['--ro-bind', str(root / 'empty-system')])

    def test_translate_private_paths_once_with_nested_fixture(self):
        repo = PurePosixPath('/var/lib/job/source')
        root = repo / '.tools/fixture'
        source = str(root / 'test.sh') + ' ' + str(repo / 'install.sh')
        expected = '/work/fixture/test.sh /work/source/install.sh'
        self.assertEqual(fixture.translate(source, root, repo), expected)
        self.assertEqual(fixture.translate(expected, root, repo), expected)
        self.assertEqual(fixture.translate(str(root) + '-other/test.sh', root, repo), '/work/source/.tools/fixture-other/test.sh')

    @unittest.skipUnless(sys.platform == 'linux' and all(shutil.which(tool) for tool in ('bwrap', 'strace', 'bash')), 'requires actual Linux bwrap and strace')
    def test_linux_bwrap_starts_with_source_and_fixture_under_var_lib(self):
        repo = Path(__file__).resolve().parent.parent
        if not repo.is_relative_to('/var/lib'):
            self.skipTest('run the regression from an isolated checkout below /var/lib')
        with tempfile.TemporaryDirectory(prefix='mount-regression-', dir=repo.parent) as directory:
            root = Path(directory)
            result = subprocess.run([sys.executable, '-B', str(repo / 'scripts/installer-fixture.py'), '--root', str(root), '--probe'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            audit = json.loads((root / 'canary-audit.json').read_text())
            self.assertTrue(audit['expected_attempts_seen'])
            self.assertEqual(audit['exit'], 0)
            self.assertFalse((root / 'canary').exists())
            self.assertFalse((root / 'escape').exists())
            self.assertFalse((root / 'parent').exists())

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
