#!/usr/bin/env python3
"""Build and test an exact candidate in a clean local clone."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import shutil
import subprocess
import sys
import tempfile

sys.dont_write_bytecode = True
from release_prepare import prepare, version_key


ROOT = Path(__file__).resolve().parent.parent
ASSETS = {f"404-probe-{role}-linux-{arch}" for role in ("server", "agent")
          for arch in ("amd64", "arm64")}
TEXT_ASSETS = {"install.sh", "RELEASE-METADATA.json", "SHA256SUMS"}
STAGES = ("prepare", "build", "verify", "go", "race", "node", "shell", "installer")


def stamp():
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True, encoding="utf-8").strip()


def ensure_clean(root):
    if git(root, "status", "--porcelain", "--untracked-files=all"):
        raise ValueError(f"Git checkout must be clean: {root}")


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def node_suites(root, paths):
    scripts = {p.relative_to(root).as_posix(): p.read_text(encoding="utf-8")
               for p in (root / "scripts").rglob("*.cjs")}
    selected = {"scripts/test-release-version.cjs"}
    known = set(paths)
    while True:
        names = {PurePosixPath(path).name for path in known}
        # Test drivers reference fixtures in require(), fs calls and HTML they load.
        references = names | known
        reached = {path for path, content in scripts.items()
                   if any(re.search(r"(?<![\w.-])" + re.escape(name) + r"(?![\w.-])", content)
                          or (name.endswith(".cjs") and name[:-4] in content)
                          for name in references)}
        # A change in an in-page module also affects browser drivers that open the
        # complete entry point, even if they never name that JavaScript module.
        if any(path.startswith("web/static/") for path in known):
            reached.update(path for path, content in scripts.items()
                           if "index.html" in content or "chromium" in content)
        if reached <= known:
            break
        known.update(reached)
    selected.update(path for path in known if path in scripts and Path(path).name.startswith("test-"))
    return [(path, "node:test" in scripts[path]) for path in sorted(selected)]


class Release:
    def __init__(self, args):
        self.args = args
        self.out = args.out.resolve()
        if self.out.is_relative_to(ROOT) or not re.fullmatch(r"[A-Za-z0-9_./:-]+", self.out.as_posix()):
            raise ValueError("--out must be an absolute ASCII path outside the source checkout, without spaces")
        self.out.mkdir(parents=True, exist_ok=True)
        self.source = self.out / "candidate"
        self.assets = self.out / "assets"
        self.state_file = self.out / "release-state.json"
        self.state = {}
        suffix = ".exe" if os.name == "nt" else ""
        local_go = ROOT / ".tools/go/bin" / ("go" + suffix)
        main_go = ROOT.parent / "404-probe/.tools/go/bin" / ("go" + suffix)
        go = args.go or next((str(path) for path in (local_go, main_go) if path.is_file()), None)
        self.go = go or shutil.which("go") or "go"
        git_path = shutil.which("git")
        bundled_bash = Path(git_path).parent.parent / "bin/bash.exe" if git_path else None
        self.bash = (args.bash or (str(bundled_bash) if bundled_bash and bundled_bash.is_file() else None)
                     or shutil.which("bash") or "bash")
        self.env = dict(os.environ)
        go_path = shutil.which(self.go) or self.go
        if Path(go_path).is_absolute():
            self.env["PATH"] = str(Path(go_path).parent) + os.pathsep + self.env["PATH"]
        self.env.update(R31_PYTHON=sys.executable, R31_BASH=self.bash)
        if not Path(self.bash).is_absolute():
            self.env["R31_BASH"] = shutil.which(self.bash) or self.bash

    def report(self):
        lines = [f"# 发布验收 {self.state.get('version', '')}", "",
                 f"候选提交：{self.state.get('commit', '待生成')}", "",
                 "| 步骤 | 结果 | 日志 / 说明 |", "|---|---|---|"]
        for row in self.state.get("steps", []):
            lines.append("| " + " | ".join(row) + " |")
        lines += ["", "| 文件 | 字节 | SHA-256 |", "|---|---:|---|"]
        for name in sorted(ASSETS | TEXT_ASSETS):
            path = self.assets / name
            if path.is_file() and not path.is_symlink():
                lines.append(f"| {name} | {path.stat().st_size} | `{sha256(path)}` |")
            else:
                lines.append(f"| {name} | — | — |")
        for path, content in ((self.state_file, json.dumps(self.state, ensure_ascii=False, indent=2)),
                              (self.out / "results.md", "\n".join(lines))):
            temporary = path.with_suffix(".tmp")
            temporary.write_text(content + "\n", encoding="utf-8", newline="\n")
            temporary.replace(path)

    def run(self, command, *, env=None, cwd=None):
        print("$ " + subprocess.list2cmdline(list(map(str, command))), file=self.log, flush=True)
        result = subprocess.run(command, cwd=cwd or self.source, env=env or self.env,
                                stdout=self.log, stderr=subprocess.STDOUT)
        if result.returncode:
            raise subprocess.CalledProcessError(result.returncode, command)

    def release_head(self):
        if not (self.source / ".git").is_dir() or "commit" not in self.state:
            raise ValueError("Run prepare first")
        ensure_clean(self.source)
        if git(self.source, "rev-parse", "HEAD") != self.state["commit"]:
            raise ValueError("Candidate HEAD differs from the recorded release commit")

    def compiler(self):
        actual = subprocess.check_output([self.go, "version"], cwd=self.source,
                                         env=self.env, text=True).split()[2]
        required = re.search(r"^toolchain (go[\d.]+)$", (self.source / "go.mod").read_text(),
                             re.M)[1]
        if actual != required:
            raise ValueError(f"go version shows {actual}; expected {required}. Use --go with the pinned Go entry point")
        print(f"Go toolchain: {actual}", file=self.log)
        return actual

    def prepare(self):
        ensure_clean(ROOT)
        version_key(self.args.version)
        version_key(self.args.from_version)
        if self.source.exists():
            self.release_head()
            if (self.state["version"], self.state["base"]) != (self.args.version, self.args.from_version):
                raise ValueError("Existing candidate uses a different version or upgrade origin; choose another --out")
            return "通过"

        base = self.args.from_version
        expected = json.loads((ROOT / "scripts/r31-versions.json").read_text())["versions"][base]["commit"]
        if git(ROOT, "rev-parse", f"refs/tags/{base}^{{commit}}") != expected:
            raise ValueError(f"Local tag {base} differs from the R31 registry")
        if subprocess.run(["git", "merge-base", "--is-ancestor", expected, "HEAD"], cwd=ROOT).returncode:
            raise ValueError(f"HEAD does not descend from {base}")
        notes = self.args.notes.read_bytes().decode("utf-8") if self.args.notes else None
        name, email = git(ROOT, "var", "GIT_AUTHOR_IDENT").split(" <")[0], git(ROOT, "config", "user.email")
        with tempfile.TemporaryDirectory(prefix="prepare-", dir=self.out) as temp:
            clone = Path(temp) / "repo"
            self.run(["git", "-c", "core.autocrlf=false", "clone", "--no-hardlinks", "--no-checkout",
                      str(ROOT), str(clone)], cwd=ROOT)
            self.run(["git", "config", "core.autocrlf", "false"], cwd=clone)
            self.run(["git", "switch", "--detach", git(ROOT, "rev-parse", "HEAD")], cwd=clone)
            prepare(clone, self.args.version, base, notes)
            self.run(["git", "add", "-A"], cwd=clone)
            self.run(["git", "diff", "--cached", "--check"], cwd=clone)
            self.run(["git", "-c", f"user.name={name}", "-c", f"user.email={email}", "commit",
                      "-m", f"chore(release): prepare {self.args.version}"], cwd=clone)
            commit = git(clone, "rev-parse", "HEAD")
            clone.rename(self.source)
        self.state.update(version=self.args.version, base=base, commit=commit)
        return "通过"

    def build(self):
        self.compiler()
        with tempfile.TemporaryDirectory(prefix="build-", dir=self.out) as temp:
            output = Path(temp) / "assets"
            env = dict(self.env, PROBE_404_RELEASE_OUTPUT=output.as_posix(),
                       PROBE_404_RELEASE_COMMIT=self.state["commit"])
            self.run([self.bash, "scripts/build-release-assets.sh", self.state["version"]], env=env)
            if self.assets.exists():
                self.assets.rename(self.out / ("assets-before-" + stamp()))
            output.rename(self.assets)
        return "通过"

    def verify(self):
        expected = ASSETS | TEXT_ASSETS
        actual = {path.name for path in self.assets.iterdir()} if self.assets.is_dir() else set()
        if actual != expected:
            raise ValueError(f"Release assets differ: missing {sorted(expected - actual)}, extra {sorted(actual - expected)}")
        for name in expected:
            path = self.assets / name
            if path.is_symlink() or not path.is_file() or not path.stat().st_size:
                raise ValueError(f"Expected a nonempty regular file: {name}")
        for name in TEXT_ASSETS:
            if b"\r" in (self.assets / name).read_bytes():
                raise ValueError(f"Release asset contains CR: {name}")
        sums = {}
        for line in (self.assets / "SHA256SUMS").read_text(encoding="ascii").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
            if not match or match[2] in sums:
                raise ValueError(f"Invalid or duplicate SHA256SUMS entry: {line!r}")
            sums[match[2]] = match[1]
        if set(sums) != expected - {"SHA256SUMS"}:
            raise ValueError("SHA256SUMS does not cover the six checksummed assets")
        for name, digest in sums.items():
            if sha256(self.assets / name) != digest:
                raise ValueError(f"SHA-256 mismatch: {name}")
        metadata = json.loads((self.assets / "RELEASE-METADATA.json").read_bytes())
        if (metadata["version"], metadata["commit"]) != (self.state["version"], self.state["commit"]):
            raise ValueError("Release metadata version or commit differs from the candidate")
        rows = metadata["assets"]
        if len(rows) != len(ASSETS) or {row["name"] for row in rows} != ASSETS:
            raise ValueError("Release metadata must list the four expected ELF assets once each")
        compiler = self.compiler()
        for row in rows:
            name = row["name"]
            arch = name.rsplit("-", 1)[1]
            if (row["goos"], row["goarch"], row["sha256"]) != ("linux", arch, sums[name]):
                raise ValueError(f"Release metadata asset mismatch: {name}")
            with (self.assets / name).open("rb") as file:
                if file.read(4) != b"\x7fELF":
                    raise ValueError(f"Not an ELF file: {name}")
            info = subprocess.check_output([self.go, "version", "-m", str(self.assets / name)],
                                           cwd=self.source, env=self.env, text=True)
            fields = {line.strip() for line in info.splitlines()}
            required = {f"build\tvcs.revision={self.state['commit']}", "build\tvcs.modified=false",
                        "build\tGOOS=linux", f"build\tGOARCH={arch}"}
            if not info.splitlines()[0].endswith(": " + compiler) or not required <= fields:
                raise ValueError(f"Go build info mismatch: {name}")
        return "通过"

    def changed(self):
        output = subprocess.check_output(["git", "diff", "--name-only", "-z", "--no-renames",
                                          self.state["base"], "HEAD"], cwd=self.source)
        return set(output.decode("utf-8").strip("\x00").split("\x00")) - {""}

    def go_tests(self):
        if hasattr(os, "geteuid") and os.geteuid() == 0:
            raise ValueError("Run full Go tests as a non-root user")
        self.compiler()
        self.run([self.go, "vet", "./..."])
        self.run([self.go, "test", "./...", "-count=1", "-v"])
        return "通过"

    def race(self):
        self.compiler()
        changed = self.changed()
        if {"go.mod", "go.sum"} & changed:
            packages = ["./..."]
        else:
            # Use the actual package list so removed packages and helper Go files
            # excluded by build tags do not become invalid test arguments.
            dirs = subprocess.check_output([self.go, "list", "-f", "{{.Dir}}", "./..."],
                                           cwd=self.source, env=self.env, text=True).splitlines()
            available = {Path(path).relative_to(self.source).as_posix() for path in dirs}
            touched = {PurePosixPath(path).parent.as_posix() for path in changed if path.endswith(".go")}
            packages = ["./" + path for path in sorted(touched & available)]
        if not packages:
            return "通过（无相关 Go 包）"
        env = dict(self.env, CGO_ENABLED="1")
        self.run([self.go, "test", "-race", "-count=1", "-timeout=60m", *packages], env=env)
        return "通过"

    def node(self):
        for path, uses_runner in node_suites(self.source, self.changed()):
            self.run(["node", *(["--test", "--test-force-exit"] if uses_runner else []), path])
        return "通过"

    def shell(self):
        for name in ("test-release-installer-manifest.sh", "test-release-rc.sh", "test-install-banner.sh"):
            self.run([self.bash, f"scripts/{name}"])
        for path in sorted((self.source / "scripts").glob("test-*.py")):
            if path.name in {"test-r31.py", "test-machine-release.py"}:
                continue  # R31 runs via Go. The test-machine CLI requires explicit arguments.
            if path.name == "test-installer-download.py" and sys.platform != "linux":
                print(f"SKIP {path.name}: Linux PTY and installer fixture required", file=self.log)
                continue
            self.run([sys.executable, "-B", str(path)])
        return "通过"

    def installer(self):
        if sys.platform != "linux":
            print("Requires Linux with sudo, bubblewrap, strace and a verified namespace", file=self.log)
            return "跳过（Linux 专用）"
        # Compile as the caller and run only the selected programs inside the fixture.
        with tempfile.TemporaryDirectory(prefix="installer-", dir=self.out) as temp:
            for package in ("cmd/server", "internal/agent", "internal/updater"):
                self.run([self.go, "test", "-c", f"-gcflags=all=-trimpath={self.source}=>/work/source",
                          "-o", str(Path(temp) / (Path(package).name + ".test")),
                          "./" + package])
            self.run(["sudo", "--", sys.executable, "-B",
                      str(self.source / "scripts/release_install_fixtures.py"),
                      str(self.source), str(self.assets), temp])
        return "通过"

    def step(self, name):
        log = self.out / f"{name}-{stamp()}.log"
        print(f"{name}: {log}", flush=True)
        try:
            with log.open("w", encoding="utf-8", newline="\n") as self.log:
                try:
                    if name != "prepare":
                        self.release_head()
                    action = self.go_tests if name == "go" else getattr(self, name)
                    status = action()
                except Exception as error:
                    print(f"{type(error).__name__}: {error}", file=self.log)
                    raise
        except Exception as error:
            retry = [sys.executable, str(Path(__file__).resolve()), name, *sys.argv[2:]]
            command = (
                "& " + " ".join("'" + item.replace("'", "''") + "'" for item in retry)
                if os.name == "nt" else shlex.join(retry)
            )
            self.state.setdefault("steps", []).append([name, "失败", log.name])
            self.report()
            print(f"FAILED {name}: {error}\nLog: {log}\nRetry this step: {command}", file=sys.stderr)
            return False
        self.state.setdefault("steps", []).append([name, status, log.name])
        self.report()
        return True


def main():
    if sys.platform == "linux" and os.geteuid() == 0:
        print("Linux 发布工具拒绝以 root 运行。请用非 root 用户 + sudo 运行。", file=sys.stderr)
        return 1
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("stage", choices=("all", *STAGES))
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--version")
    parser.add_argument("--from-version")
    parser.add_argument("--notes", type=Path, help="Approved release notes in UTF-8 LF")
    parser.add_argument("--go", help="Pinned Go entry point")
    parser.add_argument("--bash", help="Bash executable (Git Bash on Windows)")
    parser.add_argument("--wsl", metavar="DISTRO", help="Run the complete pipeline in this local WSL distribution")
    args = parser.parse_args()
    if args.stage in ("all", "prepare") and not (args.version and args.from_version):
        parser.error("all and prepare require --version and --from-version")
    if args.wsl:
        if os.name != "nt":
            parser.error("--wsl is a Windows entry point")
        wsl = ["wsl.exe", "-d", args.wsl, "--"]

        def linux_path(path):
            return subprocess.check_output([*wsl, "wslpath", "-a", str(path.resolve())],
                                           text=True, encoding="utf-8").strip()

        try:
            # A Windows worktree's .git file can contain a drive-letter path
            # that Linux Git cannot resolve. Hand WSL a normal local clone.
            ensure_clean(ROOT)
            out = args.out.resolve()
            if out.is_relative_to(ROOT):
                raise ValueError("--out must be outside the source worktree")
            out.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(prefix="wsl-source-", dir=out) as seed:
                source = Path(seed) / "repo"
                subprocess.run(["git", "-c", "core.autocrlf=false", "clone", "--no-hardlinks",
                                "--no-checkout", str(ROOT), str(source)], check=True)
                git(source, "config", "core.autocrlf", "false")
                git(source, "config", "user.name", git(ROOT, "config", "user.name"))
                git(source, "config", "user.email", git(ROOT, "config", "user.email"))
                git(source, "switch", "--detach", git(ROOT, "rev-parse", "HEAD"))
                command = [*wsl, "python3", "-B", linux_path(source / "scripts/release.py"), args.stage,
                           "--out", linux_path(out)]
                if args.notes:
                    command.extend(["--notes", linux_path(args.notes)])
                for flag, value in (("--version", args.version), ("--from-version", args.from_version),
                                    ("--go", args.go), ("--bash", args.bash)):
                    if value:
                        command.extend([flag, value])
                code = subprocess.run(command).returncode
            if code:
                print("Use the same Windows entry point with --wsl and the failed step name.", file=sys.stderr)
            return code
        except (OSError, ValueError, subprocess.CalledProcessError) as error:
            parser.exit(1, f"Cannot start the release pipeline in the requested local WSL distribution: {error}\n")
    args.out = args.out.resolve()
    lock = args.out / ".release.lock"
    try:
        release = Release(args)
        with lock.open("x"):
            pass
    except (OSError, ValueError) as error:
        parser.exit(1, f"{error}\n")
    try:
        if release.state_file.exists():
            release.state = json.loads(release.state_file.read_text(encoding="utf-8"))
        stages = STAGES if args.stage == "all" else (args.stage,)
        for stage in stages:
            if not release.step(stage):
                return 1
        print(f"Report: {release.out / 'results.md'}")
        return 0
    finally:
        lock.unlink()


if __name__ == "__main__":
    sys.exit(main())
