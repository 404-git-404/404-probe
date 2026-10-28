#!/usr/bin/env python3
"""Build narrowly mapped, non-nestable user namespaces for the fixture only.

All setup privileges stay in disposable namespace keepers. The workload is
started by bwrap with only the four explicitly configured capabilities.
"""
import argparse
import ctypes
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys

CLONE_NEWUSER = 0x10000000
TOKEN = '@INSTALLER_FIXTURE_USERNS@'
LIMIT = Path('/proc/sys/user/max_user_namespaces')


def checked_identity(identity):
    if not 1 <= identity < 1000:
        raise ValueError('fixture identity must be a non-root system UID/GID')
    for database in ('passwd', 'group'):
        result = subprocess.run(['/usr/bin/getent', database, str(identity)], capture_output=True)
        if result.returncode != 2 or result.stdout:
            raise ValueError('fixture UID/GID is occupied or lookup failed')


def identity_mapping(identity):
    if not 1 <= identity < 1000:
        raise ValueError('fixture identity must be a non-root system UID/GID')
    return f'0 0 1\n{identity} {identity} 1\n'


def map_identity(pid, identity):
    # The controller has CAP_SETUID/SETGID in the direct parent namespace, so
    # setgroups can stay allowed; only root and one unused fixture ID are mapped.
    mapping = identity_mapping(identity)
    for name in ('uid_map', 'gid_map'):
        Path(f'/proc/{pid}/{name}').write_text(mapping)
    if Path(f'/proc/{pid}/setgroups').read_text().strip() != 'allow':
        raise RuntimeError('fixture namespace must allow switching its mapped groups')


def unshare_user():
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.unshare(CLONE_NEWUSER):
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))
    # The owning controller must be able to open the descendant namespace FD
    # without relying on CAP_SYS_PTRACE in the host namespace.
    if libc.prctl(4, 1, 0, 0, 0):  # PR_SET_DUMPABLE
        raise OSError(ctypes.get_errno(), 'PR_SET_DUMPABLE')


def die_with_parent(parent):
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(1, signal.SIGKILL, 0, 0, 0):
        raise OSError(ctypes.get_errno(), 'PR_SET_PDEATHSIG')
    if os.getppid() != parent:
        os._exit(1)


def drop_setup_capabilities():
    """Keepers only hold namespace references after setup; they retain no caps."""
    libc = ctypes.CDLL(None, use_errno=True)
    last = int(Path('/proc/sys/kernel/cap_last_cap').read_text())
    for capability in range(last + 1):
        if libc.prctl(24, capability, 0, 0, 0):  # PR_CAPBSET_DROP
            raise OSError(ctypes.get_errno(), 'PR_CAPBSET_DROP')
    class Header(ctypes.Structure):
        _fields_ = [('version', ctypes.c_uint32), ('pid', ctypes.c_int)]
    class Data(ctypes.Structure):
        _fields_ = [('effective', ctypes.c_uint32), ('permitted', ctypes.c_uint32), ('inheritable', ctypes.c_uint32)]
    header = Header(0x20080522, 0)  # _LINUX_CAPABILITY_VERSION_3
    data = (Data * 2)()
    if libc.capset(ctypes.byref(header), ctypes.byref(data)):
        raise OSError(ctypes.get_errno(), 'capset')


def receive(fd):
    ready, _, _ = select.select([fd], [], [], 30)
    if not ready:
        raise TimeoutError('fixture namespace setup timed out')
    data = os.read(fd, 4096).decode().strip()
    if not data or data.startswith('ERROR '):
        raise RuntimeError(data or 'fixture namespace keeper exited')
    return data


def keeper(identity, to_parent, from_parent):
    unshare_user()
    os.write(to_parent, b'OUTER\n')
    if receive(from_parent) != 'MAPPED':
        raise RuntimeError('outer mapping handshake')
    # This sysctl is per-user-namespace. A single child consumes the outer
    # allowance, and the child cannot raise its ancestor's limit (bwrap's model).
    LIMIT.write_text('1\n')
    child_ready_r, child_ready_w = os.pipe()
    child_wait_r, child_wait_w = os.pipe()
    parent = os.getpid()
    child = os.fork()
    if child == 0:
        try:
            die_with_parent(parent)
            os.close(child_ready_r)
            os.close(child_wait_w)
            unshare_user()
            os.write(child_ready_w, b'INNER\n')
            if receive(child_wait_r) != 'MAPPED':
                raise RuntimeError('inner mapping handshake')
            drop_setup_capabilities()
            os.write(child_ready_w, b'READY\n')
            while os.read(child_wait_r, 1):
                pass
        except BaseException as error:
            os.write(child_ready_w, ('ERROR ' + repr(error)).encode())
        finally:
            os._exit(0)
    os.close(child_ready_w)
    os.close(child_wait_r)
    try:
        if receive(child_ready_r) != 'INNER':
            raise RuntimeError('inner namespace not ready')
        map_identity(child, identity)
        os.write(child_wait_w, b'MAPPED\n')
        if receive(child_ready_r) != 'READY':
            raise RuntimeError('inner namespace mapping not ready')
        drop_setup_capabilities()
        os.write(to_parent, f'INNER {child}\n'.encode())
        while os.read(from_parent, 1):
            pass
    finally:
        os.close(child_wait_w)
        try:
            os.kill(child, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(child, 0)


def launch(identity, command, audit_file=None):
    if os.geteuid() != 0:
        raise PermissionError('multi-user fixture setup requires root')
    checked_identity(identity)
    original_limit = LIMIT.read_text()
    notify_r, notify_w = os.pipe()
    control_r, control_w = os.pipe()
    parent = os.getpid()
    controller = os.fork()
    if controller == 0:
        try:
            die_with_parent(parent)
            os.close(notify_r)
            os.close(control_w)
            keeper(identity, notify_w, control_r)
        except BaseException as error:
            os.write(notify_w, ('ERROR ' + repr(error)).encode())
        finally:
            os._exit(0)
    os.close(notify_w)
    os.close(control_r)
    namespace_fd = None
    record = {'identity': identity, 'mapping': identity_mapping(identity),
              'host_userns_limit_before': original_limit.strip(), 'workload_exit': None}
    try:
        if receive(notify_r) != 'OUTER':
            raise RuntimeError('outer namespace not ready')
        map_identity(controller, identity)
        os.write(control_w, b'MAPPED\n')
        message = receive(notify_r)
        if not message.startswith('INNER '):
            raise RuntimeError('inner namespace keeper not ready')
        child = int(message.split()[1])
        record['keepers_capabilities'] = {}
        for pid in (controller, child):
            status = dict(line.split(':', 1) for line in Path(f'/proc/{pid}/status').read_text().splitlines())
            caps = {name: int(status[name], 16) for name in ('CapEff', 'CapPrm', 'CapInh', 'CapBnd', 'CapAmb')}
            if any(caps.values()):
                raise RuntimeError('namespace keeper retained setup capabilities')
            record['keepers_capabilities'][str(pid)] = caps
        namespace_fd = os.open(f'/proc/{child}/ns/user', os.O_RDONLY)
        if LIMIT.read_text() != original_limit:
            raise RuntimeError('host namespace limit changed')
        if command.count(TOKEN) != 1:
            raise ValueError('exactly one fixture namespace FD required')
        command = [str(namespace_fd) if value == TOKEN else value for value in command]
        record['workload_exit'] = subprocess.run(command, pass_fds=(namespace_fd,)).returncode
        return record['workload_exit']
    finally:
        if namespace_fd is not None:
            os.close(namespace_fd)
        os.close(control_w)
        try:
            os.kill(controller, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(controller, 0)
        os.close(notify_r)
        record['host_userns_limit_after'] = LIMIT.read_text().strip()
        if audit_file:
            Path(audit_file).write_text(json.dumps(record, indent=2) + '\n')
        if LIMIT.read_text() != original_limit:
            raise RuntimeError('host namespace limit changed after fixture')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--identity', type=int, required=True)
    parser.add_argument('--audit-file')
    parser.add_argument('command', nargs=argparse.REMAINDER)
    opts = parser.parse_args()
    command = opts.command[1:] if opts.command[:1] == ['--'] else opts.command
    try:
        return launch(opts.identity, command, opts.audit_file)
    except (OSError, RuntimeError, ValueError, TimeoutError) as error:
        print('SKIP multi-user namespace setup: ' + str(error), file=sys.stderr)
        return 77


if __name__ == '__main__':
    sys.exit(main())
