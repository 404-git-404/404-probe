"""Run the selected release Node suites with isolated local fixtures."""

from collections import Counter
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


PLAYWRIGHT_VERSION = "1.55.0"
DEPENDENCY = re.compile(r"""require\(\s*['"](\.[^'"]+\.cjs)['"]\s*\)""")
SOURCE_DEPENDENCY = re.compile(r"""require\(path\.join\(\w+,\s*['"](scripts/[^'"]+\.cjs)['"]\)\)""")
TEST_ENV = {
    "M02_ROOT", "M02_SERVER", "M02_RECORD_VIDEO", "R45_SERVER_BINARY", "R45_DONOR_FIXTURE",
    "V109_SERVER", "V109_SCREENS", "V109_REFERENCE", "V109_TASK_TWO", "V109_COMPARE",
    "V110_SERVER", "V110_TEMP_ROOT", "V110_TEST_SOURCE", "V111_CLICK_ONLY", "V111_CLICK_SOURCE",
    "V111_THEME_SOURCE", "V11_QUOTA_GO",
}


def fixture_variables(source, suite):
    """Read env references from the suite and its local CommonJS dependencies."""
    current = {source / suite}
    visited = set()
    variables = set()
    while current:
        file = current.pop()
        if file in visited or not file.is_relative_to(source / "scripts") or not file.is_file():
            continue
        visited.add(file)
        code = file.read_text(encoding="utf-8")
        variables.update(re.findall(r"\bprocess\.env\.([A-Z][A-Z_0-9]*)\b", code))
        current.update((file.parent / name).resolve() for name in DEPENDENCY.findall(code))
        current.update(source / name for name in SOURCE_DEPENDENCY.findall(code))
    return variables


def known_failures(source):
    def unique_object(pairs):
        fields = dict(pairs)
        if len(fields) != len(pairs):
            raise ValueError("Duplicate suite in the known Node failures file")
        return fields

    manifest = source / "scripts/release-node-known-failures.json"
    known = json.loads(manifest.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if not isinstance(known, dict):
        raise ValueError("Known Node failures must map suites to exact case names")
    for suite, cases in known.items():
        if (not re.fullmatch(r"scripts/(?:[\w.-]+/)*test-[\w.-]+\.cjs", suite)
                or not (source / suite).is_file()
                or not isinstance(cases, list) or not cases
                or any(not isinstance(case, str) or not case.strip() or "\n" in case for case in cases)
                or len(cases) != len(set(cases))):
            raise ValueError(f"Invalid known Node failures for {suite}")
    return known


def classify_cases(records, allowed, exit_code):
    """Only uniquely identified, non-skipped failed cases can match the list."""
    counts = Counter(case["name"] for case in records)
    matched = set()
    findings = []
    for case in records:
        name = case["name"]
        if case.get("skip"):
            reason = case["skip"] if isinstance(case["skip"], str) else None
            findings.append(["SKIPPED", f"{name} ({reason})" if reason else name])
            continue
        recognized = name in allowed and counts[name] == 1
        if recognized:
            matched.add(name)
        if case["type"] == "test:fail":
            findings.append(["KNOWN FAIL" if recognized else "FAIL", name])
        elif recognized:
            findings.append(["XPASS", name])
    findings.extend(["NOT RUN", name] for name in allowed if name not in matched)
    saw_failure = any(status in {"FAIL", "KNOWN FAIL"} for status, _ in findings)
    if not records or exit_code not in (0, 1) or (exit_code == 1) != saw_failure:
        findings.append(["FAIL", f"unexpected Node runner result (exit {exit_code})"])
    return findings


def chromium_paths(env):
    yield env.get("M02_CHROMIUM")
    for command in ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "msedge"):
        yield shutil.which(command, path=env.get("PATH"))
    if os.name == "nt":
        for key in ("PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"):
            if env.get(key):
                yield Path(env[key]) / "Google/Chrome/Application/chrome.exe"
                yield Path(env[key]) / "Microsoft/Edge/Application/msedge.exe"
    elif sys.platform == "darwin":
        yield "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


class NodeFixtures:
    def __init__(self, release, temp, node):
        self.release, self.temp, self.node = release, temp, node
        self.env = {key: value for key, value in release.env.items()
                    if key not in TEST_ENV and not key.startswith("PROBE_404_")}
        self.browser_error = self.go_error = self.server_error = None

    def prepare_browser(self):
        env = self.env
        executable = next((str(Path(path).resolve()) for path in chromium_paths(env)
                           if path and Path(path).is_file()), None)
        if not executable:
            raise ValueError("no local Chromium, Chrome or Edge executable found")
        package = env.get("M02_PLAYWRIGHT")
        if package and Path(package).exists():
            package = str(Path(package).resolve())
        elif not package:
            for name in ("playwright-core", "playwright"):
                resolved = subprocess.run(
                    [self.node, "-p", f"require.resolve('{name}')"], cwd=self.release.source,
                    env=env, capture_output=True, text=True,
                )
                if resolved.returncode == 0:
                    package = resolved.stdout.strip()
                    break
        if not package:
            npm = shutil.which("npm.cmd" if os.name == "nt" else "npm", path=env.get("PATH"))
            if not npm:
                raise ValueError("npm missing and no local Playwright package found")
            self.release.run([npm, "install", "--prefix", str(self.temp), "--ignore-scripts",
                              "--no-audit", "--no-fund", "--no-package-lock",
                              "--fetch-retries=0", "--fetch-timeout=30000",
                              "playwright-core@" + PLAYWRIGHT_VERSION], env=env)
            package = str(self.temp / "node_modules/playwright-core")
        env.update(M02_PLAYWRIGHT=package, M02_CHROMIUM=executable, M02_RECORD_VIDEO="0")
        check = ("const {chromium}=require(process.env.M02_PLAYWRIGHT);"
                 "(async()=>{const b=await chromium.launch({headless:true,"
                 "executablePath:process.env.M02_CHROMIUM,timeout:30000});await b.close()})()"
                 ".catch(e=>{console.error(e.message);process.exitCode=1});")
        self.release.run([self.node, "-e", check], env=env)

    def prepare_server(self, m02):
        binary = self.temp / ("fixture-server.exe" if os.name == "nt" else "fixture-server")
        # Build for the machine running Node, regardless of exported release build targets.
        env = {key: value for key, value in self.env.items()
               if key not in {"GOOS", "GOARCH", "GOFLAGS"}}
        self.release.run([self.release.go, "build", "-o", str(binary), "./cmd/server"], env=env)
        self.env.update(M02_SERVER=str(binary), R45_SERVER_BINARY=str(binary),
                        V109_SERVER=str(binary), V110_SERVER=str(binary))
        if m02:
            location = self.temp / "m02"
            location.mkdir()
            command = [str(binary), "web", "password-hash"]
            with (location / "password.hash").open("xb") as output:
                subprocess.run(command, input=b"isolated-m02-password\n", cwd=location,
                               stdout=output, stderr=self.release.log, env=env, check=True)
            self.env["M02_ROOT"] = str(location)

    def prepare(self, variables):
        if "M02_PLAYWRIGHT" in variables:
            try:
                self.prepare_browser()
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                self.browser_error = f"browser fixture unavailable: {error}"
        server = variables & {"M02_SERVER", "V109_SERVER", "V110_SERVER", "R45_SERVER_BINARY"}
        if "V11_QUOTA_GO" in variables or (server and not self.browser_error):
            try:
                self.release.compiler()
                self.env["V11_QUOTA_GO"] = self.release.go
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                self.go_error = f"Go toolchain unavailable: {error}"
        if server and not self.go_error and not self.browser_error:
            try:
                self.prepare_server("M02_ROOT" in variables)
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                self.server_error = f"local Server fixture unavailable: {error}"

    def suite_environment(self, suite, variables):
        if "M02_PLAYWRIGHT" in variables and self.browser_error:
            return None, self.browser_error
        if "V11_QUOTA_GO" in variables and self.go_error:
            return None, self.go_error
        if variables & {"M02_SERVER", "V109_SERVER", "V110_SERVER", "R45_SERVER_BINARY"}:
            if self.go_error or self.server_error:
                return None, self.go_error or self.server_error
        env = self.env.copy()
        if "V109_SCREENS" in variables:
            env["V109_SCREENS"] = str(self.temp / Path(suite).stem)
            env["V109_COMPARE"] = str(self.temp / "v109-compare")
        if "V110_TEMP_ROOT" in variables:
            env["V110_TEMP_ROOT"] = str(self.temp)
        return env, None


def execute_suite(release, node, suite, uses_runner, allowed, env):
    command = [node]
    if uses_runner:
        command += ["--test", "--test-force-exit",
                    "--test-reporter=./scripts/release-node-reporter.cjs"]
    command.append(suite)
    print("$ " + subprocess.list2cmdline(command), file=release.log, flush=True)
    result = subprocess.run(command, cwd=release.source, env=env, capture_output=True,
                            encoding="utf-8", errors="replace")
    if result.stderr:
        print(result.stderr, end="", file=release.log)
    if not uses_runner:
        print(result.stdout, end="", file=release.log)
        notes = [["SKIPPED", line.removeprefix("SKIPPED ")]
                 for line in result.stdout.splitlines() if line.startswith("SKIPPED ")]
        if result.returncode:
            notes.append(["FAIL", f"standalone suite exited {result.returncode}"])
        return notes
    records = []
    for line in result.stdout.splitlines():
        try:
            case = json.loads(line)
        except ValueError:
            print(line, file=release.log)
            return [["FAIL", "invalid Node reporter output"]]
        if case["type"] in {"test:stdout", "test:stderr"}:
            print(case["message"], end="", file=release.log)
        else:
            print(f"{case['type']} {case['name']}", file=release.log)
            if case.get("error"):
                print(case["error"], file=release.log)
            records.append(case)
    return classify_cases(records, allowed, result.returncode)


def run_node(release, suites):
    results = []
    release.state["node_suites"] = results
    node = shutil.which("node", path=release.env.get("PATH"))
    if not node:
        raise ValueError("Node executable not found")
    known = known_failures(release.source)
    required = {suite: fixture_variables(release.source, suite) for suite, _ in suites}
    with tempfile.TemporaryDirectory(prefix="node-", dir=release.out) as temp:
        fixtures = NodeFixtures(release, Path(temp), node)
        fixtures.prepare(set().union(*required.values()))
        for suite, uses_runner in suites:
            env, skip = fixtures.suite_environment(suite, required[suite])
            if skip:
                status, notes = "SKIPPED", [["SKIPPED", skip]]
            else:
                notes = execute_suite(release, node, suite, uses_runner, known.get(suite, ()), env)
                status = "FAIL" if any(kind == "FAIL" for kind, _ in notes) else "PASS"
            results.append([suite, status, notes])
            for kind, detail in notes:
                print(f"{kind} {suite}: {detail}", file=release.log, flush=True)
                print(f"{kind} {suite}: {detail}", flush=True)
    counts = Counter(status for _, status, _ in results)
    details = Counter(kind for _, _, notes in results for kind, _ in notes)
    summary = (f"{counts['PASS']} passed, {counts['SKIPPED']} skipped, {counts['FAIL']} failed suites; "
               f"{details['KNOWN FAIL']} known failures, {details['XPASS']} newly passing cases, "
               f"{details['SKIPPED'] - counts['SKIPPED']} skipped cases")
    print(f"Node: {summary}", flush=True)
    if counts["FAIL"]:
        raise ValueError(summary)
    return f"通过（{summary}）"
