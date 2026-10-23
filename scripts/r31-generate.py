#!/usr/bin/env python3
"""R31 schema-34 synthetic fixture. Python standard library only; never uses a live DB.

The supplied v1.0.6 Server creates the schema via `agent list`/storage.Open.
This data adapter intentionally refuses other schemas. For the next release retain
this adapter and the old release binary; add a separately tested adapter for the
new previous-release schema, never copy handwritten DDL into this tool.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import random
import sqlite3
import struct
import subprocess
import time
from pathlib import Path

NOW = 1792713600000  # fixed 2026-10-23 UTC; reproducible retention windows
DAY = 86400000


def run(server, *args):
    p = subprocess.run([str(server), *map(str, args)], capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError(f'Server {args[0]} exited {p.returncode}: {p.stderr.strip()}')
    return p.stdout


def uvar(n):
    out = bytearray()
    while n >= 128:
        out.append((n & 127) | 128)
        n >>= 7
    out.append(n)
    return bytes(out)


def bucket_wire(resolution):
    # Exact v1.0.6 qualityEncodeBucket v1 format; successful synthetic TCP RTTs.
    n = resolution // 30000
    bins = min(n, 8)
    counts = [n // bins + (i < n % bins) for i in range(bins)]
    wire = b'\x01' + b''.join(uvar(x) for x in
        [n, n, n, n, n, n, 0, 0, 0, max(n-1, 0), 0, 0, 30000, 30000])
    wire += struct.pack('<dd', n * 40.0, max(n-1, 0) * 0.25) + uvar(0) + uvar(bins)
    wire += b''.join(struct.pack('<HI', 157+i, count) for i, count in enumerate(counts))
    wire += b''.join(uvar(x) for x in [1, NOW-resolution, 1, n, NOW-30000, n, 0, 1, 1, 1, 1])
    return wire + struct.pack('<dB', 0.0, 0)


def bucket_wire_full(resolution):
    # Full retained windows have richer RTT histograms. Increase real histogram bins
    # with resolution (30-second sampling), rather than adding arbitrary blob padding.
    n = resolution // 30000
    bins = min(n, 120)
    counts = [n // bins + (i < n % bins) for i in range(bins)]
    wire = b'\x01' + b''.join(uvar(x) for x in
        [n, n, n, n, n, n, 0, 0, 0, max(n-1,0), 0, 0, 30000, 30000])
    wire += struct.pack('<dd', n*40.0, max(n-1,0)*0.25) + uvar(0) + uvar(bins)
    wire += b''.join(struct.pack('<HI', 120+i, count) for i,count in enumerate(counts))
    wire += b''.join(uvar(x) for x in [1,NOW-resolution,1,n,NOW-30000,n,0,1,1,1,1])
    return wire + struct.pack('<dB',0.0,0)


def fingerprint(c):
    rows = c.execute("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name").fetchall()
    return hashlib.sha256(json.dumps(rows).encode()).hexdigest()


def validate(c):
    if c.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
        raise RuntimeError('integrity_check failed')
    if c.execute('PRAGMA foreign_key_check').fetchone():
        raise RuntimeError('foreign_key_check failed')
    for kind, count in c.execute('SELECT kind,value FROM quality_totals'):
        actual = c.execute(f'SELECT count(*) FROM quality_{kind}').fetchone()[0]
        if actual != count:
            raise RuntimeError(f'quality_totals mismatch: {kind}')
    for kind in ['raw', 'minute', 'targets', 'intervals', 'gaps']:
        bad = c.execute(f'''SELECT count(*) FROM quality_usage u WHERE {kind} <>
            (SELECT count(*) FROM quality_{kind} t WHERE t.agent_id=u.agent_id)''').fetchone()[0]
        if bad:
            raise RuntimeError(f'quality_usage mismatch: {kind}')


def generate(server, output, scale, seed=31, anchor_ms=NOW):
    global NOW
    NOW = anchor_ms
    start = time.monotonic()
    output = Path(output).absolute()
    if output.exists() or any(Path(str(output)+s).exists() for s in ['-wal', '-shm']):
        raise ValueError('output must not exist (including WAL/SHM)')
    identity = json.loads(run(server, 'version', '--json'))
    if identity['version'] != 'v1.0.6' or identity.get('dirty'):
        raise ValueError('a clean v1.0.6 release Server is required')
    output.parent.mkdir(parents=True, exist_ok=True)
    run(server, 'agent', 'list', '--db', output)  # official storage.Open, no DDL here
    c = sqlite3.connect(output)
    c.execute('PRAGMA foreign_keys=ON')
    c.execute('PRAGMA cache_size=-32768')
    if c.execute('SELECT max(version) FROM schema_migrations').fetchone()[0] != 34:
        raise ValueError('only schema 34 is supported')
    schema_hash = fingerprint(c)
    rng = random.Random(seed)
    def uid():
        return f'{rng.getrandbits(128):032x}'
    def insert(table, rows):
        rows = iter(rows)
        first = next(rows, None)
        if first is None:
            return
        keys = list(first)
        sql = f'INSERT INTO {table} ({",".join(keys)}) VALUES ({",".join("?" for _ in keys)})'
        c.execute(sql, list(first.values()))
        batch = []
        for row in rows:
            batch.append([row[k] for k in keys])
            if len(batch) == 5000:
                c.executemany(sql, batch)
                batch.clear()
        c.executemany(sql, batch)
        c.commit()
    tiny = scale == 'tiny'
    def size(n):
        return max(1, n//10000) if tiny else n
    agents = [uid() for _ in range(71)]
    insert('agents', (dict(id=a, name=f'example-{i:02}', token_hash=hashlib.sha256(a.encode()).digest(),
        revoked=int(i>=31), created_at=NOW-50*DAY, updated_at=NOW) for i,a in enumerate(agents)))
    insert('agent_sessions', (dict(agent_id=agents[i%71], session_id=f'example-session-{i}', started_at=NOW-i*60000,
        active=int(i<31), first_seen=NOW-i*60000, epoch=i+1) for i in range(228)))
    # Populate required state columns from the official schema, with realistic numeric values.
    columns = c.execute('PRAGMA table_info(agent_state)').fetchall()
    states = []
    for i,a in enumerate(agents[:59]):
        row = {name: (0 if 'INT' in typ or 'REAL' in typ else 'example')
               for _,name,typ,required,default,pk in columns if required and default is None}
        row.update(agent_id=a, session_id=f'example-session-{i}', hostname='example', os='linux', arch='amd64',
                   collected_at=NOW, last_seen=NOW, ram_total=1073741824, disk_total=21474836480)
        states.append(row)
    insert('agent_state', states)
    insert('agent_upgrade_operations', (dict(operation_id=uid(),agent_id=agents[i%71],from_version='v1.0.5',
        target_version='v1.0.6',status='succeeded',created_at=NOW-i*60000,updated_at=NOW,
        finished_at=NOW) for i in range(113)))
    insert('quality_config', (dict(agent_id=a,revision=1,enabled=0,ipv6=0,choices='[]',updated_at=NOW,
        settings_revision=1) for a in agents[:32]))
    slots = ['telecom','unicom','mobile']
    insert('quality_slot_settings', (dict(agent_id=agents[i//3],slot=slots[i%3],kind='custom',
        region='example',isp='example',updated_revision=1) for i in range(66)))
    insert('quality_slots', (dict(agent_id=agents[i//12],slot=slots[(i//4)%3],family=['ipv4','ipv6'][i%2],
        protocol=['tcp','icmp'][(i//2)%2],revision=1,target_id=None,enabled=0) for i in range(264) if not ((i//2)%2 == 1 and i//12 >= 20)))
    targets = [uid() for _ in range(131)]
    def owner(i):
        return agents[i%23]
    insert('quality_targets', (dict(id=t,agent_id=owner(i),fingerprint=uid(),payload=json.dumps(dict(
        slot=slots[i%3],family='ipv4',slot_revision='1',source='user',protocol='tcp',host=f'192.0.2.{i%250+1}',
        port=443,status='ready')),created_at=NOW-30*DAY,retired_at=NOW)
        for i,t in enumerate(targets)))
    insert('quality_series', (dict(id=i+1,agent_id=owner(i),target_id=targets[i],semantics='staged_probe_v2') for i in range(128)))
    insert('quality_sessions', (dict(id=i+1,agent_id=agents[i],epoch=1,session_id=f'example-quality-{i}') for i in range(23)))
    insert('quality_intervals', (dict(agent_id=owner(i),slot=slots[i%3],family='ipv4',protocol='tcp',
        revision=i+1,target_id=targets[i],start_at=NOW-30*DAY,end_at=NOW,config_from=1,config_until=1,
        v1_authorized=0) for i in range(111)))
    insert('quality_gaps', (dict(agent_id=agents[i%23],epoch=1,session_id=f'example-quality-{i%23}',
        start_at=NOW-i*60000-30000,end_at=NOW-i*60000,reason='example',dropped=1) for i in range(95)))
    metric_count = 1280 if tiny else (31*43200 if scale=='full' else 1252235)
    def metrics(count, offset=0, owners=40):
        for i in range(count):
            minute = (i//owners)*43200//max(1, (count+owners-1)//owners) + offset if offset == 0 else i//owners+offset
            yield dict(agent_id=agents[i%owners],bucket=NOW-(minute+1)*60000,samples=2,
                cpu_sum=35.125+i%80,ram_sum=94.125,swap_sum=0.0,disk_sum=66.25,
                load1_sum=0.125,load5_sum=0.25,load15_sum=0.375,rx_rate_sum=12345.125,
                tx_rate_sum=5432.125,rx_total=123456789012+max(0,43200-minute-1)*12000,tx_total=98765432101+max(0,43200-minute-1)*6000)
    insert('minute_metrics',metrics(metric_count,owners=31 if scale=='full' else 40))
    # Cycle starts lie inside the retained window, with an actual preceding row.
    # Match usage exactly to the mode's accumulated delta (including calibrated plans).
    plans=[]
    for i,a in enumerate(agents[:43]):
        cycle_start=NOW-(7+i%19)*DAY
        first=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? ORDER BY bucket DESC LIMIT 1',(a,cycle_start)).fetchone()
        last=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? ORDER BY bucket DESC LIMIT 1',(a,NOW)).fetchone()
        mode=['rx','tx','sum'][i%3]
        rx,tx=(last[0]-first[0],last[1]-first[1]) if first and last else (0,0)
        plans.append(dict(agent_id=a,traffic_mode=mode,quota_value='1',quota_unit='TiB',quota_bytes=1099511627776,
            cycle_kind='days',cycle_count=30,cycle_anchor=datetime.fromtimestamp(cycle_start/1000,timezone.utc).strftime('%Y-%m-%d'),
            timezone='UTC',cycle_start=cycle_start,cycle_end=cycle_start+30*DAY,
            usage_bytes={'rx':rx,'tx':tx,'sum':rx+tx}[mode],usage_status='calibrated' if first and i%4==0 else 'partial',updated_at=NOW))
        if last:
            c.execute('UPDATE agent_state SET rx_total=?,tx_total=? WHERE agent_id=?',(*last,a))
    insert('agent_plans',plans)
    insert('agent_plan_config',(dict(agent_id=a,revision=uid()) for a in agents[:43]))
    insert('quality_retired',(dict(session_key=i+1,seq=0,sample_time=NOW-2*DAY-30000) for i in range(23)))
    insert('tcp_mini_state',(dict(agent_id=a,evidence_start=NOW-3*DAY,scanned_at=NOW,initialized=1,revision=1) for a in agents))
    insert('agent_google_status',(dict(agent_id=a,youtube_status='unknown',search_status='unknown',signin_status='unknown',
        gemini_status='unknown',checked_at=NOW,unknown_streak=0,next_due_at=NOW+DAY) for a in agents[:48]))
    insert('agent_google_status_capabilities',(dict(agent_id=a,supported=1,updated_at=NOW) for a in agents[:48]))
    receipts = size(108*5760 if scale=='full' else 433443)
    insert('quality_receipts', (dict(session_key=i%23+1,seq=i+1,series_id=i%108+1,
        sample_time=NOW-(i//108*5760//max(1,(receipts+107)//108)+1)*30000,config_revision=1,slot_revision=1,
        content_hash=hashlib.sha256(str(i).encode()).digest(),canonical_length=512) for i in range(receipts)))
    for res, count in [(60000,155520 if scale=='full' else 61343),
                       (300000,217728 if scale=='full' else 122483),(3600000,77760 if scale=='full' else 10817)]:
        wire = bucket_wire(res)
        if scale == 'full':
            wire = bucket_wire_full(res)
        insert('quality_buckets',(dict(series_id=i%108+1,resolution=res,bucket_start=NOW-(i//108*{60000:1440,300000:2016,3600000:720}[res]//max(1,(size(count)+107)//108)+1)*res,
            data=wire) for i in range(size(count))))
    insert('quality_minute',(dict(agent_id=owner(i%108),target_id=targets[i%108],bucket=NOW-(i//108*1440//max(1,(size(155520 if scale=='full' else 61235)+107)//108)+1)*60000,
        count=2,attempts=2,successes=2,latency_sum=80.25,sent=0,received=0) for i in range(size(155520 if scale=='full' else 61235))))
    insert('quality_facts',(dict(raw_id=i+1,target_id=targets[i],series_id=i+1,session_key=i%23+1,seq=i+1,
        sample_time=NOW-30000,config_revision=1,slot_revision=1,resolved_ip='198.51.100.1',eligible=1,
        rtt=40.0,interval_ms=30000) for i in range(128)))
    security = json.dumps({'example': 'example event; '*810})
    insert('agent_security_batches',(dict(agent_id=agents[i%31],batch_id=uid(),content_hash=hashlib.sha256(str(i).encode()).digest(),
        epoch=1,session_id='example',window_start=NOW-(i+1)*3000000,window_end=NOW-i*3000000,
        collected_at=NOW-i*3000000,status='complete',total_events=1,source_count=1,payload_json=security,
        received_at=NOW-i*3000000) for i in range(size(863))))
    insert('tcp_mini_high',(dict(agent_id=agents[i%31],minute=NOW-(i//31+1)*60000,peak_ms=2500.25,
        source=json.dumps({'example':'example '*43})) for i in range(size(8611))))
    jobs = [uid() for _ in range(size(2855))]
    insert('probe_jobs',(dict(id=j,agent_id=agents[i%31],probe_type='http',config_json=json.dumps(
        {'url':'https://example.invalid/','example':'example '*45}),timeout_ms=5000,created_at=NOW-i*1500000,
        scheduled_for=NOW-i*1500000,not_before=NOW-i*1500000,expires_at=NOW-i*1500000+60000,
        status='finished',attempt=1,finished_at=NOW-i*1500000+1000) for i,j in enumerate(jobs)))
    insert('probe_results',(dict(job_id=j,attempt=1,received_at=NOW-i*1500000+1000,agent_epoch=1,session_id='example',
        agent_started_at=NOW-i*1500000,agent_finished_at=NOW-i*1500000+1000,duration_ms=1000,success=1,
        resolved_ip='203.0.113.1',payload_json=json.dumps({'example':'example '*25}),result_hash=b'0'*32) for i,j in enumerate(jobs)))
    c.execute('UPDATE schema_migrations SET applied_at=?',(NOW//1000,))
    c.execute('UPDATE web_domain_policy SET updated_at=?',(NOW//1000,))
    c.commit()
    scattered = []
    if scale == 'snapshot':
        # Reproduce the snapshot's page-internal fragmentation, as well as free
        # pages. Allocate missing minute positions, compact once, then delete
        # these interleaved rows before raising the high-water mark.
        occupied = {i*43200//((metric_count+39)//40) for i in range((metric_count+39)//40)}
        scattered = sorted(set(range(43200))-occupied)[::2]
        def fragmentation_rows():
            for i,row in enumerate(metrics(len(scattered)*40,offset=100000)):
                row['bucket'] = NOW-(scattered[i//40]+1)*60000
                yield row
        insert('minute_metrics',fragmentation_rows())
    c.execute('VACUUM')
    if scattered:
        c.executemany('DELETE FROM minute_metrics WHERE bucket=?',((NOW-(i+1)*60000,) for i in scattered))
        c.commit()
    if scale == 'snapshot':
        # Reuse real metric rows to allocate pages, then delete them with all indexes intact.
        # No padding table, DDL, disabled FK or disabled triggers.
        offset = 100000
        while c.execute('PRAGMA page_count').fetchone()[0]*4096 < 515*1024*1024:
            insert('minute_metrics',metrics(10000, offset))
            offset += 250
        c.execute('DELETE FROM minute_metrics WHERE bucket < ?', (NOW-100000*60000,))
        c.commit()
    validate(c)
    if fingerprint(c) != schema_hash:
        raise RuntimeError('schema changed during generation')
    c.execute('PRAGMA wal_checkpoint(TRUNCATE)')
    tables = [r[0] for r in c.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
    result = dict(scale=scale,seed=seed,anchor_ms=NOW,server=identity,schema_sha256=schema_hash,
        page_size=c.execute('PRAGMA page_size').fetchone()[0],page_count=c.execute('PRAGMA page_count').fetchone()[0],
        freelist_count=c.execute('PRAGMA freelist_count').fetchone()[0],
        rows={t:c.execute(f'SELECT count(*) FROM {t}').fetchone()[0] for t in tables},integrity='ok',foreign_keys='ok',counters='ok')
    c.close()
    # Exercise official Open and read-only verification on the finished fixture.
    run(server,'agent','list','--db',output)
    result['verify'] = json.loads(run(server,'database','verify','--db',output))
    result['file_bytes'] = output.stat().st_size
    result['elapsed_seconds'] = round(time.monotonic()-start,3)
    Path(str(output)+'.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--server',required=True,type=Path)
    p.add_argument('--out',required=True,type=Path)
    p.add_argument('--scale',choices=['tiny','snapshot','full','compact'],required=True)
    p.add_argument('--seed',type=int,default=31)
    p.add_argument('--anchor-ms',type=int,default=NOW,help='UTC window end; fixed by default, use current minute for live upgrade rehearsal')
    a=p.parse_args()
    print(json.dumps(generate(a.server.absolute(),a.out,a.scale,a.seed,a.anchor_ms),indent=2))

if __name__ == '__main__':
    main()
