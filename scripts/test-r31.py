#!/usr/bin/env python3
"""Unit tests plus tiny end-to-end tests when R31_PREVIOUS_SERVER is set."""
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import sqlite3
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
        server=os.environ.get('R31_PREVIOUS_SERVER')
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

if __name__=='__main__': unittest.main()
