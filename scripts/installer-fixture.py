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
import stat
from contextlib import contextmanager, ExitStack

# Rebase complete managed roots, including OpenRC units/logs and OS/default
# paths used by extracted functions. Kernel/process pseudo-files remain read-only.
PREFIXES = ("/etc", "/usr/local/bin", "/usr/local/sbin", "/var/lib", "/var/log", "/run", "/root")
SKIP = 77
SOURCE = PurePosixPath('/work/source')
FIXTURE = PurePosixPath('/work/fixture')
MULTIUSER_CAPABILITIES = ('CAP_CHOWN', 'CAP_FOWNER', 'CAP_SETUID', 'CAP_SETGID')
MULTIUSER_CAP_MASK = sum(1 << bit for bit in (0, 3, 7, 6))


def skipped(reason):
    print('INSTALLER-FIXTURE-SKIPPED: ' + reason, file=sys.stderr)
    return 1 if os.environ.get('INSTALLER_FIXTURE_REQUIRED') == '1' else SKIP


def device_snapshot():
    if sys.platform != 'linux':
        return {}
    result = {}
    for name in ('null', 'zero', 'full', 'random', 'urandom', 'tty'):
        info = os.lstat('/dev/' + name)
        result[name] = (stat.S_IMODE(info.st_mode), info.st_uid, info.st_gid, info.st_ctime_ns)
    return result


def account_files(identity, metadata):
    """Entire synthetic /etc; no file content is taken from the host's /etc."""
    if not 1 <= identity < 1000:
        raise ValueError('fixture identity must be a non-root system UID/GID')
    return {
        'passwd': f'root:x:0:0:Fixture root:/work/fixture:/bin/bash\n404-probe:x:{identity}:{identity}:Fixture service:/work/fixture/system/var/lib/404-probe:/usr/sbin/nologin\n',
        'group': f'root:x:0:\n404-probe:x:{identity}:\n',
        'shadow': 'root:!:1:0:99999:7:::\n404-probe:!:1:0:99999:7:::\n',
        'nsswitch.conf': 'passwd: files\ngroup: files\nshadow: files\nhosts: files\n',
        'login.defs': 'UID_MIN 1000\nGID_MIN 1000\n',
        'pam.d/runuser': 'auth required pam_rootok.so\naccount required pam_permit.so\nsession required pam_permit.so\n',
        'fixture-root-only': 'synthetic protected control file\n',
        'installer-fixture-profile.json': json.dumps(metadata, indent=2) + '\n',
    }


@contextmanager
def synthetic_accounts(root):
    if os.geteuid() != 0:
        raise PermissionError('multi-user fixture requires a root setup process')
    checks = []
    identity = None
    for candidate in range(973, 999):
        row = {'identity': candidate}
        for database in ('passwd', 'group'):
            result = subprocess.run(['/usr/bin/getent', database, str(candidate)], capture_output=True)
            row[database] = {'exit': result.returncode, 'absent': result.returncode == 2 and not result.stdout}
        checks.append(row)
        if all(row[name]['absent'] for name in ('passwd', 'group')):
            identity = candidate
            break
    if identity is None:
        raise ValueError('no verified unused fixture UID/GID')
    metadata = {'profile': 'multiuser', 'identity': identity,
                'host_pid': os.getpid(), 'host_pid_namespace': os.readlink('/proc/self/ns/pid'),
                'host_net_namespace': os.readlink('/proc/self/ns/net'), 'host_lookup': checks}
    # Outside the writable fixture bind: /etc has no writable alias in the guest.
    with tempfile.TemporaryDirectory(prefix=root.name + '-accounts-', dir=root.parent) as temporary:
        control = Path(temporary)
        directory = control / 'etc'
        directory.mkdir()
        audits = control / 'audits'
        audits.mkdir(mode=0o700)
        files = account_files(identity, metadata)
        for name, content in files.items():
            target = directory / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(content, encoding='utf-8', newline='\n')
            target.chmod(0o600 if name in ('shadow', 'fixture-root-only') else 0o644)
        directory.chmod(0o755)
        payload = json.dumps({'metadata': metadata, 'files': files}, indent=2) + '\n'
        fixture_write(root, root / 'multiuser-accounts.json', payload)
        try:
            yield {'directory': directory, 'audit_directory': audits, 'identity': identity, 'metadata': metadata}
        finally:
            for audit in audits.glob('*.json'):
                fixture_write(root, root / audit.name, audit.read_text())
            fixture_write(root, root / 'multiuser-accounts.json', payload)


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


def fixture_file(root, path, write=False):
    """Parent-side I/O must not follow links planted in the writable guest root."""
    checked_path(root, path)
    flags = (os.O_WRONLY | os.O_CREAT) if write else os.O_RDONLY
    flags |= getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0)
    fd = os.open(path, flags, 0o666)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError('fixture control file must be regular with one link')
        if hasattr(os, 'geteuid') and info.st_uid != os.geteuid():
            raise ValueError('fixture control file owner changed')
        if write:
            os.ftruncate(fd, 0)
        return os.fdopen(fd, 'w' if write else 'r', encoding='utf-8', errors='replace', newline='\n')
    except BaseException:
        os.close(fd)
        raise


def fixture_write(root, path, content):
    with fixture_file(root, path, write=True) as file:
        file.write(content)


def sandbox(root, command, trace=None, profile='default', accounts=None):
    args = ['bwrap', '--die-with-parent']
    if profile == 'default':
        args += ['--unshare-all']
    elif profile == 'multiuser' and accounts:
        args += ['--userns', '@INSTALLER_FIXTURE_USERNS@', '--unshare-ipc', '--unshare-pid',
                 '--unshare-net', '--unshare-uts', '--unshare-cgroup-try']
    else:
        raise ValueError('unknown or unprepared fixture profile')
    args += ['--new-session', '--cap-drop', 'ALL']
    if profile == 'multiuser':
        for capability in MULTIUSER_CAPABILITIES:
            args += ['--cap-add', capability]
    args += ['--tmpfs', '/', '--ro-bind', '/usr', '/usr']
    # Build mountpoints on a fresh root, never under a masked host directory.
    # Preserve merged-/usr links without exposing a second alias of the host root.
    for name in ('bin', 'sbin', 'lib', 'lib64'):
        path = Path('/') / name
        if path.is_symlink():
            args += ['--symlink', os.readlink(path), str(path)]
        elif path.exists():
            args += ['--ro-bind', str(path), str(path)]
    args += ["--proc", "/proc", "--dev", "/dev"]
    # Authorized S5 fallback: bwrap's readonly bind sets nodev, breaking I/O.
    # Device metadata is guarded by wrappers, fd-aware trace audit and host
    # mode/uid/gid/ctime snapshots. Do not claim kernel isolation for devices.
    # Hide real installed project files from reads as well as writes. These
    # mounts are read-only, including when a test uses an absolute executable.
    for path in ("/etc", "/run", "/var/lib", "/var/log", "/usr/local", "/root", "/home"):
        if path == '/etc' and profile == 'multiuser':
            args += ['--ro-bind', str(accounts['directory']), path]
        else:
            args += ['--tmpfs', path, '--remount-ro', path]
    repo = Path(__file__).resolve().parent.parent
    args += ["--dir", "/work", "--ro-bind", str(repo), str(SOURCE)]
    if root is None:
        # Complete bootstrap proof uses only new namespace tmpfs: no host writes.
        args += ['--tmpfs', str(FIXTURE), '--tmpfs', '/tmp']
    else:
        args += ['--bind', str(root), str(FIXTURE), '--bind', str(root / 'tmp'), '/tmp']
    args += ['--remount-ro', '/', '--chdir', str(FIXTURE)]
    if trace:
        args += ["strace", "-f", "-qq", "-yy", "-s", "1024", "-e", "trace=%file,%process,fchmod,fchown",
                 "-o", translate(str(trace), root, repo)]
    args += command
    if profile == 'multiuser':
        namespace_audit = accounts['audit_directory'] / ((trace.stem if trace else 'namespace') + '-userns.json')
        args = [sys.executable, str(repo / 'scripts/installer-fixture-userns.py'),
                '--identity', str(accounts['identity']), '--audit-file', str(namespace_audit), '--'] + args
    return args


def translate(value, root, repo):
    # Longest prefix first: a Go fixture can be a child of the source checkout.
    mappings = sorted(((str(root), str(FIXTURE)), (str(repo), str(SOURCE))),
                      key=lambda pair: len(pair[0]), reverse=True)
    pattern = '|'.join(re.escape(old) + r'(?=/|$|[\s\x27\x22;:)])' for old, _ in mappings)
    replacements = dict(mappings)
    return re.sub(pattern, lambda match: replacements[match.group()], value)


def project_access(audit):
    return re.findall(r'"(/(?:run|etc|var/(?:lib|log)|usr/local/(?:bin|sbin))/[^"\n]*404-probe[^"\n]*)"', audit)


def audit_workload(audit):
    """Defensive audit, not adversarial authentication; mounts are the boundary.

    Process ancestry comes from outer strace, never a caller-controlled marker
    or PID file. All other accesses, including failed reads/stats, still fail.
    """
    records, devices, children, proofs, pending = [], [], {}, set(), set()
    signatures = ['", "-c", ' + json.dumps(script[:128])[:-1] for script in (CANARY, MULTIUSER_CANARY)]
    for line in audit.splitlines() if isinstance(audit, str) else audit:
        line = line.rstrip('\n')
        match = re.match(r'^(\d+)\s+(.*)', line)
        pid, body = (int(match[1]), match[2]) if match else (None, line)
        paths = project_access(body)
        if re.search(r'\b(?:chmod|chown|lchown|fchmod\w*|fchown\w*|utimensat)\(', body):
            device_paths = re.findall(r'/dev/[^"<>\s,)]+', body)
            if device_paths:
                devices.append((pid, device_paths))
        if paths:
            records.append((pid, paths))
        if re.match(r'execve\("/(?:usr/)?bin/bash",', body) and any(signature in body for signature in signatures):
            if body.endswith('= 0'):
                proofs.add(pid)
            elif '<unfinished ...>' in body:
                pending.add(pid)
        if pid in pending and body.startswith('<... execve resumed>') and body.endswith('= 0'):
            proofs.add(pid)
        if re.search(r'(?:clone3?|fork|vfork)(?:\(| resumed>)', body):
            child = re.search(r'= (\d+)$', body)
            if child:
                children.setdefault(pid, set()).add(int(child[1]))
    frontier = list(proofs)
    while frontier:
        for child in children.get(frontier.pop(), ()):
            if child not in proofs:
                proofs.add(child)
                frontier.append(child)
    expected, unexpected = [], []
    for pid, paths in records:
        (expected if pid in proofs else unexpected).extend(paths)
    return {'device_metadata_access': [path for pid, paths in devices if pid not in proofs for path in paths],
            'original_project_access': unexpected,
            'expected_canary_access': sorted(set(expected)),
            'canary_processes': sorted(pid for pid in proofs if pid is not None)}


CANARY = r'''
set -Eeuo pipefail
root="$INSTALLER_FIXTURE_ROOT"
[[ -r /work/source/install.sh ]]
grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status
for mount in / /etc /run /var/lib /var/log /usr/local /root /home /work/source; do
  readonly_mount=
  while read -r mount_id parent_id device mount_root mount_path mount_options remaining; do
    if [[ "$mount_path" == "$mount" ]]; then
      readonly_mount=0
      [[ ",$mount_options," != *,ro,* ]] || readonly_mount=1
    fi
  done </proc/self/mountinfo
  [[ "$readonly_mount" == 1 ]]
done
python3 - <<'PY'
import json, socket
from pathlib import Path
multiuser = Path('/etc/installer-fixture-profile.json').is_file()
for path in ('/run', '/var/lib', '/var/log', '/usr/local', '/root', '/home') + (() if multiuser else ('/etc',)):
    assert not list(Path(path).iterdir()), 'hidden tree exposed: ' + path
assert {name for _, name in socket.if_nameindex()} <= {'lo'}
assert all(line.split()[0] == 'lo' for line in Path('/proc/net/route').read_text().splitlines()[1:])
# bwrap's init is PID 1; the host procfs cannot satisfy this private-PID proof.
assert Path('/proc/1/comm').read_text().strip() == 'bwrap'
status = dict(line.split(':', 1) for line in Path('/proc/self/status').read_text().splitlines())
print('FIXTURE-CAPS:' + json.dumps({name: int(status[name], 16) for name in ('CapEff', 'CapPrm', 'CapBnd')}))
PY
# Boundary checks above are read-only and precede ALL writable fixture probes.
if [[ "${INSTALLER_FIXTURE_READ_ONLY_PROOF:-0}" == 1 ]]; then exit 0; fi
printf '' >/dev/null
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

MULTIUSER_CANARY = CANARY.replace(
    "grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status",
    "grep -Eq '^CapEff:[[:space:]]+0*c9$' /proc/self/status") + r'''
for path in /etc/404-probe /run/404-probe /var/lib/404-probe /var/lib/404-probe-updater /var/log/404-probe / /etc/fixture-root-only /work/source/install.sh; do
  if /bin/chown 404-probe:404-probe "$path" 2>/dev/null; then exit 90; fi
  if /bin/chmod 0777 "$path" 2>/dev/null; then exit 91; fi
done
if printf unsafe >/etc/fixture-root-only 2>/dev/null; then exit 92; fi
mkdir "$root/mount-probe"
if /bin/mount -t tmpfs tmpfs "$root/mount-probe" 2>/dev/null; then exit 93; fi
if /bin/mount -o remount,rw / 2>/dev/null; then exit 94; fi
if /bin/umount /etc 2>/dev/null; then exit 95; fi
rmdir "$root/mount-probe"
python3 - "$root" <<'PY'
import ctypes, json, os, socket, sys
from pathlib import Path
root = Path(sys.argv[1])
metadata = json.loads(Path('/etc/installer-fixture-profile.json').read_text())
identity = metadata['identity']
status = dict(line.split(':', 1) for line in Path('/proc/self/status').read_text().splitlines())
assert all(int(status[name], 16) == 201 for name in ('CapEff', 'CapPrm', 'CapBnd'))
assert os.readlink('/proc/self/ns/pid') != metadata['host_pid_namespace']
assert os.readlink('/proc/self/ns/net') != metadata['host_net_namespace']
assert not Path('/proc/' + str(metadata['host_pid'])).exists()
assert {name for _, name in socket.if_nameindex()} <= {'lo'}
routes = Path('/proc/net/route').read_text().splitlines()[1:]
assert all(line.split()[0] == 'lo' for line in routes)
libc = ctypes.CDLL(None, use_errno=True)
assert libc.unshare(0x10000000) == -1, 'new user namespace unexpectedly succeeded'
directory = root / 'service-canary'
directory.mkdir()
os.chown(directory, identity, identity)
# The root caller no longer owns it: this chmod exercises CAP_FOWNER.
directory.chmod(0o700)
PY
/usr/sbin/runuser -u 404-probe -- python3 - "$root" <<'PY'
import ctypes, json, os, sys
from pathlib import Path
root = Path(sys.argv[1])
metadata = json.loads(Path('/etc/installer-fixture-profile.json').read_text())
assert os.getuid() == metadata['identity'] and os.getgid() == metadata['identity']
assert set(os.getgroups()) <= {metadata['identity']}
status = dict(line.split(':', 1) for line in Path('/proc/self/status').read_text().splitlines())
assert int(status['CapEff'], 16) == 0
file = root / 'service-canary/owned'
file.write_text('service-owned data')
assert file.read_text() == 'service-owned data'
for path in ('/etc/404-probe', '/run/404-probe', '/var/lib/404-probe', '/var/lib/404-probe-updater', '/var/log/404-probe', '/etc/fixture-root-only'):
    for mode in ('r', 'w'):
        try:
            opened = open(path, mode)
        except OSError:
            pass
        else:
            opened.close()
            raise AssertionError('service escaped fixture: ' + path + ' ' + mode)
libc = ctypes.CDLL(None, use_errno=True)
assert libc.unshare(0x10000000) == -1, 'service regained a user namespace'
PY
/bin/chown 0:0 "$root/service-canary"
/bin/rm -rf "$root/service-canary"
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root")
    parser.add_argument("--probe", action="store_true")
    parser.add_argument("--script")
    parser.add_argument('--profile', choices=('default', 'multiuser'), default='default')
    parser.add_argument("command", nargs=argparse.REMAINDER)
    opts = parser.parse_args()
    repo = Path(__file__).resolve().parent.parent
    if not all((repo / name).is_file() for name in ('install.sh', 'scripts/installer-fixture-commands.sh', 'scripts/installer-fixture-entry.sh')):
        return skipped('source tree is incomplete (install.sh and fixture support scripts required)')
    if opts.root:
        reason = validate_root(Path(opts.root), selected_profile(opts, repo), in_place_candidate())
        if reason:
            return skipped(reason)
    tools = ('bash',) if in_place_candidate() else ('bwrap', 'strace', 'bash')
    if sys.platform != "linux" or not all(shutil.which(x) for x in tools):
        return skipped('verified Linux bubblewrap + strace boundary unavailable')
    if in_place_candidate():
        # Never mkdir for a candidate, even with forged flags or a missing root.
        value = opts.root or os.environ.get('INSTALLER_FIXTURE_ROOT') or str(FIXTURE)
        return run(opts, Path(value).resolve(strict=True))
    if not bootstrap_boundary():
        return skipped('namespace bootstrap canary could not prove the boundary')
    if opts.root:
        return run(opts, Path(opts.root).resolve(strict=True), bootstrap_proven=True)
    # Caller must prepare its explicitly recorded per-run directory. No global
    # mkdir here; the fallback name cannot be confused with installed project data.
    parent = Path(os.environ.get('INSTALLER_FIXTURE_BASE', '/var/lib/installer-test-fixtures'))
    if not parent.is_dir() or parent.is_symlink():
        return skipped('provide an existing private INSTALLER_FIXTURE_BASE or --root')
    with tempfile.TemporaryDirectory(prefix='installer-fixture-', dir=parent) as temporary:
        if selected_profile(opts, repo) == 'multiuser':
            Path(temporary).chmod(0o755)
        print('Installer fixture location: ' + temporary, file=sys.stderr)
        return run(opts, Path(temporary), bootstrap_proven=True)


def validate_root(root, profile, in_place, uid=None):
    """Read-only preflight; no bootstrap or host write may precede this check."""
    try:
        info = root.lstat()
        resolved = root.resolve(strict=True)
        if not stat.S_ISDIR(info.st_mode) or root.is_symlink() or resolved != root.absolute():
            return '--root must be an existing ordinary directory without symlink ancestors'
        if info.st_uid != (os.geteuid() if uid is None else uid):
            return '--root must be owned by the current user'
        system = ('/', '/var', '/var/lib', '/var/log', '/run', '/etc', '/usr', '/usr/local', '/usr/local/bin', '/usr/local/sbin', '/root', '/home', '/tmp', '/dev', '/proc', '/sys')
        if resolved.as_posix() in system or any(resolved.is_relative_to(Path(p)) for p in ('/etc', '/dev', '/proc', '/sys')):
            return '--root cannot be a system directory'
        if any(resolved.is_relative_to(Path(p)) for p in ('/run/404-probe', '/etc/404-probe', '/var/lib/404-probe', '/var/lib/404-probe-updater', '/var/log/404-probe')):
            return '--root cannot be an installed project directory'
        mode = stat.S_IMODE(info.st_mode)
        if mode & 0o022 or (profile == 'multiuser' and mode != 0o755):
            return '--root requires private write permissions and multiuser requires 0755 traversal'
        if not in_place:
            prefixes = ('installer-fixture-', 'canary', 'filesystem', 'multiuser-', 'fixture-test.sh', 'in-place-proof.json')
            if any(p.name not in ('tmp', 'system') and not p.name.startswith(prefixes) for p in resolved.iterdir()):
                return '--root must be empty or contain only installer-fixture entry files'
    except (OSError, ValueError) as error:
        return '--root preflight failed: ' + str(error).splitlines()[0]
    return ''


def bootstrap_boundary():
    env = proof_environment(dict(os.environ, INSTALLER_FIXTURE_ROOT=str(FIXTURE), INSTALLER_FIXTURE_READ_ONLY_PROOF='0'))
    result = subprocess.run(sandbox(None, ['bash', '-c', CANARY]), env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if result.returncode:
        print(result.stderr, end='', file=sys.stderr)
    return result.returncode == 0


def in_place_candidate():
    # Hints only select the fail-closed proof path; they never authorize a body.
    return (Path(__file__).resolve().parent.parent.as_posix() == str(SOURCE)
            or bool(os.environ.get('INSTALLER_FIXTURE_ACTIVE'))
            or bool(os.environ.get('INSTALLER_FIXTURE_ROOT')))


def proof_environment(env):
    clean = {key: value for key, value in env.items()
             if key not in ('BASH_ENV', 'ENV') and not key.startswith('BASH_FUNC_')}
    clean['PATH'] = '/usr/bin:/bin'
    return clean


def selected_profile(opts, repo):
    # Only an explicit launcher choice or this exact registered harness selects
    # multi-user execution. Environment variables never select capabilities.
    if opts.script and Path(opts.script).resolve() == repo / 'scripts/test-selector-order-existing.sh':
        return 'multiuser'
    return getattr(opts, 'profile', 'default')


def run(opts, root, bootstrap_proven=False):
    if not in_place_candidate() and not bootstrap_proven and not bootstrap_boundary():
        return skipped('namespace bootstrap canary could not prove the boundary')
    profile = selected_profile(opts, Path(__file__).resolve().parent.parent)
    if profile == 'multiuser' and not in_place_candidate():
        with ExitStack() as stack:
            try:
                accounts = stack.enter_context(synthetic_accounts(root))
            except (OSError, ValueError) as error:
                return skipped('multi-user fixture preparation: ' + str(error))
            # Workload, audit and teardown errors must never become SKIP.
            return run_verified(opts, root, profile, accounts)
    return run_verified(opts, root, profile)


def run_verified(opts, root, profile='default', accounts=None):
    repo = Path(__file__).resolve().parent.parent
    in_place = in_place_candidate()
    inside_root = root if in_place else FIXTURE
    env = dict(os.environ) if in_place else {key: translate(value, root, repo) for key, value in os.environ.items()}
    env.update(INSTALLER_FIXTURE_ROOT=str(inside_root), TMPDIR=str(inside_root / 'tmp'), INSTALLER_FIXTURE_READ_ONLY_PROOF='0',
               INSTALLER_FIXTURE_ACTIVE=str(inside_root))
    # A successful canary is mandatory for every invocation, never an env bypass.
    canary_trace = root / 'canary.trace'
    canary = MULTIUSER_CANARY if profile == 'multiuser' else CANARY
    if in_place:
        if not any(root.is_relative_to(path) for path in (Path(str(FIXTURE)), Path('/tmp'))):
            return skipped('in-place fixture is outside the private write roots')
        # A complete read-only mount/caps/pid/net proof precedes positive probes,
        # audit files, tmp creation, installer extraction and any workload.
        readonly = subprocess.run(['bash', '-c', canary],
                                 env=proof_environment(dict(env, INSTALLER_FIXTURE_READ_ONLY_PROOF='1')),
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if readonly.returncode:
            print(readonly.stderr, end='', file=sys.stderr)
            return skipped('in-place read-only boundary proof failed')
        proof_command = ['bash', '-c', canary]
    else:
        (root / 'tmp').mkdir(exist_ok=True)
        proof_command = sandbox(root, ['bash', '-c', canary], canary_trace, profile, accounts)
    proof = subprocess.run(proof_command, env=proof_environment(env),
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if proof.returncode:
        print(proof.stderr, end="", file=sys.stderr)
        return skipped(('in-place' if in_place else 'namespace') + ' canary could not prove the boundary')
    measured = [json.loads(line.removeprefix('FIXTURE-CAPS:')) for line in proof.stdout.splitlines() if line.startswith('FIXTURE-CAPS:')]
    if len(measured) != 1:
        raise ValueError('missing measured capability record')
    (root / 'tmp').mkdir(exist_ok=True)
    if in_place:
        fixture_write(root, root / 'in-place-proof.json', json.dumps({'mode': 'in-place', 'profile': profile, 'verified': True}) + '\n')
    else:
        with fixture_file(root, canary_trace) as file:
            attempts = sorted(set(project_access(file.read())))
        expected = ['/run/404-probe', '/etc/404-probe', '/var/lib/404-probe',
                    '/var/lib/404-probe-updater', '/var/log/404-probe']
        audit = {'exit': proof.returncode, 'profile': profile, 'cap_eff': measured[0]['CapEff'], 'capabilities': measured[0], 'original_project_access': attempts,
                 'expected_attempts_seen': all(path in attempts for path in expected)}
        fixture_write(root, root / 'canary-audit.json', json.dumps(audit, indent=2) + '\n')
        if not audit['expected_attempts_seen']:
            print('FAIL installer canary: strace did not observe every original project path')
            return 86
    if opts.probe:
        print('PASS installer ' + ('in-place' if in_place else 'namespace') + ' canary: fixture delete; absolute, redirect, symlink and parent escape denied')
        return 0
    installer = root / "installer-fixture.sh"
    fixture_write(root, installer, redirect((repo / "install.sh").read_text(encoding="utf-8"), inside_root))
    env["INSTALLER_FIXTURE_INSTALLER"] = str(inside_root / installer.name)
    env["BASH_ENV"] = str(SOURCE / "scripts/installer-fixture-commands.sh")
    command = list(opts.command) if in_place else [translate(value, root, repo) for value in opts.command]
    if command[:1] == ["--"]:
        command = command[1:]
    if opts.script:
        # This entry is removed only after the complete kernel-boundary proof.
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
        body = "\n".join(lines) + "\n"
        fixture_write(root, child, redirect(body if in_place else translate(body, root, repo), inside_root))
        command = ["bash", str(inside_root / child.name)] + command
    elif not in_place:
        # Generated Go harnesses embed their host fixture/source paths. Rewrite
        # only private shell fixtures, never files in the source checkout.
        for script in root.rglob('*.sh'):
            checked_path(root, script)
            with fixture_file(root, script) as file:
                text = file.read()
            fixture_write(root, script, translate(text, root, repo))
    trace = root / "filesystem.trace"
    # Inside an already proven namespace the outer process owns the only trace.
    result = subprocess.run(command if in_place else sandbox(root, command, trace, profile, accounts),
                            env=env, cwd=root if in_place else None)
    if in_place:
        return result.returncode
    # Even harmless stat/read attempts against the original project paths fail
    # the test. The namespace already prevents access to their real host objects.
    with fixture_file(root, trace) as audit:
        checked = audit_workload(audit)
    escaped = checked['original_project_access'] + checked['device_metadata_access']
    fixture_write(root, root / 'filesystem-audit.json', json.dumps(dict(checked, canary='passed', profile=profile, mode='new-namespace', exit=result.returncode), indent=2) + '\n')
    if escaped:
        print("FAIL installer attempted original project paths: " + repr(sorted(set(escaped))), file=sys.stderr)
        return 86
    return result.returncode


if __name__ == "__main__":
    before = device_snapshot()
    try:
        code = main()
    finally:
        if device_snapshot() != before:
            raise RuntimeError('STOP: host device mode/uid/gid/ctime changed')
    sys.exit(code)
