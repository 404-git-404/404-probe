#!/usr/bin/env python3
"""Install or upgrade a release on a locally configured test machine."""
import argparse
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile

sys.dont_write_bytecode = True
from release import ASSETS, TEXT_ASSETS, sha256
from release_prepare import version_key


PASSWORD = "/root/404-probe-test-web-password"
SSH = ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15"]
REMOTE = Path(__file__).with_name("release_test_remote.py")


def config(key, pattern):
    name = "PROBE_404_TEST_" + key
    value = os.environ.get(name, "")
    if not re.fullmatch(pattern, value):
        raise ValueError(f"Set {name} in local configuration")
    return value


def ssh(host, command, label, *, data=None, stream=None, timeout=2700):
    # Captured output can include credentials or local test names. Print only
    # the fixed stage label and an exit status, including on failure.
    result = subprocess.run([*SSH, host, shlex.join(command)], input=data, stdin=stream,
                            capture_output=True, timeout=timeout)
    if result.returncode:
        raise ValueError(f"{label} failed (SSH exit {result.returncode})")
    return result.stdout


def remote(host, label, *args):
    # The script goes over stdin: a multi-line quoted argument breaks on the
    # Windows OpenSSH command line ("unexpected EOF while looking for matching").
    return ssh(host, ["python3", "-I", "-", *args], label, data=REMOTE.read_bytes())


def remote_directory(host):
    name = ssh(host, ["mktemp", "-d", "/var/lib/probe-release.XXXXXXXX"], "Allocate test staging").decode().strip()
    if not re.fullmatch(r"/var/lib/probe-release\.[A-Za-z0-9]{8}", name):
        raise ValueError("Unrecognized remote staging directory")
    return name


def cleanup(host, directory):
    # Windows alias/config and remotely chosen paths never enter a shell
    # expression. The directory was just allocated by remote_directory().
    ssh(host, ["rm", "-rf", "--", directory], "Remove test staging", timeout=30)


def verify_assets(path):
    expected = ASSETS | TEXT_ASSETS
    if {entry.name for entry in path.iterdir()} != expected:
        raise ValueError("Expected exactly seven release assets")
    for name in expected:
        if not (path / name).is_file() or (path / name).is_symlink():
            raise ValueError(f"Expected a regular local asset: {name}")
    hashes = {name: sha256(path / name) for name in expected - {"SHA256SUMS"}}
    manifest = (path / "SHA256SUMS").read_bytes().splitlines(keepends=True)
    if sorted(manifest) != sorted(f"{digest}  {name}\n".encode() for name, digest in hashes.items()):
        raise ValueError("The local SHA256SUMS does not match all six inputs")
    metadata = json.loads((path / "RELEASE-METADATA.json").read_bytes())
    version_key(metadata["version"])
    if not re.fullmatch(r"[0-9a-f]{40}", metadata["commit"]):
        raise ValueError("Missing candidate commit")
    if sorted((row["name"], row["sha256"]) for row in metadata["assets"]) != sorted(
            (name, hashes[name]) for name in ASSETS):
        raise ValueError("Release metadata and binary digests disagree")
    return metadata


def install(args, metadata, server, host):
    version, commit = metadata["version"], metadata["commit"]
    remote(host, "Check test installation", "preflight", args.role, args.mode, args.from_version or "")
    directory = remote_directory(host)
    token_directory = None
    try:
        with tempfile.TemporaryFile() as archive:
            with tarfile.open(fileobj=archive, mode="w") as tar:
                for name in sorted(ASSETS | TEXT_ASSETS):
                    tar.add(args.assets / name, arcname=name, recursive=False)
            archive.seek(0)
            ssh(host, ["tar", "--no-same-owner", "-xf", "-", "-C", directory],
                "Upload verified test assets", stream=archive)
        remote(host, "Verify uploaded candidate", "verify", directory, version, commit)
        origin, name = "", ""
        if args.role == "agent":
            origin = config("AGENT_ORIGIN", r"https://[^/\s]+|http://127\.0\.0\.1(?::[0-9]+)?")
            name = config("AGENT_NAME", r"[\w][\w .-]{0,62}")
            token_directory = remote_directory(server)
            token_file = token_directory + "/token"
            ssh(server, ["/usr/local/sbin/404-probe-install", "oneclick-token", name,
                         "--output", token_file], "Create one-time test enrollment token")
            token = ssh(server, ["cat", "--", token_file], "Read one-time test token")
            if not re.fullmatch(rb"[A-Za-z0-9_-]{43}\n?", token):
                raise ValueError("Unexpected one-time test token format")
            ssh(host, ["sh", "-c", 'umask 077; set -C; cat >"$1"', "token",
                       directory + "/token"], "Write test token in private staging", data=token)
            del token
        remote(host, "Install and verify test candidate", "install", args.role, args.mode, directory,
               version, commit, args.from_version or "", origin, name)
    finally:
        try:
            cleanup(host, directory)
        finally:
            if token_directory:
                cleanup(server, token_directory)


def upgrade_agent(args, metadata, server, host):
    version, commit = metadata["version"], metadata["commit"]
    agent_id = remote(host, "Identify the configured test Agent", "agent-id", args.from_version).decode().strip()
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", agent_id):
        raise ValueError("The configured test Agent did not return a valid ID")
    remote(server, "Run test Web upgrade", "web-upgrade", version, commit, agent_id)
    remote(host, "Verify installed test Agent build", "installed", "agent", version, commit,
           *(next(row["sha256"] for row in metadata["assets"]
                  if row["name"] == f"404-probe-agent-linux-{arch}") for arch in ("amd64", "arm64")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("role", choices=("server", "agent"))
    parser.add_argument("mode", choices=("fresh", "upgrade"))
    parser.add_argument("--assets", required=True, type=Path)
    parser.add_argument("--from-version", help="Required installed version for an upgrade")
    parser.add_argument("--dry-run", action="store_true", help="Verify local assets and print the plan without SSH")
    args = parser.parse_args()
    try:
        metadata = verify_assets(args.assets)
        if args.mode == "upgrade":
            if not args.from_version or version_key(args.from_version) >= version_key(metadata["version"]):
                raise ValueError("An upgrade needs --from-version older than the candidate")
        elif args.from_version:
            raise ValueError("--from-version only applies to an upgrade")
        if args.dry_run:
            compile(REMOTE.read_bytes(), REMOTE.name, "exec")
            steps = {
                ("server", "fresh"): "check empty test Server; upload and verify seven files; "
                                     "install with random password in a root-only 0600 file",
                ("server", "upgrade"): "check installed test Server version and backup rotation; "
                                       "upload and verify seven files; confirm protected upgrade",
                ("agent", "fresh"): "check empty test Agent; upload and verify seven files; use "
                                    "one-time 0600 token files, then remove both",
                ("agent", "upgrade"): "identify the test Agent; match the Server to the candidate; "
                                      "run the Web upgrade and confirm the installed Agent digest",
            }
            print(f"DRY RUN {args.role} {args.mode} → {metadata['version']}: {steps[args.role, args.mode]}")
            return 0
        server = config("SERVER_SSH", r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}")
        host = server if args.role == "server" else config("AGENT_SSH", r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}")
        if args.role == "agent" and host == server:
            raise ValueError("Use separate configured test Server and Agent aliases")
        if (args.role, args.mode) == ("agent", "upgrade"):
            upgrade_agent(args, metadata, server, host)
        else:
            install(args, metadata, server, host)
        print(f"Test {args.role} {args.mode} verified: {metadata['version']}")
        return 0
    except (OSError, ValueError, KeyError, TimeoutError, subprocess.TimeoutExpired):
        error = sys.exc_info()[1]
        # TimeoutExpired carries the SSH argv. Do not include local configuration in logs.
        print("Test helper timed out" if isinstance(error, subprocess.TimeoutExpired)
              else f"Test helper failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
