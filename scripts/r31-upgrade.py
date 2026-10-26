#!/usr/bin/env python3
"""R31 Linux systemd upgrade rehearsal using LOCAL official release assets.

Run as root, only in a maintainer-assigned exclusive test slot. The existing
Server must be installed and its service account must already exist. Original
files are moved aside while stopped; finally restores their exact bytes/modes.
Fresh installation prompts (including password) remain on /dev/tty for the human.
No tokens, passwords or installation transcripts are saved by this tool.
"""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import shlex
import signal
import socket
import tempfile
import subprocess
import sys
import time
import r31lib as releases
sys.dont_write_bytecode = True

HANDLED_SIGNALS=[getattr(signal,name) for name in ['SIGINT','SIGTERM','SIGHUP'] if hasattr(signal,name)]
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
    return releases.assets(directory,version,arch)


def instrument(text,helper,events,fail=False):
    '''Exact anchors; disposable installer only. Preserve commands and statuses.'''
    helper_quoted=shlex.quote(str(helper)); events_quoted=shlex.quote(str(events))
    anchors=[('SERVER_UPGRADE_CANDIDATE','verify','precheck-verify'),
             ('SERVER_UPGRADE_CANDIDATE','migrate-copy','migrate-copy'),
             ('SERVER_UPGRADE_CANDIDATE','migrate-protected','migrate-protected'),
             ('SERVER_BINARY','verify','postcheck-verify')]
    for binary,mode,phase in anchors:
        old=f'runuser -u "${{SERVICE_USER}}" -- "${{{binary}}}" database {mode} --db'
        if text.count(old)!=1:
            raise ValueError(f'unsupported installer timing anchor: {phase}')
        text=text.replace(old,f'python3 {helper_quoted} --measure {events_quoted} {phase} '+old)
    wrappers=[]
    for function,phase in [('restore_server_upgrade','rollback-total'),
                           ('restore_server_files_from_backup','rollback-restore-files'),
                           ('verify_restored_server_files','rollback-verify-files'),
                           ('verify_server_stable_readiness','health-observation')]:
        anchor=function+'() {'
        if text.count(anchor)!=1:
            raise ValueError(f'unsupported function timing anchor: {function}')
        text=text.replace(anchor,'r31_original_'+anchor)
        wrappers.append(f'''FUNCTION() {{
  local r31_start r31_status=0
  r31_start="$(python3 -c 'import time; print(time.monotonic_ns())')"
  r31_original_FUNCTION "$@" || r31_status=$?
  python3 HELPER --record EVENTS PHASE "$r31_start" "$r31_status" || return 1
  return "$r31_status"
}}
'''.replace('FUNCTION',function).replace('HELPER',helper_quoted).replace('EVENTS',events_quoted).replace('PHASE',phase))
    # The protected backup is inline. Its start/end anchors must also match once.
    backup_start='  cp -a -- "${SERVER_BINARY}" "${SERVER_UPGRADE_BACKUP}/server-binary"'
    if text.count(backup_start)!=1:
        raise ValueError('unsupported backup start anchor')
    text=text.replace(backup_start,"  local r31_backup_start\n  r31_backup_start=\"$(python3 -c 'import time; print(time.monotonic_ns())')\"\n"+backup_start)
    lines=text.splitlines(True)
    for prefix,inserted in [
        ('write_server_upgrade_state backup-complete ',f'  python3 {helper_quoted} --record {events_quoted} protected-backup "$r31_backup_start" 0 || die "R31 backup timing failed"\n'),
        ('write_server_upgrade_state binary-replaced ','  exit 87 # R31 simulated failure after migration and binary replacement\n' if fail else '')]:
        hits=[i for i,line in enumerate(lines) if line.lstrip().startswith(prefix)]
        if len(hits)!=1:
            raise ValueError('unsupported installer state anchor: '+prefix)
        if inserted: lines.insert(hits[0]+1,inserted)
    text=''.join(lines)
    anchor='\nmain "$@"'
    if text.count(anchor)!=1:
        raise ValueError('unsupported installer entry anchor')
    return text.replace(anchor,'\n'+''.join(wrappers)+anchor)


@contextmanager
def recovery_signals():
    '''A second Ctrl-C must not interrupt restoration, result persistence or cleanup.'''
    previous={sig:signal.signal(sig,signal.SIG_IGN) for sig in HANDLED_SIGNALS}
    try:
        yield
    finally:
        for sig,handler in previous.items(): signal.signal(sig,handler)


def systemctl(*args):
    return subprocess.run(['systemctl',*args],capture_output=True,text=True)


def remove_path(path):
    if path.is_symlink():
        raise RuntimeError('cleanup target became symlink: '+str(path))
    if path.is_dir(): shutil.rmtree(path)
    else: path.unlink(missing_ok=True)


def move_path(source,destination):
    '''Rename on one filesystem; cp -a for the pre-approved runtime tmpfs only.'''
    if destination.exists() or destination.is_symlink():
        raise RuntimeError('move destination already exists: '+str(destination))
    if source.stat().st_dev==destination.parent.stat().st_dev:
        source.rename(destination)
    else:
        # preflight_filesystems permits cross-device movement only for the root-owned /run lock.
        call('cp','-a','--',source,destination)
        def relative_state(root):
            return {str(Path(k).relative_to(root)):v for k,v in tree_state([root]).items()}
        if relative_state(source)!=relative_state(destination):
            raise RuntimeError('cross-device copy did not preserve contents/ownership')
        remove_path(source)


def preflight_filesystems(paths,backup_parent,runtime_path):
    device=backup_parent.stat().st_dev
    for path in paths:
        if not path.exists(): continue
        if path.stat().st_dev!=device:
            if path!=runtime_path or any(v[1:3]!=[0,0] for v in tree_state([path]).values()):
                raise ValueError('installation path must share backup filesystem: '+str(path))


def preflight_output(out):
    out.mkdir(mode=0o700,parents=True,exist_ok=True)
    for name in ['result.json','summary.md']:
        if (out/name).exists() or (out/name).is_symlink():
            raise ValueError('result output must be fresh: '+str(out/name))
    with tempfile.NamedTemporaryFile(prefix='.r31-write-check-',dir=out) as probe:
        probe.write(b'R31'); probe.flush(); os.fsync(probe.fileno())


def confirm_test_host(value):
    if value!=socket.gethostname():
        raise ValueError('--confirm-test-host must exactly match this test machine hostname')


def verify_schema(verification,expected):
    if verification.get('integrity')!='ok' or verification.get('schema_version')!=expected:
        raise RuntimeError(f'expected verified schema {expected}, got {verification}')
    return verification


def wait_ready(control=systemctl,probe=None,timeout=30):
    if probe is None:
        probe=lambda: subprocess.run(['curl','--silent','--output','/dev/null','--write-out','%{http_code}',
            '--connect-timeout','1','--max-time','1','--request','POST',
            'http://127.0.0.1:8080/api/v1/agent/jobs/claim'],capture_output=True,text=True).stdout.strip()
    deadline=time.monotonic()+timeout
    while True:
        if control('is-active','--quiet',SERVICE).returncode==0 and probe()=='401': return
        if time.monotonic()>=deadline: raise RuntimeError('Server did not become healthy')
        time.sleep(0.25)


def restore_installation(paths,candidates,backup,moved,before,cleanup_started,active,enabled,
                         control=systemctl,move=move_path,remove=remove_path,state=tree_state):
    '''Independent recovery steps: collect errors, continue every safe remaining step.

    An active DB is never replaced; a failed hash check never starts the service.
    stop=5 with an absent unit is a warning, not a reason to skip file restoration.
    '''
    errors=[]; warnings=[]; steps=[]
    def attempt(name,action):
        try:
            value=action(); steps.append(dict(step=name,ok=True)); return True,value
        except Exception as e:
            errors.append(f'{name}: {e}'); steps.append(dict(step=name,ok=False)); return False,None
    def checked(*args):
        proc=control(*args)
        if proc.returncode: raise RuntimeError(f'systemctl {args[0]} exit {proc.returncode}')
        return proc
    with recovery_signals():
        # stop may fail because the unit was already moved away. Always query state.
        try:
            stopped=control('stop',SERVICE)
            if stopped.returncode: warnings.append(f'systemctl stop exit {stopped.returncode}; checking inactivity')
        except Exception as e:
            warnings.append('systemctl stop: '+str(e))
        ok,activity=attempt('check-stopped',lambda:control('is-active','--quiet',SERVICE))
        safe_stopped=ok and activity.returncode in [3,4,5]
        if ok and not safe_stopped: errors.append('Server may still be active; file replacement withheld')
        if safe_stopped:
            if cleanup_started:
                for path in paths+candidates: attempt('remove '+str(path),lambda p=path:remove(p))
            for i,path in moved:
                source=backup/str(i)
                def restore_one(a=source,b=path):
                    if not a.exists(): raise RuntimeError('original backup missing: '+str(a))
                    move(a,b)
                attempt('restore '+str(path),restore_one)
        reload_ok,_=attempt('daemon-reload',lambda:checked('daemon-reload'))
        attempt('enablement',lambda:checked('enable' if enabled else 'disable',SERVICE))
        def compare():
            if before is not None and state(paths)!=before:
                raise RuntimeError('restored files/modes/owners differ from frozen original')
        files_ok,_=attempt('verify-original-files',compare)
        if active and safe_stopped and files_ok and reload_ok:
            attempt('start-original',lambda:checked('start',SERVICE))
            attempt('check-original-active',lambda:checked('is-active','--quiet',SERVICE))
        return dict(restored=not errors,restore_errors=errors,restore_warnings=warnings,restore_steps=steps)

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
           ]
    lines+=['',f"Expected schema: {result.get('expected_schema')}; observed: {result.get('post_upgrade_verification',{}).get('schema_version')}",
        result.get('cache_policy',''),'']
    lines+=['| phase | seconds | exit |','|---|---:|---:|']
    for phase in result.get('events',[]):
        lines.append(f"| {phase['run']}/{phase['phase']} | {phase['seconds']:.3f} | {phase['exit_code']} |")
        if phase['phase']=='rollback-total':
            lines.append(f"| {phase['run']}/rollback-excluding-health | {phase['seconds_excluding_health']:.3f} | {phase['exit_code']} |")
    if result.get('error'):
        lines+=['',result['error']]
    if not result.get('restored'):
        lines+=['',f"Manual recovery required. Private backup: `{result.get('backup')}`. See r31-usage.md."]
    (out/'summary.md').write_text('\n'.join(lines)+'\n')


def rehearse(args):
    if sys.platform!='linux' or os.geteuid()!=0:
        raise ValueError('requires Linux root and an exclusive test slot')
    confirm_test_host(args.confirm_test_host)
    versions=[]
    for v in [args.from_version,args.to_version]:
        if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',v):
            raise ValueError('only canonical stable vX.Y.Z versions are accepted')
        versions.append(tuple(map(int,v[1:].split('.'))))
    if versions[0]>=versions[1]:
        raise ValueError('target must be newer')
    releases.registered(args.from_version)
    expected_schema=releases.expected_schema(args.to_version,args.expected_schema)
    if expected_schema < releases.registered(args.from_version)['schema']:
        raise ValueError('target schema cannot precede starting schema')
    arch={'x86_64':'amd64','aarch64':'arm64'}.get(platform.machine())
    if not arch:
        raise ValueError('only linux amd64/arm64 supported')
    old,old_server,old_identity=assets(args.from_assets,args.from_version,arch)
    new,new_server,new_identity=assets(args.to_assets,args.to_version,arch)
    base_assets=Path(args.base_assets) if args.base_assets else old if args.from_version=='v1.0.6' else None
    if base_assets is None:
        raise ValueError('--base-assets required for later starting versions')
    assets(base_assets,'v1.0.6',arch)
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
    preflight_output(out)
    preflight_filesystems(PATHS,work.parent,Path('/run/404-probe-upgrade'))
    work.mkdir(mode=0o700)
    backup=work/'original'; backup.mkdir(mode=0o700)
    result=dict(from_version=args.from_version,to_version=args.to_version,scale=args.scale,
        from_identity=old_identity,to_identity=new_identity,status='failed',restored=False,
        backup=str(backup),events=[],expected_schema=expected_schema,
        cache_policy='not flushed; failure runs first, success follows with warmer page cache')
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
    for sig in HANDLED_SIGNALS:
        signal.signal(sig,stop_signal)
    try:
        call('systemctl','stop',SERVICE)
        before=tree_state(PATHS)
        # Freeze ALL application data, including SQLite WAL/SHM, before the first install.
        for i,p in enumerate(PATHS):
            if p.exists():
                move_path(p,backup/str(i)); moved.append((i,p))
        cleanup_started=True
        call('systemctl','daemon-reload')
        generator=Path(__file__).with_name('r31-generate.py')
        import importlib.util
        spec=importlib.util.spec_from_file_location('r31_generate',generator)
        module=importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        result['fixture']=module.generate(old_server,work/'fixture.db',args.scale,anchor_ms=int(time.time()//60)*60000,
                                          start_version=args.from_version,base_assets=base_assets)
        if install(old,args.from_version)!=0:
            raise RuntimeError('starting-version local installation failed/cancelled')
        call('systemctl','stop',SERVICE)
        for suffix in ['', '-wal','-shm']:
            Path(str(DB)+suffix).unlink(missing_ok=True)
        shutil.copy2(work/'fixture.db',DB)
        shutil.chown(DB,user='404-probe',group='404-probe'); DB.chmod(0o600)
        call('runuser','-u','404-probe','--',old_server,'agent','list','--db',DB,stdout=subprocess.DEVNULL)
        call('systemctl','start',SERVICE)
        wait_ready()
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
                rollbacks=[e for e in recorded if e['phase']=='rollback-total']
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
            required=['precheck-verify','migrate-copy','migrate-protected','protected-backup']
            if not fail: required.append('postcheck-verify')
            for phase in required:
                if not any(e['phase']==phase and e['exit_code']==0 for e in recorded):
                    raise RuntimeError(f'missing successful timed phase: {phase}')
            expected=old_server if fail else new_server
            if digest(BINARY)!=digest(expected):
                raise RuntimeError('unexpected installed binary')
            call('systemctl','is-active','--quiet',SERVICE)
            if not fail:
                post=next(e['verification'] for e in recorded if e['phase']=='postcheck-verify')
                result['post_upgrade_verification']=verify_schema(post,expected_schema)
        result['status']='passed'
    except Exception as e:
        result['error']=f'{type(e).__name__}: {e}'
    finally:
        with recovery_signals():
            result.update(restore_installation(PATHS,CANDIDATES,backup,moved,before,cleanup_started,active,enabled))
            if not result['restored']:
                result['status']='failed'
                result['error']=result.get('error','')+'; restore: '+'; '.join(result['restore_errors'])
            try:
                write_results(out,result)
            except Exception as e:
                result['status']='failed'
                print('Could not persist results; private recovery workspace retained: '+str(work),file=sys.stderr)
                print(str(e),file=sys.stderr)
            else:
                if result['restored']:
                    try:
                        shutil.rmtree(work)
                    except Exception as e:
                        result['status']='failed'; result['error']='temporary workspace cleanup failed: '+str(e)
                        write_results(out,result)
    return 0 if result['status']=='passed' and result['restored'] else 1


def main():
    # Internal timing entry points keep command exit status; do not save stdout/stderr.
    if len(sys.argv)>1 and sys.argv[1] in ['--measure','--record']:
        mode,event,phase=sys.argv[1:4]
        if mode=='--measure':
            start=time.monotonic_ns()
            proc=subprocess.run(sys.argv[4:],stdout=subprocess.PIPE if phase=='postcheck-verify' else None,text=True)
            rc=proc.returncode
            if phase=='postcheck-verify': sys.stdout.write(proc.stdout)
        else:
            start=int(sys.argv[4]); rc=int(sys.argv[5])
        entry=dict(phase=phase,seconds=(time.monotonic_ns()-start)/1e9,exit_code=rc,start_ns=start)
        if mode=='--measure' and phase=='postcheck-verify' and rc==0:
            entry['verification']=json.loads(proc.stdout)
        if phase=='health-observation': entry['configured_minimum_seconds']=5
        if phase=='rollback-total':
            previous=[json.loads(line) for line in Path(event).read_text().splitlines()] if Path(event).exists() else []
            health=sum(e['seconds'] for e in previous if e['phase']=='health-observation' and e['start_ns']>=start)
            entry['health_observation_seconds']=health
            entry['seconds_excluding_health']=max(0,entry['seconds']-health)
        with open(event,'a') as f: f.write(json.dumps(entry)+'\n')
        return rc if mode=='--measure' else 0
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('from_version'); p.add_argument('to_version')
    p.add_argument('from_assets'); p.add_argument('to_assets')
    p.add_argument('scale',choices=['tiny','snapshot','full','compact'])
    p.add_argument('--out',required=True)
    p.add_argument('--confirm-test-host',required=True,help='type this test machine hostname exactly; never use on production')
    p.add_argument('--expected-schema',type=int,help='registered target schema, or required explicit schema for a new target')
    p.add_argument('--base-assets',help='official v1.0.6 assets for the schema34 fixture base')
    return rehearse(p.parse_args())

if __name__=='__main__':
    sys.exit(main())
