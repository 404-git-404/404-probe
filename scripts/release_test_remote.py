"""Linux-side commands for the test-machine release helper."""
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


PASSWORD = Path("/root/404-probe-test-web-password")
BIN = Path("/usr/local/bin")
ORIGIN = "http://127.0.0.1:8080"


def identity(role):
    binary = BIN / f"404-probe-{role}"
    if binary.is_symlink() or not binary.is_file():
        raise ValueError("Expected a regular installed binary")
    return json.loads(subprocess.check_output([binary, "version", "--json"]))


def architecture():
    return {"x86_64": "amd64", "aarch64": "arm64"}[os.uname().machine]


def installed(role, version, commit, amd64, arm64):
    build = identity(role)
    if (build["version"], build["commit"]) != (version, commit) or build.get("dirty") is not False:
        raise ValueError("Installed build identity mismatch")
    with (BIN / f"404-probe-{role}").open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    if digest != {"amd64": amd64, "arm64": arm64}[architecture()]:
        raise ValueError("Installed binary checksum mismatch")
    subprocess.run(["systemctl", "is-active", "--quiet", f"404-probe-{role}.service"], check=True)


def preflight(role, mode, start):
    if mode == "fresh":
        # The installer independently checks full role-specific residue. Never
        # feed a confirmation to an unexpected upgrader or uninstall anything.
        common = [Path("/etc/404-probe"), Path("/var/lib/404-probe"),
                  Path("/var/lib/404-probe-upgrade")]
        for service in ("server", "agent"):
            common.extend((BIN / f"404-probe-{service}",
                           Path(f"/etc/systemd/system/404-probe-{service}.service")))
        if any(os.path.lexists(item) for item in common):
            raise ValueError("Existing installation or data must be handled before a fresh installation")
        if role == "server" and os.path.lexists(PASSWORD):
            raise ValueError("Existing root password file must be retained or moved by its owner")
    else:
        if identity(role)["version"] != start:
            raise ValueError("Installed version differs from the requested upgrade origin")
        if role == "server" and os.path.lexists("/var/lib/404-probe-upgrade/backup.previous"):
            raise ValueError("The next upgrade would rotate an existing protected backup")


def verify(directory, version, commit):
    root = Path(directory)
    subprocess.run(["sha256sum", "--check", "--strict", "SHA256SUMS"], cwd=root, check=True)
    notes = json.loads((root / "RELEASE-METADATA.json").read_bytes())
    if (notes["version"], notes["commit"]) != (version, commit):
        raise ValueError("Remote assets do not identify the candidate")
    return {row["name"]: row["sha256"] for row in notes["assets"]}


def save_password(password):
    fd = os.open(PASSWORD, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as output:
        output.write(password + b"\n")
        output.flush()
        os.fsync(output.fileno())


def server_tty(directory, mode, env):
    if mode == "fresh":
        prompts = [
            (rb"Select a role \[1-2\]: ", b"1\n"),
            (rb"Web public origin \[http://127\.0\.0\.1:8080\]: ", b"\n"),
            (rb"Allowed root domain suffixes, comma-separated \[exact origin only\]: ", b"\n"),
            (rb"Choice \[1\]: ", b"1\n"),
            (rb"\r?\n  ([A-Za-z0-9_-]{43})\r?\n\r?\nPress Enter after you have saved it\. ", b"\n"),
        ]
    else:
        prompts = [(rb"Proceed with this Server upgrade\? \[y/N\]: ", b"y\n")]
    pid, master = pty.fork()
    if not pid:
        os.execve("/bin/bash", ["bash", str(Path(directory) / "install.sh")], env)
    pending = b""
    finished = False
    try:
        deadline = time.monotonic() + 2400
        while time.monotonic() < deadline:
            if not select.select([master], [], [], 1)[0]:
                continue
            try:
                chunk = os.read(master, 8192)
            except OSError as error:
                if error.errno != 5:  # A closed Linux PTY reports EIO.
                    raise
                chunk = b""
            if not chunk:
                finished = True
                break
            pending = (pending + chunk)[-16384:]
            if prompts:
                pattern, reply = prompts[0]
                matched = re.search(pattern, pending)
                if matched:
                    if matched.lastindex:
                        save_password(matched[1])
                    pending = pending[matched.end():]
                    prompts.pop(0)
                    os.write(master, reply)
        if not finished:
            raise TimeoutError("Installer prompt or completion timed out")
    finally:
        os.close(master)
        if not finished:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        _, status = os.waitpid(pid, 0)
    if status or prompts:
        raise ValueError("Installer did not complete the expected terminal conversation")


def install(role, mode, directory, version, commit, start, origin, name):
    preflight(role, mode, start)
    hashes = verify(directory, version, commit)
    env = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8",
           "PROBE_404_VERSION": version, "PROBE_404_LOCAL_ASSET_DIRECTORY": directory}
    if role == "server":
        server_tty(directory, mode, env)
    else:
        env.update(PROBE_404_ROLE="agent", PROBE_404_SERVER=origin, PROBE_404_AGENT_NAME=name,
                   PROBE_404_ENROLL_TOKEN_FILE=directory + "/token")
        subprocess.run(["bash", str(Path(directory) / "install.sh")], env=env,
                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL, check=True)
    installed(role, version, commit, hashes[f"404-probe-{role}-linux-amd64"],
              hashes[f"404-probe-{role}-linux-arm64"])


def agent_id(start):
    preflight("agent", "upgrade", start)
    matches = re.findall(r"^PROBE_404_AGENT_ID=([A-Za-z0-9_-]+)$",
                         Path("/etc/404-probe/agent.env").read_text(), re.M)
    if len(matches) != 1:
        raise ValueError("Agent identity is missing or ambiguous")
    print(matches[0])


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def web_upgrade(version, commit, agent):
    build = identity("server")
    if (build["version"], build["commit"]) != (version, commit) or build.get("dirty") is not False:
        raise ValueError("The test Server must run the exact candidate build")
    fd = os.open(PASSWORD, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as file:
        info = os.fstat(file.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("The test password must be in the root-only 0600 file")
        password = file.read(128).strip()
    if not re.fullmatch(rb"[A-Za-z0-9_-]{43}", password):
        raise ValueError("The file must contain an installer-generated test password")
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                         urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(path, *, data=None, content_type=None, csrf=None, expected=(200,)):
        headers = {"Origin": ORIGIN, "Sec-Fetch-Site": "same-origin"}
        if content_type:
            headers["Content-Type"] = content_type
        if csrf:
            headers["X-CSRF-Token"] = csrf
        req = urllib.request.Request(ORIGIN + path, data=data, headers=headers)
        try:
            response = client.open(req, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            if response.code not in expected:
                raise ValueError(f"Test Web request failed: HTTP {response.code}")
            return response.read()

    login = request("/login")
    token = re.search(rb'name="csrf_token" value="([^"]+)"', login)[1]
    request("/login", data=urllib.parse.urlencode({"csrf_token": token, "password": password}).encode(),
            content_type="application/x-www-form-urlencoded", expected=(302, 303))
    del password
    session = json.loads(request("/api/v1/web/session"))
    url = f"/api/v1/web/agents/{agent}/upgrade"
    operation = json.loads(request(url, data=json.dumps({"channel": "stable", "target_version": version}).encode(),
                                   content_type="application/json", csrf=session["csrf_token"], expected=(201,)))
    deadline = time.monotonic() + 2100
    while time.monotonic() < deadline:
        current = json.loads(request(url))
        if current.get("operation_id") != operation["operation_id"]:
            raise ValueError("Unexpected test Agent upgrade operation")
        state = current["status"]
        if state == "succeeded":
            return
        if state not in {"requested", "claimed", "downloading", "verifying", "staging",
                         "installing", "restarting", "health_check"}:
            raise ValueError("Test Agent upgrade did not succeed")
        time.sleep(3)
    raise TimeoutError("Test Agent upgrade did not reach success")


if __name__ == "__main__":
    if os.geteuid() != 0 or sys.platform != "linux":
        sys.exit("The configured test alias must connect as Linux root")
    commands = {"preflight": preflight, "verify": verify, "install": install,
                "installed": installed, "agent-id": agent_id, "web-upgrade": web_upgrade}
    commands[sys.argv[1]](*sys.argv[2:])
