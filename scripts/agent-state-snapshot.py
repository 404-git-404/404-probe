#!/usr/bin/env python3
"""R05: snapshots of the Agent hosts before and after a Web upgrade, and the difference between them.

  agent-state-snapshot.py collect --label before --out DIR HOST [HOST ...]
  agent-state-snapshot.py compare DIR before after [HOST ...]

collect runs scripts/agent-state-snapshot.sh on each host over ssh (read only: it changes nothing on the host) and writes
DIR/<label>-<host>.json. DIR must be outside the repository: a snapshot names machines and settings.

A snapshot holds no secret, nor anything a secret can be recovered from. The host script shows a value only for a name on its
allowlist; every other value is redacted or reduced to a keyed digest, sha256(KEY ":" value). KEY is 32 random bytes made once
per directory (DIR/.snapshot-key, private to the current user from the moment it exists - mode 600, or an ACL with that user alone on Windows -
checked after it is written, and refused when another user can read it; never committed) and handed to the host on the standard input only, so a token and a
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
import stat
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


class KeyPermissionError(RuntimeError):
    """The key file is, or cannot be confirmed to be, private to the current user: nothing is collected with it."""


def system_tool(name):
    """A Windows system tool by its full path: the PATH of a shell can put a GNU tool of the same name first."""
    if os.name == "nt":
        return os.path.join(os.environ.get("SystemRoot", r"C:\Windows"), "System32", name + ".exe")
    return name


def decode_console(data):
    try:
        return data.decode("oem", errors="replace")
    except LookupError:
        return data.decode("utf-8", errors="replace")


def windows_user(runner=subprocess.run):
    """('DOMAIN\\name', 'S-1-5-...') of the user who runs this."""
    done = runner([system_tool("whoami"), "/user", "/fo", "csv", "/nh"], capture_output=True, timeout=30)
    if done.returncode != 0:
        raise KeyPermissionError("whoami failed: cannot tell whose key this is")
    fields = [field.strip().strip('"') for field in decode_console(done.stdout).strip().split('","')]
    if len(fields) != 2 or not re.fullmatch(r"S-1-[0-9-]+", fields[1].strip('"')):
        raise KeyPermissionError("whoami did not answer with a user and a SID")
    return fields[0].strip('"'), fields[1].strip('"')


ACE = re.compile(r"^\s*(.+?):((?:\([^()]*\))+)\s*$")


def acl_is_private(text, path, user):
    """The output of icacls for one file: true only when the access list holds nothing but entries of this user, none of them
    inherited. The summary line after the blank line is in the language of the machine and is not read."""
    entries = 0
    for index, line in enumerate(text.splitlines()):
        if not line.strip():
            break
        found = ACE.match(line)
        if not found:
            return False
        principal = found.group(1).strip()
        if index == 0:
            # the first line starts with the path (as it was passed, spaces and drive letter included)
            if not principal.casefold().endswith(user.casefold()) or (len(principal) > len(user) and principal[-len(user) - 1] != " "):
                return False
        elif principal.casefold() != user.casefold():
            return False
        if "I" in re.findall(r"\(([^()]*)\)", found.group(2)):
            return False
        entries += 1
    return entries > 0


def _windows(windows):
    return os.name == "nt" if windows is None else windows


def key_is_private(path, runner=subprocess.run, windows=None):
    """Only the user who owns the file can reach it. False when that cannot be confirmed."""
    try:
        if _windows(windows):
            user, _ = windows_user(runner)
            done = runner([system_tool("icacls"), path], capture_output=True, timeout=30)
            return done.returncode == 0 and acl_is_private(decode_console(done.stdout), path, user)
        info = os.lstat(path)
        return stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) & 0o077 == 0 and info.st_uid == os.geteuid()
    except (OSError, KeyPermissionError):
        return False


def make_private(path, runner=subprocess.run, windows=None):
    """Remove every other right on the file and confirm it; raises KeyPermissionError when that cannot be confirmed."""
    try:
        if _windows(windows):
            _, sid = windows_user(runner)
            # /reset drops explicit entries of others (grant:r only replaces the entries of the user it names), then the inherited
            # ones go and the user alone is granted.
            for arguments in (["/reset"], ["/inheritance:r", "/grant:r", "*%s:(R,W)" % sid]):
                done = runner([system_tool("icacls"), path] + arguments, capture_output=True, timeout=30)
                if done.returncode != 0:
                    raise KeyPermissionError("icacls could not restrict %s" % path)
        else:
            os.chmod(path, 0o600)
    except OSError as error:
        raise KeyPermissionError("cannot restrict %s: %s" % (path, error))
    if not key_is_private(path, runner, windows):
        raise KeyPermissionError("%s is not private to the current user after restricting it" % path)


def snapshot_key(directory, runner=subprocess.run, windows=None):
    """The key of the keyed digests: random, made once per snapshot directory, kept only there, never committed. The file is
    private to the current user from before the key is written to it (an empty file is made, restricted and checked first), and a
    key that others can read - one that was made by an older version, or a copied file - is refused, not used.
    The digests of one directory can be compared with each other; without the key they cannot be checked against a guess."""
    path = os.path.join(directory, KEY_FILE)
    if os.path.lexists(path):
        if not key_is_private(path, runner, windows):
            raise KeyPermissionError("%s can be read by others (or its permissions cannot be confirmed); restrict it or remove it" % path)
        with open(path, encoding="ascii") as handle:
            key = handle.read().strip()
        if len(key) == 64 and all(c in "0123456789abcdef" for c in key):
            return key
        raise ValueError("%s is not a snapshot key" % path)
    key = os.urandom(32).hex()
    os.close(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600))
    try:
        make_private(path, runner, windows)
        with open(path, "w", encoding="ascii", newline="\n") as handle:
            handle.write(key + "\n")
        if not key_is_private(path, runner, windows):
            raise KeyPermissionError("%s is not private after the key was written" % path)
    except BaseException as error:
        try:
            os.remove(path)
        except OSError:
            pass
        if isinstance(error, (KeyPermissionError, KeyboardInterrupt)):
            raise
        raise KeyPermissionError("cannot make a private key file: %s" % error)
    return key


# Defence in depth behind the host script's allowlist: a value that looks like a token (a long run of letters of both cases,
# digits, "_" or "-") is never written, whatever field it came in. Hex (hashes, ids, keyed digests) is not that shape.
SECRET_SHAPE = re.compile(r"[A-Za-z0-9_-]{32,}")


def looks_like_secret(value):
    for run in SECRET_SHAPE.findall(value):
        if any(c.islower() for c in run) and any(c.isupper() for c in run) and any(c.isdigit() for c in run):
            return True
    return False


# Shapes of a credential that the length of a token does not catch: a user or a query in an address, a fragment, "password=x".
CREDENTIAL_PARTS = (
    re.compile(r"://[^\s/?#]*@"),
    re.compile(r"://\S*[?#]"),
    re.compile(r"(?i)(?<![A-Za-z0-9])(password|passwd|passphrase|secret|token|apikey|api[-_]key|credentials?)=(?!<redacted>|<none>|$)\S"),
)


def check_no_secret(facts):
    for key, value in facts.items():
        if looks_like_secret(value) or any(shape.search(value) for shape in CREDENTIAL_PARTS):
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
        try:
            key = snapshot_key(args.out)
        except (KeyPermissionError, ValueError) as error:
            print(error, file=sys.stderr)
            return 2
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
