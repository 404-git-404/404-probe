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
	end := strings.Index(script[start:], "\njson_string_field() {")
	if start < 0 || end < 0 {
		t.Fatal("could not extract uninstall implementation")
	}
	functionSource := script[start : start+end]
	rootNative := t.TempDir()
	root := shellTestPath(rootNative)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.bootstrap"`, `"${TEST_AGENT_BOOTSTRAP}"`)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.candidate"`, `"${TEST_AGENT_CANDIDATE}"`)
	functionSource = strings.ReplaceAll(functionSource, `"/usr/local/bin/.404-probe-agent.previous"`, `"${TEST_AGENT_PREVIOUS}"`)
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
touch "${SERVER_UNIT}" "${AGENT_UNIT}" "${SERVER_BINARY}" "${AGENT_BINARY}" "${INSTALL_HELPER}" "${CONFIG_DIRECTORY}/agent.env" "${STATE_DIRECTORY}/agent.epoch" "${AGENT_UPDATER_UNIT}" "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" "${TEST_AGENT_BOOTSTRAP}" "${TEST_AGENT_CANDIDATE}" "${TEST_AGENT_PREVIOUS}"
printf active >"${task_root}/agent.state"
require_root_linux_systemd() { :; }
validate_service_user() { :; }
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
source '` + shellTestPath(functionPath) + `'
uninstall_role agent --confirm-delete-data
uninstall_role agent --confirm-delete-data
[[ -e "${SERVER_UNIT}" && -e "${SERVER_BINARY}" && -e "${INSTALL_HELPER}" ]]
[[ ! -e "${AGENT_UNIT}" && ! -e "${AGENT_BINARY}" && ! -e "${TEST_AGENT_BOOTSTRAP}" && ! -e "${TEST_AGENT_CANDIDATE}" && ! -e "${TEST_AGENT_PREVIOUS}" ]]
[[ ! -e "${AGENT_UPDATER_STATE}" && ! -e "${SECURITY_STATE_DIRECTORY}" ]]
[[ ! -e "${task_root}/unexpected" ]]
`
	harnessPath := filepath.Join(rootNative, "uninstall.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, shellTestPath(harnessPath))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("uninstall harness failed: %v\n%s", err, output)
	}
}
