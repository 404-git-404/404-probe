#!/usr/bin/env python3
"""R31 Linux systemd upgrade rehearsal using LOCAL official release assets.

Run as root, only in a maintainer-assigned exclusive test slot. The existing
Server must be installed and its service account must already exist. Original
files are moved aside while stopped; finally restores their exact bytes/modes.
Fresh installation prompts (including password) remain on /dev/tty for the human.
No tokens, passwords or installation transcripts are saved by this tool.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import shlex
import signal
import subprocess
import sys
import time
sys.dont_write_bytecode = True

SERVICE='404-probe-server.service'
DB=Path('/var/lib/404-probe/404-probe.db')
BINARY=Path('/usr/local/bin/404-probe-server')
PATHS=[Path(x) for x in [
    '/var/lib/404-probe', '/etc/404-probe', '/usr/local/bin/404-probe-server',
    '/usr/local/sbin/404-probe-install','/etc/systemd/system/404-probe-server.service',
    '/var/lib/404-probe-upgrade', '/run/404-probe-upgrade']]
CANDIDATES=[Path('/usr/local/bin/.404-probe-server.candidate'),
            Path('/usr/local/sbin/404-probe-install.candidate')]


def call(*args, **kw):
    return subprocess.run(list(map(str,args)), check=True, **kw)


def digest(path):
    h=hashlib.sha256()
    with open(path,'rb') as f:
        for block in iter(lambda:f.read(1024*1024),b''):
            h.update(block)
    return h.hexdigest()


def tree_state(paths):
    result={}
    for root in paths:
        if not root.exists():
            continue
        for p in [root, *sorted(root.rglob('*'))] if root.is_dir() else [root]:
            st=p.lstat()
            result[str(p)]=[st.st_mode,st.st_uid,st.st_gid,
                os.readlink(p) if p.is_symlink() else digest(p) if p.is_file() else None]
    return result


def assets(directory,version,arch):
    directory=Path(directory).resolve(strict=True)
    names=['404-probe-'+role+'-linux-'+cpu for role in ['server','agent'] for cpu in ['amd64','arm64']]
    names+=['install.sh','RELEASE-METADATA.json']
    manifest={}
    for line in (directory/'SHA256SUMS').read_text().splitlines():
        match=re.fullmatch(r'([a-f0-9]{64})  ([A-Za-z0-9_.-]+)',line)
        if not match:
            raise ValueError('invalid local asset manifest')
        manifest[match[2]]=match[1]
    for name in names:
        p=directory/name
        if p.is_symlink() or not p.is_file() or digest(p)!=manifest.get(name):
            raise ValueError(f'asset checksum/type mismatch: {name}')
    for name in ['install.sh','RELEASE-METADATA.json','SHA256SUMS']:
        if b'\r' in (directory/name).read_bytes():
            raise ValueError(f'CR in {name}')
    server=directory/f'404-probe-server-linux-{arch}'
    identity=json.loads(subprocess.check_output([server,'version','--json']))
    if identity.get('version')!=version or identity.get('dirty') is not False:
        raise ValueError('asset Server version/clean identity mismatch')
    return directory,server,identity


def instrument(text,helper,events,fail=False):
    """Wrap real CLI calls, preserve their status and the installer's rollback logic.

    This edits a disposable script copy only, never the checksummed asset. Exact
    anchors deliberately fail closed when an upstream installer changes layout.
    """
    helper_quoted=shlex.quote(str(helper))
    events_quoted=shlex.quote(str(events))
    for binary in ['SERVER_UPGRADE_CANDIDATE','SERVER_BINARY']:
        for mode in ['verify','migrate-copy','migrate-protected']:
            old=f'runuser -u "${{SERVICE_USER}}" -- "${{{binary}}}" database {mode} --db'
            new=f'python3 {helper_quoted} --measure {events_quoted} {mode} '+old
            text=text.replace(old,new)
    if '--measure' not in text or text.count('restore_server_upgrade() {')!=1:
        raise ValueError('unsupported installer timing anchors')
    text=text.replace('restore_server_upgrade() {','r31_original_restore_server_upgrade() {')
    wrapper='''
restore_server_upgrade() {
  local r31_start r31_status=0
  r31_start="$(python3 -c 'import time; print(time.monotonic_ns())')"
  r31_original_restore_server_upgrade "$@" || r31_status=$?
  python3 HELPER --record EVENTS rollback "$r31_start" "$r31_status" || return 1
  return "$r31_status"
}
'''.replace('HELPER',helper_quoted).replace('EVENTS',events_quoted)
    anchor='\nmain "$@"'
    if text.count(anchor)!=1:
        raise ValueError('unsupported installer entry anchor')
    text=text.replace(anchor,wrapper+anchor)
    if fail:
        lines=text.splitlines(True)
        hits=[i for i,line in enumerate(lines) if line.lstrip().startswith('write_server_upgrade_state binary-replaced ')]
        if len(hits)!=1:
            raise ValueError('unsupported failure injection anchor')
        lines.insert(hits[0]+1,'  exit 87 # R31 simulated failure after migration and binary replacement\n')
        text=''.join(lines)
    return text


def install(directory,version,script=None):
    env=os.environ.copy()
    # Do not accidentally carry one-click/production credentials into a rehearsal.
    for key in list(env):
        if key.startswith('PROBE_404_'):
            del env[key]
    env.update(PROBE_404_LOCAL_ASSET_DIRECTORY=str(directory),PROBE_404_VERSION=version)
    return subprocess.run(['bash',str(script or directory/'install.sh')],env=env).returncode


def write_results(out,result):
    out.mkdir(mode=0o700,parents=True,exist_ok=True)
    (out/'result.json').write_text(json.dumps(result,indent=2)+'\n')
    lines=[f"# R31 {result['from_version']} → {result['to_version']} ({result['scale']})",'',
           f"Status: {result['status']}; original installation restored: {result.get('restored',False)}",'',
           '| phase | seconds | exit |','|---|---:|---:|']
    for phase in result.get('events',[]):
        lines.append(f"| {phase['run']}/{phase['phase']} | {phase['seconds']:.3f} | {phase['exit_code']} |")
    if result.get('error'):
        lines+=['',result['error']]
    if not result.get('restored'):
        lines+=['',f"Manual recovery required. Private backup: `{result.get('backup')}`. See r31-usage.md."]
    (out/'summary.md').write_text('\n'.join(lines)+'\n')


def rehearse(args):
    if sys.platform!='linux' or os.geteuid()!=0:
        raise ValueError('requires Linux root and an exclusive test slot')
    versions=[]
    for v in [args.from_version,args.to_version]:
        if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',v):
            raise ValueError('only canonical stable vX.Y.Z versions are accepted')
        versions.append(tuple(map(int,v[1:].split('.'))))
    if versions[0]>=versions[1]:
        raise ValueError('target must be newer')
    arch={'x86_64':'amd64','aarch64':'arm64'}.get(platform.machine())
    if not arch:
        raise ValueError('only linux amd64/arm64 supported')
    old,old_server,old_identity=assets(args.from_assets,args.from_version,arch)
    new,new_server,new_identity=assets(args.to_assets,args.to_version,arch)
    # The generator adapter is pinned to v1.0.6. v1.0.5 has the identical schema34.
    fixture_server=new_server if args.to_version=='v1.0.6' else old_server
    if 'v1.0.6' not in [args.from_version,args.to_version]:
        raise ValueError('this adapter requires v1.0.6 as source (or v1.0.5 → v1.0.6 flow check)')
    if not BINARY.is_file() or not DB.is_file():
        raise ValueError('requires an existing Server installation to restore')
    for p in PATHS+CANDIDATES:
        if p.is_symlink():
            raise ValueError(f'unsupported symlink: {p}')
    call('getent','passwd','404-probe',stdout=subprocess.DEVNULL)
    call('getent','group','404-probe',stdout=subprocess.DEVNULL)
    if Path('/etc/systemd/system/404-probe-server.service.d').exists():
        raise ValueError('service drop-ins require manual rehearsal planning')
    if Path('/etc/systemd/system/404-probe-agent.service').exists() or any(p.exists() for p in CANDIDATES):
        raise ValueError('mixed Agent installation or pending candidate; no changes made')
    if Path('/var/lib/404-probe-upgrade/pending').exists():
        raise ValueError('pending upgrade requires recovery before rehearsal')
    out=Path(args.out).resolve()
    work=Path('/var/lib')/('404-probe-r31-'+str(time.time_ns()))
    # Output/assets must be outside the paths that are moved/restored.
    for p in [out,old,new]:
        if any(p==r or r in p.parents for r in PATHS):
            raise ValueError('output/assets overlap installation')
    if shutil.disk_usage('/var/lib').free < 8*1024**3:
        raise ValueError('at least 8 GiB free in /var/lib required')
    # Check timing/failure anchors before stopping or moving the existing install.
    for fail in [False,True]:
        script_text=instrument((new/'install.sh').read_text(),Path(__file__).resolve(),work/'events.jsonl',fail)
        call('bash','-n',input=script_text,text=True)
    work.mkdir(mode=0o700)
    backup=work/'original'; backup.mkdir(mode=0o700)
    result=dict(from_version=args.from_version,to_version=args.to_version,scale=args.scale,
        from_identity=old_identity,to_identity=new_identity,status='failed',restored=False,
        backup=str(backup),events=[])
    active_state=subprocess.run(['systemctl','is-active',SERVICE],capture_output=True,text=True).stdout.strip()
    enabled_state=subprocess.run(['systemctl','is-enabled',SERVICE],capture_output=True,text=True).stdout.strip()
    if active_state not in ['active','inactive'] or enabled_state not in ['enabled','disabled']:
        shutil.rmtree(work)
        raise ValueError('requires active/inactive and enabled/disabled original service state')
    active=active_state=='active'; enabled=enabled_state=='enabled'
    result['original_active']=active; result['original_enabled']=enabled
    result['original_identity']=json.loads(subprocess.check_output([BINARY,'version','--json']))
    moved=[]; before=None; cleanup_started=False
    def stop_signal(signum,frame):
        raise RuntimeError(f'interrupted by signal {signum}')
    for sig in [signal.SIGINT,signal.SIGTERM,signal.SIGHUP]:
        signal.signal(sig,stop_signal)
    try:
        call('systemctl','stop',SERVICE)
        before=tree_state(PATHS)
        # Freeze ALL application data, including SQLite WAL/SHM, before the first install.
        for i,p in enumerate(PATHS):
            if p.exists():
                shutil.move(str(p),str(backup/str(i))); moved.append((i,p))
        cleanup_started=True
        call('systemctl','daemon-reload')
        generator=Path(__file__).with_name('r31-generate.py')
        import importlib.util
        spec=importlib.util.spec_from_file_location('r31_generate',generator)
        module=importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        result['fixture']=module.generate(fixture_server,work/'fixture.db',args.scale,anchor_ms=int(time.time()//60)*60000)
        if install(old,args.from_version)!=0:
            raise RuntimeError('starting-version local installation failed/cancelled')
        call('systemctl','stop',SERVICE)
        for suffix in ['', '-wal','-shm']:
            Path(str(DB)+suffix).unlink(missing_ok=True)
        shutil.copy2(work/'fixture.db',DB)
        shutil.chown(DB,user='404-probe',group='404-probe'); DB.chmod(0o600)
        call(old_server,'agent','list','--db',DB,stdout=subprocess.DEVNULL)
        call('systemctl','start',SERVICE)
        # First rehearse actual automatic rollback; its restored old installation
        # is then the starting point for the successful upgrade.
        helper=Path(__file__).resolve()
        for name,fail in [('failure',True),('success',False)]:
            events=work/(name+'.jsonl')
            script=work/(name+'.sh')
            script.write_text(instrument((new/'install.sh').read_text(),helper,events,fail))
            script.chmod(0o700)
            call('bash','-n',script)
            rc=install(new,args.to_version,script)
            recorded=[json.loads(line) for line in events.read_text().splitlines()] if events.exists() else []
            result['events'] += [dict(run=name,**e) for e in recorded]
            if fail:
                rollbacks=[e for e in recorded if e['phase']=='rollback']
                if rc!=87 or len(rollbacks)!=1 or rollbacks[0]['exit_code']!=0:
                    raise RuntimeError('simulated failure did not complete automatic rollback')
                if digest(BINARY)!=digest(old_server):
                    raise RuntimeError('rollback did not restore source binary')
                if Path('/var/lib/404-probe-upgrade/pending').exists():
                    raise RuntimeError('rollback still pending')
                # Installer validates backup/restored DB,WAL,SHM SHA256 before restart.
                result['rollback_database']='installer backup manifest and restored file hashes passed'
            elif rc!=0:
                raise RuntimeError(f'upgrade failed (exit {rc})')
            required=['verify','migrate-copy','migrate-protected']
            for phase in required:
                if not any(e['phase']==phase and e['exit_code']==0 for e in recorded):
                    raise RuntimeError(f'missing successful timed phase: {phase}')
            expected=old_server if fail else new_server
            if digest(BINARY)!=digest(expected):
                raise RuntimeError('unexpected installed binary')
            call('systemctl','is-active','--quiet',SERVICE)
        result['status']='passed'
    except Exception as e:
        result['error']=f'{type(e).__name__}: {e}'
    finally:
        try:
            call('systemctl','stop',SERVICE)
            if cleanup_started:
                for p in PATHS+CANDIDATES:
                    if p.is_symlink():
                        raise RuntimeError('cleanup target became symlink; preserve backup for manual recovery')
                    if p.is_dir(): shutil.rmtree(p)
                    else: p.unlink(missing_ok=True)
            for i,p in moved:
                if (backup/str(i)).exists(): shutil.move(str(backup/str(i)),str(p))
            call('systemctl','daemon-reload')
            call('systemctl','enable' if enabled else 'disable',SERVICE)
            if before is not None and tree_state(PATHS)!=before:
                raise RuntimeError('restored files/modes/owners differ from frozen original')
            if active:
                call('systemctl','start',SERVICE)
                call('systemctl','is-active','--quiet',SERVICE)
            result['restored']=True
        except Exception as e:
            result['status']='failed'; result['restore_error']=str(e)
            result['error']=result.get('error','')+'; restore: '+str(e)
        write_results(out,result)
        if result['restored']:
            shutil.rmtree(work)
    return 0 if result['status']=='passed' and result['restored'] else 1


def main():
    # Internal timing entry points keep command exit status; do not save stdout/stderr.
    if len(sys.argv)>1 and sys.argv[1] in ['--measure','--record']:
        mode,event,phase=sys.argv[1:4]
        if mode=='--measure':
            start=time.monotonic_ns(); rc=subprocess.run(sys.argv[4:]).returncode
        else:
            start=int(sys.argv[4]); rc=int(sys.argv[5])
        entry=dict(phase=phase,seconds=(time.monotonic_ns()-start)/1e9,exit_code=rc)
        with open(event,'a') as f: f.write(json.dumps(entry)+'\n')
        return rc if mode=='--measure' else 0
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('from_version'); p.add_argument('to_version')
    p.add_argument('from_assets'); p.add_argument('to_assets')
    p.add_argument('scale',choices=['tiny','snapshot','full','compact'])
    p.add_argument('--out',required=True)
    return rehearse(p.parse_args())

if __name__=='__main__':
    sys.exit(main())
