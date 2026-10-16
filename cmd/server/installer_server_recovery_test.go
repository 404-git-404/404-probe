package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func m12InstallerSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate installer")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestInstallerM12OfflineChecksPrecedeCandidateStartup(t *testing.T) {
	s := m12InstallerSource(t)
	start := strings.Index(s, "upgrade_existing_server() (")
	end := strings.Index(s[start:], "\nprint_install_banner() {")
	if start < 0 || end < 0 {
		t.Fatal("cannot isolate upgrade transaction")
	}
	s = s[start : start+end]
	last := -1
	for _, step := range []string{
		`database migrate-protected --db`,
		`write_server_upgrade_state binary-replaced`,
		`"${SERVER_BINARY}" database verify --db`,
		`write_server_upgrade_state helper-replaced`,
		`"${INSTALL_HELPER}" --help`,
		`"${INSTALL_HELPER}" domains list`,
		`systemctl start 404-probe-server.service`,
		`write_server_upgrade_state candidate-started`,
		`wait_for_server_readiness 404-probe-server.service`,
		`running Server process is not the installed candidate`,
		`clear_server_upgrade_state`,
	} {
		idx := strings.Index(s, step)
		if idx <= last {
			t.Fatalf("upgrade step missing or out of order: %s", step)
		}
		last = idx
	}
	if strings.Count(s, `"${INSTALL_HELPER}" domains list`) != 1 {
		t.Fatal("offline domain check must remain present exactly once")
	}
}

// These execute the product recovery function with private files and explicit
// process/readiness faults. Independent Linux tests provide real systemd/HTTP.
func TestInstallerM12RecoveryPreservesPendingUntilHealthy(t *testing.T) {
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
	}
	if _, err := exec.LookPath(bash); err != nil {
		t.Skip("bash unavailable")
	}
	s := m12InstallerSource(t)
	start := strings.Index(s, "verify_restored_server_files() {")
	end := strings.Index(s[start:], "\nserver_upgrade_trap() {")
	if start < 0 || end < 0 {
		t.Fatal("cannot isolate recovery functions")
	}
	rootNative := t.TempDir()
	root := shellTestPath(rootNative)
	functions := filepath.Join(rootNative, "recovery.sh")
	if err := os.WriteFile(functions, []byte(s[start:start+end]), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `set -Eeuo pipefail
task_root='` + root + `'
source '` + shellTestPath(functions) + `'
SERVICE_USER=fixture
SERVER_BINARY="${task_root}/bin/server"
STATE_DIRECTORY="${task_root}/state"
SERVER_DATABASE="${STATE_DIRECTORY}/404-probe.db"
SERVER_UPGRADE_DIRECTORY="${task_root}/upgrade"
SERVER_UPGRADE_BACKUP="${SERVER_UPGRADE_DIRECTORY}/backup"
SERVER_UPGRADE_STATE="${SERVER_UPGRADE_DIRECTORY}/pending"
SERVER_UPGRADE_CANDIDATE="${task_root}/bin/candidate"
SERVER_UPGRADE_HELPER_CANDIDATE="${task_root}/bin/helper.candidate"
INSTALL_HELPER="${task_root}/bin/helper"
mkdir -p "${task_root}/bin" "${STATE_DIRECTORY}" "${SERVER_UPGRADE_BACKUP}/database"
reject_openrc_deferred() { :; }
release_version() { [[ "$1" == v1.0.3 ]]; }
server_upgrade_state_value() { sed -n "s/^$1=//p" "${SERVER_UPGRADE_STATE}"; }
verify_server_upgrade_backup() { [[ -f "${SERVER_UPGRADE_BACKUP}/complete" ]]; }
sync() { :; }
clear_server_upgrade_state() { printf clear >>"${task_root}/events"; rm "${SERVER_UPGRADE_STATE}"; }
runuser() {
  [[ "$4" == "${SERVER_BINARY}" && "$5 $6 $7" == 'database verify --db' && "$8" == "${SERVER_DATABASE}" ]]
  printf verify >>"${task_root}/events"
  [[ "${fault}" != unreadable && "$(cat "${SERVER_DATABASE}")" == old-db ]]
}
systemctl() {
  case "$1" in
    stop) printf inactive >"${task_root}/active" ;;
    start) printf start >>"${task_root}/events"; [[ "${fault}" != start ]] || return 1; printf active >"${task_root}/active" ;;
    is-active) [[ "$(cat "${task_root}/active")" == active ]] ;;
    enable|disable) printf '%s' "$1" >"${task_root}/enabled" ;;
    *) return 98 ;;
  esac
}
verify_restored_server_readiness() {
  printf health >>"${task_root}/events"
  [[ "${fault}" != health ]]
}
prepare() {
  printf old-binary >"${SERVER_UPGRADE_BACKUP}/server-binary"
  printf old-helper >"${SERVER_UPGRADE_BACKUP}/install-helper"
  printf '1\n' >"${SERVER_UPGRADE_BACKUP}/helper-state"
  touch "${SERVER_UPGRADE_BACKUP}/complete"
  printf '404-probe.db\n404-probe.db-wal\n404-probe.db-shm\n' >"${SERVER_UPGRADE_BACKUP}/database-files"
  printf old-db >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db"
  printf old-wal >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db-wal"
  printf old-shm >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db-shm"
  printf new-binary >"${SERVER_BINARY}"
  printf new-db >"${SERVER_DATABASE}"
  printf new-wal >"${SERVER_DATABASE}-wal"
  printf new-shm >"${SERVER_DATABASE}-shm"
  printf 'phase=helper-replaced\noriginal_active=%s\noriginal_enabled=%s\ntarget=v1.0.3\n' "$1" "$2" >"${SERVER_UPGRADE_STATE}"
  : >"${task_root}/events"
  printf inactive >"${task_root}/active"
}
for active in 0 1; do
  for enabled in 0 1; do
    fault=none
    prepare "$active" "$enabled"
    restore_server_upgrade
    [[ ! -e "${SERVER_UPGRADE_STATE}" ]]
    [[ "$(cat "${SERVER_BINARY}")" == old-binary ]]
    [[ "$(cat "${SERVER_DATABASE}-wal")" == old-wal && "$(cat "${SERVER_DATABASE}-shm")" == old-shm ]]
    if (( active )); then
      [[ "$(cat "${task_root}/events")" == verifystarthealthclear && "$(cat "${task_root}/active")" == active ]]
    else
      [[ "$(cat "${task_root}/events")" == verifyclear && "$(cat "${task_root}/active")" == inactive ]]
    fi
    if (( enabled )); then [[ "$(cat "${task_root}/enabled")" == enable ]]; else [[ "$(cat "${task_root}/enabled")" == disable ]]; fi
  done
done
for fault in unreadable start health; do
  prepare 1 0
  if restore_server_upgrade; then printf 'fault unexpectedly passed: %s\n' "$fault"; exit 90; fi
  [[ -f "${SERVER_UPGRADE_STATE}" && -f "${SERVER_UPGRADE_BACKUP}/complete" && "$(cat "${task_root}/active")" == inactive ]]
  [[ "$(cat "${task_root}/events")" != *clear* ]]
  fault=none
  restore_server_upgrade
  [[ ! -e "${SERVER_UPGRADE_STATE}" && "$(cat "${task_root}/active")" == active ]]
done
# Missing/extra/corrupt sidecars and a mismatched binary fail before SQLite.
fault=none
prepare 0 0
cp "${SERVER_UPGRADE_BACKUP}/server-binary" "${SERVER_BINARY}"
cp "${SERVER_UPGRADE_BACKUP}/database/"* "${STATE_DIRECTORY}/"
verify_restored_server_files "${SERVER_UPGRADE_BACKUP}"
printf wrong >>"${SERVER_DATABASE}-wal"
if verify_restored_server_files "${SERVER_UPGRADE_BACKUP}"; then exit 91; fi
cp "${SERVER_UPGRADE_BACKUP}/database/404-probe.db-wal" "${SERVER_DATABASE}-wal"
rm "${SERVER_DATABASE}-shm"
if verify_restored_server_files "${SERVER_UPGRADE_BACKUP}"; then exit 92; fi
cp "${SERVER_UPGRADE_BACKUP}/database/404-probe.db-shm" "${SERVER_DATABASE}-shm"
printf wrong >>"${SERVER_BINARY}"
if verify_restored_server_files "${SERVER_UPGRADE_BACKUP}"; then exit 93; fi
printf 'PASS: four state combinations, failed recovery pending, retry, full sidecar verification\n'
`
	path := filepath.Join(rootNative, "harness.sh")
	if err := os.WriteFile(path, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(bash, shellTestPath(path)).CombinedOutput()
	if err != nil {
		t.Fatalf("recovery harness: %v\n%s", err, output)
	}
	t.Log(string(output))
}
