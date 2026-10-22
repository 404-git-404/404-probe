#!/usr/bin/env python3
"""Tests of the R05 snapshot (scripts/agent-state-snapshot.sh and .py). Run: python3 -I scripts/test-agent-state-snapshot.py

SNAPSHOT_SH=<path> runs the host-script tests against another copy of the script (to show a test fails on older code)."""
import hashlib
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
SCRIPT = os.environ.get("SNAPSHOT_SH") or os.path.join(HERE, "agent-state-snapshot.sh")
KEY = "0123456789abcdef" * 4
TOKEN = "A" * 43
SECRETS = ["R05_SYNTHETIC_SECRET_NOT_REAL", "R05_SYNTHETIC_OPENRC_SECRET", "hunter2-password", "enrollValue1234567890", "DropInSecret987", "UrlPassword42"]


class Compare(unittest.TestCase):
    def test_an_upgrade_changes_only_version_binary_and_process(self):
        before = {"agent.version": "v1.0.4", "binary.agent.sha256": "a", "runtime.x.service.main_pid": "10", "runtime.x.service.active": "active", "unit.x.service.mac": "u", "config.env.A": "1"}
        after = {**before, "agent.version": "v1.0.5", "binary.agent.sha256": "b", "runtime.x.service.main_pid": "99"}
        rows = snapshot.compare_facts(before, after)
        self.assertEqual({r[1]: r[0] for r in rows}, {"agent.version": "expected", "binary.agent.sha256": "expected", "runtime.x.service.main_pid": "expected"})

    def test_everything_else_is_unexpected(self):
        before = {"unit.a.service.mac": "u", "hardening.a.service.ProtectSystem": "strict", "config.env.A": "1", "config.env.T.mac": "m", "config.agent.env.mac": "e", "unit.a.service.file.mode": "644 root:root", "state.collector_unavailable_marker": "absent"}
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


class SecretShape(unittest.TestCase):
    def test_a_token_shaped_value_is_refused_wherever_it_comes_from(self):
        for token in ["Zk3Qm9xTpL2vNc8RbYh4WdJf7GsA1eUoXiK5tPqE6", "aB3-" * 10, "PROBE_" + "xY9" * 12]:
            self.assertTrue(snapshot.looks_like_secret(token), token)
            with self.assertRaises(RuntimeError):
                snapshot.check_no_secret({"anything": "pre " + token})
        for fine in ["16ec70376964b72413c610014d1839ff", hashlib.sha256(b"x").hexdigest(), "/var/lib/404-probe-security/export", "v1.0.5-rc.7", "http://127.0.0.1:18080", "Debian GNU/Linux 12 (bookworm)", "--interval=10s --token=<redacted>"]:
            self.assertFalse(snapshot.looks_like_secret(fine), fine)
        snapshot.check_no_secret({"a": "ok"})

    def test_the_key_is_random_made_once_private_and_goes_to_the_host_on_stdin_only(self):
        with tempfile.TemporaryDirectory() as out:
            key = snapshot.snapshot_key(out)
            self.assertRegex(key, r"^[0-9a-f]{64}$")
            self.assertEqual(snapshot.snapshot_key(out), key, "the same directory keeps its key, or before and after could not be compared")
            with tempfile.TemporaryDirectory() as other:
                self.assertNotEqual(snapshot.snapshot_key(other), key)
            if os.name == "posix":
                self.assertEqual(stat.S_IMODE(os.stat(os.path.join(out, ".snapshot-key")).st_mode), 0o600)
            seen = {}

            def runner(cmd, input=None, capture_output=None, timeout=None):
                seen["cmd"], seen["input"] = cmd, input
                return subprocess.CompletedProcess(cmd, 0, b"host.init\tsystemd\n", b"")

            self.assertEqual(snapshot.main(["collect", "--label", "before", "--out", out, "h"], runner=runner), 0)
            self.assertNotIn(key, " ".join(seen["cmd"]), "never in an argument (ps)")
            self.assertTrue(seen["input"].startswith(("SNAPSHOT_KEY=%s\n" % key).encode()), "the first thing on stdin")
            with open(os.path.join(out, "before-h.json"), encoding="utf-8") as handle:
                self.assertNotIn(key, handle.read(), "never in a snapshot")


@unittest.skipUnless(SH, "needs a POSIX sh")
class RemoteScript(unittest.TestCase):
    """The script of the host, run against a fixture tree with stand-in systemctl, rc-service and rc-update."""

    START = "/usr/local/bin/404-probe-agent --interval 10s --job-interval 10s --timeout 8s --allow-insecure-http"

    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.env_lines = ["PROBE_404_SERVER=https://example.org", "PROBE_404_AGENT_ID=abc", "PROBE_404_TOKEN=" + TOKEN, "PROBE_404_COUNTRY_CODE=US", "# comment=ignored", "PROBE_404_ENROLL_KEY="]
        self.write("etc/os-release", 'PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n')
        self.write_env()
        self.write("etc/404-probe/selector-order.json", "[]\n")
        self.write("usr/local/bin/404-probe-agent", "#!/bin/sh\necho v1.0.4\n", executable=True)
        self.write("etc/systemd/system/404-probe-agent.service", "[Service]\nUser=404-probe\nProtectSystem=strict\n")
        os.makedirs(os.path.join(self.root, "run/systemd/system"))
        self.unit_text = "[Service]\\nUser=404-probe\\nProtectSystem=strict\\nExecStart=%s\\n" % self.START
        self.write_systemctl()

    def write_env(self):
        self.write("etc/404-probe/agent.env", "\n".join(self.env_lines) + "\n")

    def write_systemctl(self):
        self.write("bin/systemctl", """#!/bin/sh
case "$1" in
  cat) case "$2" in 404-probe-agent.service) printf '%s\\n' '""" + self.unit_text + """' | sed 's/\\\\n/\\n/g';; *) exit 1;; esac ;;
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

    def run_script(self, key=KEY, fixture=None):
        env = dict(os.environ, SNAPSHOT_ROOT=fixture or self.root, SNAPSHOT_KEY=key, PATH=os.path.join(fixture or self.root, "bin") + os.pathsep + os.environ["PATH"])
        done = subprocess.run([SH, SCRIPT], env=env, capture_output=True, text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stderr)
        return done.stdout

    def openrc_fixture(self, init_script):
        root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root, True)
        for rel in ("etc/os-release", "etc/404-probe/agent.env", "usr/local/bin/404-probe-agent"):
            os.makedirs(os.path.dirname(os.path.join(root, rel)), exist_ok=True)
            shutil.copy(os.path.join(self.root, rel), os.path.join(root, rel))
        os.makedirs(os.path.join(root, "etc/init.d"))
        with open(os.path.join(root, "etc/init.d/404-probe-agent"), "w", newline="\n") as handle:
            handle.write(init_script)
        for name, body in (("rc-service", "#!/bin/sh\necho ' * status: started'\n"), ("rc-update", "#!/bin/sh\necho '404-probe-agent | default'\n")):
            path = os.path.join(root, "bin", name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", newline="\n") as handle:
                handle.write(body)
            os.chmod(path, 0o755)
        return root

    # ---- what is reported ----
    def test_it_reports_version_unit_sandbox_and_settings(self):
        facts = snapshot.parse(self.run_script())
        self.assertEqual(facts["host.os"], "Debian GNU/Linux 12 (bookworm)")
        self.assertEqual(facts["host.init"], "systemd")
        self.assertEqual(facts["agent.version"], "v1.0.4")
        self.assertRegex(facts["binary.agent.sha256"], r"^[0-9a-f]{64}$")
        self.assertRegex(facts["unit.404-probe-agent.service.mac"], r"^[0-9a-f]{64}$")
        self.assertEqual(facts["unit.404-probe-agent-updater.service.mac"], "missing")
        self.assertEqual(facts["unit.404-probe-agent.service.exec.argv0"], "/usr/local/bin/404-probe-agent")
        self.assertEqual(facts["unit.404-probe-agent.service.exec.args"], "--interval=10s --job-interval=10s --timeout=8s --allow-insecure-http")
        self.assertEqual(facts["hardening.404-probe-agent.service.ProtectSystem"], "strict")
        self.assertEqual(facts["hardening.404-probe-agent.service.CapabilityBoundingSet"], "cap_net_raw")
        self.assertEqual(facts["runtime.404-probe-agent.service.main_pid"], "4242")
        self.assertEqual(facts["config.env.PROBE_404_SERVER"], "https://example.org")
        self.assertEqual(facts["config.env.PROBE_404_COUNTRY_CODE"], "US")
        self.assertEqual(facts["config.env.PROBE_404_TOKEN"], "<redacted>")
        self.assertEqual(facts["config.env.PROBE_404_ENROLL_KEY"], "<empty>")
        self.assertNotIn("comment", " ".join(facts))
        self.assertEqual(facts["state.collector_unavailable_marker"], "absent")

    def test_the_key_is_required(self):
        env = dict(os.environ, SNAPSHOT_ROOT=self.root, PATH=os.path.join(self.root, "bin") + os.pathsep + os.environ["PATH"])
        env.pop("SNAPSHOT_KEY", None)
        done = subprocess.run([SH, SCRIPT], env=env, capture_output=True, text=True, timeout=60)
        self.assertNotEqual(done.returncode, 0)

    # ---- no secret, in any form (Codex's reproductions, and the ways around them) ----
    def assert_no_secret(self, out, *secrets):
        for secret in secrets:
            self.assertNotIn(secret, out, "a secret left the host")
            for form in (secret.encode(), secret.upper().encode()):
                self.assertNotIn(hashlib.sha256(form).hexdigest(), out, "a plain hash of a secret is a guessing oracle")
            self.assertNotIn(hashlib.sha256((secret + "\n").encode()).hexdigest(), out)

    def test_a_secret_in_a_systemd_start_command_never_leaves_the_host(self):
        self.unit_text = "[Service]\\nUser=404-probe\\nExecStart=%s --token R05_SYNTHETIC_SECRET_NOT_REAL --password=hunter2-password --enroll-token enrollValue1234567890 --server https://u:UrlPassword42@example.org\\nEnvironment=PROBE_404_TOKEN=DropInSecret987\\n" % self.START
        self.write_systemctl()
        out = self.run_script()
        self.assert_no_secret(out, "R05_SYNTHETIC_SECRET_NOT_REAL", "hunter2-password", "enrollValue1234567890", "UrlPassword42", "DropInSecret987")
        facts = snapshot.parse(out)
        args = facts["unit.404-probe-agent.service.exec.args"]
        self.assertIn("--token=<redacted>", args)
        self.assertIn("--password=<redacted>", args)
        self.assertIn("--enroll-token=<redacted>", args)
        self.assertIn("--server=<redacted>", args, "an address with credentials in it is not shown")
        self.assertIn("--interval=10s", args)
        snapshot.check_no_secret(facts)

    def test_a_secret_in_an_openrc_script_never_leaves_the_host(self):
        script = 'command="/usr/local/bin/404-probe-agent"\ncommand_args="--interval 10s --token R05_SYNTHETIC_OPENRC_SECRET"\ncommand_user="404-probe:404-probe"\nexport PROBE_404_TOKEN=R05_SYNTHETIC_OPENRC_SECRET\nexport PROBE_404_SERVER="http://127.0.0.1:18080"\nexport PROBE_404_PASSWORD=\'hunter2-password\'\nsupervisor=supervise-daemon\n'
        out = self.run_script(fixture=self.openrc_fixture(script))
        self.assert_no_secret(out, "R05_SYNTHETIC_OPENRC_SECRET", "hunter2-password")
        facts = snapshot.parse(out)
        self.assertEqual(facts["host.init"], "openrc")
        self.assertEqual(facts["hardening.404-probe-agent.args"], "--interval=10s --token=<redacted>")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_TOKEN"], "<redacted>")
        self.assertRegex(facts["hardening.404-probe-agent.export.PROBE_404_TOKEN.mac"], r"^[0-9a-f]{64}$")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_SERVER"], "http://127.0.0.1:18080")
        self.assertEqual(facts["hardening.404-probe-agent.run"], "command=/usr/local/bin/404-probe-agent;command_user=404-probe:404-probe;supervisor=supervise-daemon;")
        snapshot.check_no_secret(facts)

    def test_the_environment_file_shows_only_allowlisted_values(self):
        self.env_lines += ["SOME_NEW_SETTING=enrollValue1234567890", "PROBE_404_SERVER_PASSWORD=hunter2-password", "PROBE_404_WEB_PUBLIC_ORIGIN=https://x:UrlPassword42@example.org"]
        self.write_env()
        out = self.run_script()
        self.assert_no_secret(out, TOKEN, "enrollValue1234567890", "hunter2-password", "UrlPassword42")
        facts = snapshot.parse(out)
        self.assertEqual(facts["config.env.SOME_NEW_SETTING"], "<redacted>", "a name nobody listed is redacted by default")
        self.assertEqual(facts["config.env.PROBE_404_WEB_PUBLIC_ORIGIN"], "<redacted>")

    # ---- a changed credential is found without saying what it was ----
    def test_a_replaced_token_is_a_difference_and_neither_value_is_in_the_snapshot(self):
        first = self.run_script()
        self.env_lines[2] = "PROBE_404_TOKEN=" + "B" * 43
        self.write_env()
        second = self.run_script()
        self.assert_no_secret(first + second, TOKEN, "B" * 43)
        rows = snapshot.compare_facts(snapshot.parse(first), snapshot.parse(second))
        self.assertEqual({(r[0], r[1]) for r in rows}, {("unexpected", "config.env.PROBE_404_TOKEN.mac"), ("unexpected", "config.agent.env.mac")})

    def test_the_same_credential_gives_the_same_digest_and_another_key_another_one(self):
        a, b = snapshot.parse(self.run_script()), snapshot.parse(self.run_script())
        self.assertEqual(snapshot.compare_facts(a, b), [])
        other = snapshot.parse(self.run_script(key="f" * 64))
        self.assertNotEqual(a["config.env.PROBE_404_TOKEN.mac"], other["config.env.PROBE_404_TOKEN.mac"], "digests of different directories are not comparable and say nothing about each other")

    def test_a_change_in_the_unit_or_its_start_command_is_a_difference(self):
        first = snapshot.parse(self.run_script())
        self.unit_text = "[Service]\\nUser=404-probe\\nExecStart=%s --token R05_SYNTHETIC_SECRET_NOT_REAL\\n" % self.START
        self.write_systemctl()
        out = self.run_script()
        keys = {r[1] for r in snapshot.compare_facts(first, snapshot.parse(out))}
        self.assertIn("unit.404-probe-agent.service.mac", keys)
        self.assertIn("unit.404-probe-agent.service.exec.args", keys)
        self.assert_no_secret(out, "R05_SYNTHETIC_SECRET_NOT_REAL")

    def test_the_same_host_twice_and_an_upgrade(self):
        first, second = snapshot.parse(self.run_script()), snapshot.parse(self.run_script())
        self.assertEqual(snapshot.compare_facts(first, second), [])
        self.write("usr/local/bin/404-probe-agent", "#!/bin/sh\necho v1.0.5\n", executable=True)
        upgraded = snapshot.parse(self.run_script())
        self.assertEqual({(r[0], r[1]) for r in snapshot.compare_facts(first, upgraded)}, {("expected", "agent.version"), ("expected", "binary.agent.sha256")})

    def test_collect_goes_through_ssh_and_survives_one_unreachable_host(self):
        script = self.run_script()

        def runner(cmd, input=None, capture_output=None, timeout=None):
            self.assertEqual(cmd[0], "ssh")
            self.assertIn("BatchMode=yes", cmd)
            self.assertEqual(cmd[-2:], ["sh", "-s"])
            self.assertIn(b"SNAPSHOT_KEY=", input)
            host = cmd[cmd.index("sh") - 1]
            if host == "down":
                return subprocess.CompletedProcess(cmd, 255, b"", b"Connection closed")
            return subprocess.CompletedProcess(cmd, 0, script.encode(), b"")

        with tempfile.TemporaryDirectory() as out:
            self.assertEqual(snapshot.main(["collect", "--label", "before", "--out", out, "up", "down"], runner=runner), 1)
            self.assertEqual(sorted(n for n in os.listdir(out) if n.endswith(".json")), ["before-up.json"])
            with open(os.path.join(out, "before-up.json"), encoding="utf-8") as handle:
                self.assertEqual(json.load(handle)["facts"]["agent.version"], "v1.0.4")
            self.assertEqual(snapshot.main(["collect", "--label", "after", "--out", out, "up"], runner=runner), 0)
            self.assertEqual(snapshot.main(["compare", out, "before", "after"]), 0)

    def test_a_token_shaped_answer_from_a_host_is_not_written(self):
        def runner(cmd, input=None, capture_output=None, timeout=None):
            return subprocess.CompletedProcess(cmd, 0, b"host.init\tsystemd\nsome.unknown.field\tZk3Qm9xTpL2vNc8RbYh4WdJf7GsA1eUoXiK5tPqE6\n", b"")

        with tempfile.TemporaryDirectory() as out:
            self.assertEqual(snapshot.main(["collect", "--label", "before", "--out", out, "h"], runner=runner), 1)
            self.assertEqual([n for n in os.listdir(out) if n.endswith(".json")], [])


class ReadOnly(unittest.TestCase):
    def test_the_host_script_cannot_change_anything(self):
        with open(SCRIPT, encoding="utf-8") as handle:
            text = handle.read()
        body = "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
        for forbidden in [r"\brm\b", r"\bmv\b", r"\bcp\b", r"\bchmod\b", r"\bchown\b", r"\btee\b", r"sed\s+-i", r"\bsystemctl\s+(start|stop|restart|reload|enable|disable|daemon-reload|mask|unmask|kill|edit|set-property)\b",
                          r"\brc-service\s+\S+\s+(start|stop|restart|reload)\b", r"\brc-update\s+(add|del)\b", r"\bkill\b", r"\bapt\b", r"\bapk\b", r"\bcurl\b", r"\bwget\b"]:
            self.assertIsNone(re.search(forbidden, body), forbidden)
        # Redirections go to /dev/null or into another descriptor, or are the loop's "< file" input, never into a file.
        for target in re.findall(r"(?:^|\s)\d?>>?\s*(\S+)", body, re.M):
            self.assertTrue(target == "/dev/null" or target.startswith("&"), target)
        self.assertNotRegex(text, "\r")


if __name__ == "__main__":
    unittest.main()
