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


WIDE_ACL = r"""D:\records\.snapshot-key BUILTIN\Administrators:(I)(F)
                    NT AUTHORITY\SYSTEM:(I)(F)
                    NT AUTHORITY\Authenticated Users:(I)(M)
                    BUILTIN\Users:(I)(RX)

Successfully processed 1 files; Failed processing 0 files
"""
ME = r"DESKTOP-1\alice"


def widen(path):
    """Let somebody else read the file."""
    if os.name == "nt":
        subprocess.run([snapshot.system_tool("icacls"), path, "/inheritance:e", "/grant", "*S-1-1-0:(R)"], check=True, capture_output=True)
    else:
        os.chmod(path, 0o644)


class KeyFile(unittest.TestCase):
    """The key of the digests is private to the user who made it, from the moment it exists, and a key that is not is refused."""

    def test_the_acl_of_a_private_key_is_recognised_and_every_other_acl_is_not(self):
        path = r"D:\records\.snapshot-key"
        self.assertTrue(snapshot.acl_is_private(path + " " + ME + ":(R,W)\n\nSuccessfully processed 1 files; Failed processing 0 files\n", path, ME))
        self.assertTrue(snapshot.acl_is_private(path + " " + ME.upper() + ":(F)\n", path, ME), "names are not case sensitive")
        self.assertTrue(snapshot.acl_is_private(path + " " + ME + ":(R,W)\n\n\u5df2\u6210\u529f\u5904\u7406 1 \u4e2a\u6587\u4ef6\n", path, ME), "the summary line is in the language of the machine")
        for text, why in [(WIDE_ACL, "inherited Users, Authenticated Users, SYSTEM and Administrators"),
                          (path + " " + ME + ":(I)(F)\n", "inherited from the directory"),
                          (path + " " + ME + ":(R,W)\n" + " " * 30 + r"NT AUTHORITY\SYSTEM:(F)" + "\n", "another principal"),
                          (path + " " + ME + ":(R,W)\n" + " " * 30 + r"Everyone:(R)" + "\n", "Everyone"),
                          (path + r" DESKTOP-1\bob:(R,W)" + "\n", "somebody else"),
                          (path + " " + ME + ":(R,W)\n" + " " * 30 + ME + r"2:(R)" + "\n", "a name that only starts like ours"),
                          ("", "nothing at all"),
                          ("Successfully processed 0 files; Failed processing 1 files\n", "no entry to read")]:
            self.assertFalse(snapshot.acl_is_private(text, path, ME), why)

    def test_a_new_key_is_private_as_soon_as_it_exists_and_checked_after_it_is_written(self):
        with tempfile.TemporaryDirectory() as directory:
            key = snapshot.snapshot_key(directory)
            path = os.path.join(directory, ".snapshot-key")
            self.assertTrue(snapshot.key_is_private(path))
            if os.name == "nt":
                listing = subprocess.run([snapshot.system_tool("icacls"), path], capture_output=True).stdout.decode("oem", errors="replace")
                listing = listing.replace(path, "")
                for wide in ("Users", "Everyone", "Authenticated", "SYSTEM", "Administrators", "(I)"):
                    self.assertNotIn(wide, listing, listing)
            else:
                self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600)
            self.assertEqual(snapshot.snapshot_key(directory), key)

    def test_a_key_that_others_can_read_is_refused_not_used(self):
        with tempfile.TemporaryDirectory() as directory:
            snapshot.snapshot_key(directory)
            path = os.path.join(directory, ".snapshot-key")
            widen(path)
            self.assertFalse(snapshot.key_is_private(path))
            with self.assertRaises(snapshot.KeyPermissionError):
                snapshot.snapshot_key(directory)

            def runner(cmd, input=None, capture_output=None, timeout=None):
                self.fail("nothing is collected with a key that others can read")

            self.assertNotEqual(snapshot.main(["collect", "--label", "before", "--out", directory, "h"], runner=runner), 0)
            self.assertEqual([n for n in os.listdir(directory) if n.endswith(".json")], [])
            snapshot.make_private(path)
            self.assertTrue(snapshot.key_is_private(path))
            snapshot.snapshot_key(directory)

    def test_when_the_permissions_cannot_be_confirmed_no_key_is_left_behind(self):
        def denied(cmd, **kw):
            return subprocess.CompletedProcess(cmd, 1, b"", b"denied")

        def unreadable(cmd, **kw):
            return subprocess.CompletedProcess(cmd, 0, b"nothing we can read\n", b"")

        def missing(cmd, **kw):
            raise OSError("no icacls")

        for failing in (denied, unreadable, missing):
            with tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(snapshot.KeyPermissionError):
                    snapshot.snapshot_key(directory, runner=failing, windows=True)
                self.assertFalse(os.path.exists(os.path.join(directory, ".snapshot-key")), "a key whose permissions are not confirmed is removed")

    def test_a_key_that_group_or_others_can_reach_is_refused_on_posix(self):
        if os.name != "posix":
            self.skipTest("POSIX modes")
        with tempfile.TemporaryDirectory() as directory:
            snapshot.snapshot_key(directory)
            path = os.path.join(directory, ".snapshot-key")
            for mode in (0o640, 0o604, 0o660, 0o666, 0o700 | 0o010):
                os.chmod(path, mode)
                self.assertFalse(snapshot.key_is_private(path), oct(mode))


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
        self.unit_text = "[Service]\nUser=404-probe\nProtectSystem=strict\nExecStart=%s\n" % self.START
        self.write_systemctl()

    def write_env(self):
        self.write("etc/404-probe/agent.env", "\n".join(self.env_lines) + "\n")

    def write_systemctl(self):
        # The unit text is a file the stand-in prints, so quotes, backslashes and newlines in it reach the script as they are.
        self.write("unit.txt", self.unit_text)
        self.write("bin/systemctl", """#!/bin/sh
case "$1" in
  cat) case "$2" in 404-probe-agent.service) cat "$(dirname "$0")/../unit.txt";; *) exit 1;; esac ;;
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

    def written(self, out):
        """What collect writes to disk for this host answer: '' when collect refuses it."""
        def runner(cmd, input=None, capture_output=None, timeout=None):
            return subprocess.CompletedProcess(cmd, 0, out.encode(), b"")

        with tempfile.TemporaryDirectory() as directory:
            snapshot.main(["collect", "--label", "before", "--out", directory, "h"], runner=runner)
            path = os.path.join(directory, "before-h.json")
            if not os.path.exists(path):
                return ""
            with open(path, encoding="utf-8") as handle:
                return handle.read()

    def assert_clean(self, out, *secrets):
        """No secret on the host's answer, and none in the file collect writes from it."""
        self.assert_no_secret(out, *secrets)
        self.assert_no_secret(self.written(out), *secrets)

    def run_unit(self, tail, extra=""):
        self.unit_text = "[Service]\nUser=404-probe\n%sExecStart=%s %s\n" % (extra, self.START, tail)
        self.write_systemctl()
        return self.run_script()

    def run_env(self, *lines):
        self.env_lines = ["PROBE_404_SERVER=https://example.org", "PROBE_404_AGENT_ID=abc"] + list(lines)
        self.write_env()
        return self.run_script()

    def run_openrc(self, *lines):
        return self.run_script(fixture=self.openrc_fixture("\n".join(lines) + "\n"))

    # ---- the attack: a credential in every place and shape we could think of ----
    def test_a_credential_in_a_systemd_start_command_in_any_form(self):
        # (arguments, the credentials in them). Every form of a flag, a value that starts with "-", quotes, escapes, URLs.
        cases = [
            ("--token=Qz1Kw", "Qz1Kw"),
            ("--token Qz2Kw", "Qz2Kw"),
            ("-t Qz3Kw", "Qz3Kw"),
            ("--token -Qz4Kw", "-Qz4Kw"),
            ("--token --Qz5Kw", "--Qz5Kw"),
            ("--token=-Qz6Kw", "-Qz6Kw"),
            ("-t -Qz7Kw", "-Qz7Kw"),
            ("--token -" + "Zk3Qm9xTpL2vNc8RbYh4WdJf7GsA1eUoXiK5tPqE6", "Zk3Qm9xTpL2vNc8RbYh4WdJf7GsA1eUoXiK5tPqE6"),
            ("--token\t-Qz8Kw", "-Qz8Kw"),
            ('--token "Qz9Kw"', "Qz9Kw"),
            ("--token 'Qz10Kw'", "Qz10Kw"),
            ("--token 'Qz11Kw Qz12Kw'", "Qz11Kw"),
            ("--token=Qz13Kw\\ Qz14Kw", "Qz13Kw"),
            ("--password=Qz15Kw --pass Qz16Kw -p Qz17Kw", "Qz15Kw"),
            ("--password=Qz15Kw --pass Qz16Kw -p Qz17Kw", "Qz16Kw"),
            ("--password=Qz15Kw --pass Qz16Kw -p Qz17Kw", "Qz17Kw"),
            ("--interval 10s Qz18Kw", "Qz18Kw"),
            ("-- --token Qz19Kw", "Qz19Kw"),
            ("--interval -Qz20Kw", "-Qz20Kw"),
            ("--interval=Qz21Kw", "Qz21Kw"),
            ("--timeout Qz22Kw", "Qz22Kw"),
            ("--listen 127.0.0.1:8080?x=Qz23Kw", "Qz23Kw"),
            ("--db /var/lib/x?pw=Qz24Kw", "Qz24Kw"),
            ("--db=/var/lib/x/Qz25Kw", "Qz25Kw"),
            ("--control-token-file=/etc/x?t=Qz26Kw", "Qz26Kw"),
            ("--server https://example.org/?password=Qz27Kw", "Qz27Kw"),
            ("--server=https://example.org/?password=Qz28Kw", "Qz28Kw"),
            ("--server https://example.org?token=Qz29Kw", "Qz29Kw"),
            ("--server https://example.org/#Qz30Kw", "Qz30Kw"),
            ("--server https://example.org/path/Qz31Kw", "Qz31Kw"),
            ("--server https://u:Qz32Kw@example.org", "Qz32Kw"),
            ("--server https://Qz33Kw@example.org", "Qz33Kw"),
            ("--server=https://u:Qz34Kw@example.org:8443/x", "Qz34Kw"),
            ("--server https://example.org:Qz35Kw", "Qz35Kw"),
            ("--server http://[::1]:8080/?k=Qz36Kw", "Qz36Kw"),
            ("--server -Qz37Kw", "-Qz37Kw"),
            ("--web-public-origin https://example.org?x=Qz38Kw", "Qz38Kw"),
            ("--web-public-origin=https://u:Qz39Kw@example.org", "Qz39Kw"),
            ("--web-password-hash-file /etc/x --web-password Qz40Kw", "Qz40Kw"),
            ("--allow-insecure-http Qz41Kw", "Qz41Kw"),
            ("--allow-insecure-http=Qz42Kw", "Qz42Kw"),
            ("serve Qz43Kw", "Qz43Kw"),
            ("Qz44Kw", "Qz44Kw"),
            ("-Qz45Kw", "-Qz45Kw"),
            ("--Qz46Kw", "--Qz46Kw"),
            ("-Qz47Kw=Qz48Kw", "Qz47Kw"),
            ("-Qz47Kw=Qz48Kw", "Qz48Kw"),
            ("--TOKEN=Qz49Kw --Token Qz50Kw", "Qz49Kw"),
            ("--TOKEN=Qz49Kw --Token Qz50Kw", "Qz50Kw"),
            ("$TOKEN ${Qz52Kw} $(cat /x) `cat /y`", "Qz52Kw"),
            ("--token=$(cat /etc/Qz51Kw)", "Qz51Kw"),
        ]
        for tail, secret in cases:
            with self.subTest(tail=tail, secret=secret):
                out = self.run_unit(tail)
                self.assert_clean(out, secret)
                facts = snapshot.parse(out)
                self.assertIn("--interval=10s", facts["unit.404-probe-agent.service.exec.args"], "what is allowlisted and valid is still shown")
                self.assertRegex(facts["unit.404-probe-agent.service.exec.args.mac"], r"^[0-9a-f]{64}$")

    def test_what_is_shown_of_a_start_command_is_only_what_was_recognised(self):
        out = self.run_unit("--token Qz1Kw --server https://example.org --listen 127.0.0.1:8080 --db /var/lib/404-probe/x.db -t -Qz2Kw --job-interval -Qz3Kw")
        args = snapshot.parse(out)["unit.404-probe-agent.service.exec.args"]
        self.assertEqual(args, "--interval=10s --job-interval=10s --timeout=8s --allow-insecure-http <redacted> <redacted> --server=https://example.org --listen=127.0.0.1:8080 --db=/var/lib/404-probe/x.db <redacted> <redacted> --job-interval=<redacted>")
        self.assertNotIn("--token", args, "the name of a flag that nobody listed is not shown either")

    def test_the_effective_start_command_of_a_drop_in_and_a_continued_line(self):
        # A drop-in resets ExecStart= and sets its own: the first line is empty, the effective one is the last.
        self.unit_text = "[Service]\nExecStart=%s --token Qz1Kw\n\n[Service]\nExecStart=\nExecStart=%s --token Qz2Kw\nExecStartPre=/bin/true --token Qz3Kw\nEnvironment=PROBE_404_TOKEN=Qz4Kw\nEnvironment=\"PROBE_404_TOKEN=Qz5Kw\"\nEnvironmentFile=/etc/Qz6Kw\nLoadCredential=t:/etc/Qz7Kw\nSetCredential=t:Qz8Kw\nImportCredential=Qz9Kw\n" % (self.START, self.START)
        self.write_systemctl()
        out = self.run_script()
        self.assert_clean(out, "Qz1Kw", "Qz2Kw", "Qz3Kw", "Qz4Kw", "Qz5Kw", "Qz6Kw", "Qz7Kw", "Qz8Kw", "Qz9Kw")
        self.assertIn("--interval=10s", snapshot.parse(out)["unit.404-probe-agent.service.exec.args"], "the effective line is the one summarised")
        self.unit_text = "[Service]\nExecStart=%s \\\n    --token Qz10Kw \\\n    --password=Qz11Kw\n" % self.START
        self.write_systemctl()
        self.assert_clean(self.run_script(), "Qz10Kw", "Qz11Kw")

    def test_a_credential_in_the_environment_file_in_any_form(self):
        cases = [
            ("PROBE_404_TOKEN=Qz1Kw", "Qz1Kw"),
            ("PROBE_404_TOKEN=-Qz2Kw", "-Qz2Kw"),
            ("PROBE_404_TOKEN=--Qz3Kw", "--Qz3Kw"),
            ("export PROBE_404_TOKEN=Qz4Kw", "Qz4Kw"),
            ("export   PROBE_404_TOKEN=Qz5Kw", "Qz5Kw"),
            ("  PROBE_404_TOKEN=Qz6Kw", "Qz6Kw"),
            ("\tPROBE_404_TOKEN=Qz7Kw", "Qz7Kw"),
            ("PROBE_404_TOKEN = Qz8Kw", "Qz8Kw"),
            ("PROBE_404_TOKEN= Qz9Kw", "Qz9Kw"),
            ("PROBE_404_TOKEN =\tQz10Kw", "Qz10Kw"),
            ('PROBE_404_TOKEN="Qz11Kw"', "Qz11Kw"),
            ("PROBE_404_TOKEN='Qz12Kw'", "Qz12Kw"),
            ('PROBE_404_TOKEN="Qz13Kw', "Qz13Kw"),
            ("PROBE_404_TOKEN=Qz14Kw\r", "Qz14Kw"),
            ("PROBE_404_TOKEN=Qz15Kw # note", "Qz15Kw"),
            ("PROBE_404_TOKEN=Qz16Kw;export X=1", "Qz16Kw"),
            ("PROBE_404_TOKEN=a=Qz17Kw", "Qz17Kw"),
            ("PROBE_404_TOKEN=\\Qz18Kw", "Qz18Kw"),
            ("probe_404_token=Qz19Kw", "Qz19Kw"),
            ("PROBE_404_TOKEN-X=Qz20Kw", "Qz20Kw"),
            ("Qz21Kw=Qz22Kw", "Qz21Kw"),
            ("Qz21Kw=Qz22Kw", "Qz22Kw"),
            ("bad name=Qz23Kw", "Qz23Kw"),
            ("=Qz24Kw", "Qz24Kw"),
            ("PROBE_404_SERVER=https://example.org/?password=Qz25Kw", "Qz25Kw"),
            ('PROBE_404_SERVER="https://example.org/?password=Qz26Kw"', "Qz26Kw"),
            ("PROBE_404_SERVER='https://example.org?token=Qz27Kw'", "Qz27Kw"),
            ("export PROBE_404_SERVER=https://example.org/?k=Qz28Kw", "Qz28Kw"),
            ("PROBE_404_SERVER = https://example.org/?k=Qz29Kw", "Qz29Kw"),
            ("PROBE_404_SERVER=https://example.org#Qz30Kw", "Qz30Kw"),
            ("PROBE_404_SERVER=https://u:Qz31Kw@example.org", "Qz31Kw"),
            ("PROBE_404_SERVER=https://Qz32Kw@example.org", "Qz32Kw"),
            ("PROBE_404_SERVER=https://example.org/Qz33Kw", "Qz33Kw"),
            ("PROBE_404_SERVER=https://example.org:80/;Qz34Kw", "Qz34Kw"),
            ("PROBE_404_SERVER=-Qz35Kw", "-Qz35Kw"),
            ("PROBE_404_SERVER=https://example.org Qz36Kw", "Qz36Kw"),
            ("PROBE_404_WEB_PUBLIC_ORIGIN=https://example.org?x=Qz37Kw", "Qz37Kw"),
            ("PROBE_404_WEB_PUBLIC_ORIGIN=https://u:Qz38Kw@example.org", "Qz38Kw"),
            ("PROBE_404_AGENT_ID=Qz39Kw # trailing", "Qz39Kw"),
            ("PROBE_404_AGENT_ID=abc?k=Qz40Kw", "Qz40Kw"),
            ("PROBE_404_COUNTRY_CODE=Qz41Kw", "Qz41Kw"),
            ("PROBE_404_STATE=/some?x=Qz42Kw", "Qz42Kw"),
            ("PROBE_404_SECURITY_ACKS=a,b?token=Qz43Kw", "Qz43Kw"),
            ("PROBE_404_SECURITY_EXPORT=/var/Qz44Kw?x", "Qz44Kw"),
            ("PROBE_404_SELECTOR_ORDER=a;b=Qz45Kw", "Qz45Kw"),
            ("PROBE_404_VERSION=v1.0.5?x=Qz46Kw", "Qz46Kw"),
            ("PROBE_404_ALLOW_INSECURE_HTTP=Qz47Kw", "Qz47Kw"),
            ("PROBE_404_SERVER_PASSWORD=Qz48Kw", "Qz48Kw"),
            ("PROBE_404_ENROLL_TOKEN=-Qz49Kw", "-Qz49Kw"),
        ]
        for line, secret in cases:
            with self.subTest(line=line):
                out = self.run_env(line)
                self.assert_clean(out, secret)
                facts = snapshot.parse(out)
                self.assertRegex(facts["config.agent.env.mac"], r"^[0-9a-f]{64}$")

    def test_a_multiline_value_in_the_environment_file(self):
        out = self.run_env('PROBE_404_TOKEN="Qz1Kw', "Qz2Kw=Qz3Kw", 'Qz4Kw"', "PROBE_404_X='Qz5Kw", "Qz6Kw'", "PROBE_404_Y=Qz7Kw\\", "Qz8Kw")
        self.assert_clean(out, "Qz1Kw", "Qz2Kw", "Qz3Kw", "Qz4Kw", "Qz5Kw", "Qz6Kw", "Qz7Kw", "Qz8Kw")

    def test_an_environment_file_names_nobody_listed_are_not_shown_and_a_valid_value_is(self):
        out = self.run_env("Qz1Kw=1", "PROBE_404_COUNTRY_CODE=US", "export PROBE_404_STATE=/var/lib/404-probe/agent.epoch", 'PROBE_404_SERVER="https://example.org:8443"', "PROBE_404_WEB_PUBLIC_ORIGIN=https://probe.example.org")
        facts = snapshot.parse(out)
        self.assertNotIn("Qz1Kw", " ".join(facts))
        self.assertEqual(facts["config.env.PROBE_404_COUNTRY_CODE"], "US")
        self.assertEqual(facts["config.env.PROBE_404_STATE"], "/var/lib/404-probe/agent.epoch")
        self.assertEqual(facts["config.env.PROBE_404_SERVER"], "https://example.org:8443")
        self.assertEqual(facts["config.env.PROBE_404_WEB_PUBLIC_ORIGIN"], "https://probe.example.org")

    def test_a_redacted_allowlisted_value_still_shows_that_it_changed(self):
        a = snapshot.parse(self.run_env("PROBE_404_SERVER=https://example.org/?password=Qz1Kw"))
        b = snapshot.parse(self.run_env("PROBE_404_SERVER=https://example.org/?password=Qz2Kw"))
        self.assertEqual(a["config.env.PROBE_404_SERVER"], "<redacted>")
        self.assertNotEqual(a["config.env.PROBE_404_SERVER.mac"], b["config.env.PROBE_404_SERVER.mac"])

    def test_a_credential_in_an_openrc_script_in_any_form(self):
        base = ['command="/usr/local/bin/404-probe-agent"', 'command_user="404-probe:404-probe"']
        cases = [
            (['command_args="--interval 10s --token Qz1Kw"'], "Qz1Kw"),
            (["command_args='--token -Qz2Kw'"], "-Qz2Kw"),
            (['command_args="--token=Qz3Kw"'], "Qz3Kw"),
            (['command_args="-t Qz4Kw"'], "Qz4Kw"),
            (['command_args="-t -Qz5Kw"'], "-Qz5Kw"),
            (["command_args=--token Qz6Kw"], "Qz6Kw"),
            (['command_args="--server https://example.org/?password=Qz7Kw"'], "Qz7Kw"),
            (['command_args="--server=https://u:Qz8Kw@example.org"'], "Qz8Kw"),
            (['command_args="--interval 10s \\', '    --token Qz9Kw"'], "Qz9Kw"),
            (['command_args="--interval 10s', '  --token Qz10Kw', '  --password=Qz11Kw"'], "Qz10Kw"),
            (['command_args="--interval 10s', '  --token Qz10Kw', '  --password=Qz11Kw"'], "Qz11Kw"),
            (['command_args="${command_args} --token Qz12Kw"'], "Qz12Kw"),
            (['command_args+=" --token Qz13Kw"'], "Qz13Kw"),
            (['command_args="--token $(cat /etc/Qz14Kw)"'], "Qz14Kw"),
            (['  command_args="--token Qz15Kw"'], "Qz15Kw"),
            (['command_args = "--token Qz16Kw"'], "Qz16Kw"),
            (['command="/usr/local/bin/404-probe-agent --token Qz17Kw"'], "Qz17Kw"),
            (['command=/usr/local/bin/404-probe-agent', 'command_user="404-probe:404-probe --pw Qz18Kw"'], "Qz18Kw"),
            (['supervisor="supervise-daemon --env PROBE_404_TOKEN=Qz19Kw"'], "Qz19Kw"),
            (['capabilities="^cap_net_raw=Qz20Kw"'], "Qz20Kw"),
            (['rc_ulimit="-n Qz21Kw"'], "Qz21Kw"),
            (['rc_cgroup_memory="Qz22Kw"'], "Qz22Kw"),
            (["export PROBE_404_TOKEN=Qz23Kw"], "Qz23Kw"),
            (["export PROBE_404_TOKEN=-Qz24Kw"], "-Qz24Kw"),
            (['export PROBE_404_TOKEN="Qz25Kw"'], "Qz25Kw"),
            (["  export PROBE_404_TOKEN=Qz26Kw"], "Qz26Kw"),
            (["\texport PROBE_404_TOKEN='Qz27Kw'"], "Qz27Kw"),
            (["export PROBE_404_A=1 PROBE_404_TOKEN=Qz28Kw"], "Qz28Kw"),
            (["export  PROBE_404_TOKEN = Qz29Kw"], "Qz29Kw"),
            (["export PROBE_404_TOKEN=Qz30Kw;"], "Qz30Kw"),
            (['export PROBE_404_SERVER="https://example.org/?password=Qz31Kw"'], "Qz31Kw"),
            (["export PROBE_404_SERVER=https://example.org#Qz32Kw"], "Qz32Kw"),
            (["export PROBE_404_SERVER=https://u:Qz33Kw@example.org"], "Qz33Kw"),
            (["export PROBE_404_SERVER=-Qz34Kw"], "-Qz34Kw"),
            (["export PROBE_404_STATE=/x?Qz35Kw"], "Qz35Kw"),
            (['export PROBE_404_PASSWORD="Qz36Kw', 'Qz37Kw"'], "Qz36Kw"),
            (['export PROBE_404_PASSWORD="Qz36Kw', 'Qz37Kw"'], "Qz37Kw"),
        ]
        for lines, secret in cases:
            with self.subTest(lines=lines):
                out = self.run_openrc(*(base + lines))
                self.assert_clean(out, secret)
                self.assertEqual(snapshot.parse(out)["host.init"], "openrc")

    def test_what_is_shown_of_an_openrc_script_is_only_what_was_recognised(self):
        out = self.run_openrc('command="/usr/local/bin/404-probe-agent"', 'command_args="--interval 10s --token Qz1Kw --server http://127.0.0.1:18080 -t -Qz2Kw"', 'command_user="404-probe:404-probe"',
                              "export PROBE_404_SERVER=http://127.0.0.1:18080", "export PROBE_404_TOKEN=Qz3Kw", "supervisor=supervise-daemon", 'rc_ulimit="-n 1024"', 'capabilities="CAP_NET_RAW,CAP_NET_BIND_SERVICE"')
        facts = snapshot.parse(out)
        self.assertEqual(facts["hardening.404-probe-agent.args"], "--interval=10s <redacted> <redacted> --server=http://127.0.0.1:18080 <redacted> <redacted>")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_SERVER"], "http://127.0.0.1:18080")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_TOKEN"], "<redacted>")
        self.assertEqual(facts["hardening.404-probe-agent.run"], "command=/usr/local/bin/404-probe-agent;command_user=404-probe:404-probe;supervisor=supervise-daemon;rc_ulimit=-n 1024;capabilities=CAP_NET_RAW,CAP_NET_BIND_SERVICE;")

    def test_capabilities_are_shown_only_as_a_list_of_canonical_names(self):
        for value in ("CAP_NET_RAW", "CAP_NET_RAW,CAP_NET_BIND_SERVICE", "CAP_BPF,CAP_PERFMON,CAP_CHECKPOINT_RESTORE"):
            with self.subTest(shown=value):
                out = self.run_openrc('command="/usr/local/bin/404-probe-agent"', 'capabilities="%s"' % value)
                self.assertIn("capabilities=%s;" % value, snapshot.parse(out)["hardening.404-probe-agent.run"])
        for value, secret in (("^cap_net_raw=Qz1Kw", "Qz1Kw"), ("^cap_net_raw+ep", "cap_net_raw"), ("cap_net_raw", "cap_net_raw"), ("CAP_NET_RAW=Qz2Kw", "Qz2Kw"), ("CAP_Qz3Kw", "Qz3Kw"),
                              ("CAP_NET_RAW,Qz4Kw", "Qz4Kw"), ("CAP_NET_RAW,", "x"), (",CAP_NET_RAW", "x"), ("CAP_NET_RAW,,CAP_BPF", "x"), ("CAP_NET_RAW CAP_BPF", "x"), ("CAP_NET_RAW,CAP_Qz5Kw", "Qz5Kw"),
                              ("CAP_NET_RAW+ep", "x"), ("cap_net_raw,CAP_NET_RAW", "x"), ("CAP_NET_RAW,-Qz6Kw", "Qz6Kw"), ("", "x")):
            with self.subTest(redacted=value):
                out = self.run_openrc('command="/usr/local/bin/404-probe-agent"', 'capabilities="%s"' % value)
                run = snapshot.parse(out)["hardening.404-probe-agent.run"]
                self.assertIn("capabilities=<redacted>;", run)
                self.assertNotIn(secret, run)
                self.assert_clean(out, secret) if secret != "x" and secret != "cap_net_raw" else None

    def test_an_openrc_value_that_is_not_what_its_name_says_is_not_shown(self):
        out = self.run_openrc('command="/usr/local/bin/404-probe-agent --token Qz1Kw"', 'command_user="Qz2Kw?x"', 'supervisor="Qz3Kw@host"')
        self.assertEqual(snapshot.parse(out)["hardening.404-probe-agent.run"], "command=<redacted>;command_user=<redacted>;supervisor=<redacted>;")

    def test_a_secret_in_a_systemd_start_command_never_leaves_the_host(self):
        out = self.run_unit("--token R05_SYNTHETIC_SECRET_NOT_REAL --password=hunter2-password --enroll-token enrollValue1234567890 --server https://u:UrlPassword42@example.org", extra="Environment=PROBE_404_TOKEN=DropInSecret987\n")
        self.assert_clean(out, "R05_SYNTHETIC_SECRET_NOT_REAL", "hunter2-password", "enrollValue1234567890", "UrlPassword42", "DropInSecret987")
        facts = snapshot.parse(out)
        args = facts["unit.404-probe-agent.service.exec.args"]
        self.assertIn("--interval=10s", args)
        self.assertIn("--server=<redacted>", args, "an address with credentials in it is not shown")
        snapshot.check_no_secret(facts)

    def test_the_environment_file_shows_only_allowlisted_values(self):
        self.env_lines += ["SOME_NEW_SETTING=enrollValue1234567890", "PROBE_404_SERVER_PASSWORD=hunter2-password", "PROBE_404_WEB_PUBLIC_ORIGIN=https://x:UrlPassword42@example.org"]
        self.write_env()
        out = self.run_script()
        self.assert_clean(out, TOKEN, "enrollValue1234567890", "hunter2-password", "UrlPassword42")
        facts = snapshot.parse(out)
        self.assertEqual(facts["config.env.SOME_NEW_SETTING"], "<redacted>", "a name nobody listed is redacted by default")
        self.assertEqual(facts["config.env.PROBE_404_WEB_PUBLIC_ORIGIN"], "<redacted>")

    def test_a_secret_in_an_openrc_script_never_leaves_the_host(self):
        script = 'command="/usr/local/bin/404-probe-agent"\ncommand_args="--interval 10s --token R05_SYNTHETIC_OPENRC_SECRET"\ncommand_user="404-probe:404-probe"\nexport PROBE_404_TOKEN=R05_SYNTHETIC_OPENRC_SECRET\nexport PROBE_404_SERVER="http://127.0.0.1:18080"\nexport PROBE_404_PASSWORD=\'hunter2-password\'\nsupervisor=supervise-daemon\n'
        out = self.run_script(fixture=self.openrc_fixture(script))
        self.assert_clean(out, "R05_SYNTHETIC_OPENRC_SECRET", "hunter2-password")
        facts = snapshot.parse(out)
        self.assertEqual(facts["host.init"], "openrc")
        self.assertEqual(facts["hardening.404-probe-agent.args"], "--interval=10s <redacted> <redacted>")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_TOKEN"], "<redacted>")
        self.assertRegex(facts["hardening.404-probe-agent.export.PROBE_404_TOKEN.mac"], r"^[0-9a-f]{64}$")
        self.assertEqual(facts["hardening.404-probe-agent.export.PROBE_404_SERVER"], "http://127.0.0.1:18080")
        self.assertEqual(facts["hardening.404-probe-agent.run"], "command=/usr/local/bin/404-probe-agent;command_user=404-probe:404-probe;supervisor=supervise-daemon;")
        snapshot.check_no_secret(facts)

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
        out = self.run_unit("--token R05_SYNTHETIC_SECRET_NOT_REAL")
        keys = {r[1] for r in snapshot.compare_facts(first, snapshot.parse(out))}
        self.assertIn("unit.404-probe-agent.service.mac", keys)
        self.assertIn("unit.404-probe-agent.service.exec.args.mac", keys, "a changed start command shows even when everything unknown in it is redacted")
        self.assert_clean(out, "R05_SYNTHETIC_SECRET_NOT_REAL")

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
