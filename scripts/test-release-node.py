#!/usr/bin/env python3
"""Check the release Node allowlist and reporter without browser fixtures."""

import io
import os
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest

from release_node import classify_cases, execute_suite, fixture_variables


class ReleaseNodeTests(unittest.TestCase):
    def test_known_failure_xpass_new_failure_and_skip(self):
        events = [
            {"type": "test:fail", "name": "historical failure"},
            {"type": "test:pass", "name": "fixed"},
            {"type": "test:fail", "name": "unexpected failure"},
            {"type": "test:pass", "name": "disabled", "skip": "missing sample"},
        ]
        self.assertEqual(
            classify_cases(events, ["historical failure", "fixed", "absent"], 1),
            [["KNOWN FAIL", "historical failure"], ["XPASS", "fixed"],
             ["FAIL", "unexpected failure"], ["SKIPPED", "disabled (missing sample)"],
             ["NOT RUN", "absent"]],
        )

    def test_unexplained_exit_and_ambiguous_name_block(self):
        event = {"type": "test:fail", "name": "historical"}
        for cases, exit_code in [([], 1), ([event], 2), ([event], 0), ([event, event], 1)]:
            with self.subTest(cases=cases, exit_code=exit_code):
                self.assertIn("FAIL", [status for status, _ in classify_cases(cases, ["historical"], exit_code)])

    def test_fixture_detection_follows_wrappers(self):
        source = Path(__file__).resolve().parent.parent
        self.assertIn("V109_SERVER", fixture_variables(source, "scripts/test-v109-task-two.cjs"))
        self.assertIn("M02_PLAYWRIGHT", fixture_variables(source, "scripts/test-v111-theme.cjs"))
        self.assertNotIn("M02_PLAYWRIGHT", fixture_variables(source, "scripts/test-release-version.cjs"))

    @unittest.skipUnless(shutil.which("node"), "Node is unavailable")
    def test_real_runner_preserves_leaf_results_and_ignores_test_stdout(self):
        fixture = '''
const {test} = require('node:test');
test('parent', async t => {
  await t.test('known', () => { throw Error('historical'); });
  await t.test('fixed', () => { console.log('not ok 1 - fixed'); });
  await t.test('optional', {skip: 'fixture missing'}, () => {});
});
'''
        reporter = Path(__file__).with_name("release-node-reporter.cjs")
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp)
            scripts = source / "scripts"
            scripts.mkdir()
            shutil.copyfile(reporter, scripts / reporter.name)
            suite = "scripts/test-example.cjs"
            (source / suite).write_text(fixture, encoding="utf-8")
            release = SimpleNamespace(source=source, log=io.StringIO())
            self.assertEqual(
                execute_suite(release, shutil.which("node"), suite, True, ["known", "fixed"], dict(os.environ)),
                [["KNOWN FAIL", "known"], ["XPASS", "fixed"], ["SKIPPED", "optional (fixture missing)"]],
            )


if __name__ == "__main__":
    unittest.main()
