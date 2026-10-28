#!/usr/bin/env python3
"""Fail-closed boundary for tests that execute installer functions.

No production installer environment variable enables this boundary. Linux needs
both bubblewrap and strace; other hosts skip execution instead of trusting mocks.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import shlex
import subprocess
import sys
import tempfile

# Rebase complete managed roots, including OpenRC units/logs and OS/default
# paths used by extracted functions. Kernel/process pseudo-files remain read-only.
PREFIXES = ("/etc", "/usr/local/bin", "/usr/local/sbin", "/var/lib", "/var/log", "/run")
SKIP = 77


def redirect(source, root):
    """One pass also covers the helper heredoc and previous/candidate/lock names."""
    # Do not rewrite an already redirected /fixture/.../etc path or a harness
    # variable such as ${temporary}/etc/404-probe a second time.
    paths = "(?:" + "|".join(map(re.escape, PREFIXES)) + ")"
    # Consume shell parameter operators explicitly: '-' otherwise also occurs
    # in path components, where rewriting would incorrectly rebase a child path.
    pattern = r"(\$\{[A-Za-z_][A-Za-z0-9_]*(?::?[-=+?])[\"']?)(" + paths + r")|(?:(?<![A-Za-z0-9_}/.\-])|(?<=[\s=]-))(" + paths + ")"
    return re.sub(pattern, lambda m: (m.group(1) or "") + Path(root).as_posix() + "/system" + (m.group(2) or m.group(3)), source)


def checked_path(root, value):
    """Command-wrapper contract: check every ancestor, not just the leaf."""
    root = Path(root).resolve(strict=True)
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts or path == root:
        raise ValueError("fixture requires an absolute child path")
    if not path.is_relative_to(root):
        raise ValueError("outside fixture")
    current = root
    for part in path.relative_to(root).parts:
        current /= part
        if current.is_symlink():
            raise ValueError("fixture symlink")
    if not path.resolve().is_relative_to(root):
        raise ValueError("fixture parent replaced")
    return path


def sandbox(root, command, trace=None):
    empty = root / "empty-system"
    empty.mkdir(exist_ok=True)
    args = ["bwrap", "--die-with-parent", "--unshare-all", "--new-session",
            "--cap-drop", "ALL", "--ro-bind", "/", "/", "--proc", "/proc",
            "--dev", "/dev"]
    # Hide real installed project files from reads as well as writes. These
    # mounts are read-only, including when a test uses an absolute executable.
    for path in ("/etc", "/run", "/var/lib", "/var/log", "/usr/local", "/root", "/home"):
        args += ["--ro-bind", str(empty), path]
    repo = Path(__file__).resolve().parent.parent
    args += ["--ro-bind", str(repo), str(repo)]
    args += ["--bind", str(root / "tmp"), "/tmp", "--bind", str(root), str(root),
             "--chdir", str(root)]
    if trace:
        args += ["strace", "-f", "-qq", "-s", "4096", "-e", "trace=%file",
                 "-o", str(trace)]
    return args + command


CANARY = r'''
set -Eeuo pipefail
root="$INSTALLER_FIXTURE_ROOT"
printf safe >"$root/canary"
/bin/rm "$root/canary"
[[ ! -e "$root/canary" ]]
for path in /run/404-probe /etc/404-probe /var/lib/404-probe /var/lib/404-probe-updater /var/log/404-probe; do
  if mkdir -p "$path" 2>/dev/null; then exit 81; fi
  if printf unsafe >"$path" 2>/dev/null; then exit 82; fi
done
if /bin/touch /etc/fixture-escape 2>/dev/null; then exit 83; fi
ln -s /etc "$root/escape"
if /bin/touch "$root/escape/fixture-escape" 2>/dev/null; then exit 84; fi
/bin/rm "$root/escape"
mkdir "$root/parent"
rmdir "$root/parent"
ln -s /run "$root/parent"
if /bin/touch "$root/parent/fixture-escape" 2>/dev/null; then exit 85; fi
/bin/rm "$root/parent"
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root")
    parser.add_argument("--probe", action="store_true")
    parser.add_argument("--script")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    opts = parser.parse_args()
    if sys.platform != "linux" or not all(shutil.which(x) for x in ("bwrap", "strace", "bash")):
        print("SKIP installer execution: verified Linux bubblewrap + strace boundary unavailable")
        return SKIP
    if not opts.root:
        with tempfile.TemporaryDirectory(prefix="installer-fixture-") as temporary:
            return run(opts, Path(temporary))
    return run(opts, Path(opts.root).resolve(strict=True))


def run(opts, root):
    (root / "tmp").mkdir(exist_ok=True)
    env = dict(os.environ, INSTALLER_FIXTURE_ROOT=str(root), TMPDIR=str(root / "tmp"))
    # A successful canary is mandatory for every invocation, never an env bypass.
    proof = subprocess.run(sandbox(root, ["bash", "-c", CANARY]), env=env,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if proof.returncode:
        print("SKIP installer execution: namespace canary could not prove the boundary")
        print(proof.stderr, end="")
        return SKIP
    if opts.probe:
        print("PASS installer namespace canary: fixture delete; absolute, redirect, symlink and parent escape denied")
        return 0
    repo = Path(__file__).resolve().parent.parent
    installer = root / "installer-fixture.sh"
    installer.write_text(redirect((repo / "install.sh").read_text(encoding="utf-8"), root), encoding="utf-8", newline="\n")
    env["INSTALLER_FIXTURE_INSTALLER"] = str(installer)
    env["BASH_ENV"] = str(repo / "scripts/installer-fixture-commands.sh")
    command = opts.command
    if command[:1] == ["--"]:
        command = command[1:]
    if opts.script:
        # The child entry verifies namespace mounts before accepting this flag.
        env["INSTALLER_FIXTURE_ACTIVE"] = str(root)
        source = Path(opts.script).read_text(encoding="utf-8")
        lines = []
        for line in source.splitlines():
            if "installer-fixture-entry.sh" in line:
                continue  # The outer entry already established/proved the namespace.
            if line.startswith("root=") and "BASH_SOURCE" in line:
                line = "root=" + shlex.quote(str(repo))
            lines.append(line)
        # Rebase assertions and the existing per-test sed mappings together,
        # keeping their custom layout while catching any new literal prefix.
        child = root / "fixture-test.sh"
        child.write_text(redirect("\n".join(lines) + "\n", root), encoding="utf-8", newline="\n")
        command = ["bash", str(child)] + command
    trace = root / "filesystem.trace"
    result = subprocess.run(sandbox(root, command, trace), env=env)
    audit = trace.read_text(encoding="utf-8", errors="replace")
    # Even harmless stat/read attempts against the original project paths fail
    # the test. The namespace already prevents access to their real host objects.
    attempts = re.findall(r'"(/(?:run|etc|var/(?:lib|log)|usr/local/(?:bin|sbin))/[^"\n]*404-probe[^"\n]*)"', audit)
    escaped = [p for p in attempts if not p.startswith(str(root) + "/") and not p.startswith("/tmp/")]
    (root / "filesystem-audit.json").write_text(json.dumps({"canary": "passed", "original_project_access": escaped, "exit": result.returncode}, indent=2) + "\n", encoding="utf-8")
    if escaped:
        print("FAIL installer attempted original project paths: " + repr(sorted(set(escaped))), file=sys.stderr)
        return 86
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
