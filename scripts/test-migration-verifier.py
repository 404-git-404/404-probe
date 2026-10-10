"""Unexecuted dirty ELF specimens test rejection; clean positive proof belongs to C."""
import hashlib
import importlib.util
import json
import sys
import unittest
from pathlib import Path

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('migration_verifier', Path(__file__).with_name('verify-migration-runner.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
verify = module.verify


class MigrationVerifierTests(unittest.TestCase):
    specimens = None

    def inputs(self, filename):
        binary = (self.specimens / filename).read_bytes()
        commit = (self.specimens / 'source-commit.txt').read_text().strip()
        assets = [{'name': '404-probe-' + role + '-linux-' + arch, 'goos': 'linux', 'goarch': arch,
                   'sha256': hashlib.sha256(binary).hexdigest()} for role in ('agent', 'server') for arch in ('amd64', 'arm64')]
        doc = {'schema_version': 1, 'version': 'v1.0.1', 'commit': commit, 'assets': assets}
        sums = ''.join(a['sha256'] + '  ' + a['name'] + '\n' for a in assets) + '0' * 64 + '  install.sh\n'
        return binary, json.dumps(doc).encode(), sums.encode()

    def test_real_dirty_correct_version_reaches_vcs_rejection(self):
        for arch in ('amd64', 'arm64'):
            with self.subTest(arch=arch), self.assertRaisesRegex(ValueError, 'Go path/platform/VCS proof failed'):
                verify(*self.inputs('agent-' + arch + '-correct'), arch)

    def test_same_commit_real_wrong_version_is_rejected(self):
        for arch in ('amd64', 'arm64'):
            with self.subTest(arch=arch), self.assertRaisesRegex(ValueError, 'linked Version/Commit mismatch'):
                verify(*self.inputs('agent-' + arch + '-wrong-version'), arch)

    def test_stripped_and_wrong_platform_are_rejected(self):
        with self.assertRaises(ValueError):
            verify(*self.inputs('agent-amd64-stripped'), 'amd64')
        with self.assertRaisesRegex(ValueError, 'unsupported ELF layout'):
            verify(*self.inputs('agent-amd64-correct'), 'arm64')

    def test_strict_metadata_and_manifest(self):
        binary, meta, sums = self.inputs('agent-amd64-correct')
        invalid = [meta[:-1] + b',"version":"v1.0.1"}', meta.replace(b'"schema_version": 1', b'"schema_version": true'), meta + b' {}']
        for value in invalid:
            with self.subTest(value=value[:30]), self.assertRaises(ValueError):
                verify(binary, value, sums, 'amd64')
        for value in (sums + sums.splitlines(keepends=True)[0], sums.splitlines(keepends=True)[0], sums.replace(b'  install.sh', b'  injected.sh'), sums.replace(b'0' * 64, b'G' * 64)):
            with self.subTest(value=value[-30:]), self.assertRaises(ValueError):
                verify(binary, meta, value, 'amd64')
        with self.assertRaisesRegex(ValueError, 'binary checksum mismatch'):
            verify(binary + b'x', meta, sums, 'amd64')

    def test_installer_embeds_exact_verifier(self):
        root = Path(__file__).resolve().parent.parent
        installer = (root / 'install.sh').read_text(encoding='utf-8')
        embedded = installer.split("<<'PY_MIGRATION'", 1)[1].split('\n', 1)[1].split('PY_MIGRATION\n', 1)[0]
        self.assertEqual(embedded, (root / 'scripts/verify-migration-runner.py').read_text(encoding='utf-8'))


if __name__ == '__main__':
    MigrationVerifierTests.specimens = Path(sys.argv.pop(1))
    unittest.main()
