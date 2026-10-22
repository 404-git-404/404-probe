#!/usr/bin/env python3
"""R05: snapshots of the Agent hosts before and after a Web upgrade, and the difference between them.

  agent-state-snapshot.py collect --label before --out DIR HOST [HOST ...]
  agent-state-snapshot.py compare DIR before after [HOST ...]

collect runs scripts/agent-state-snapshot.sh on each host over ssh (read only: it changes nothing on the host and prints no secret)
and writes DIR/<label>-<host>.json. DIR must be outside the repository: a snapshot names machines and settings.

compare reads the two snapshots of every host and prints what differs. A difference in the version, the binary hash or the process
(keys "agent.version", "binary.agent.sha256", "runtime.*") is what an upgrade is for. Anything else - unit file, sandbox, setting,
file mode, a service that no longer runs - is not, and makes the exit status 1.
"""
import argparse
import json
import os
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


def collect_host(host, runner=subprocess.run):
    with open(REMOTE_SCRIPT, "rb") as handle:
        script = handle.read()
    done = runner(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20", host, "sh", "-s"], input=script, capture_output=True, timeout=120)
    if done.returncode != 0:
        raise RuntimeError("%s: ssh exit %d: %s" % (host, done.returncode, done.stderr.decode(errors="replace").strip()[:200]))
    facts = parse(done.stdout.decode(errors="replace"))
    if "host.init" not in facts:
        raise RuntimeError("%s: the host did not answer like a snapshot" % host)
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
        failed = 0
        for host in args.hosts:
            try:
                path = write_snapshot(args.out, args.label, host, collect_host(host, runner))
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
