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
        self.assertIn('--record /private/events rollback',result)
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
            self.assertEqual(a['rows']['agent_plans'],31)
            with self.assertRaises(ValueError): gen.generate(Path(server),first,'tiny')
            c=sqlite3.connect(first)
            self.assertEqual(c.execute('select protocol,family,count(*) from quality_slots group by protocol,family').fetchall(),
                [('icmp','ipv4',60),('icmp','ipv6',60),('tcp','ipv4',66),('tcp','ipv6',66)])
            c.execute("UPDATE quality_totals SET value=value+1 WHERE kind='receipts'"); c.commit()
            with self.assertRaisesRegex(RuntimeError,'quality_totals'): gen.validate(c)
            c.close()

if __name__=='__main__': unittest.main()
