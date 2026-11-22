#!/usr/bin/env python3
"""Run the release installer suite through its existing Linux isolation boundary."""
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile


SHELL_CASES = {
    "test-install-sandbox.sh": (),
    "test-install-oneclick.sh": (),
    "test-install-oneclick-token.sh": (),
    "test-install-password.sh": (),
    "test-install-web-password-reset.sh": ("server",),
    "test-server-upgrade-transaction.sh": (),
    "test-selector-order-existing.sh": ("agent",),
    "test-upgrade-floor.sh": (),
    "test-release-build-gates.sh": (),
}
GO_CASES = {
    "server": (
        "TestInstallerDownloadResumesAcrossOfficialAPIAssetFallback",
        "TestInstallerAgentUninstallExecutionIsCompleteIdempotentAndPreservesSharedHelper",
        "TestInstallerServerUpdateCheckCleanup",
        "TestK1DiskProbeLeftover",
        "TestSetupSecurityFailureRestoresStoppedAgentAndActiveTimer",
        "TestInstallerM12RecoveryPreservesPendingUntilHealthy",
        "TestInstallerExplicitBetaAndStableLatestBoundary",
        "TestInstallerTwoDigitUpgradeAndNonUpgradePreserveInstallation",
    ),
    "updater": ("TestUpdaterHealth",),
    "agent": ("TestSelectorOrderRootDirectoryIsReadableButNotReplaceableByAgent",),
}


def snapshot():
    paths = {p for directory in ("/run", "/etc", "/var/lib", "/var/log", "/usr/local/bin",
                                  "/usr/local/sbin", "/etc/systemd/system")
             for p in Path(directory).glob("404-probe*")}
    paths.update(Path("/etc/systemd/system").glob("*/404-probe*"))
    paths.update(Path("/dev") / name for name in ("null", "zero", "full", "random", "urandom", "tty"))
    metadata = {}
    for path in sorted(paths):
        info = path.lstat()
        metadata[str(path)] = (info.st_mode, info.st_uid, info.st_gid, info.st_ctime_ns)
    units = subprocess.run(["systemctl", "list-units", "404-probe*", "--all", "--plain",
                            "--no-legend", "--no-pager"], capture_output=True, text=True)
    return {"paths_and_devices": metadata, "services": (units.returncode, units.stdout, units.stderr)}


def run(source, assets, binaries):
    if sys.platform != "linux" or os.geteuid() != 0:
        raise ValueError("The installer fixture driver requires Linux and sudo")
    if Path("/etc/404-probe").exists() or any(Path("/var/lib").glob("404-probe*")):
        raise ValueError("Use a disposable Linux test runner without a managed installation")
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(os.uname().machine)
    if not arch:
        raise ValueError("The installer fixtures require Linux amd64 or arm64")
    # Inherited Go/temp paths may refer to paths masked by the fixture. The
    # fixture supplies its private TMPDIR; tests build outside the namespace.
    env = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8",
           "INSTALLER_FIXTURE_REQUIRED": "1"}
    fixture = [sys.executable, "-B", str(source / "scripts/installer-fixture.py")]
    before = snapshot()
    print("Host before: " + json.dumps(before), flush=True)

    with tempfile.TemporaryDirectory(prefix="probe-release-", dir="/var/lib") as temporary:
        parent = Path(temporary)

        def case(label, options, *, files=None, profile="default"):
            root = parent / label
            root.mkdir(mode=0o755 if profile == "multiuser" else 0o700)
            for name, original in (files or {}).items():
                target = root / "tmp" / name
                target.parent.mkdir(exist_ok=True)
                shutil.copyfile(original, target)
                target.chmod(stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR | stat.S_IRGRP |
                             stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)
            if label in {"test-install-web-password-reset", "test-selector-order-existing"}:
                fake_etc = root / "system/etc"
                fake_etc.mkdir(parents=True)
                (fake_etc / "os-release").write_text("ID=debian\nVERSION_ID=13\n")
                (root / "system/run/systemd/system").mkdir(parents=True)
                if label == "test-selector-order-existing":
                    (root / "system/var/lib/404-probe").mkdir(parents=True)
            result = subprocess.run([*fixture, "--root", str(root), "--profile", profile, *options],
                                    cwd=source, env=env, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, text=True, errors="replace")
            print(f"\n{label}\n{result.stdout}", end="", flush=True)
            passes = len(re.findall(r"^\s*(?:--- )?PASS(?=[: ]|$)", result.stdout, re.M))
            skips = len(re.findall(r"^\s*(?:--- )?SKIP(?=[: ]|$)", result.stdout, re.M))
            print(f"{label}: PASS lines={passes}, SKIP lines={skips}, exit={result.returncode}", flush=True)
            if before != snapshot():
                raise RuntimeError(f"Host paths, services or device metadata changed during {label}")
            if result.returncode or "INSTALLER-FIXTURE-SKIPPED:" in result.stdout:
                raise RuntimeError(f"Installer fixture failed: {label}")

        for profile in ("default", "multiuser"):
            case("canary-" + profile, ["--probe"], profile=profile)

        inputs = {"server": assets / f"404-probe-server-linux-{arch}",
                  "agent": assets / f"404-probe-agent-linux-{arch}"}
        for script, names in SHELL_CASES.items():
            label = Path(script).stem
            root = parent / label
            args = [str(root / "tmp" / name) for name in names]
            multiuser = script in {"test-install-web-password-reset.sh", "test-selector-order-existing.sh"}
            if script == "test-selector-order-existing.sh":
                args.insert(0, str(root / "installer-fixture.sh"))
            case(label, ["--script", str(source / "scripts" / script), "--", *args],
                 files={name: inputs[name] for name in names},
                 profile="multiuser" if multiuser else "default")

        for role, names in GO_CASES.items():
            directory = "cmd/server" if role == "server" else "internal/" + role
            label = "go-" + role
            # Use one path translation for the source and a separate translation for
            # the precompiled binary. Keep the temp paths within the guest.
            expression = "^(" + "|".join(names) + ")" + ("" if role == "updater" else "$")
            binary = parent / label / "tmp/suite.test"
            case(label, ["--", "bash", "-c", 'cd "$1"; exec "$2" -test.v -test.count=1 "-test.run=$3"',
                         "fixture", str(source / directory), str(binary), expression],
                 files={"suite.test": binaries / (role + ".test")},
                 profile="default" if role == "server" else "multiuser")
    print("Host after: " + json.dumps(snapshot()), flush=True)


if __name__ == "__main__":
    run(*map(Path, sys.argv[1:]))
