#!/usr/bin/env python3
"""Tests of the R05 snapshot (scripts/agent-state-snapshot.sh and .py). Run: python3 -I scripts/test-agent-state-snapshot.py"""
import importlib.util
import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("snapshot", os.path.join(HERE, "agent-state-snapshot.py"))
snapshot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(snapshot)
SH = shutil.which("sh")


class Compare(unittest.TestCase):
    def test_an_upgrade_changes_only_version_binary_and_process(self):
        before = {"agent.version": "v1.0.4", "binary.agent.sha256": "a", "runtime.x.service.main_pid": "10", "runtime.x.service.active": "active", "unit.x.service.sha256": "u", "config.env.A": "1"}
        after = {**before, "agent.version": "v1.0.5", "binary.agent.sha256": "b", "runtime.x.service.main_pid": "99"}
        rows = snapshot.compare_facts(before, after)
        self.assertEqual({r[1]: r[0] for r in rows}, {"agent.version": "expected", "binary.agent.sha256": "expected", "runtime.x.service.main_pid": "expected"})

    def test_everything_else_is_unexpected(self):
        before = {"unit.a.service.sha256": "u", "hardening.a.service.ProtectSystem": "strict", "config.env.A": "1", "unit.a.service.file.mode": "644 root:root", "state.collector_unavailable_marker": "absent"}
        for key in before:
            after = dict(before)
            after[key] = "changed"
            rows = snapshot.compare_facts(before, after)
            self.assertEqual([(r[0], r[1]) for r in rows], [("unexpected", key)], key)

    def test_a_service_that_stopped_or_a_key_that_vanished_is_a_finding(self):
        before = {"runtime.a.service.active": "active", "hardening.a.service.User": "404-probe"}
        rows = snapshot.compare_facts(before, {"runtime.a.service.active": "failed"})
        self.assertEqual(sorted((r[0], r[1]) for r in rows), [("unexpected", "hardening.a.service.User"), ("unexpected", "runtime.a.service.active")])
        self.assertEqual(snapshot.compare_facts(before, before), [])

    def test_a_snapshot_is_never_overwritten_and_never_goes_into_the_repository(self):
        with tempfile.TemporaryDirectory() as out:
            snapshot.write_snapshot(out, "before", "h", {"a": "b"})
            with self.assertRaises(FileExistsError):
                snapshot.write_snapshot(out, "before", "h", {"a": "c"})
            with open(os.path.join(out, "before-h.json"), "rb") as handle:
                self.assertNotIn(b"\r", handle.read())
        self.assertFalse(snapshot.outside_repository(os.path.join(snapshot.REPO, "build")))
        self.assertTrue(snapshot.outside_repository(tempfile.gettempdir()))
        self.assertEqual(snapshot.main(["collect", "--label", "x", "--out", os.path.join(snapshot.REPO, "snap"), "h"]), 2)


@unittest.skipUnless(SH, "needs a POSIX sh")
class RemoteScript(unittest.TestCase):
    """The script of the host, run against a fixture tree with a stand-in systemctl."""

    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        w = self.write
        w("etc/os-release", 'PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n')
        w("etc/404-probe/agent.env", "PROBE_404_SERVER=https://example.org\nPROBE_404_AGENT_ID=abc\nPROBE_404_TOKEN=supersecretvalue123\nPROBE_404_COUNTRY_CODE=US\n# comment=ignored\nPROBE_404_ENROLL_KEY=\n")
        w("etc/404-probe/selector-order.json", "[]\n")
        w("usr/local/bin/404-probe-agent", "#!/bin/sh\necho v1.0.4\n", executable=True)
        w("etc/systemd/system/404-probe-agent.service", "[Service]\nUser=404-probe\nProtectSystem=strict\n")
        os.makedirs(os.path.join(self.root, "run/systemd/system"))
        w("bin/systemctl", """#!/bin/sh
case "$1" in
  cat) case "$2" in 404-probe-agent.service) printf '[Service]\\nUser=404-probe\\nProtectSystem=strict\\nExecStart=/usr/local/bin/404-probe-agent --interval 10s\\n';; *) exit 1;; esac ;;
  show) case "$*" in *MainPID*) echo MainPID=4242;; *NRestarts*) echo NRestarts=0;; *) printf 'User=404-probe\\nNoNewPrivileges=yes\\nProtectSystem=strict\\nCapabilityBoundingSet=cap_net_raw\\n';; esac ;;
  is-active) echo active ;;
  is-enabled) echo enabled ;;
esac
""", executable=True)

    def write(self, rel, text, executable=False):
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", newline="\n") as handle:
            handle.write(text)
        if executable:
            os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)

    def run_script(self):
        env = dict(os.environ, SNAPSHOT_ROOT=self.root, PATH=os.path.join(self.root, "bin") + os.pathsep + os.environ["PATH"])
        done = subprocess.run([SH, os.path.join(HERE, "agent-state-snapshot.sh")], env=env, capture_output=True, text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stderr)
        return done.stdout

    def test_it_reports_version_unit_sandbox_and_settings_and_no_secret(self):
        out = self.run_script()
        facts = snapshot.parse(out)
        self.assertEqual(facts["host.os"], "Debian GNU/Linux 12 (bookworm)")
        self.assertEqual(facts["host.init"], "systemd")
        self.assertEqual(facts["agent.version"], "v1.0.4")
        self.assertRegex(facts["binary.agent.sha256"], r"^[0-9a-f]{64}$")
        self.assertRegex(facts["unit.404-probe-agent.service.sha256"], r"^[0-9a-f]{64}$")
        self.assertEqual(facts["unit.404-probe-agent.service.missing"] if False else facts["unit.404-probe-agent-updater.service.sha256"], "missing")
        self.assertEqual(facts["hardening.404-probe-agent.service.ProtectSystem"], "strict")
        self.assertEqual(facts["hardening.404-probe-agent.service.CapabilityBoundingSet"], "cap_net_raw")
        self.assertEqual(facts["runtime.404-probe-agent.service.main_pid"], "4242")
        self.assertEqual(facts["config.env.PROBE_404_SERVER"], "https://example.org")
        self.assertEqual(facts["config.env.PROBE_404_TOKEN"], "<set>")
        self.assertEqual(facts["config.env.PROBE_404_ENROLL_KEY"], "<empty>")
        self.assertNotIn("comment", " ".join(facts))
        self.assertEqual(facts["state.collector_unavailable_marker"], "absent")
        self.assertNotIn("supersecretvalue123", out, "a secret never leaves the host")

    def test_the_same_host_twice_differs_in_nothing_but_the_process(self):
        first, second = snapshot.parse(self.run_script()), snapshot.parse(self.run_script())
        self.assertEqual(snapshot.compare_facts(first, second), [])
        self.write("usr/local/bin/404-probe-agent", "#!/bin/sh\necho v1.0.5\n", executable=True)
        upgraded = snapshot.parse(self.run_script())
        self.assertEqual({(r[0], r[1]) for r in snapshot.compare_facts(first, upgraded)}, {("expected", "agent.version"), ("expected", "binary.agent.sha256")})
        self.write("etc/systemd/system/404-probe-agent.service", "[Service]\nUser=root\n")  # a unit rewritten behind the upgrade's back
        with open(os.path.join(self.root, "bin/systemctl")) as handle:
            stub = handle.read()
        self.write("bin/systemctl", stub.replace("User=404-probe\\nProtectSystem=strict", "User=root\\nProtectSystem=no", 1), executable=True)
        changed = snapshot.compare_facts(first, snapshot.parse(self.run_script()))
        self.assertTrue(any(r[0] == "unexpected" and r[1].startswith("unit.404-probe-agent.service.sha256") for r in changed), changed)

    def test_collect_goes_through_ssh_and_survives_one_unreachable_host(self):
        script = self.run_script()

        def runner(cmd, input=None, capture_output=None, timeout=None):
            self.assertEqual(cmd[0], "ssh")
            self.assertIn("BatchMode=yes", cmd)
            self.assertEqual(cmd[-2:], ["sh", "-s"])
            self.assertIn(b"SNAPSHOT_ROOT", input)
            host = cmd[cmd.index("sh") - 1]
            if host == "down":
                return subprocess.CompletedProcess(cmd, 255, b"", b"Connection closed")
            return subprocess.CompletedProcess(cmd, 0, script.encode(), b"")

        with tempfile.TemporaryDirectory() as out:
            self.assertEqual(snapshot.main(["collect", "--label", "before", "--out", out, "up", "down"], runner=runner), 1)
            self.assertEqual(sorted(os.listdir(out)), ["before-up.json"])
            with open(os.path.join(out, "before-up.json"), encoding="utf-8") as handle:
                self.assertEqual(json.load(handle)["facts"]["agent.version"], "v1.0.4")
            self.assertEqual(snapshot.main(["collect", "--label", "after", "--out", out, "up"], runner=runner), 0)
            self.assertEqual(snapshot.main(["compare", out, "before", "after"]), 0)


class ReadOnly(unittest.TestCase):
    def test_the_host_script_cannot_change_anything(self):
        with open(os.path.join(HERE, "agent-state-snapshot.sh"), encoding="utf-8") as handle:
            text = handle.read()
        body = "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
        for forbidden in [r"\brm\b", r"\bmv\b", r"\bcp\b", r"\bchmod\b", r"\bchown\b", r"\btee\b", r"sed\s+-i", r"\bsystemctl\s+(start|stop|restart|reload|enable|disable|daemon-reload|mask|unmask|kill|edit|set-property)\b",
                          r"\brc-service\s+\S+\s+(start|stop|restart|reload)\b", r"\brc-update\s+(add|del)\b", r"\bkill\b", r"\bapt\b", r"\bapk\b", r"\bcurl\b", r"\bwget\b"]:
            self.assertIsNone(re.search(forbidden, body), forbidden)
        # Redirections go to /dev/null or into another descriptor, never into a file.
        for target in re.findall(r"(?:^|\s)\d?>>?\s*(\S+)", body, re.M):
            self.assertTrue(target == "/dev/null" or target.startswith("&"), target)
        self.assertNotRegex(text, "\r")


if __name__ == "__main__":
    unittest.main()
