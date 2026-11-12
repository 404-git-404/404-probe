package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerAgentUninstallExecutionIsCompleteIdempotentAndPreservesSharedHelper(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "installer", true: "generated-helper"}[early], func(t *testing.T) { runInstallerAgentUninstallFixture(t, early) })
	}
}

func TestInstallerServerUpdateCheckCleanup(t *testing.T) {
	for _, role := range []string{"installer", "generated-helper", "openrc", "fresh-rollback"} {
		t.Run(role, func(t *testing.T) {
			rootNative := t.TempDir()
			root := shellTestPath(rootNative)
			source := m12InstallerSource(t)
			source = strings.TrimSuffix(strings.TrimSpace(source), `main "$@"`)
			if role == "generated-helper" {
				start := strings.Index(source, "cat <<'HELPER'\n")
				end := strings.Index(source, "\nreject_deferred_command_on_host \"${1:-}\"\nrequire_root\n")
				if start < 0 || end <= start {
					t.Fatal("could not locate generated helper definitions")
				}
				source = source[start+len("cat <<'HELPER'\n") : end]
			}
			path := filepath.Join(rootNative, "installer-source.sh")
			if err := os.WriteFile(path, []byte(installerFixtureSource(source, root)), 0600); err != nil {
				t.Fatal(err)
			}
			harness := `set -Eeuo pipefail
task_root='` + root + `'
mode='` + role + `'
source "$task_root/installer-source.sh"
require_root_linux_systemd() { :; }
validate_service_user() { :; }
validate_removable_service_user() { :; }
openrc_role_residue() { return 1; }
mountpoint() { return 1; }
rm() {
  # The general fixture wrapper rejects symlink operands. Permit only this
  # fixture's literal cache leaf to be unlinked, with its parent still checked.
  if [[ $# == 3 && "$1" == -f && "$2" == -- && -L "$3" ]] &&
     [[ "$3" == "$STATE_DIRECTORY/update-check.json" || "$3" == "$STATE_DIRECTORY"/.update-check-*.tmp ]] &&
     [[ "${3%/*}" == "$STATE_DIRECTORY" ]]; then
    fixture_path "$STATE_DIRECTORY" || return
    command rm "$@"
  else
    fixture_mutate rm "$@"
  fi
}
stat() {
  if [[ "$1" == -c && "$2" == %U ]]; then
    # The fixture uses synthetic account records, so model the corresponding
    # owner names while leaving the actual mode and other stat calls intact.
    if [[ "${*: -1}" == "$SERVER_DATABASE"* ]]; then
      printf '%s\n' "$SERVICE_USER"
    else
      printf 'root\n'
    fi
  else
    command stat "$@"
  fi
}
systemctl() {
  case "$1" in is-active) return 1;; stop|disable|daemon-reload) return 0;; *) return 98;; esac
}
id() { [[ -f "$task_root/account" ]]; }
getent() {
  case "$1:${2:-}" in
    passwd:404-probe) id && printf '404-probe:x:451:451::%s:/usr/sbin/nologin\n' "$STATE_DIRECTORY" ;;
    group:404-probe) [[ -f "$task_root/group" ]] && printf '404-probe:x:451:\n' ;;
    passwd:) return 0 ;;
    *) return 2 ;;
  esac
}
userdel() { rm -- "$task_root/account"; }
groupdel() { rm -- "$task_root/group"; }
write_cache() {
  mkdir -p "$STATE_DIRECTORY"
  printf '{"enabled":false}\n' >"$STATE_DIRECTORY/update-check.json"
  chmod 0600 "$STATE_DIRECTORY/update-check.json"
  touch "$STATE_DIRECTORY/.update-check-x.tmp"
  case "$1" in
    directories)
      rm -- "$STATE_DIRECTORY/update-check.json"
      mkdir -- "$STATE_DIRECTORY/update-check.json" "$STATE_DIRECTORY/.update-check-directory.tmp"
      printf preserve >"$STATE_DIRECTORY/update-check.json/unknown"
      ;;
    symlinks)
      printf preserve >"$task_root/link-target"
      mkdir -p "$task_root/linked-directory"
      printf preserve >"$task_root/linked-directory/unknown"
      rm -- "$STATE_DIRECTORY/update-check.json"
      ln -s -- "$task_root/link-target" "$STATE_DIRECTORY/update-check.json"
      ln -s -- "$task_root/linked-directory" "$STATE_DIRECTORY/.update-check-link.tmp"
      ln -s -- "$task_root/absent" "$STATE_DIRECTORY/.update-check-dangling.tmp"
      ;;
  esac
}
verify_cache_removed() {
  [[ ! -e "$STATE_DIRECTORY/.update-check-x.tmp" ]]
  if [[ "$1" == directories ]]; then
    [[ "$(cat "$STATE_DIRECTORY/update-check.json/unknown")" == preserve && -d "$STATE_DIRECTORY/.update-check-directory.tmp" ]]
    rm -- "$STATE_DIRECTORY/update-check.json/unknown"
    rmdir -- "$STATE_DIRECTORY/update-check.json" "$STATE_DIRECTORY/.update-check-directory.tmp"
  else
    for path in update-check.json .update-check-link.tmp .update-check-dangling.tmp; do
      [[ ! -e "$STATE_DIRECTORY/$path" && ! -L "$STATE_DIRECTORY/$path" ]]
    done
  fi
  if [[ "$1" == symlinks ]]; then
    [[ "$(cat "$task_root/link-target")" == preserve && "$(cat "$task_root/linked-directory/unknown")" == preserve ]]
  fi
}
if [[ "$mode" == fresh-rollback ]]; then
  for INSTALL_INIT in systemd openrc; do
    for layout in clean directories symlinks; do
      write_cache "$layout"
      openrc_stop() { return 0; }
      touch "$task_root/tracked-file"
      set +e
      (
        set -e
        INSTALL_TRANSACTION_ACTIVE=1
        INSTALL_TRANSACTION_ROLE=server
        INSTALL_TRANSACTION_UNIT=404-probe-server
        INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=1
        INSTALL_TRANSACTION_PATHS=("$task_root/tracked-file")
        rollback_install_transaction 42
      )
      status=$?
      set -e
      [[ "$status" == 42 && ! -e "$task_root/tracked-file" ]]
      if [[ "$layout" == directories ]]; then [[ -d "$STATE_DIRECTORY" ]]; else [[ ! -e "$STATE_DIRECTORY" ]]; fi
      verify_cache_removed "$layout"
      if [[ "$layout" == directories ]]; then rmdir -- "$STATE_DIRECTORY"; fi
    done
    write_cache clean
    set +e
    (
      set -e
      INSTALL_TRANSACTION_ACTIVE=1
      INSTALL_TRANSACTION_ROLE=server
      INSTALL_TRANSACTION_UNIT=
      INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=1
      rollback_install_transaction 42
    )
    status=$?
    set -e
    [[ "$status" == 42 && "$(cat "$STATE_DIRECTORY/update-check.json")" == '{"enabled":false}' && -f "$STATE_DIRECTORY/.update-check-x.tmp" ]]
    rm -- "$STATE_DIRECTORY/update-check.json" "$STATE_DIRECTORY/.update-check-x.tmp"
    rmdir -- "$STATE_DIRECTORY"
  done
  exit 0
fi
if [[ "$mode" == openrc ]]; then
  INSTALL_INIT=openrc
  openrc_stop() { return 0; }
  openrc_safe_parents() { return 0; }
fi
uninstall_server() {
  if [[ "$mode" == generated-helper ]]; then
    uninstall server --confirm-delete-data
  else
    uninstall_role server --confirm-delete-data
  fi
}
for layout in clean unknown legacy directories symlinks; do
  write_cache "$layout"
  touch "$task_root/account" "$task_root/group"
  if [[ "$layout" == legacy ]]; then
    if server_residue_exists; then exit 98; fi
    if [[ "$mode" == installer ]]; then
      interactive_install() { touch "$task_root/fresh-path"; }
      upgrade_existing_server() { return 99; }
      install_or_upgrade
      [[ -f "$task_root/fresh-path" ]]
    fi
  else
    touch "$SERVER_DATABASE" "$SERVER_DATABASE-wal" "$SERVER_DATABASE-shm"
    server_residue_exists
  fi
  if [[ "$layout" == unknown ]]; then printf preserve >"$STATE_DIRECTORY/unknown"; fi
  uninstall_server
  verify_cache_removed "$layout"
  [[ ! -e "$SERVER_DATABASE" && ! -e "$SERVER_DATABASE-wal" && ! -e "$SERVER_DATABASE-shm" ]]
  if [[ "$layout" == directories ]]; then
    [[ -d "$STATE_DIRECTORY" && -e "$task_root/account" && -e "$task_root/group" ]]
  elif [[ "$layout" == unknown ]]; then
    [[ "$(cat "$STATE_DIRECTORY/unknown")" == preserve && -e "$task_root/account" && -e "$task_root/group" ]]
    rm -- "$STATE_DIRECTORY/unknown"
  else
    [[ ! -e "$STATE_DIRECTORY" && ! -e "$task_root/account" && ! -e "$task_root/group" ]]
  fi
  uninstall_server
  [[ ! -e "$STATE_DIRECTORY" && ! -e "$task_root/account" && ! -e "$task_root/group" ]]
done
`
			harnessPath := filepath.Join(rootNative, "update-check-cleanup.sh")
			if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
				t.Fatal(err)
			}
			if output, err := installerFixtureCommand(t, "bash", shellTestPath(harnessPath)).CombinedOutput(); err != nil {
				t.Fatalf("Server update-check cleanup: %v\n%s", err, output)
			}
		})
	}
}

func TestK1DiskProbeLeftover(t *testing.T) {
	for _, entry := range []string{"installer", "generated-helper", "openrc", "rollback-systemd", "rollback-openrc"} {
		for _, layout := range []string{"file", "directory", "symlink"} {
			t.Run(entry+"/"+layout, func(t *testing.T) {
				rootNative := t.TempDir()
				root := shellTestPath(rootNative)
				source := strings.TrimSuffix(strings.TrimSpace(m12InstallerSource(t)), `main "$@"`)
				if entry == "generated-helper" {
					_, helper, ok := strings.Cut(source, "cat <<'HELPER'\n")
					source, _, _ = strings.Cut(helper, "\nreject_deferred_command_on_host \"${1:-}\"\nrequire_root\n")
					if !ok || source == helper {
						t.Fatal("could not locate generated helper definitions")
					}
				}
				if err := os.WriteFile(filepath.Join(rootNative, "source.sh"), []byte(installerFixtureSource(source, root)), 0600); err != nil {
					t.Fatal(err)
				}
				harness := `set -Eeuo pipefail
task_root='` + root + `'
entry='` + entry + `'
layout='` + layout + `'
source "$task_root/source.sh"
require_root_linux_systemd() { :; }
validate_service_user() { :; }
validate_removable_service_user() { :; }
openrc_role_residue() { return 1; }
openrc_stop() { :; }
openrc_safe_parents() { :; }
mountpoint() { return 1; }
stat() {
  # The default namespace hides passwd, so stat cannot resolve UID 0.
  if [[ "$1" == -c && "$2" == %U && "$(command stat -c %u -- "${*: -1}")" == 0 ]]; then
    printf 'root\n'
  else
    command stat "$@"
  fi
}
systemctl() {
  case "$1" in is-active) return 1;; stop|disable|daemon-reload) return 0;; *) return 98;; esac
}
id() { [[ -f "$task_root/account" ]]; }
getent() {
  case "$1:${2:-}" in
    passwd:404-probe) id && printf '404-probe:x:451:451::%s:/usr/sbin/nologin\n' "$STATE_DIRECTORY" ;;
    group:404-probe) [[ -f "$task_root/group" ]] && printf '404-probe:x:451:\n' ;;
    passwd:) return 0 ;;
    *) return 2 ;;
  esac
}
userdel() { rm -- "$task_root/account"; }
groupdel() { rm -- "$task_root/group"; }
rm() {
  # The fixture wrapper rejects symlinks. Validate the parent and permit
  # unlinking only this fixture leaf with the installer's exact arguments.
  if [[ $# == 3 && "$1" == -f && "$2" == -- && "$3" == "$STATE_DIRECTORY/.disk-probe" && -L "$3" ]]; then
    fixture_path "$STATE_DIRECTORY" || return
    command rm "$@"
  else
    fixture_mutate rm "$@"
  fi
}
mkdir -p "$STATE_DIRECTORY" "$CONFIG_DIRECTORY"
probe="$STATE_DIRECTORY/.disk-probe"
touch "$task_root/account" "$task_root/group"
case "$layout" in
  file) touch "$probe" ;;
  directory) mkdir "$probe"; printf preserve >"$probe/unknown" ;;
  symlink) printf preserve >"$task_root/target"; ln -s -- "$task_root/target" "$probe" ;;
esac
if [[ "$entry" != generated-helper ]] && agent_residue_exists; then
  echo 'a disk probe alone was detected as an installed Agent' >&2
  exit 99
fi
case "$entry" in
  installer) uninstall_role agent --confirm-delete-data ;;
  generated-helper) uninstall agent --confirm-delete-data ;;
  openrc) INSTALL_INIT=openrc; uninstall_role agent --confirm-delete-data ;;
  rollback-*)
    set +e
    (
      set -e
      INSTALL_INIT=${entry#rollback-}
      INSTALL_TRANSACTION_ACTIVE=1
      INSTALL_TRANSACTION_ROLE=agent
      INSTALL_TRANSACTION_UNIT=404-probe-agent
      INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=1
      INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=1
      INSTALL_TRANSACTION_USER_CREATED=1
      rollback_install_transaction 42
    )
    status=$?
    set -e
    [[ "$status" == 42 ]]
    ;;
esac
[[ ! -e "$CONFIG_DIRECTORY" ]]
if [[ "$layout" == directory ]]; then
  [[ "$(cat "$probe/unknown")" == preserve ]]
else
  [[ ! -e "$STATE_DIRECTORY" && ! -L "$probe" ]]
fi
if [[ "$layout" == symlink ]]; then [[ "$(cat "$task_root/target")" == preserve ]]; fi
# Fresh-install rollback preserves its new locked account for a retry.
if [[ "$layout" == directory || "$entry" == rollback-* ]]; then
  [[ -f "$task_root/account" && -f "$task_root/group" ]]
else
  [[ ! -e "$task_root/account" && ! -e "$task_root/group" ]]
fi
`
				harnessPath := filepath.Join(rootNative, "disk-probe-cleanup.sh")
				if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
					t.Fatal(err)
				}
				if output, err := installerFixtureCommand(t, "bash", shellTestPath(harnessPath)).CombinedOutput(); err != nil {
					t.Fatalf("Agent disk probe cleanup: %v\n%s", err, output)
				}
			})
		}
	}
}

func runInstallerAgentUninstallFixture(t *testing.T, early bool) {
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
	}
	if _, err := os.Stat(bash); runtime.GOOS == "windows" && err != nil {
		t.Skip("Git Bash is unavailable")
	}
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath(bash); err != nil {
			t.Skip("bash is unavailable")
		}
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.LastIndex(script, "confirm_uninstall() {")
	endMarker := "\njson_string_field() {"
	if early {
		start = strings.Index(script, "confirm_uninstall() {")
		endMarker = "\nsetup_security() {"
	}
	end := strings.Index(script[start:], endMarker)
	if start < 0 || end < 0 {
		t.Fatal("could not extract uninstall implementation")
	}
	functionSource := strings.Replace(script[start:start+end], "\nuninstall() {", "\nuninstall_role() {", 1)
	if !early {
		const signature = "\nremove_disk_probe_file() {\n"
		_, definitions, found := strings.Cut(script, signature)
		body, _, closed := strings.Cut(definitions, "\n}\n")
		if !found || !closed {
			t.Fatal("could not extract installer disk probe cleanup")
		}
		functionSource = signature + body + "\n}\n" + functionSource
	}
	rootNative := t.TempDir()
	root := shellTestPath(rootNative)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.bootstrap"`, `"${TEST_AGENT_BOOTSTRAP}"`)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.candidate"`, `"${TEST_AGENT_CANDIDATE}"`)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.previous"`, `"${TEST_AGENT_PREVIOUS}"`)
	functionSource = installerFixtureSource(functionSource, root)
	functionPath := filepath.Join(rootNative, "uninstall-function.sh")
	if err := os.WriteFile(functionPath, []byte(functionSource), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `#!/usr/bin/env bash
set -Eeuo pipefail
task_root='` + root + `'
SERVICE_USER=404-probe
CONFIG_DIRECTORY="${task_root}/etc/404-probe"
STATE_DIRECTORY="${task_root}/var/lib/404-probe"
SERVER_UNIT="${task_root}/units/server.service"
AGENT_UNIT="${task_root}/units/agent.service"
SERVER_BINARY="${task_root}/bin/server"
AGENT_BINARY="${task_root}/bin/agent"
INSTALL_HELPER="${task_root}/sbin/install-helper"
SELECTOR_ORDER_FILE="${CONFIG_DIRECTORY}/selector-order.json"
AGENT_UPDATER_UNIT="${task_root}/units/agent-updater.service"
AGENT_UPDATER_SOCKET="${task_root}/run/agent-updater.sock"
AGENT_UPDATER_STATE="${task_root}/var/lib/updater"
AGENT_REMOVAL_WORKER_UNIT="${task_root}/units/agent-removal.service"
AGENT_REMOVAL_FINALIZER_UNIT="${task_root}/units/agent-removal-finalize.service"
AGENT_REMOVAL_STATE_DIRECTORY="${AGENT_UPDATER_STATE}/removal"
AGENT_REMOVAL_REQUEST_FILE="${AGENT_REMOVAL_STATE_DIRECTORY}/request.json"
AGENT_REMOVAL_FINISHED_MARKER="${AGENT_REMOVAL_STATE_DIRECTORY}/agent-uninstalled"
AGENT_REMOVAL_WORKER_BINARY="${task_root}/bin/agent-removal-worker"
SECURITY_STATE_DIRECTORY="${task_root}/var/lib/security"
SECURITY_SERVICE_UNIT="${task_root}/units/security.service"
SECURITY_TIMER_UNIT="${task_root}/units/security.timer"
SERVER_DATABASE="${STATE_DIRECTORY}/404-probe.db"
SERVER_UPGRADE_CANDIDATE="${task_root}/bin/server.candidate"
SERVER_UPGRADE_HELPER_CANDIDATE="${task_root}/sbin/install-helper.candidate"
SERVER_UPGRADE_DIRECTORY="${task_root}/var/lib/server-upgrade"
SERVER_UPGRADE_LOCK_DIRECTORY="${task_root}/run/server-upgrade"
TEST_AGENT_BOOTSTRAP="${task_root}/bin/agent.bootstrap"
TEST_AGENT_CANDIDATE="${task_root}/bin/agent.candidate"
TEST_AGENT_PREVIOUS="${task_root}/bin/agent.previous"
mkdir -p "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" "${task_root}/units" "${task_root}/bin" "${task_root}/sbin" "${AGENT_UPDATER_STATE}" "${SECURITY_STATE_DIRECTORY}" "${task_root}/run"
touch "${SERVER_UNIT}" "${AGENT_UNIT}" "${SERVER_BINARY}" "${AGENT_BINARY}" "${INSTALL_HELPER}" "${CONFIG_DIRECTORY}/agent.env" "${STATE_DIRECTORY}/agent.epoch" "${AGENT_UPDATER_UNIT}" "${AGENT_REMOVAL_FINALIZER_UNIT}" "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" "${TEST_AGENT_BOOTSTRAP}" "${TEST_AGENT_CANDIDATE}" "${TEST_AGENT_PREVIOUS}"
printf active >"${task_root}/agent.state"
require_root_linux_systemd() { :; }
validate_service_user() { :; }
validate_removable_service_user() { :; }
server_residue_exists() { [[ -e "${SERVER_UNIT}" || -e "${SERVER_BINARY}" ]]; }
remove_service_account_for_uninstall() { printf account-cleanup >>"${task_root}/unexpected"; return 1; }
die() { printf '%s\n' "$*" >&2; return 1; }
note() { :; }
mountpoint() { return 1; }
stat() { if [[ "$1" == -c && "$2" == %U ]]; then printf 'root\n'; else command stat "$@"; fi; }
id() { return 0; }
getent() { return 0; }
userdel() { printf userdel >>"${task_root}/unexpected"; return 1; }
groupdel() { printf groupdel >>"${task_root}/unexpected"; return 1; }
systemctl() {
  local action="$1"; shift
  case "${action}" in
    is-active)
      while [[ "${1:-}" == --* ]]; do shift; done
      [[ "${1:-}" == 404-probe-agent.service && "$(cat "${task_root}/agent.state")" == active ]]
      ;;
    stop) printf inactive >"${task_root}/agent.state" ;;
    disable|daemon-reload) return 0 ;;
    *) return 0 ;;
  esac
}
mkdir -p "${task_root}/system/run/404-probe"
touch "${task_root}/system/run/404-probe/private-socket-sentinel"
source '` + shellTestPath(functionPath) + `'
uninstall_role agent --confirm-delete-data
uninstall_role agent --confirm-delete-data
[[ -e "${SERVER_UNIT}" && -e "${SERVER_BINARY}" && -e "${INSTALL_HELPER}" ]]
[[ ! -e "${AGENT_UNIT}" && ! -e "${AGENT_BINARY}" && ! -e "${TEST_AGENT_BOOTSTRAP}" && ! -e "${TEST_AGENT_CANDIDATE}" && ! -e "${TEST_AGENT_PREVIOUS}" ]]
[[ ! -e "${AGENT_UPDATER_STATE}" && ! -e "${SECURITY_STATE_DIRECTORY}" && ! -e "${AGENT_REMOVAL_FINALIZER_UNIT}" ]]
[[ ! -e "${task_root}/unexpected" && ! -e "${task_root}/system/run/404-probe" ]]
`
	harnessPath := filepath.Join(rootNative, "uninstall.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}
	command := installerFixtureCommand(t, bash, shellTestPath(harnessPath))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("uninstall harness failed: %v\n%s", err, output)
	}
}
