package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupSecurityFailureRestoresStoppedAgentAndActiveTimer(t *testing.T) {
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
	installerPath := filepath.Join(filepath.Dir(filename), "..", "..", "install.sh")
	content, err := os.ReadFile(installerPath)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.Index(script, "setup_security_existing() (")
	endMarker := "\n)\n\nbootstrap_existing_agent()"
	end := strings.Index(script[start:], endMarker)
	if start < 0 || end < 0 {
		t.Fatal("could not extract setup_security_existing")
	}
	functionSource := script[start : start+end+3]

	testParent := filepath.Join(filepath.Dir(filename), "..", "..", ".tools")
	rootNative, err := os.MkdirTemp(testParent, "installer-rollback-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootNative) })
	root := shellTestPath(rootNative)
	functionPath := filepath.Join(rootNative, "setup-function.sh")
	// Keep the extracted production function separate so the harness cannot
	// accidentally execute install.sh's main dispatcher.
	if err := os.WriteFile(functionPath, []byte(functionSource), 0600); err != nil {
		t.Fatal(err)
	}
	functionPath = shellTestPath(functionPath)
	harness := `#!/usr/bin/env bash
set -Eeuo pipefail
task_root='` + root + `'
mkdir -p "${task_root}/config" "${task_root}/units" "${task_root}/state" "${task_root}/tmp"
export TMPDIR="${task_root}/tmp"
SERVICE_USER=404-probe
DEFAULT_VERSION=v0.9.0
AGENT_BINARY="${task_root}/agent"
AGENT_UNIT="${task_root}/units/agent.service"
CONFIG_DIRECTORY="${task_root}/config"
STATE_DIRECTORY="${task_root}/state"
INSTALL_HELPER="${task_root}/install-helper"
SECURITY_STATE_DIRECTORY="${task_root}/security"
SECURITY_EXPORT_DIRECTORY="${SECURITY_STATE_DIRECTORY}/export"
SECURITY_SERVICE_UNIT="${task_root}/units/security.service"
SECURITY_TIMER_UNIT="${task_root}/units/security.timer"
printf '#!/usr/bin/env bash\nprintf '\''{"version":"v0.9.0","commit":"testcommit","dirty":false}\\n'\''\n' >"${AGENT_BINARY}"
chmod 755 "${AGENT_BINARY}"
printf '[Service]\nUser=404-probe\nGroup=404-probe\nExecStart=%s --interval 10s\nEnvironmentFile=%s/agent.env\n[Install]\n' "${AGENT_BINARY}" "${CONFIG_DIRECTORY}" >"${AGENT_UNIT}"
printf 'PROBE_404_AGENT_ID=fixture\nPROBE_404_TOKEN=fixture\n' >"${CONFIG_DIRECTORY}/agent.env"
printf 'original service\n' >"${SECURITY_SERVICE_UNIT}"
printf 'original timer\n' >"${SECURITY_TIMER_UNIT}"
printf active >"${task_root}/timer.active"
printf enabled >"${task_root}/timer.enabled"
printf inactive >"${task_root}/agent.active"
printf inactive >"${task_root}/collector.active"
require_root_linux_systemd() { :; }
validate_service_user() { :; }
die() { return 1; }
note() { :; }
wait_for_service() { :; }
render_local_helper() { printf '#!/usr/bin/env bash\nexit 0\n'; }
chown() {
  if [[ "${FAIL_STAGE:-permission}" == chown && "$*" == *agent-security-env* ]]; then return 1; fi
  return 0
}
cp() {
  local destination="${@: -1}"
  [[ ! -e "${destination}" ]] || chmod u+w "${destination}"
  command cp -f "$@"
}
stat() {
  case "${@: -1}" in
    "${AGENT_BINARY}") printf 'root:root:755\n';;
    "${AGENT_UNIT}") printf 'root:root:644\n';;
    "${CONFIG_DIRECTORY}/agent.env") printf '404-probe:404-probe:400\n';;
    "${SECURITY_SERVICE_UNIT}"|"${SECURITY_TIMER_UNIT}") printf 'root:root:644\n';;
    *) command stat "$@";;
  esac
}
install_security_collector() {
  mkdir -p "${SECURITY_EXPORT_DIRECTORY}"
  printf changed-service >"${SECURITY_SERVICE_UNIT}"
  printf changed-timer >"${SECURITY_TIMER_UNIT}"
  printf '{}\n' >"${SECURITY_EXPORT_DIRECTORY}/current.json"
}
runuser() { return 1; }
systemctl() {
  local action="$1"; shift
  if [[ "${action}" == is-active ]]; then
    shift
    case "$1" in
      404-probe-agent.service) [[ "$(cat "${task_root}/agent.active")" == active ]];;
      404-probe-security-collect.timer) [[ "$(cat "${task_root}/timer.active")" == active ]];;
      404-probe-security-collect.service) [[ "$(cat "${task_root}/collector.active")" == active ]];;
    esac
    return
  fi
  if [[ "${action}" == is-enabled ]]; then shift; [[ "$(cat "${task_root}/timer.enabled")" == enabled ]]; return; fi
  case "${action}" in
    stop)
      for unit in "$@"; do case "${unit}" in *agent.service) printf inactive >"${task_root}/agent.active";; *timer) printf inactive >"${task_root}/timer.active";; *collect.service) printf inactive >"${task_root}/collector.active";; esac; done;;
    disable) printf disabled >"${task_root}/timer.enabled";;
    enable) printf enabled >"${task_root}/timer.enabled";;
    start)
      for unit in "$@"; do case "${unit}" in *timer) printf active >"${task_root}/timer.active";; *collect.service) printf active >"${task_root}/collector.active";; esac; done;;
    restart) printf active >"${task_root}/agent.active";;
  esac
}
source '` + functionPath + `'
setup_security_existing
`
	harnessPath := filepath.Join(rootNative, "rollback.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"permission", "chown"} {
		t.Run(stage, func(t *testing.T) {
			command := exec.Command(bash, shellTestPath(harnessPath))
			command.Env = append(os.Environ(), "FAIL_STAGE="+stage)
			output, commandErr := command.CombinedOutput()
			if commandErr == nil {
				t.Fatalf("injected %s failure unexpectedly succeeded: %s", stage, output)
			}
			if strings.Contains(string(output), "unbound variable") {
				t.Fatalf("rollback used an uninitialized candidate: %s", output)
			}
			for name, expected := range map[string]string{
				"timer.active": "active", "timer.enabled": "enabled", "agent.active": "inactive",
				"units/security.service": "original service\n", "units/security.timer": "original timer\n",
			} {
				data, err := os.ReadFile(filepath.Join(rootNative, filepath.FromSlash(name)))
				if err != nil || string(data) != expected {
					t.Fatalf("rollback %s=%q err=%v want %q; harness output=%s", name, data, err, expected, output)
				}
			}
			if _, err := os.Stat(filepath.Join(rootNative, "install-helper")); !os.IsNotExist(err) {
				t.Fatalf("new helper survived rollback: %v", err)
			}
			temporaryEntries, err := os.ReadDir(filepath.Join(rootNative, "tmp"))
			if err != nil || len(temporaryEntries) != 0 {
				t.Fatalf("backup or helper candidates survived rollback: entries=%v err=%v", temporaryEntries, err)
			}
			for _, pattern := range []string{"config/.agent-security-env.*", "units/.agent-security-unit.*"} {
				matches, err := filepath.Glob(filepath.Join(rootNative, filepath.FromSlash(pattern)))
				if err != nil || len(matches) != 0 {
					t.Fatalf("candidate survived rollback for %s: matches=%v err=%v", pattern, matches, err)
				}
			}
			environment, err := os.ReadFile(filepath.Join(rootNative, "config", "agent.env"))
			if err != nil || strings.Contains(string(environment), "PROBE_404_SECURITY_") {
				t.Fatalf("Agent environment was not restored: %q err=%v", environment, err)
			}
			agentUnit, err := os.ReadFile(filepath.Join(rootNative, "units", "agent.service"))
			if err != nil || strings.Contains(string(agentUnit), "ReadOnlyPaths=") {
				t.Fatalf("Agent unit was not restored: %q err=%v", agentUnit, err)
			}
		})
	}
}

func shellTestPath(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS != "windows" {
		return path
	}
	volume := filepath.VolumeName(path)
	return "/" + strings.ToLower(strings.TrimSuffix(volume, ":")) + "/" + strings.TrimPrefix(path[len(volume):], "/")
}
