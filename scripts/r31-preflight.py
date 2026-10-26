#!/usr/bin/env python3
"""Read-only rehearsal checks and automatic operator records. No release-specific gates."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import time
import r31lib as releases
sys.dont_write_bytecode = True


def upgrade_module():
    spec = importlib.util.spec_from_file_location('r31_upgrade', Path(__file__).with_name('r31-upgrade.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def private_directory(path, fresh=False):
    path = Path(path)
    if path.is_symlink() or not path.is_dir():
        raise ValueError('private directory required: '+str(path))
    if os.name == 'posix' and (path.stat().st_mode & 0o777 != 0o700 or path.stat().st_uid != os.geteuid()):
        raise ValueError('private directories must be owned by caller and mode 0700')
    if fresh and any(path.iterdir()):
        raise ValueError('fresh empty directory required: '+str(path))


def configuration(filename):
    doc = json.loads(Path(filename).read_text(encoding='utf-8'))
    required = {'from_version','to_version','from_assets','to_assets','base_assets','work_dir','scale'}
    if not required <= set(doc) or set(doc)-required-{'expected_schema'}:
        raise ValueError('configuration keys differ from documented format')
    releases.registered(doc['from_version'])
    if releases.version_tuple(doc['from_version']) >= releases.version_tuple(doc['to_version']):
        raise ValueError('target must be newer')
    doc['expected_schema'] = releases.expected_schema(doc['to_version'], doc.get('expected_schema'))
    if doc['expected_schema'] < releases.registered(doc['from_version'])['schema']:
        raise ValueError('target schema cannot precede starting schema')
    if doc['scale'] not in ('tiny','snapshot','full','compact'):
        raise ValueError('invalid scale')
    for key in ('from_assets','to_assets','base_assets','work_dir'):
        path = Path(doc[key])
        if not path.is_absolute() or '\n' in str(path) or '\r' in str(path) or path.is_symlink():
            raise ValueError('absolute non-symlink paths required')
        doc[key] = path.resolve(strict=True)
    private_directory(doc['work_dir'])
    return doc


def check(doc, module=None):
    module = module or upgrade_module()
    arch = {'x86_64':'amd64','aarch64':'arm64'}.get(platform.machine())
    if arch is None:
        raise ValueError('only Linux amd64/arm64 supported')
    old, server, identity = releases.assets(doc['from_assets'], doc['from_version'], arch, True)
    releases.assets(doc['base_assets'], 'v1.0.6', arch, True)
    new, _, target = releases.assets(doc['to_assets'], doc['to_version'], arch)
    work = doc['work_dir']
    for path in (work, old, new, doc['base_assets']):
        if any(path == root or root in path.parents for root in module.PATHS):
            raise ValueError('workspace/assets overlap installation')
    if any((parent/'.git').exists() for parent in (work, *work.parents)):
        raise ValueError('rehearsal workspace must be outside a checkout')
    for name in ('evidence','results'):
        private_directory(work/name, fresh=True)
    for name in ('run-started','operator-exit.txt','operator-summary.json'):
        if (work/name).exists() or (work/name).is_symlink():
            raise ValueError('previous attempt exists: '+name)
    if (work/'rehearsal.lock').is_symlink():
        raise ValueError('unsafe lock path')
    if any(path.is_symlink() for path in module.PATHS+module.CANDIDATES):
        raise ValueError('unsafe installation symlink')
    installed = json.loads(subprocess.check_output([str(module.BINARY),'version','--json']))
    if installed != identity or releases.digest(module.BINARY) != releases.digest(server):
        raise ValueError('installed Server differs from starting release')
    database = json.loads(subprocess.check_output(['runuser','-u','404-probe','--',str(module.BINARY),
                                                'database','verify','--db',str(module.DB)]))
    if database.get('integrity') != 'ok' or database.get('schema_version') != releases.registered(doc['from_version'])['schema']:
        raise ValueError('installed database integrity/schema differs from starting release')
    for action in ('is-active','is-enabled'):
        if module.systemctl(action,'--quiet',module.SERVICE).returncode:
            raise ValueError('service must be active and enabled')
    unit = module.PATHS[4]
    if unit.with_name(unit.name+'.d').exists() or unit.with_name('404-probe-agent.service').exists():
        raise ValueError('drop-in or mixed Agent installation present')
    if (module.PATHS[5]/'pending').exists() or any(p.exists() for p in module.CANDIDATES):
        raise ValueError('pending upgrade or candidate present')
    varlib = module.DB.parent.parent
    if any(p != work for p in varlib.glob('404-probe-r31-*')):
        raise ValueError('residual private recovery workspace')
    free = shutil.disk_usage(varlib).free
    if free < 8*1024**3:
        raise ValueError('at least 8 GiB free in /var/lib required')
    module.preflight_filesystems(module.PATHS,varlib,module.PATHS[6])
    for fail in (False, True):
        text = module.instrument((new/'install.sh').read_text(encoding='utf-8'),
                                 Path(__file__).with_name('r31-upgrade.py'), work/'events.jsonl', fail)
        subprocess.run(['bash','-n'],input=text,text=True,check=True)
    return dict(ready=True, installed=installed, database=database, target=target,
                expected_schema=doc['expected_schema'], free_bytes=free)


def operator_run(doc):
    if sys.platform != 'linux' or os.geteuid() != 0 or not sys.stdin.isatty() or not sys.stdout.isatty():
        raise ValueError('requires an interactive Linux root terminal')
    work = doc['work_dir']
    if not (work/'run-started').is_file():
        raise ValueError('use the locked r31-rehearse.sh entry')
    command = [sys.executable,'-B',str(Path(__file__).with_name('r31-run-with-evidence.py')),
               '--evidence-dir',str(work/'evidence'),doc['from_version'],doc['to_version'],
               str(doc['from_assets']),str(doc['to_assets']),doc['scale'],
               '--base-assets',str(doc['base_assets']),'--expected-schema',str(doc['expected_schema']),
               '--out',str(work/'results'),'--confirm-test-host',socket.gethostname()]
    rc = 1
    try:
        rc = subprocess.run(command).returncode  # inherited terminal, never capture installer output
    finally:
        with (work/'operator-exit.txt').open('x',encoding='utf-8') as stream:
            stream.write(str(rc)+'\n')
        def read(path):
            try:
                return json.loads(path.read_text(encoding='utf-8'))
            except (OSError,ValueError):
                return {}
        result = read(work/'results/result.json')
        frozen = read(work/'evidence/frozen-compare.json')
        summary = dict(tool_exit_code=rc,status=result.get('status'),restored=result.get('restored'),
                       restore_errors=result.get('restore_errors'),restore_warnings=result.get('restore_warnings'),
                       frozen_equal=frozen.get('equal'),schema=result.get('post_upgrade_verification',{}).get('schema_version'),
                       integrity=result.get('post_upgrade_verification',{}).get('integrity'),
                       from_version=result.get('from_version'),to_version=result.get('to_version'),
                       finished_at_utc=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()))
        valid = (rc == 0 and summary['status'] == 'passed' and summary['restored'] is True
                 and not summary['restore_errors'] and summary['frozen_equal'] is True
                 and summary['schema'] == doc['expected_schema'] and summary['integrity'] == 'ok'
                 and summary['from_version'] == doc['from_version'] and summary['to_version'] == doc['to_version'])
        summary['verified'] = valid
        with (work/'operator-summary.json').open('x',encoding='utf-8') as stream:
            stream.write(json.dumps(summary,indent=2)+'\n')
        print(json.dumps(summary,indent=2))
    return 0 if valid else rc or 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    for mode in ('check','work-dir','operator-run'):
        group.add_argument('--'+mode,type=Path)
    args = parser.parse_args()
    if sys.platform != 'linux' or os.geteuid() != 0:
        raise ValueError('requires Linux root')
    doc = configuration(args.check or args.work_dir or args.operator_run)
    if args.check:
        print(json.dumps(check(doc),indent=2))
        return 0
    if args.work_dir:
        print(doc['work_dir'])
        return 0
    return operator_run(doc)


if __name__ == '__main__':
    raise SystemExit(main())
