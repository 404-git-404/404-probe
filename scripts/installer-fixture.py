#!/usr/bin/env python3
"""Fail-closed boundary for tests that execute installer functions.

No production installer environment variable enables this boundary. Linux needs
both bubblewrap and strace; other hosts skip execution instead of trusting mocks.
"""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
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
SOURCE = PurePosixPath('/work/source')
FIXTURE = PurePosixPath('/work/fixture')


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
            "--cap-drop", "ALL", "--tmpfs", "/", "--ro-bind", "/usr", "/usr"]
    # Build mountpoints on a fresh root, never under a masked host directory.
    # Preserve merged-/usr links without exposing a second alias of the host root.
    for name in ('bin', 'sbin', 'lib', 'lib64'):
        path = Path('/') / name
        if path.is_symlink():
            args += ['--symlink', os.readlink(path), str(path)]
        elif path.exists():
            args += ['--ro-bind', str(path), str(path)]
    args += ["--proc", "/proc", "--dev", "/dev"]
    # Hide real installed project files from reads as well as writes. These
    # mounts are read-only, including when a test uses an absolute executable.
    for path in ("/etc", "/run", "/var/lib", "/var/log", "/usr/local", "/root", "/home"):
        args += ["--ro-bind", str(empty), path]
    repo = Path(__file__).resolve().parent.parent
    args += ["--dir", "/work", "--ro-bind", str(repo), str(SOURCE),
             "--bind", str(root), str(FIXTURE),
             "--bind", str(root / "tmp"), "/tmp", "--remount-ro", "/",
             "--chdir", str(FIXTURE)]
    if trace:
        args += ["strace", "-f", "-qq", "-s", "4096", "-e", "trace=%file",
                 "-o", translate(str(trace), root, repo)]
    return args + command


def translate(value, root, repo):
    # Longest prefix first: a Go fixture can be a child of the source checkout.
    mappings = sorted(((str(root), str(FIXTURE)), (str(repo), str(SOURCE))),
                      key=lambda pair: len(pair[0]), reverse=True)
    pattern = '|'.join(re.escape(old) + r'(?=/|$|[\s\x27\x22;:)])' for old, _ in mappings)
    replacements = dict(mappings)
    return re.sub(pattern, lambda match: replacements[match.group()], value)


def project_access(audit):
    return re.findall(r'"(/(?:run|etc|var/(?:lib|log)|usr/local/(?:bin|sbin))/[^"\n]*404-probe[^"\n]*)"', audit)


CANARY = r'''
set -Eeuo pipefail
root="$INSTALLER_FIXTURE_ROOT"
[[ -r /work/source/install.sh ]]
grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status
for mount in / /etc /run /var/lib /var/log /usr/local /root /home /work/source; do
  readonly_mount=
  while read -r mount_id parent_id device mount_root mount_path mount_options remaining; do
    if [[ "$mount_path" == "$mount" && ",$mount_options," == *,ro,* ]]; then
      readonly_mount=1
      break
    fi
  done </proc/self/mountinfo
  [[ "$readonly_mount" == 1 ]]
done
if /bin/touch /work/source/fixture-escape 2>/dev/null; then exit 80; fi
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
    repo = Path(__file__).resolve().parent.parent
    env = {key: translate(value, root, repo) for key, value in os.environ.items()}
    env.update(INSTALLER_FIXTURE_ROOT=str(FIXTURE), TMPDIR=str(FIXTURE / 'tmp'))
    # A successful canary is mandatory for every invocation, never an env bypass.
    canary_trace = root / 'canary.trace'
    proof_env = {key: value for key, value in env.items()
                 if key not in ('BASH_ENV', 'ENV') and not key.startswith('BASH_FUNC_')}
    proof_env['PATH'] = '/usr/bin:/bin'
    proof = subprocess.run(sandbox(root, ["bash", "-c", CANARY], canary_trace), env=proof_env,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if proof.returncode:
        print("SKIP installer execution: namespace canary could not prove the boundary")
        print(proof.stderr, end="")
        return SKIP
    attempts = sorted(set(project_access(canary_trace.read_text(encoding='utf-8'))))
    expected = ['/run/404-probe', '/etc/404-probe', '/var/lib/404-probe',
                '/var/lib/404-probe-updater', '/var/log/404-probe']
    audit = {'exit': proof.returncode, 'original_project_access': attempts,
             'expected_attempts_seen': all(path in attempts for path in expected)}
    (root / 'canary-audit.json').write_text(json.dumps(audit, indent=2) + '\n', encoding='utf-8')
    if not audit['expected_attempts_seen']:
        print('FAIL installer canary: strace did not observe every original project path')
        return 86
    if opts.probe:
        print("PASS installer namespace canary: fixture delete; absolute, redirect, symlink and parent escape denied")
        return 0
    installer = root / "installer-fixture.sh"
    installer.write_text(redirect((repo / "install.sh").read_text(encoding="utf-8"), FIXTURE), encoding="utf-8", newline="\n")
    env["INSTALLER_FIXTURE_INSTALLER"] = str(FIXTURE / installer.name)
    env["BASH_ENV"] = str(SOURCE / "scripts/installer-fixture-commands.sh")
    command = [translate(value, root, repo) for value in opts.command]
    if command[:1] == ["--"]:
        command = command[1:]
    if opts.script:
        # The child entry verifies namespace mounts before accepting this flag.
        env["INSTALLER_FIXTURE_ACTIVE"] = str(FIXTURE)
        source = Path(opts.script).read_text(encoding="utf-8")
        lines = []
        for line in source.splitlines():
            if "installer-fixture-entry.sh" in line:
                continue  # The outer entry already established/proved the namespace.
            if line.startswith("root=") and "BASH_SOURCE" in line:
                line = "root=" + shlex.quote(str(SOURCE))
            lines.append(line)
        # Rebase assertions and the existing per-test sed mappings together,
        # keeping their custom layout while catching any new literal prefix.
        child = root / "fixture-test.sh"
        child.write_text(redirect(translate("\n".join(lines) + "\n", root, repo), FIXTURE), encoding="utf-8", newline="\n")
        command = ["bash", str(FIXTURE / child.name)] + command
    else:
        # Generated Go harnesses embed their host fixture/source paths. Rewrite
        # only private shell fixtures, never files in the source checkout.
        for script in root.rglob('*.sh'):
            checked_path(root, script)
            script.write_text(translate(script.read_text(encoding='utf-8'), root, repo), encoding='utf-8', newline='\n')
    trace = root / "filesystem.trace"
    result = subprocess.run(sandbox(root, command, trace), env=env)
    audit = trace.read_text(encoding="utf-8", errors="replace")
    # Even harmless stat/read attempts against the original project paths fail
    # the test. The namespace already prevents access to their real host objects.
    escaped = project_access(audit)
    (root / "filesystem-audit.json").write_text(json.dumps({"canary": "passed", "original_project_access": escaped, "exit": result.returncode}, indent=2) + "\n", encoding="utf-8")
    if escaped:
        print("FAIL installer attempted original project paths: " + repr(sorted(set(escaped))), file=sys.stderr)
        return 86
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
