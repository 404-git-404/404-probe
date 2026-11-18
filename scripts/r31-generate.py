#!/usr/bin/env python3
"""R31 synthetic fixtures built on the official v1.2.0 Server's schema 37."""
import argparse
from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import random
import sqlite3
import struct
import subprocess
import time
from pathlib import Path
import r31lib as releases

NOW = 1792713600000  # fixed 2026-10-23 UTC; reproducible retention windows
DAY = 86400000
USAGE_KINDS = ('raw', 'minute', 'targets', 'intervals', 'gaps')
TOTAL_KINDS = set(USAGE_KINDS) | {'buckets', 'facts', 'receipts', 'series', 'sessions'}
EMPTY_TABLES = {
    'agent_enrollment_tokens': 'No temporary enrollment credentials are issued.',
    'agent_removal_operations': 'No remote uninstall is in progress.',
    'agent_removal_receipts': 'No recently completed remote uninstall awaits receipt replay.',
    'agent_plan_renewal_requests': 'No recent renewal request awaits retry or undo.',
}


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


def bucket_wire(resolution, anchor_ms=NOW):
    # Exact v1.0.6 qualityEncodeBucket v1 format; successful synthetic TCP RTTs.
    n = resolution // 30000
    bins = min(n, 8)
    counts = [n // bins + (i < n % bins) for i in range(bins)]
    wire = b'\x01' + b''.join(uvar(x) for x in
        [n, n, n, n, n, n, 0, 0, 0, max(n-1, 0), 0, 0, 30000, 30000])
    wire += struct.pack('<dd', n * 40.0, max(n-1, 0) * 0.25) + uvar(0) + uvar(bins)
    wire += b''.join(struct.pack('<HI', 157+i, count) for i, count in enumerate(counts))
    wire += b''.join(uvar(x) for x in [1, anchor_ms-resolution, 1, n, anchor_ms-30000, n, 0, 1, 1, 1, 1])
    return wire + struct.pack('<dB', 0.0, 0)


def bucket_wire_full(resolution, anchor_ms=NOW):
    # Full retained windows have richer RTT histograms. Increase real histogram bins
    # with resolution (30-second sampling), rather than adding arbitrary blob padding.
    n = resolution // 30000
    bins = min(n, 120)
    counts = [n // bins + (i < n % bins) for i in range(bins)]
    wire = b'\x01' + b''.join(uvar(x) for x in
        [n, n, n, n, n, n, 0, 0, 0, max(n-1,0), 0, 0, 30000, 30000])
    wire += struct.pack('<dd', n*40.0, max(n-1,0)*0.25) + uvar(0) + uvar(bins)
    wire += b''.join(struct.pack('<HI', 120+i, count) for i,count in enumerate(counts))
    wire += b''.join(uvar(x) for x in [1,anchor_ms-resolution,1,n,anchor_ms-30000,n,0,1,1,1,1])
    return wire + struct.pack('<dB',0.0,0)


def fingerprint(c):
    rows = c.execute("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name").fetchall()
    return hashlib.sha256(json.dumps(rows).encode()).hexdigest()


def validate(c):
    if c.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
        raise RuntimeError('integrity_check failed')
    if c.execute('PRAGMA foreign_key_check').fetchone():
        raise RuntimeError('foreign_key_check failed')
    totals = dict(c.execute('SELECT kind,value FROM quality_totals'))
    if totals.keys() != TOTAL_KINDS:
        raise RuntimeError('quality_totals keys differ from expected counters')
    for kind, count in totals.items():
        actual = c.execute(f'SELECT count(*) FROM quality_{kind}').fetchone()[0]
        if actual != count:
            raise RuntimeError(f'quality_totals mismatch: {kind}')
    for kind in USAGE_KINDS:
        bad = c.execute(f'''SELECT count(*) FROM quality_usage u WHERE {kind} <>
            (SELECT count(*) FROM quality_{kind} t WHERE t.agent_id=u.agent_id)''').fetchone()[0]
        missing = c.execute(f'''SELECT 1 FROM quality_{kind} t WHERE NOT EXISTS
            (SELECT 1 FROM quality_usage u WHERE u.agent_id=t.agent_id) LIMIT 1''').fetchone()
        if bad or missing:
            raise RuntimeError(f'quality_usage mismatch: {kind}')


def generate_base(server, output, scale, seed=31, anchor_ms=NOW):
    start = time.monotonic()
    output = Path(output).absolute()
    paths = [output, Path(str(output)+'-wal'), Path(str(output)+'-shm')]
    if any(p.exists() or p.is_symlink() for p in paths):
        raise ValueError('output must not exist (including WAL/SHM)')
    identity = json.loads(run(server, 'version', '--json'))
    if (identity.get('version') != releases.BASE_VERSION or identity.get('dirty') is not False
            or identity.get('commit') != releases.registered(releases.BASE_VERSION)['commit']):
        raise ValueError('a clean official v1.2.0 Server is required')
    output.parent.mkdir(parents=True, exist_ok=True)
    # Reserve a fresh file so concurrent generation targeting the same path fails.
    with output.open('xb'):
        pass
    run(server, 'agent', 'list', '--db', output)  # official storage.Open, no DDL here
    with closing(sqlite3.connect(output)) as c:
        c.execute('PRAGMA foreign_keys=ON')
        c.execute('PRAGMA cache_size=-32768')
        if c.execute('SELECT max(version) FROM schema_migrations').fetchone()[0] != releases.BASE_SCHEMA:
            raise ValueError('the official base must have schema 37')
        schema_hash = fingerprint(c)
        populate(c, scale, seed, anchor_ms)
        if fingerprint(c) != schema_hash:
            raise RuntimeError('schema changed during generation')
        checkpoint(c)
    result = audit(output, releases.BASE_SCHEMA)
    verification, seconds = official_database(server, 'verify', output, releases.BASE_SCHEMA)
    return dict(scale=scale, seed=seed, anchor_ms=anchor_ms, server=identity, **result,
                integrity='ok', foreign_keys='ok', counters='ok', verify=verification,
                verify_seconds=seconds, file_bytes=output.stat().st_size,
                elapsed_seconds=round(time.monotonic()-start,3))


def populate(c, scale, seed, anchor_ms):
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
        revoked=int(i>=31), created_at=anchor_ms-50*DAY, updated_at=anchor_ms) for i,a in enumerate(agents)))
    insert('agent_sessions', (dict(agent_id=agents[i%71], session_id=f'example-session-{i}', started_at=anchor_ms-i*60000,
        active=int(i<31), first_seen=anchor_ms-i*60000, epoch=i+1) for i in range(228)))
    # Populate required state columns from the official schema, with realistic numeric values.
    columns = c.execute('PRAGMA table_info(agent_state)').fetchall()
    states = []
    for i,a in enumerate(agents[:59]):
        row = {name: (0 if 'INT' in typ or 'REAL' in typ else 'example')
               for _,name,typ,required,default,pk in columns if required and default is None}
        row.update(agent_id=a, session_id=f'example-session-{i}', hostname='example', os='linux', arch='amd64',
                   collected_at=anchor_ms, last_seen=anchor_ms, ram_total=1073741824, disk_total=21474836480)
        if i < 24:
            row['host_health_json'] = json.dumps(dict(current=dict(disk=dict(
                status='ok', checked_at=anchor_ms, probe_ms=2, read_back_verified=True))))
        else:
            row['host_health_json'] = None
        states.append(row)
    insert('agent_state', states)
    insert('agent_upgrade_operations', (dict(operation_id=uid(),agent_id=agents[i%71],from_version='v1.0.5',
        target_version='v1.0.6',status='succeeded',created_at=anchor_ms-i*60000,updated_at=anchor_ms,
        finished_at=anchor_ms) for i in range(113)))
    insert('quality_config', (dict(agent_id=a,revision=1,enabled=0,ipv6=0,choices='[]',updated_at=anchor_ms,
        settings_revision=1,slow_threshold_ms=500) for a in agents[:32]))
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
        port=443,status='ready')),created_at=anchor_ms-30*DAY,retired_at=anchor_ms)
        for i,t in enumerate(targets)))
    insert('quality_series', (dict(id=i+1,agent_id=owner(i),target_id=targets[i],semantics='staged_probe_v2') for i in range(128)))
    insert('quality_sessions', (dict(id=i+1,agent_id=agents[i],epoch=1,session_id=f'example-quality-{i}') for i in range(23)))
    insert('quality_intervals', (dict(agent_id=owner(i),slot=slots[i%3],family='ipv4',protocol='tcp',
        revision=i+1,target_id=targets[i],start_at=anchor_ms-30*DAY,end_at=anchor_ms,config_from=1,config_until=1,
        v1_authorized=0) for i in range(111)))
    insert('quality_gaps', (dict(agent_id=agents[i%23],epoch=1,session_id=f'example-quality-{i%23}',
        start_at=anchor_ms-i*60000-30000,end_at=anchor_ms-i*60000,reason='example',dropped=1) for i in range(95)))
    metric_count = 1280 if tiny else (31*43200 if scale=='full' else 1252235)
    def metrics(count, offset=0, owners=40):
        for i in range(count):
            minute = (i//owners)*43200//max(1, (count+owners-1)//owners) + offset if offset == 0 else i//owners+offset
            yield dict(agent_id=agents[i%owners],bucket=anchor_ms-(minute+1)*60000,samples=2,
                cpu_sum=35.125+i%80,ram_sum=94.125,swap_sum=0.0,disk_sum=66.25,
                load1_sum=0.125,load5_sum=0.25,load15_sum=0.375,rx_rate_sum=12345.125,
                tx_rate_sum=5432.125,rx_total=123456789012+max(0,43200-minute-1)*12000,tx_total=98765432101+max(0,43200-minute-1)*6000)
    insert('minute_metrics',metrics(metric_count,owners=31 if scale=='full' else 40))
    # Cycle starts lie inside the retained window, with an actual preceding row.
    # Match usage exactly to the mode's accumulated delta (including calibrated plans).
    plans=[]
    for i,a in enumerate(agents[:43]):
        cycle_start=anchor_ms-(7+i%19)*DAY
        first=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? ORDER BY bucket DESC LIMIT 1',(a,cycle_start)).fetchone()
        last=c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? ORDER BY bucket DESC LIMIT 1',(a,anchor_ms)).fetchone()
        mode=['rx','tx','sum'][i%3]
        rx,tx=(last[0]-first[0],last[1]-first[1]) if first and last else (0,0)
        calibrated = bool(first and i%4==0)
        plans.append(dict(agent_id=a,traffic_mode=mode,quota_value='1',quota_unit='TiB',quota_bytes=1099511627776,
            cycle_kind='days',cycle_count=30,cycle_anchor=datetime.fromtimestamp(cycle_start/1000,timezone.utc).strftime('%Y-%m-%d'),
            timezone='UTC',cycle_start=cycle_start,cycle_end=cycle_start+30*DAY,
            usage_bytes={'rx':rx,'tx':tx,'sum':rx+tx}[mode],usage_status='calibrated' if calibrated else 'partial',
            cycle_rx_bytes=0 if calibrated else rx,cycle_tx_bytes=0 if calibrated else tx,
            cycle_dir_basis='since' if calibrated else 'exact',cycle_dir_since=anchor_ms if calibrated else None,
            observe_after=anchor_ms if calibrated else 0,updated_at=anchor_ms))
        if last:
            c.execute('UPDATE agent_state SET rx_total=?,tx_total=? WHERE agent_id=?',(*last,a))
    insert('agent_plans',plans)
    insert('agent_plan_config',(dict(agent_id=a,revision=uid()) for a in agents[:43]))
    insert('quality_retired',(dict(session_key=i+1,seq=0,sample_time=anchor_ms-2*DAY-30000) for i in range(23)))
    insert('agent_google_status',(dict(agent_id=a,youtube_status='unknown',search_status='unknown',signin_status='unknown',
        gemini_status='unknown',checked_at=anchor_ms,unknown_streak=0,next_due_at=anchor_ms+DAY) for a in agents[:48]))
    insert('agent_google_status_capabilities',(dict(agent_id=a,supported=1,updated_at=anchor_ms) for a in agents[:48]))
    receipts = size(108*5760 if scale=='full' else 433443)
    insert('quality_receipts', (dict(session_key=i%23+1,seq=i+1,series_id=i%108+1,
        sample_time=anchor_ms-(i//108*5760//max(1,(receipts+107)//108)+1)*30000,config_revision=1,slot_revision=1,
        content_hash=hashlib.sha256(str(i).encode()).digest(),canonical_length=512) for i in range(receipts)))
    for res, count in [(60000,155520 if scale=='full' else 61343),
                       (300000,217728 if scale=='full' else 122483),(3600000,77760 if scale=='full' else 10817)]:
        encoder = bucket_wire_full if scale == 'full' else bucket_wire
        wire = encoder(res, anchor_ms)
        insert('quality_buckets',(dict(series_id=i%108+1,resolution=res,bucket_start=anchor_ms-(i//108*{60000:1440,300000:2016,3600000:720}[res]//max(1,(size(count)+107)//108)+1)*res,
            data=wire) for i in range(size(count))))
    insert('quality_minute',(dict(agent_id=owner(i%108),target_id=targets[i%108],bucket=anchor_ms-(i//108*1440//max(1,(size(155520 if scale=='full' else 61235)+107)//108)+1)*60000,
        count=2,attempts=2,successes=2,latency_sum=80.25,sent=0,received=0) for i in range(size(155520 if scale=='full' else 61235))))
    insert('quality_facts',(dict(raw_id=i+1,target_id=targets[i],series_id=i+1,session_key=i%23+1,seq=i+1,
        sample_time=anchor_ms-30000,config_revision=1,slot_revision=1,resolved_ip='198.51.100.1',eligible=1,
        rtt=40.0,interval_ms=30000) for i in range(128)))
    security = json.dumps({'example': 'example event; '*810})
    insert('agent_security_batches',(dict(agent_id=agents[i%31],batch_id=uid(),content_hash=hashlib.sha256(str(i).encode()).digest(),
        epoch=1,session_id='example',window_start=anchor_ms-(i+1)*3000000,window_end=anchor_ms-i*3000000,
        collected_at=anchor_ms-i*3000000,status='complete',total_events=1,source_count=1,payload_json=security,
        received_at=anchor_ms-i*3000000) for i in range(size(863))))
    jobs = [uid() for _ in range(size(2855))]
    insert('probe_jobs',(dict(id=j,agent_id=agents[i%31],probe_type='http',config_json=json.dumps(
        {'url':'https://example.invalid/','example':'example '*45}),timeout_ms=5000,created_at=anchor_ms-i*1500000,
        scheduled_for=anchor_ms-i*1500000,not_before=anchor_ms-i*1500000,expires_at=anchor_ms-i*1500000+60000,
        status='finished',attempt=1,finished_at=anchor_ms-i*1500000+1000) for i,j in enumerate(jobs)))
    insert('probe_results',(dict(job_id=j,attempt=1,received_at=anchor_ms-i*1500000+1000,agent_epoch=1,session_id='example',
        agent_started_at=anchor_ms-i*1500000,agent_finished_at=anchor_ms-i*1500000+1000,duration_ms=1000,success=1,
        resolved_ip='203.0.113.1',payload_json=json.dumps({'example':'example '*25}),result_hash=b'0'*32) for i,j in enumerate(jobs)))

    # Durable auxiliary state, using the same synthetic agents and timestamps.
    insert('agent_management_capabilities', (dict(agent_id=a,remote_removal=1,country_code_lookup=1,
        epoch=i+1,session_id=f'example-session-{i}',updated_at=anchor_ms) for i,a in enumerate(agents[:24])))
    insert('agent_country_code_lookups', (dict(agent_id=a,operation_id=uid(),status='succeeded',requested_epoch=i+1,
        requested_session_id=f'example-session-{i}',active_epoch=i+1,active_session_id=f'example-session-{i}',
        result_code='US',created_at=anchor_ms-60000,updated_at=anchor_ms) for i,a in enumerate(agents[:5])))
    insert('agent_outbound_snapshots', (dict(agent_id=a,available=0,payload_json='[]',checked_at=anchor_ms,
        status='not_detected') for a in agents[:12]))
    insert('agent_public_egress_control', (dict(agent_id=a,enabled=1,supported=1,next_check_at=anchor_ms+60000,
        failure_step=1,session_epoch=i+1,session_id=f'example-session-{i}',updated_at=anchor_ms)
        for i,a in enumerate(agents[:12])))
    # The product rejects documentation addresses for a verified public egress.
    insert('agent_public_egress', (dict(agent_id=a,payload=json.dumps(dict(version=1,supported=True,epoch=str(i+1),
        sequence='1',source='job',ipv4=dict(family='ipv4',status='unknown',reason='timeout',tried=3,
        checked_at=anchor_ms,valid_until=anchor_ms+300000),ipv6=dict(family='ipv6',status='unavailable',
        reason='no_route',checked_at=anchor_ms,valid_until=anchor_ms+300000)))) for i,a in enumerate(agents[:12])))
    batches = c.execute('SELECT agent_id,batch_id,epoch,session_id,collected_at FROM agent_security_batches '
                        'ORDER BY collected_at DESC,agent_id')
    latest_batches = {}
    for agent, batch, epoch, session, at in batches:
        latest_batches.setdefault(agent, (batch, epoch, session, at))
    insert('agent_security_capabilities', (dict(agent_id=a,supported=1,status='complete',epoch=epoch,
        session_id=session,current_batch_id=batch,current_collected_at=at,updated_at=at)
        for a,(batch,epoch,session,at) in latest_batches.items()))
    baselines = [dict(agent_id=a,rx_total=123456789012,tx_total=98765432101,
                      started_at=anchor_ms-30*DAY,request_id=uid()) for a in agents[:6]]
    insert('agent_traffic_reset_requests', baselines)
    insert('agent_traffic_baselines', baselines)
    insert('probe_schedules', (dict(id=uid(),agent_id=a,name=f'example-schedule-{i}',probe_type='http',
        config_json='{"url":"https://example.invalid/"}',timeout_ms=5000,interval_seconds=3600,
        next_run_at=anchor_ms+3600000,created_at=anchor_ms-DAY,updated_at=anchor_ms) for i,a in enumerate(agents[:4])))
    insert('quality_capability', (dict(agent_id=a,epoch=i+1,session_id=f'example-session-{i}',supported=1,
        ipv6_address='none') for i,a in enumerate(agents[:23])))
    # Legacy intake rows match the existing facts' raw IDs and session keys.
    insert('quality_raw', (dict(id=i+1,agent_id=owner(i),target_id=targets[i],epoch=1,session_id=f'example-quality-{i%23}',
        seq=i+1,slot_revision=1,config_revision=1,sample_time=anchor_ms-30000,received_at=anchor_ms,
        scheduled_at=anchor_ms-30000,finished_at=anchor_ms-29960,duration_ms=40.0,outcome='success',
        resolved_ip='198.51.100.1',raw_sent=0,raw_received=0,content_hash=hashlib.sha256(str(i).encode()).digest(),
        attempt=1,success=1,latency=40.0,packet_sent=0,packet_received=0) for i in range(128)))
    insert('web_domain_suffixes', [dict(suffix='example.invalid',created_at=anchor_ms)])
    c.execute("UPDATE web_domain_policy SET mode='suffix',revision=1")

    # The official migration seeded organization_meta and the immutable USD row.
    groups = [uid() for _ in range(4)]
    tags = [uid() for _ in range(10)]
    insert('groups', (dict(id=group,name=f'example-group-{i}',name_key=f'example-group-{i}',
        sort_order=i,revision=i+2) for i,group in enumerate(groups)))
    insert('tags', (dict(id=tag,name=f'example-tag-{i}',name_key=f'example-tag-{i}',revision=i+6)
        for i,tag in enumerate(tags)))
    insert('agent_organization', (dict(agent_id=a,group_id=groups[i%len(groups)] if i<24 else None,
        revision=i+16) for i,a in enumerate(agents[:28])))
    insert('agent_tags', (dict(agent_id=a,tag_id=tags[(i+offset)%len(tags)])
        for i,a in enumerate(agents[:28]) for offset in (0,3)))
    insert('expense_exchange_rates', (dict(currency=currency,units_per_usd=rate,updated_at=anchor_ms,revision=i+44)
        for i,(currency,rate) in enumerate([('EUR','0.92'),('JPY','150'),('CAD','1.36')])))
    c.execute('UPDATE organization_meta SET revision=46 WHERE id=1')
    regions = ['AL','CN','MD','US','GB','JP']
    insert('agent_youtube_region_manual', (dict(agent_id=a,region=regions[i%len(regions)],updated_at=anchor_ms,revision=1)
        for i,a in enumerate(agents[:12])))
    c.execute('UPDATE schema_migrations SET applied_at=?',(anchor_ms//1000,))
    c.execute('UPDATE web_domain_policy SET updated_at=?',(anchor_ms,))
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
                row['bucket'] = anchor_ms-(scattered[i//40]+1)*60000
                yield row
        insert('minute_metrics',fragmentation_rows())
    c.execute('VACUUM')
    if scattered:
        c.executemany('DELETE FROM minute_metrics WHERE bucket=?',((anchor_ms-(i+1)*60000,) for i in scattered))
        c.commit()
    if scale == 'snapshot':
        # Reuse real metric rows to allocate pages, then delete them with all indexes intact.
        # No padding table, DDL, disabled FK or disabled triggers.
        offset = 100000
        page_size = c.execute('PRAGMA page_size').fetchone()[0]
        while c.execute('PRAGMA page_count').fetchone()[0]*page_size < 515*1024*1024:
            insert('minute_metrics',metrics(10000, offset))
            offset += 250
        c.execute('DELETE FROM minute_metrics WHERE bucket < ?', (anchor_ms-100000*60000,))
        c.commit()


def checkpoint(c):
    if c.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone()[0] != 0:
        raise ValueError('fixture checkpoint busy')


def audit(path, schema):
    uri = Path(path).resolve().as_uri() + '?mode=ro'
    with closing(sqlite3.connect(uri, uri=True)) as c:
        c.execute('PRAGMA query_only=ON')
        if c.execute('SELECT max(version) FROM schema_migrations').fetchone()[0] != schema:
            raise ValueError('fixture schema differs from registry')
        validate(c)
        tables = c.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*'").fetchall()
        rows = {t: c.execute('SELECT count(*) FROM "'+t.replace('"','""')+'"').fetchone()[0] for t, in tables}
        empty = sorted(t for t, count in rows.items() if count == 0 and t not in EMPTY_TABLES)
        if empty:
            raise ValueError('unexpected empty fixture tables: ' + ', '.join(empty))
        if c.execute('SELECT count(*) FROM quality_config WHERE slow_threshold_ms NOT BETWEEN 50 AND 3000').fetchone()[0]:
            raise ValueError('invalid slow threshold')
        for agent, start, mode, usage, rx, tx, basis, since in c.execute("""SELECT agent_id,cycle_start,
                COALESCE(traffic_mode,''),usage_bytes,cycle_rx_bytes,cycle_tx_bytes,cycle_dir_basis,cycle_dir_since
                FROM agent_plans"""):
            billed = {'rx': rx, 'tx': tx, 'sum': rx+tx, '': 0}.get(mode)
            if billed is None or min(usage, rx, tx) < 0:
                raise ValueError('invalid plan counters')
            if basis in ('exact', 'backfill'):
                if billed != usage:
                    raise ValueError('direction counters differ from billed usage')
            elif basis == 'since':
                if billed > usage:
                    raise ValueError('partial counters exceed billed usage')
            else:
                raise ValueError('invalid plan counter basis')
            if (since is None) != (basis == 'exact'):
                raise ValueError('invalid plan counter since timestamp')
            first = c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<=? '
                              'ORDER BY bucket DESC LIMIT 1', (agent, start)).fetchone()
            last = c.execute('SELECT rx_total,tx_total FROM minute_metrics WHERE agent_id=? '
                             'ORDER BY bucket DESC LIMIT 1', (agent,)).fetchone()
            if first and last:
                history_rx, history_tx = last[0]-first[0], last[1]-first[1]
                history_usage = {'rx': history_rx, 'tx': history_tx, 'sum': history_rx+history_tx, '': 0}[mode]
                if min(history_rx, history_tx) < 0 or history_usage != usage:
                    raise ValueError('plan counters differ from minute history')
                if basis == 'exact' and mode and (rx, tx) != (history_rx, history_tx):
                    raise ValueError('exact plan direction counters differ from minute history')
        return dict(rows=rows, schema_sha256=fingerprint(c),
                    page_size=c.execute('PRAGMA page_size').fetchone()[0],
                    page_count=c.execute('PRAGMA page_count').fetchone()[0],
                    freelist_count=c.execute('PRAGMA freelist_count').fetchone()[0])


def official_database(server, operation, output, schema):
    begin = time.monotonic()
    result = json.loads(run(server, 'database', operation, '--db', output))
    if result.get('integrity') != 'ok' or result.get('schema_version') != schema:
        raise ValueError(f'official database {operation} did not verify schema {schema}')
    return result, round(time.monotonic()-begin, 3)


def generate(server, output, scale, seed=31, anchor_ms=NOW, start_version=releases.BASE_VERSION, base_assets=None):
    started = time.monotonic()
    entry = releases.registered(start_version)  # reject unknown versions before any write
    server, output = Path(server).resolve(strict=True), Path(output).absolute()
    arch = server.name.removeprefix('404-probe-server-linux-')
    base_directory = base_assets if base_assets is not None else (
        server.parent if start_version == releases.BASE_VERSION else None)
    if base_directory is None:
        raise ValueError('--base-assets (official v1.2.0) required for later starting versions')
    (_, base_server, _), (_, official, identity), migration_assets = releases.generation_assets(
        server.parent, start_version, arch, base_directory)
    if official != server:
        raise ValueError('Server must be the named official asset')
    evidence = Path(str(output)+'.json')
    base_evidence = Path(str(output)+'.schema37.json')
    for sidecar in (evidence, base_evidence):
        if sidecar.exists() or sidecar.is_symlink():
            raise ValueError('fixture evidence must not exist')
    base = generate_base(base_server, output, scale, seed, anchor_ms)
    with base_evidence.open('x', encoding='utf-8', newline='\n') as stream:
        stream.write(json.dumps(base, indent=2)+'\n')
    steps = []
    prior_version = releases.BASE_VERSION
    prior_schema = releases.BASE_SCHEMA
    stats = ('rows', 'schema_sha256', 'page_size', 'page_count', 'freelist_count')
    after = {key: base[key] for key in stats}
    verified = base['verify']
    for step_version, _, step_server, step_identity in migration_assets:
        step_schema = releases.registered(step_version)['schema']
        migrated, migration_seconds = official_database(step_server, 'migrate-protected', output, step_schema)
        verified, verify_seconds = official_database(step_server, 'verify', output, step_schema)
        with closing(sqlite3.connect(output)) as c:
            checkpoint(c)
        begin = time.monotonic()
        step_audit = audit(output, step_schema)
        releases.check_counts(after['rows'], step_audit['rows'], prior_version, step_version)
        if prior_schema == step_schema and step_audit['schema_sha256'] != after['schema_sha256']:
            raise ValueError('official migration changed the schema fingerprint without a schema version change')
        steps.append(dict(from_version=prior_version, to_version=step_version, server=step_identity,
                          asset_sha256=releases.digest(step_server), schema=step_schema,
                          audit=step_audit, rows_checked=True, audit_seconds=round(time.monotonic()-begin,3),
                          migration=migrated, migration_seconds=migration_seconds,
                          verification=verified, verify_seconds=verify_seconds))
        prior_version, prior_schema, after = step_version, step_schema, step_audit
    if prior_version != start_version or prior_schema != entry['schema']:
        raise ValueError('generation did not reach the starting release')
    size = output.stat().st_size
    if scale in ('snapshot', 'full') and size < 500_000_000:
        raise ValueError(f'{scale} fixture below 500,000,000 bytes: {size}')
    if scale == 'compact' and after['freelist_count'] != 0:
        raise ValueError('compact fixture contains free pages')
    result = dict(scale=scale, seed=seed, anchor_ms=anchor_ms, base_version=releases.BASE_VERSION,
                  starting_version=start_version, server=identity, base_generation=base,
                  official_migrations=steps, verify=verified, file_bytes=size,
                  integrity='ok', foreign_keys='ok', counters='ok', rows_checked=True,
                  elapsed_seconds=round(time.monotonic()-started,3), **after)
    with evidence.open('x', encoding='utf-8', newline='\n') as stream:
        stream.write(json.dumps(result, indent=2)+'\n')
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--server',required=True,type=Path)
    p.add_argument('--start-version',default=releases.BASE_VERSION,help='registered starting release')
    p.add_argument('--base-assets',type=Path,help='official v1.2.0 assets for the schema 37 base')
    p.add_argument('--out',required=True,type=Path)
    p.add_argument('--scale',choices=['tiny','snapshot','full','compact'],required=True,
                   help='snapshot/full require 500,000,000 bytes; compact requires no free pages, with no size floor')
    p.add_argument('--seed',type=int,default=31)
    p.add_argument('--anchor-ms',type=int,default=NOW,help='UTC window end; fixed by default, use current minute for live upgrade rehearsal')
    a=p.parse_args()
    print(json.dumps(generate(a.server.absolute(),a.out,a.scale,a.seed,a.anchor_ms,a.start_version,a.base_assets),indent=2))

if __name__ == '__main__':
    main()
