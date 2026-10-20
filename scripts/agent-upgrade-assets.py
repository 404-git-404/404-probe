#!/usr/bin/env python3
"""Offline preflight for the isolated systemd Agent upgrade rehearsal."""

import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys


BINARIES = {
    f"404-probe-{role}-linux-{arch}": (role, arch)
    for role in ("agent", "server") for arch in ("amd64", "arm64")
}
PAYLOADS = set(BINARIES) | {"install.sh", "RELEASE-METADATA.json"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def stable_version(value):
    require(len(value) <= 64 and re.fullmatch(
        r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", value),
        "versions must be canonical stable vX.Y.Z (no beta/rc)")
    parts = tuple(map(int, value[1:].split(".")))
    require(all(n <= 2**64 - 1 for n in parts), "version integer overflow")
    return parts


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate metadata key: " + key)
        result[key] = value
    return result


def inspect_binary(go_binary, path, role, arch, version, commit):
    result = subprocess.run([go_binary, "version", "-m", str(path)],
                            capture_output=True, text=True, encoding="utf-8", check=True)
    settings = {}
    module_path = None
    for line in result.stdout.splitlines():
        fields = line.strip().split("\t")
        if fields[0] == "path" and len(fields) == 2:
            module_path = fields[1]
        if fields[0] == "build" and len(fields) == 2 and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            require(key not in settings, "duplicate binary build setting")
            settings[key] = value
    require(module_path == "404-probe/cmd/" + role, "binary command mismatch: " + path.name)
    for key, expected in {"GOOS": "linux", "GOARCH": arch, "vcs.revision": commit,
                          "vcs.modified": "false"}.items():
        require(settings.get(key) == expected, "binary " + key + " mismatch: " + path.name)
    # Release builds using -trimpath may omit -ldflags from Go build settings.
    # Native executables are also checked with `version --json` by the runner.
    if "-ldflags" not in settings:
        return
    value = settings["-ldflags"]
    if value.startswith('"'):
        value = json.loads(value)
    flags = shlex.split(value)
    linked = [flags[i + 1] for i, flag in enumerate(flags[:-1]) if flag == "-X"]
    require(linked.count("404-probe/internal/buildinfo.Version=" + version) == 1,
            "binary linked version mismatch: " + path.name)
    commits = [v for v in linked if v.startswith("404-probe/internal/buildinfo.Commit=")]
    require(not commits or commits == ["404-probe/internal/buildinfo.Commit=" + commit],
            "binary linked commit mismatch: " + path.name)


def validate_release(directory, version, go_binary):
    directory = Path(directory)
    require(directory.is_dir(), "release directory is missing")
    for name in PAYLOADS | {"SHA256SUMS"}:
        path = directory / name
        require(path.is_file() and not path.is_symlink(), "missing regular release asset: " + name)
    for name in ("install.sh", "RELEASE-METADATA.json", "SHA256SUMS"):
        require(b"\r" not in (directory / name).read_bytes(), "release text contains CR: " + name)
    checksums = {}
    for line in (directory / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._-]+)", line)
        require(match is not None, "invalid SHA256SUMS line")
        digest, name = match.groups()
        require(name not in checksums, "duplicate SHA256SUMS entry")
        checksums[name] = digest
    require(set(checksums) == PAYLOADS, "SHA256SUMS must cover exactly the six release payloads")
    for name, expected in checksums.items():
        require(hashlib.sha256((directory / name).read_bytes()).hexdigest() == expected,
                "SHA256SUMS mismatch: " + name)
    metadata = json.loads((directory / "RELEASE-METADATA.json").read_text(encoding="utf-8"),
                          object_pairs_hook=unique_object)
    require(metadata.get("schema_version") == 2 and metadata.get("version") == version,
            "metadata version/schema mismatch (requires schema 2)")
    commit = metadata.get("commit", "")
    require(isinstance(commit, str) and re.fullmatch(r"[0-9a-f]{40}", commit), "invalid metadata commit")
    compatibility = metadata.get("compatibility", {})
    require(compatibility.get("upgrade_protocol") == 2, "requires upgrade protocol 2")
    require(stable_version(compatibility.get("min_server_version", "")) <= stable_version(version),
            "release requires a newer Server than the target")
    assets = metadata.get("assets", [])
    require(isinstance(assets, list) and len(assets) == 4, "metadata must describe four ELF assets")
    seen = set()
    for asset in assets:
        name = asset.get("name")
        require(name in BINARIES and name not in seen, "unexpected/duplicate metadata asset")
        seen.add(name)
        role, arch = BINARIES[name]
        require(asset.get("goos") == "linux" and asset.get("goarch") == arch
                and asset.get("sha256") == checksums[name], "metadata asset/hash mismatch: " + name)
        require((directory / name).read_bytes()[:4] == b"\x7fELF", "release binary is not ELF: " + name)
        inspect_binary(go_binary, directory / name, role, arch, version, commit)
    return commit


def validate_pair(source, target, source_directory, target_directory, go_binary):
    old, new = stable_version(source), stable_version(target)
    require(old >= (1, 0, 1), "source versions before v1.0.1 are unsupported")
    require(new > old, "target must be newer than source (no same-version upgrade or downgrade)")
    return (validate_release(source_directory, source, go_binary),
            validate_release(target_directory, target, go_binary))


if __name__ == "__main__":
    try:
        require(len(sys.argv) == 6, "usage: agent-upgrade-assets.py FROM TO FROM_ASSETS TO_ASSETS GO_BINARY")
        print(" ".join(validate_pair(*sys.argv[1:])))
    except (ValueError, TypeError, KeyError, AttributeError, OSError, subprocess.SubprocessError) as exc:
        print("FAIL: release preflight: " + str(exc), file=sys.stderr)
        sys.exit(1)
