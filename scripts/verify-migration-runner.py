"""Static verifier embedded in install.sh; never executes the downloaded runner."""
import hashlib
import json
import re
import struct
import sys
from pathlib import Path


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'duplicate metadata field')
        result[key] = value
    return result


def verify(binary, metadata, sums, arch):
    require(arch in ('amd64', 'arm64'), 'unsupported architecture')
    require(0 < len(binary) <= 128 << 20 and len(metadata) <= 256 << 10 and len(sums) <= 256 << 10, 'asset size limit')
    doc = json.loads(metadata, object_pairs_hook=unique_object)
    require(set(doc) == {'schema_version', 'version', 'commit', 'assets'}, 'unexpected schema1 fields')
    require(type(doc['schema_version']) is int and doc['schema_version'] == 1 and doc['version'] == 'v1.0.1', 'require v1.0.1 schema1')
    commit = doc['commit']
    require(isinstance(commit, str) and re.fullmatch('[0-9a-f]{40}', commit), 'invalid commit')
    expected = {'404-probe-' + role + '-linux-' + cpu: cpu for role in ('agent', 'server') for cpu in ('amd64', 'arm64')}
    require(isinstance(doc['assets'], list) and len(doc['assets']) == 4, 'require four binary assets')
    assets = {}
    for asset in doc['assets']:
        require(isinstance(asset, dict) and set(asset) == {'name', 'goos', 'goarch', 'sha256'}, 'unexpected asset fields')
        name = asset['name']
        require(name in expected and name not in assets and asset['goos'] == 'linux' and asset['goarch'] == expected[name], 'invalid asset identity')
        require(isinstance(asset['sha256'], str) and re.fullmatch('[0-9a-f]{64}', asset['sha256']), 'invalid asset hash')
        assets[name] = asset['sha256']
    manifest = {}
    for line in sums.decode('ascii').splitlines():
        match = re.fullmatch('([0-9a-f]{64})  ([A-Za-z0-9.-]+)', line)
        require(match is not None, 'invalid checksum line')
        digest, name = match.groups()
        require(name not in manifest and name in set(expected) | {'install.sh'}, 'unknown or duplicate checksum')
        manifest[name] = digest
    require(set(manifest) == set(expected) | {'install.sh'}, 'require schema1 five checksums')
    require(all(manifest[name] == digest for name, digest in assets.items()), 'metadata checksum mismatch')
    name = '404-probe-agent-linux-' + arch
    require(hashlib.sha256(binary).hexdigest() == assets[name], 'binary checksum mismatch')
    require(len(binary) >= 64 and binary[:7] == b'\x7fELF\x02\x01\x01', 'require ELF64 little endian')
    h = struct.unpack_from('<HHIQQQIHHHHHH', binary, 16)
    require(h[0] == 2 and h[1] == {'amd64': 62, 'arm64': 183}[arch] and h[8] == 56 and h[10] == 64, 'unsupported ELF layout')
    phoff, shoff, phnum, shnum, namesidx = h[4], h[5], h[9], h[11], h[12]
    require(0 < shnum <= 2048 and 0 < phnum <= 2048 and namesidx < shnum, 'invalid ELF table counts')
    require(shoff <= len(binary) and shnum <= (len(binary) - shoff) // 64 and phoff <= len(binary) and phnum <= (len(binary) - phoff) // 56, 'ELF tables outside file')
    sections = [struct.unpack_from('<IIQQQQIIQQ', binary, shoff + i * 64) for i in range(shnum)]
    programs = [struct.unpack_from('<IIQQQQQQ', binary, phoff + i * 56) for i in range(phnum)]
    for s in sections:
        require(s[3] + s[5] < 1 << 64 and s[4] <= len(binary) and (s[1] == 8 or s[5] <= len(binary) - s[4]), 'invalid ELF section bounds')

    def data(s):
        require(s[1] != 8 and not s[2] & 2048, 'unsupported section encoding')
        return binary[s[4]:s[4] + s[5]]

    def text(table, offset):
        require(offset < len(table), 'invalid string offset')
        end = table.find(b'\0', offset)
        require(end >= 0, 'unterminated string')
        return table[offset:end].decode('ascii')

    section_names = data(sections[namesidx])
    names = [text(section_names, s[0]) for s in sections]
    tables = [s for s in sections if s[1] == 2]
    require(len(tables) == 1, 'require retained symbol table')
    table = tables[0]
    require(table[5] <= 16 << 20 and table[9] == 24 and table[5] % 24 == 0 and table[6] < shnum, 'invalid symbol table')
    linked = sections[table[6]]
    require(linked[1] == 3 and linked[5] <= 16 << 20, 'invalid symbol names')
    strings, symbols = data(linked), {}
    raw = data(table)
    for offset in range(0, len(raw), 24):
        sym = struct.unpack_from('<IBBHQQ', raw, offset)
        nm = text(strings, sym[0])
        if nm in ('404-probe/internal/buildinfo.Version', '404-probe/internal/buildinfo.Commit'):
            require(nm not in symbols, 'ambiguous release symbol')
            symbols[nm] = sym

    def mapped(s, address, length, flags):
        matches = [p for p in programs if p[0] == 1 and address >= p[3] and address - p[3] <= p[5] and length <= p[5] - (address - p[3])]
        require(len(matches) == 1 and matches[0][1] == flags and matches[0][2] + address - matches[0][3] == s[4] + address - s[3], 'invalid ELF load mapping')

    def release_string(suffix):
        sym = symbols.get('404-probe/internal/buildinfo.' + suffix)
        require(sym is not None, 'missing release symbol')
        _, info, other, index, value, size = sym
        require(info == 17 and other == 0 and index < shnum and size == 16 and value % 8 == 0, 'invalid Go string symbol')
        s = sections[index]
        require(names[index] == '.data' and s[1] == 1 and s[2] & 3 == 3 and not s[2] & 4 and value >= s[3] and value - s[3] <= s[5] and 16 <= s[5] - (value - s[3]), 'invalid Go string header')
        mapped(s, value, 16, 6)
        pointer, length = struct.unpack_from('<QQ', data(s), value - s[3])
        require(0 < length <= 64, 'invalid Go string length')
        matches = [s for s in sections if s[1] == 1 and s[2] & 2 and not s[2] & 5 and pointer >= s[3] and pointer - s[3] <= s[5] and length <= s[5] - (pointer - s[3])]
        require(len(matches) == 1, 'invalid read-only release string')
        s = matches[0]
        mapped(s, pointer, length, 4)
        return data(s)[pointer - s[3]:pointer - s[3] + length].decode('ascii')

    require(release_string('Version') == 'v1.0.1' and release_string('Commit') == commit, 'linked Version/Commit mismatch')
    build_sections = [s for i, s in enumerate(sections) if names[i] == '.go.buildinfo']
    require(len(build_sections) == 1, 'require unique Go build info')
    raw = data(build_sections[0])
    require(len(raw) >= 32 and raw[:14] == b'\xff Go buildinf:' and raw[14] == 8 and raw[15] == 2, 'unsupported Go build info')

    def read_string(offset):
        length = 0
        for shift in range(0, 70, 7):
            require(offset < len(raw), 'truncated Go build info')
            byte = raw[offset]
            offset += 1
            require(shift < 63 or byte <= 1, 'Go build info overflow')
            length |= (byte & 127) << shift
            if byte < 128:
                require(length <= len(raw) - offset, 'Go build info outside section')
                return raw[offset:offset + length], offset + length
        raise ValueError('invalid Go build info length')

    _, offset = read_string(32)
    module, _ = read_string(offset)
    require(len(module) >= 32 and module[-17] == 10, 'invalid Go module framing')
    settings, paths = {}, []
    for line in module[16:-16].decode('utf-8').splitlines():
        if line.startswith('path\t'):
            paths.append(line[5:])
        elif line.startswith('build\t'):
            pair = line[6:].split('=', 1)
            require(len(pair) == 2 and pair[0] not in settings, 'ambiguous Go setting')
            settings[pair[0]] = pair[1]
    require(paths == ['404-probe/cmd/agent'] and settings.get('GOOS') == 'linux' and settings.get('GOARCH') == arch and settings.get('vcs.revision') == commit and settings.get('vcs.modified') == 'false', 'Go path/platform/VCS proof failed')
    return commit


if __name__ == '__main__':
    try:
        files = [Path(p) for p in sys.argv[1:4]]
        require(len(files) == 3 and len(sys.argv) == 5, 'expected binary metadata sums architecture')
        for p, limit in zip(files, (128 << 20, 256 << 10, 256 << 10)):
            require(p.is_file() and not p.is_symlink() and 0 < p.stat().st_size <= limit, 'invalid input file')
        commit = verify(*(p.read_bytes() for p in files), sys.argv[4])
        print('Static migration runner verified: v1.0.1 ' + commit)
    except (ValueError, TypeError, KeyError, UnicodeError, OSError, struct.error) as error:
        print('Static migration runner rejected: ' + str(error), file=sys.stderr)
        sys.exit(1)
