"""R31 release registry and local official-asset validation; standard library only."""
import hashlib
import json
from pathlib import Path
import re
import subprocess

REGISTRY = Path(__file__).with_name('r31-versions.json')
BINARIES = {f'404-probe-{role}-linux-{arch}'
            for role in ('agent', 'server') for arch in ('amd64', 'arm64')}
PAYLOADS = BINARIES | {'install.sh', 'RELEASE-METADATA.json'}


def version_tuple(version):
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
        raise ValueError('only canonical stable vX.Y.Z versions are accepted')
    return tuple(map(int, version[1:].split('.')))


def registry():
    doc = json.loads(REGISTRY.read_text(encoding='utf-8'))
    if doc.get('format') != 1 or doc.get('base_version') != 'v1.0.6':
        raise ValueError('unsupported R31 registry')
    versions = doc['versions']
    previous = None
    schema = 0
    for version in sorted(versions, key=version_tuple):
        row = versions[version]
        if not re.fullmatch('[0-9a-f]{40}', row['commit']):
            raise ValueError('invalid registered commit')
        if type(row['schema']) is not int or row['schema'] < schema:
            raise ValueError('invalid registered schema')
        if row['previous_version'] != previous:
            raise ValueError('registry must form one previous-release chain')
        for key in ('added_tables', 'removed_tables'):
            names = row[key]
            if len(names) != len(set(names)) or any(not re.fullmatch('[a-z][a-z0-9_]*', n) for n in names):
                raise ValueError('invalid registered table changes')
        if set(row['added_tables']) & set(row['removed_tables']):
            raise ValueError('overlapping registered table changes')
        previous, schema = version, row['schema']
    if 'v1.0.6' not in versions:
        raise ValueError('missing schema34 base')
    return doc


def registered(version):
    version_tuple(version)
    try:
        return registry()['versions'][version]
    except KeyError:
        raise ValueError('unregistered starting version: ' + version) from None


def expected_schema(version, explicit=None):
    version_tuple(version)
    row = registry()['versions'].get(version)
    if row is None and explicit is None:
        raise ValueError('unregistered target requires --expected-schema')
    if explicit is not None and (type(explicit) is not int or explicit < 1):
        raise ValueError('expected schema must be a positive integer')
    if row is not None and explicit is not None and explicit != row['schema']:
        raise ValueError('expected schema differs from registry')
    return row['schema'] if row else explicit


def digest(path):
    value = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def assets(directory, version, arch, require_registered=False):
    version_tuple(version)
    row = registered(version) if require_registered else registry()['versions'].get(version)
    if arch not in ('amd64', 'arm64'):
        raise ValueError('unsupported official asset architecture')
    directory = Path(directory).resolve(strict=True)
    if not directory.is_dir():
        raise ValueError('asset directory required')
    for name in PAYLOADS | {'SHA256SUMS'}:
        p = directory / name
        if p.is_symlink() or not p.is_file():
            raise ValueError('missing or unsafe asset: ' + name)
    manifest = {}
    for line in (directory / 'SHA256SUMS').read_text(encoding='utf-8').splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  ([A-Za-z0-9_.-]+)', line)
        if not match or match[2] in manifest:
            raise ValueError('invalid/duplicate local asset manifest')
        manifest[match[2]] = match[1]
    if set(manifest) != PAYLOADS:
        raise ValueError('manifest must contain exactly the six release payloads')
    for name in PAYLOADS:
        if digest(directory / name) != manifest[name]:
            raise ValueError('asset checksum mismatch: ' + name)
    for name in ('install.sh', 'RELEASE-METADATA.json', 'SHA256SUMS'):
        if b'\r' in (directory / name).read_bytes():
            raise ValueError('CR in asset: ' + name)
    metadata = json.loads((directory / 'RELEASE-METADATA.json').read_text(encoding='utf-8'))
    commit = metadata.get('commit', '')
    if metadata.get('version') != version or not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('release metadata identity mismatch')
    if row is not None and commit != row['commit']:
        raise ValueError('release commit differs from registry')
    items = metadata.get('assets', [])
    if len(items) != 4 or {item.get('name') for item in items} != BINARIES:
        raise ValueError('release metadata asset list mismatch')
    for item in items:
        if item.get('sha256') != manifest[item['name']]:
            raise ValueError('metadata/manifest asset identity mismatch')
    server = directory / f'404-probe-server-linux-{arch}'
    identity = json.loads(subprocess.check_output([str(server), 'version', '--json']))
    if (identity.get('version') != version or identity.get('commit') != commit
            or identity.get('dirty') is not False):
        raise ValueError('Server/metadata/registry identity mismatch')
    return directory, server, identity


def check_counts(before, after, start_version, end_version):
    rows = registry()['versions']
    start, end = registered(start_version), registered(end_version)
    if version_tuple(start_version) > version_tuple(end_version):
        raise ValueError('backward fixture migration')
    expected_tables = set(before)
    introduced = set()
    for version in sorted(rows, key=version_tuple):
        if version_tuple(start_version) < version_tuple(version) <= version_tuple(end_version):
            change = rows[version]
            removed, added = set(change['removed_tables']), set(change['added_tables'])
            if not removed <= expected_tables or added & expected_tables:
                raise ValueError('registered table transition does not fit source')
            expected_tables = (expected_tables - removed) | added
            introduced = (introduced - removed) | added
    if set(after) != expected_tables:
        raise ValueError('unregistered table additions/removals')
    for table, count in after.items():
        if table in introduced:
            continue
        expected = before[table] + (end['schema'] - start['schema'] if table == 'schema_migrations' else 0)
        if count != expected:
            raise ValueError('unexpected record count: ' + table)
