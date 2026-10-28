#!/usr/bin/env python3
"""Unit tests plus tiny end-to-end tests when R31_PREVIOUS_SERVER is set."""
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import sqlite3
import shutil
import subprocess
import tempfile
import time
import unittest
import sys
import signal
from unittest import mock
sys.dont_write_bytecode = True

ROOT=Path(__file__).resolve().parent

def module(name,file):
    spec=importlib.util.spec_from_file_location(name,ROOT/file)
    m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    return m

gen=module('r31gen','r31-generate.py')
upgrade=module('r31upgrade','r31-upgrade.py')

class ToolTests(unittest.TestCase):
    def test_histogram_sample_count_and_no_padding(self):
        # Decode v1 header independently; validate bin counts and strict end of wire.
        import struct
        def read_var(w,offset):
            value=0; shift=0
            while True:
                b=w[offset]; offset+=1; value|=(b&127)<<shift
                if b<128: return value,offset
                shift+=7
        for encoder in [gen.bucket_wire,gen.bucket_wire_full]:
            for resolution in [60000,300000,3600000]:
                w=encoder(resolution); pos=1; fields=[]
                self.assertEqual(w[0],1)
                for _ in range(14):
                    v,pos=read_var(w,pos); fields.append(v)
                pos+=16
                categories,pos=read_var(w,pos); self.assertEqual(categories,0)
                bins,pos=read_var(w,pos); total=0
                for _ in range(bins):
                    key,count=struct.unpack_from('<HI',w,pos); pos+=6
                    self.assertLessEqual(key,24000); self.assertGreater(count,0); total+=count
                self.assertEqual(total,fields[1])
                self.assertEqual(total,resolution//30000)
                for _ in range(11): _,pos=read_var(w,pos)
                self.assertEqual(pos+9,len(w))

    def test_exact_installer_instrumentation(self):
        text=(ROOT.parent/'install.sh').read_text(encoding='utf-8')
        result=upgrade.instrument(text,PurePosixPath('/private/helper.py'),PurePosixPath('/private/events'),True)
        self.assertTrue('--measure /private/events migrate-protected' in result)
        self.assertEqual(result.count('exit 87 # R31'),1)
        self.assertIn('r31_original_restore_server_upgrade "$@"',result)
        self.assertIn('--record /private/events rollback-total',result)
        self.assertIn('--measure /private/events precheck-verify',result)
        self.assertIn('--measure /private/events postcheck-verify',result)
        for phase in ['rollback-restore-files','rollback-verify-files','health-observation','protected-backup']:
            self.assertIn(phase,result)
        bash=os.environ.get('R31_BASH','bash')
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'install.sh'; p.write_text(result,encoding='utf-8',newline='\n')
            subprocess.run([bash,'-n',str(p)],check=True)
        with self.assertRaises(ValueError): upgrade.instrument('unsupported installer',Path('/h'),Path('/e'),True)

    def test_corrupt_counter_is_rejected(self):
        server=os.environ.get('R31_BASE_SERVER') or os.environ.get('R31_PREVIOUS_SERVER')
        if not server: self.skipTest('R31_PREVIOUS_SERVER not provided')
        with tempfile.TemporaryDirectory() as d:
            first=Path(d)/'first.db'; second=Path(d)/'second.db'
            a=gen.generate(Path(server),first,'tiny')
            time.sleep(1.05)  # cross the official empty DB's unixepoch() second boundary
            b=gen.generate(Path(server),second,'tiny')
            self.assertTrue(first.read_bytes()==second.read_bytes(),'fixed seed/anchor must be byte reproducible')
            self.assertEqual(a['rows'],b['rows'])
            self.assertEqual(a['verify']['schema_version'],34)
            self.assertEqual(a['rows']['agents'],71)
            self.assertEqual(a['rows']['agent_plans'],43)
            with self.assertRaises(ValueError): gen.generate(Path(server),first,'tiny')
            c=sqlite3.connect(first)
            self.assertEqual(c.execute('select protocol,family,count(*) from quality_slots group by protocol,family').fetchall(),
                [('icmp','ipv4',60),('icmp','ipv6',60),('tcp','ipv4',66),('tcp','ipv6',66)])
            self.assertEqual(c.execute('SELECT count(*) FROM quality_targets WHERE retired_at IS NULL').fetchone()[0],0)
            self.assertEqual(c.execute('SELECT count(*) FROM quality_retired').fetchone()[0],23)
            self.assertEqual(c.execute('SELECT count(*) FROM tcp_mini_state').fetchone()[0],71)
            self.assertEqual(c.execute('SELECT count(*) FROM agent_google_status').fetchone()[0],48)
            backwards=c.execute('''SELECT count(*) FROM (SELECT rx_total,tx_total,
                lag(rx_total) OVER (PARTITION BY agent_id ORDER BY bucket) prior_rx,
                lag(tx_total) OVER (PARTITION BY agent_id ORDER BY bucket) prior_tx
                FROM minute_metrics) WHERE rx_total<prior_rx OR tx_total<prior_tx''').fetchone()[0]
            self.assertEqual(backwards,0)
            accepted=calibrated=0
            for agent,start,mode,usage,status in c.execute('SELECT agent_id,cycle_start,traffic_mode,usage_bytes,usage_status FROM agent_plans'):
                anchor=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? ORDER BY bucket DESC LIMIT 1',(agent,start)).fetchone()
                last=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? ORDER BY bucket DESC LIMIT 1',(agent,)).fetchone()
                if anchor and last:
                    rx,tx=last[0]-anchor[0],last[1]-anchor[1]
                    self.assertEqual(usage,{'rx':rx,'tx':tx,'sum':rx+tx}[mode])
                    self.assertGreater(usage,0); accepted+=1; calibrated+=status=='calibrated'
            self.assertEqual(accepted,40); self.assertGreater(calibrated,0)
            c.execute("UPDATE quality_totals SET value=value+1 WHERE kind='receipts'"); c.commit()
            with self.assertRaisesRegex(RuntimeError,'quality_totals'): gen.validate(c)
            c.close()

class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.paths=[self.root/'data',self.root/'config',self.root/'server.service',self.root/'server-binary']
        for p in self.paths[:2]:
            p.mkdir(); (p/'example').write_text('original example')
        for p in self.paths[2:]: p.write_text('original example')
        self.before=upgrade.tree_state(self.paths)
        self.backup=self.root/'backup'; self.backup.mkdir()
        self.moved=[]
        self.config=self.root/'fake-systemctl.json'
        self.config.write_text(json.dumps(dict(active=False,errors={},calls=[])))
        self.fake=self.root/'fake-systemctl.py'
        self.fake.write_text("""import json,sys
from pathlib import Path
p=Path(sys.argv[1]); state=json.loads(p.read_text()); args=sys.argv[2:]; op=args[0]
state['calls'].append(op); rc=state['errors'].get(op,0)
if op=='is-active': rc=0 if state['active'] else 3
elif rc==0 and op in ['stop','start']: state['active']=op=='start'
p.write_text(json.dumps(state)); sys.exit(rc)
""")

    def control(self,*args):
        return subprocess.run([sys.executable,str(self.fake),str(self.config),*args],capture_output=True,text=True)

    def freeze(self,count=4):
        for i,p in enumerate(self.paths[:count]):
            upgrade.move_path(p,self.backup/str(i)); self.moved.append((i,p))

    def config_errors(self,**errors):
        state=json.loads(self.config.read_text()); state['errors']=errors; self.config.write_text(json.dumps(state))

    def restore(self,**kw):
        return upgrade.restore_installation(self.paths,[],self.backup,self.moved,self.before,
            kw.pop('cleanup_started',False),True,True,control=kw.pop('control',self.control),**kw)

    def calls(self): return json.loads(self.config.read_text())['calls']

    def test_unit_missing_stop5_restores_all_original_files(self):
        self.freeze(); self.config_errors(stop=5)
        result=self.restore(cleanup_started=True)
        self.assertTrue(result['restored'],result)
        self.assertEqual(upgrade.tree_state(self.paths),self.before)
        self.assertTrue(result['restore_warnings'])
        self.assertIn('start',self.calls())

    def test_move_interrupted_halfway_restores_only_moved_paths(self):
        self.freeze(2)
        result=self.restore()
        self.assertTrue(result['restored'],result)
        self.assertEqual(upgrade.tree_state(self.paths),self.before)

    def test_failed_file_restore_continues_later_paths_and_keeps_backup(self):
        self.freeze()
        def fail_one(a,b):
            if b==self.paths[0]: raise OSError('injected move failure')
            upgrade.move_path(a,b)
        result=self.restore(move=fail_one)
        self.assertFalse(result['restored'])
        self.assertTrue((self.backup/'0').exists())
        self.assertTrue(all(p.exists() for p in self.paths[1:]))
        self.assertNotIn('start',self.calls())

    def test_missing_backup_does_not_skip_later_paths(self):
        self.freeze(); upgrade.remove_path(self.backup/'0')
        result=self.restore()
        self.assertFalse(result['restored'])
        self.assertTrue(all(p.exists() for p in self.paths[1:]))
        self.assertNotIn('start',self.calls())

    def test_stop_command_exception_still_restores_when_inactive(self):
        self.freeze()
        def unavailable_stop(*args):
            if args[0]=='stop': raise OSError('injected stop exception')
            return self.control(*args)
        result=self.restore(control=unavailable_stop)
        self.assertTrue(result['restored'],result)
        self.assertEqual(upgrade.tree_state(self.paths),self.before)

    def test_cleanup_error_does_not_skip_other_restores(self):
        self.freeze()
        self.paths[1].mkdir(); (self.paths[1]/'example').write_text('temporary example')
        def fail_one(path):
            if path==self.paths[1]: raise OSError('injected cleanup failure')
            upgrade.remove_path(path)
        result=self.restore(cleanup_started=True,remove=fail_one)
        self.assertFalse(result['restored'])
        self.assertTrue(self.paths[0].exists()); self.assertTrue(self.paths[2].exists())
        self.assertTrue((self.backup/'1').exists()); self.assertNotIn('start',self.calls())

    def test_daemon_reload_failure_does_not_skip_enablement_or_hash_check(self):
        self.freeze(); self.config_errors(**{'daemon-reload':1})
        result=self.restore()
        self.assertFalse(result['restored'])
        self.assertIn('enable',self.calls()); self.assertNotIn('start',self.calls())
        self.assertTrue(any(s['step']=='verify-original-files' and s['ok'] for s in result['restore_steps']))
        self.assertEqual(upgrade.tree_state(self.paths),self.before)

    def test_enablement_failure_still_attempts_safe_start(self):
        self.freeze(); self.config_errors(enable=1)
        result=self.restore()
        self.assertFalse(result['restored']); self.assertIn('start',self.calls())

    def test_failed_stop_of_active_service_never_replaces_open_database(self):
        self.freeze(); state=json.loads(self.config.read_text()); state.update(active=True,errors={'stop':1}); self.config.write_text(json.dumps(state))
        result=self.restore()
        self.assertFalse(result['restored']); self.assertTrue((self.backup/'0').exists())
        self.assertFalse(self.paths[0].exists()); self.assertIn('daemon-reload',self.calls())

    def test_hash_mismatch_leaves_original_stopped(self):
        self.freeze()
        result=self.restore(state=lambda paths:{'injected mismatch':[]})
        self.assertFalse(result['restored']); self.assertNotIn('start',self.calls())
        self.assertTrue(all(p.exists() for p in self.paths))

    def test_second_ctrl_c_is_ignored_during_restore_and_handlers_restored(self):
        self.freeze(); previous=signal.getsignal(signal.SIGINT)
        def interrupted(*args):
            if args[0]=='daemon-reload': signal.raise_signal(signal.SIGINT)
            return self.control(*args)
        result=self.restore(control=interrupted)
        self.assertTrue(result['restored'],result)
        self.assertEqual(signal.getsignal(signal.SIGINT),previous)

    def test_all_four_missing_or_duplicate_timing_anchors_rejected(self):
        text=(ROOT.parent/'install.sh').read_text(encoding='utf-8')
        for binary,mode in [('SERVER_UPGRADE_CANDIDATE','verify'),('SERVER_UPGRADE_CANDIDATE','migrate-copy'),
                            ('SERVER_UPGRADE_CANDIDATE','migrate-protected'),('SERVER_BINARY','verify')]:
            anchor=f'runuser -u "${{SERVICE_USER}}" -- "${{{binary}}}" database {mode} --db'
            for changed in [text.replace(anchor,'missing-anchor'),text+'\n'+anchor]:
                with self.assertRaises(ValueError): upgrade.instrument(changed,Path('/helper'),Path('/events'))

    def test_output_preflight_and_test_host_mismatch(self):
        blocker=self.root/'blocked'; blocker.write_text('example')
        with self.assertRaises(OSError): upgrade.preflight_output(blocker/'out')
        out=self.root/'writable'; upgrade.preflight_output(out)
        self.assertEqual(list(out.iterdir()),[])
        (out/'result.json').write_text('{}')
        with self.assertRaises(ValueError): upgrade.preflight_output(out)
        with mock.patch.object(upgrade.socket,'gethostname',return_value='example-test-host'):
            with self.assertRaises(ValueError): upgrade.confirm_test_host('wrong-example-host')
            upgrade.confirm_test_host('example-test-host')

    def test_schema_mismatch_and_health_failure_rejected(self):
        self.assertEqual(upgrade.verify_schema({'schema_version':35,'integrity':'ok'},35)['schema_version'],35)
        with self.assertRaises(RuntimeError): upgrade.verify_schema({'schema_version':34,'integrity':'ok'},35)
        with self.assertRaises(RuntimeError): upgrade.verify_schema({'schema_version':35,'integrity':'bad'},35)
        with self.assertRaises(RuntimeError): upgrade.wait_ready(self.control,lambda:'500',timeout=0)

    def test_measurement_records_schema_and_preserves_failure_code(self):
        event=self.root/'measurements.jsonl'
        helper=[sys.executable,'-B',str(ROOT/'r31-upgrade.py')]
        subprocess.run(helper+['--measure',str(event),'postcheck-verify',sys.executable,'-c',
            'import json; print(json.dumps(dict(schema_version=35,integrity="ok")))'],check=True,capture_output=True,text=True)
        failure=subprocess.run(helper+['--measure',str(event),'precheck-verify',sys.executable,'-c','raise SystemExit(7)'])
        self.assertEqual(failure.returncode,7)
        entries=[json.loads(line) for line in event.read_text().splitlines()]
        self.assertEqual(entries[0]['verification']['schema_version'],35)
        self.assertEqual(entries[1]['exit_code'],7)

    def test_rollback_health_time_is_reported_separately(self):
        event=self.root/'timing.jsonl'; start=time.monotonic_ns()-1000000000
        event.write_text(json.dumps(dict(phase='health-observation',seconds=0.25,start_ns=start+1))+'\n')
        subprocess.run([sys.executable,'-B',str(ROOT/'r31-upgrade.py'),'--record',str(event),
            'rollback-total',str(start),'0'],check=True)
        last=json.loads(event.read_text().splitlines()[-1])
        self.assertEqual(last['health_observation_seconds'],0.25)
        self.assertAlmostEqual(last['seconds']-last['seconds_excluding_health'],0.25)

    def test_cross_device_installation_is_rejected_before_move(self):
        source=mock.Mock(); source.exists.return_value=True; source.stat.return_value.st_dev=2
        backup=mock.Mock(); backup.stat.return_value.st_dev=1
        with self.assertRaises(ValueError): upgrade.preflight_filesystems([source],backup,Path('/runtime'))

class EvidenceWrapperTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.wrapper=self.root/'run-with-evidence.py'
        shutil.copyfile(ROOT/'r31-run-with-evidence.py',self.wrapper)
        self.fake='''from pathlib import Path
root=Path(__file__).resolve().parent
PATHS=[Path('/example-data'),Path('/example-config')]
ORIGINAL={'/example-data':[33152,0,0,'example-original-sha'],'/example-config':[33152,0,0,'example-config-sha']}
_after=False
CHANGE=False

def tree_state(paths):
    state={p.as_posix():list(ORIGINAL.get(p.as_posix(),[33152,0,0,'example-runtime'])) for p in paths}
    if _after and CHANGE: state['/example-data'][-1]='example-changed-sha'
    return state

def restore_installation(*args,state=tree_state,**kwargs):
    return 0 if state(PATHS)==ORIGINAL else 9

def main():
    global _after
    (root/'main-called').write_text('example')
    tree_state([Path('/runtime-example')])
    assert not (root/'evidence/before-frozen.json').exists(), 'captured unrelated preflight state'
    tree_state(PATHS)
    _after=True
    return restore_installation()
'''

    def run_wrapper(self,changed=False):
        (self.root/'r31-upgrade.py').write_text(self.fake.replace('CHANGE=False','CHANGE='+str(changed)),encoding='utf-8')
        return subprocess.run([sys.executable,'-B',str(self.wrapper)],capture_output=True,text=True)

    def test_records_exact_before_and_after_only_at_intended_callbacks(self):
        evidence=self.root/'evidence'; evidence.mkdir(mode=0o700)
        result=self.run_wrapper()
        self.assertEqual(result.returncode,0,result.stderr)
        before=json.loads((evidence/'before-frozen.json').read_text())
        after=json.loads((evidence/'after-restored-frozen.json').read_text())
        self.assertEqual(before,after)
        comparison=json.loads((evidence/'frozen-compare.json').read_text())
        self.assertTrue(comparison['equal']); self.assertEqual(comparison['changed_paths'],[])
        self.assertEqual(comparison['before_entries'],2)
        if os.name=='posix':
            for p in evidence.iterdir(): self.assertEqual(p.stat().st_mode&0o777,0o600)

    def test_records_hash_difference_and_forwards_original_exit_code(self):
        evidence=self.root/'evidence'; evidence.mkdir(mode=0o700)
        result=self.run_wrapper(changed=True)
        self.assertEqual(result.returncode,9,result.stderr)
        comparison=json.loads((evidence/'frozen-compare.json').read_text())
        self.assertFalse(comparison['equal']); self.assertEqual(comparison['changed_paths'],['/example-data'])

    def test_missing_private_evidence_directory_refuses_before_main(self):
        result=self.run_wrapper()
        self.assertNotEqual(result.returncode,0)
        self.assertFalse((self.root/'main-called').exists())

    def test_existing_run_evidence_is_not_overwritten(self):
        evidence=self.root/'evidence'; evidence.mkdir(mode=0o700)
        before=evidence/'before-frozen.json'; before.write_text('example previous evidence')
        result=self.run_wrapper()
        self.assertNotEqual(result.returncode,0)
        self.assertEqual(before.read_text(),'example previous evidence')
        self.assertFalse((self.root/'main-called').exists())

releases=module('r31releases','r31lib.py')
preflight=module('r31preflight','r31-preflight.py')


def fake_assets(directory, version, commit=None):
    directory=Path(directory); directory.mkdir()
    commit=commit or releases.registered(version)['commit']
    for name in releases.BINARIES: (directory/name).write_bytes(name.encode()+b' synthetic asset')
    (directory/'install.sh').write_bytes((ROOT.parent/'install.sh').read_bytes())
    metadata=dict(version=version,commit=commit,assets=[dict(name=n,sha256=releases.digest(directory/n)) for n in sorted(releases.BINARIES)])
    (directory/'RELEASE-METADATA.json').write_text(json.dumps(metadata),encoding='utf-8')
    refresh_manifest(directory)
    return directory


def refresh_manifest(directory):
    (directory/'SHA256SUMS').write_text(''.join(releases.digest(directory/n)+'  '+n+'\n' for n in sorted(releases.PAYLOADS)),encoding='utf-8',newline='\n')


class RegisteredReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.assets=fake_assets(self.root/'assets','v1.0.8')
        self.identity=dict(version='v1.0.8',commit=releases.registered('v1.0.8')['commit'],dirty=False)

    def test_unknown_start_and_unregistered_target_schema(self):
        with self.assertRaisesRegex(ValueError,'unregistered'):
            gen.generate('/does/not/exist','/unused','tiny',start_version='v1.0.10')
        with self.assertRaisesRegex(ValueError,'expected-schema'): releases.expected_schema('v1.0.10')
        self.assertEqual(releases.expected_schema('v1.0.10',36),36)
        self.assertEqual(releases.expected_schema('v1.0.8',35),35)
        with self.assertRaises(ValueError): releases.expected_schema('v1.0.8',36)
        for value in (0,-1,True):
            with self.assertRaises(ValueError): releases.expected_schema('v1.0.10',value)
        for value in ('v1.0.10-rc.1','1.0.8','v01.0.8'):
            with self.assertRaises(ValueError): releases.version_tuple(value)

    def test_identity_all_three_sources_must_agree(self):
        with mock.patch.object(releases.subprocess,'check_output',return_value=json.dumps(self.identity).encode()):
            self.assertEqual(releases.assets(self.assets,'v1.0.8','amd64')[2],self.identity)
        for changed in (dict(self.identity,dirty=True),dict(self.identity,commit='a'*40),dict(self.identity,version='v1.0.7')):
            with mock.patch.object(releases.subprocess,'check_output',return_value=json.dumps(changed).encode()):
                with self.assertRaises(ValueError): releases.assets(self.assets,'v1.0.8','amd64')
        metadata=json.loads((self.assets/'RELEASE-METADATA.json').read_text()); metadata['commit']='b'*40
        (self.assets/'RELEASE-METADATA.json').write_text(json.dumps(metadata)); refresh_manifest(self.assets)
        with self.assertRaisesRegex(ValueError,'registry'): releases.assets(self.assets,'v1.0.8','amd64')

    def test_checksums_and_metadata_asset_hashes_cannot_disagree(self):
        binary=self.assets/'404-probe-agent-linux-arm64'; binary.write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError,'checksum'): releases.assets(self.assets,'v1.0.8','amd64')
        refresh_manifest(self.assets)
        with self.assertRaisesRegex(ValueError,'metadata/manifest'): releases.assets(self.assets,'v1.0.8','amd64')

    def test_metadata_version_and_manifest_metadata_hash_are_checked(self):
        p=self.assets/'RELEASE-METADATA.json'; doc=json.loads(p.read_text()); doc['version']='v1.0.7';p.write_text(json.dumps(doc))
        with self.assertRaisesRegex(ValueError,'checksum'): releases.assets(self.assets,'v1.0.8','amd64')
        refresh_manifest(self.assets)
        with self.assertRaisesRegex(ValueError,'metadata identity'): releases.assets(self.assets,'v1.0.8','amd64')

    def test_v109_registration_and_numeric_9_10_chain(self):
        row=releases.registered('v1.0.9')
        self.assertEqual(row,dict(commit='42c5eeb798f679e49ea81f9480b7bcab46aac2df',schema=35,previous_version='v1.0.8',added_tables=[],removed_tables=[]))
        document=releases.registry()
        # Insert 10 first: registry validation must sort numerically, not by JSON order.
        document['versions']={'v1.0.10':dict(commit='a'*40,schema=36,previous_version='v1.0.9',added_tables=['example'],removed_tables=[]),**document['versions']}
        registry=self.root/'registry.json';registry.write_text(json.dumps(document),encoding='utf-8')
        with mock.patch.object(releases,'REGISTRY',registry):
            self.assertEqual(releases.registered('v1.0.10')['schema'],36)
            releases.check_counts(dict(agents=71,schema_migrations=35),dict(agents=71,schema_migrations=36,example=0),'v1.0.9','v1.0.10')
            with self.assertRaisesRegex(ValueError,'backward'):
                releases.check_counts({}, {}, 'v1.0.10','v1.0.9')
            with self.assertRaisesRegex(ValueError,'record count'):
                releases.check_counts(dict(agents=71,schema_migrations=35),dict(agents=70,schema_migrations=36,example=0),'v1.0.9','v1.0.10')
        self.assertEqual(releases.version_tuple('v1.0.9') < releases.version_tuple('v1.0.10'),True)

    def test_registered_table_changes_and_only_migration_count_may_change(self):
        before=dict(agents=71,schema_migrations=34,tcp_mini_high=7,tcp_mini_state=71)
        after=dict(agents=71,schema_migrations=35)
        releases.check_counts(before,after,'v1.0.6','v1.0.7')
        releases.check_counts(before,after,'v1.0.6','v1.0.8')
        releases.check_counts(after,after,'v1.0.7','v1.0.8')
        for bad in (dict(after,agents=70),dict(after,schema_migrations=34),dict(after,new_table=0),dict(after,tcp_mini_state=0)):
            with self.assertRaises(ValueError): releases.check_counts(before,bad,'v1.0.6','v1.0.8')


class StartingVersionIntegrationTests(unittest.TestCase):
    def test_registered_start_official_migration_and_post_audit(self):
        server=os.environ.get('R31_PREVIOUS_SERVER');version=os.environ.get('R31_START_VERSION')
        if not server or not version: self.skipTest('set R31_PREVIOUS_SERVER and R31_START_VERSION for official integration')
        base=os.environ.get('R31_BASE_SERVER',server)
        with tempfile.TemporaryDirectory() as directory:
            db=Path(directory)/'fixture.db'
            result=gen.generate(Path(server),db,'tiny',start_version=version,base_assets=Path(base).parent)
            self.assertEqual(result['verify']['schema_version'],releases.registered(version)['schema'])
            self.assertEqual(result['base_version'],'v1.0.6');self.assertEqual(result['starting_version'],version)
            self.assertTrue(result['rows_checked']);self.assertEqual(result['rows']['agents'],71)
            self.assertEqual(len(result['official_migrations']),int(version!='v1.0.6'))
            if version!='v1.0.6': self.assertNotIn('tcp_mini_state',result['rows'])
            with sqlite3.connect(db) as c:c.execute('DELETE FROM agent_google_status WHERE agent_id=(SELECT agent_id FROM agent_google_status LIMIT 1)')
            after=gen.audit(db,releases.registered(version)['schema'])
            with self.assertRaisesRegex(ValueError,'record count'):
                releases.check_counts(result['base_generation']['rows'],after['rows'],'v1.0.6',version)
            with sqlite3.connect(db) as c:c.execute("UPDATE quality_totals SET value=value+1 WHERE kind='minute'")
            with self.assertRaisesRegex(RuntimeError,'quality_totals'):gen.audit(db,releases.registered(version)['schema'])


class PreflightTests(unittest.TestCase):
    def setUp(self):
        import types
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)
        self.old=fake_assets(self.root/'old','v1.0.9');self.base=fake_assets(self.root/'base','v1.0.6')
        self.new=fake_assets(self.root/'new','v1.0.10','c'*40)
        self.work=self.root/'work';self.work.mkdir(mode=0o700)
        for name in ('evidence','results'):(self.work/name).mkdir(mode=0o700)
        paths=[self.root/p for p in ['var/lib/404-probe','etc/404-probe','usr/local/bin/404-probe-server',
               'usr/local/sbin/404-probe-install','etc/systemd/system/404-probe-server.service','var/lib/404-probe-upgrade','run/404-probe-upgrade']]
        for i,p in enumerate(paths):
            p.parent.mkdir(parents=True,exist_ok=True)
            if i in (0,1,5,6):p.mkdir(exist_ok=True)
            else:p.write_text('example')
        shutil.copyfile(self.old/'404-probe-server-linux-amd64',paths[2])
        self.control_calls=[]
        self.m=types.SimpleNamespace(PATHS=paths,CANDIDATES=[self.root/'candidate'],BINARY=paths[2],DB=paths[0]/'404-probe.db',SERVICE='example.service',
             systemctl=lambda *a:self.control_calls.append(a) or types.SimpleNamespace(returncode=0),
             preflight_filesystems=lambda *a:None,instrument=upgrade.instrument)
        self.raw=dict(from_version='v1.0.9',to_version='v1.0.10',from_assets=str(self.old),to_assets=str(self.new),base_assets=str(self.base),work_dir=str(self.work),scale='tiny',expected_schema=35)
        self.config=self.root/'config.json';self.config.write_text(json.dumps(self.raw))
        self.doc=preflight.configuration(self.config)
        def output(args):
            if args[0]=='runuser':return json.dumps(dict(schema_version=35,integrity='ok')).encode()
            if Path(args[0])==self.m.BINARY:return json.dumps(dict(version='v1.0.9',commit=releases.registered('v1.0.9')['commit'],dirty=False)).encode()
            meta=json.loads(Path(args[0]).parent.joinpath('RELEASE-METADATA.json').read_text())
            return json.dumps(dict(version=meta['version'],commit=meta['commit'],dirty=False)).encode()
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(preflight.platform,'machine',return_value='x86_64').start()
        mock.patch.object(preflight.subprocess,'check_output',side_effect=output).start()
        mock.patch.object(preflight.subprocess,'run',return_value=types.SimpleNamespace(returncode=0)).start()
        mock.patch.object(preflight.shutil,'disk_usage',return_value=types.SimpleNamespace(free=9*1024**3)).start()

    def test_check_is_read_only_and_only_queries_service_state(self):
        before=upgrade.tree_state([self.root])
        self.assertTrue(preflight.check(self.doc,self.m)['ready'])
        self.assertEqual(before,upgrade.tree_state([self.root]))
        self.assertEqual(self.control_calls,[('is-active','--quiet','example.service'),('is-enabled','--quiet','example.service')])

    def test_pending_dropin_candidate_and_stale_results_rejected(self):
        for p in (self.m.PATHS[5]/'pending',self.m.CANDIDATES[0],self.m.PATHS[4].with_name(self.m.PATHS[4].name+'.d'),self.work/'run-started',self.work/'results/result.json'):
            p.write_text('example')
            with self.assertRaises(ValueError):preflight.check(self.doc,self.m)
            p.unlink()

    def test_mismatched_schema_and_insufficient_space_rejected(self):
        with mock.patch.object(preflight.shutil,'disk_usage',return_value=type('Disk',(),dict(free=0))()):
            with self.assertRaisesRegex(ValueError,'8 GiB'):preflight.check(self.doc,self.m)
        with mock.patch.object(preflight.subprocess,'check_output',return_value=b'{"version":"wrong"}'):
            with self.assertRaises(ValueError):preflight.check(self.doc,self.m)

    def test_operator_summary_is_automatic_and_rejects_incomplete_result(self):
        (self.work/'run-started').touch()
        valid=dict(status='passed',restored=True,restore_errors=[],restore_warnings=[],from_version='v1.0.9',to_version='v1.0.10',post_upgrade_verification=dict(schema_version=35,integrity='ok'))
        (self.work/'results/result.json').write_text(json.dumps(valid));(self.work/'evidence/frozen-compare.json').write_text('{"equal":true}')
        with mock.patch.object(preflight.sys,'platform','linux'),mock.patch.object(preflight.os,'geteuid',return_value=0,create=True),mock.patch.object(preflight.sys.stdin,'isatty',return_value=True),mock.patch.object(preflight.sys.stdout,'isatty',return_value=True):
            self.assertEqual(preflight.operator_run(self.doc),0)
        self.assertTrue(json.loads((self.work/'operator-summary.json').read_text())['verified'])
        self.assertEqual((self.work/'operator-exit.txt').read_text(),'0\n')
        (self.work/'operator-summary.json').unlink();(self.work/'operator-exit.txt').unlink()
        (self.work/'evidence/frozen-compare.json').write_text('{"equal":false}')
        with mock.patch.object(preflight.sys,'platform','linux'),mock.patch.object(preflight.os,'geteuid',return_value=0,create=True),mock.patch.object(preflight.sys.stdin,'isatty',return_value=True),mock.patch.object(preflight.sys.stdout,'isatty',return_value=True):
            self.assertEqual(preflight.operator_run(self.doc),2)
        self.assertFalse(json.loads((self.work/'operator-summary.json').read_text())['verified'])


@unittest.skipUnless(sys.platform=='linux' and hasattr(os,'geteuid') and os.geteuid()==0,'entry TTY/flock tests require Linux root; run on isolated test host')
class ShellEntryTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)
        self.script=self.root/'r31-rehearse.sh';shutil.copyfile(ROOT/'r31-rehearse.sh',self.script)
        (self.root/'r31-preflight.py').write_text("import sys,pathlib\np=pathlib.Path(sys.argv[2]);mode=sys.argv[1]\nif mode=='--work-dir':print(p)\nelif mode=='--operator-run':(p/'operator-called').write_text('once')\n",encoding='utf-8')

    def terminal(self):
        import pty
        pid,fd=pty.fork()
        if pid==0:os.execv('/bin/bash',['bash',str(self.script),'--config',str(self.root)])
        output=b''
        while True:
            try:b=os.read(fd,4096)
            except OSError:break
            if not b:break
            output+=b
        os.close(fd);_,status=os.waitpid(pid,0)
        return os.waitstatus_to_exitcode(status),output

    def test_non_tty_refuses_before_creating_files(self):
        before=upgrade.tree_state([self.root])
        p=subprocess.run(['bash',str(self.script),'--config',str(self.root)],capture_output=True)
        self.assertNotEqual(p.returncode,0);self.assertIn(b'interactive',p.stderr)
        self.assertEqual(before,upgrade.tree_state([self.root]))

    def test_check_creates_no_lock_or_start_marker(self):
        before=upgrade.tree_state([self.root])
        p=subprocess.run(['bash',str(self.script),'--check',str(self.root)],capture_output=True)
        self.assertEqual(p.returncode,0,p.stderr);self.assertEqual(before,upgrade.tree_state([self.root]))

    def test_run_once_and_lock_exclusion(self):
        import fcntl
        with (self.root/'rehearsal.lock').open('w') as lock:
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            rc,out=self.terminal();self.assertNotEqual(rc,0);self.assertIn(b'another rehearsal',out)
        rc,out=self.terminal();self.assertEqual(rc,0,out)
        marker=(self.root/'run-started').stat().st_mtime_ns
        rc,out=self.terminal();self.assertNotEqual(rc,0)
        self.assertEqual((self.root/'run-started').stat().st_mtime_ns,marker)
        self.assertEqual((self.root/'operator-called').read_text(),'once')


if __name__=='__main__': unittest.main()
