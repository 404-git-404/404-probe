#!/usr/bin/env python3
"""R05: snapshots of the Agent hosts before and after a Web upgrade, and the difference between them.

  agent-state-snapshot.py collect --label before --out DIR HOST [HOST ...]
  agent-state-snapshot.py compare DIR before after [HOST ...]

collect runs scripts/agent-state-snapshot.sh on each host over ssh (read only: it changes nothing on the host) and writes
DIR/<label>-<host>.json. DIR must be outside the repository: a snapshot names machines and settings.

A snapshot holds no secret, nor anything a secret can be recovered from. The host script shows a value only for a name on its
allowlist; every other value is redacted or reduced to a keyed digest, sha256(KEY ":" value). KEY is 32 random bytes made once
per directory (DIR/.snapshot-key, mode 600, never committed) and handed to the host on the standard input only, so a token and a
low-entropy password are equally unguessable, and two snapshots of the same directory still show that a credential, a unit
file or a start command changed. A value shaped like a token is refused here whatever field it arrived in.

compare reads the two snapshots of every host and prints what differs. A difference in the version, the binary hash or the process
(keys "agent.version", "binary.agent.sha256", "runtime.*") is what an upgrade is for. Anything else - unit file, sandbox, setting,
file mode, a service that no longer runs - is not, and makes the exit status 1.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
REMOTE_SCRIPT = os.path.join(HERE, "agent-state-snapshot.sh")
EXPECTED = ("agent.version", "binary.agent.sha256")
EXPECTED_PREFIX = ("runtime.",)
# A service that was running must still run: its state is runtime, but a change from active to anything else is a finding.
MUST_STAY_ACTIVE = "runtime."


def outside_repository(path):
    real, repo = os.path.realpath(path), os.path.realpath(REPO)
    try:
        return os.path.commonpath([real, repo]) != repo
    except ValueError:  # another drive
        return True


def parse(text):
    facts = {}
    for line in text.splitlines():
        if "\t" in line:
            key, value = line.split("\t", 1)
            facts[key] = value
    return facts


KEY_FILE = ".snapshot-key"


def snapshot_key(directory):
    """The key of the keyed digests: random, made once per snapshot directory, kept only there (mode 600), never committed.
    The digests of one directory can be compared with each other; without the key they cannot be checked against a guess."""
    path = os.path.join(directory, KEY_FILE)
    if os.path.exists(path):
        with open(path, encoding="ascii") as handle:
            key = handle.read().strip()
        if len(key) == 64 and all(c in "0123456789abcdef" for c in key):
            return key
        raise ValueError("%s is not a snapshot key" % path)
    key = os.urandom(32).hex()
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="ascii", newline="\n") as handle:
        handle.write(key + "\n")
    return key


# Defence in depth behind the host script's allowlist: a value that looks like a token (a long run of letters of both cases,
# digits, "_" or "-") is never written, whatever field it came in. Hex (hashes, ids, keyed digests) is not that shape.
SECRET_SHAPE = re.compile(r"[A-Za-z0-9_-]{32,}")


def looks_like_secret(value):
    for run in SECRET_SHAPE.findall(value):
        if any(c.islower() for c in run) and any(c.isupper() for c in run) and any(c.isdigit() for c in run):
            return True
    return False


def check_no_secret(facts):
    for key, value in facts.items():
        if looks_like_secret(value):
            raise RuntimeError("the value of %s looks like a credential; nothing is written" % key)


def collect_host(host, key, runner=subprocess.run):
    with open(REMOTE_SCRIPT, "rb") as handle:
        script = handle.read()
    # The key goes in on the standard input, ahead of the script: not in an argument (ps), not in a file on the host.
    stdin = ("SNAPSHOT_KEY=%s\nexport SNAPSHOT_KEY\n" % key).encode("ascii") + script
    done = runner(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20", host, "sh", "-s"], input=stdin, capture_output=True, timeout=120)
    if done.returncode != 0:
        raise RuntimeError("%s: ssh exit %d: %s" % (host, done.returncode, done.stderr.decode(errors="replace").strip()[:200]))
    facts = parse(done.stdout.decode(errors="replace"))
    if "host.init" not in facts:
        raise RuntimeError("%s: the host did not answer like a snapshot" % host)
    check_no_secret(facts)
    return facts


def write_snapshot(out, label, host, facts):
    path = os.path.join(out, "%s-%s.json" % (label, host))
    if os.path.exists(path):
        raise FileExistsError("%s exists; a snapshot is never overwritten" % path)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        json.dump({"host": host, "label": label, "taken_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "facts": facts}, handle, indent=2, sort_keys=True, ensure_ascii=False)
        handle.write("\n")
    return path


def classify(key, before, after):
    """'expected' for what an upgrade changes, 'unexpected' for the rest, None when nothing changed."""
    if before == after:
        return None
    if key in EXPECTED:
        return "expected"
    if key.startswith(EXPECTED_PREFIX):
        # The process and its restarts are expected to change; a service that stopped running is not.
        if key.endswith(".active") or key.endswith(".status"):
            return "unexpected"
        return "expected"
    return "unexpected"


def compare_facts(before, after):
    rows = []
    for key in sorted(set(before) | set(after)):
        b, a = before.get(key), after.get(key)
        kind = classify(key, b, a)
        if kind:
            rows.append((kind, key, b if b is not None else "<absent>", a if a is not None else "<absent>"))
    return rows


def load(directory, label, host):
    with open(os.path.join(directory, "%s-%s.json" % (label, host)), encoding="utf-8") as handle:
        return json.load(handle)["facts"]


def main(argv=None, runner=subprocess.run):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    c = sub.add_parser("collect")
    c.add_argument("--label", required=True)
    c.add_argument("--out", required=True)
    c.add_argument("hosts", nargs="+")
    d = sub.add_parser("compare")
    d.add_argument("directory")
    d.add_argument("before")
    d.add_argument("after")
    d.add_argument("hosts", nargs="*")
    args = parser.parse_args(argv)
    if args.command == "collect":
        if not outside_repository(args.out):
            print("--out must be outside the repository", file=sys.stderr)
            return 2
        os.makedirs(args.out, exist_ok=True)
        key = snapshot_key(args.out)
        failed = 0
        for host in args.hosts:
            try:
                path = write_snapshot(args.out, args.label, host, collect_host(host, key, runner))
                print("%s: %s" % (host, path))
            except Exception as error:  # one unreachable host must not hide the others
                failed += 1
                print("%s: FAILED %s" % (host, error), file=sys.stderr)
        return 1 if failed else 0
    hosts = args.hosts or sorted({name[len(args.before) + 1:-5] for name in os.listdir(args.directory) if name.startswith(args.before + "-") and name.endswith(".json")})
    if not hosts:
        print("no snapshots named %s in %s" % (args.before, args.directory), file=sys.stderr)
        return 2
    unexpected = 0
    for host in hosts:
        rows = compare_facts(load(args.directory, args.before, host), load(args.directory, args.after, host))
        print("== %s: %d differences (%d unexpected)" % (host, len(rows), sum(1 for r in rows if r[0] == "unexpected")))
        for kind, key, b, a in rows:
            print("  %-10s %s\n             before: %s\n             after:  %s" % (kind, key, b, a))
            unexpected += kind == "unexpected"
    print("RESULT: %s" % ("DIFFERENCES THAT NO UPGRADE SHOULD MAKE: %d" % unexpected if unexpected else "only what an upgrade changes"))
    return 1 if unexpected else 0


if __name__ == "__main__":
    sys.exit(main())
