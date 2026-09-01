package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerSecureAgentEntryContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`readonly DEFAULT_VERSION="v0.8.0"`,
		`404-probe-install agent --server <origin>`,
		`[[ $# -eq 2 && "$1" == "--server" ]]`,
		`IFS= read -r -s enrollment </dev/tty`,
		`install_agent "$2"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing secure Agent contract %q", required)
		}
	}
	if strings.Contains(script, "--enrollment") || strings.Contains(script, "--token PERMANENT_SECRET") {
		t.Fatal("installer accepts an Agent credential through command arguments")
	}
}

func TestInstallerAgentServiceRestartsOnlyAfterFailure(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.Index(script, `ExecStart=${AGENT_BINARY} --interval`)
	if start < 0 {
		t.Fatal("installer is missing the Agent service ExecStart")
	}
	end := strings.Index(script[start:], "\n[Install]")
	if end < 0 {
		t.Fatal("installer is missing the Agent service install section")
	}
	service := script[start : start+end]
	if !strings.Contains(service, "\nRestart=on-failure\n") {
		t.Fatalf("Agent service must restart crashes but not a clean revoked exit:\n%s", service)
	}
	if strings.Contains(service, "Restart=always") {
		t.Fatalf("Agent service would restart after a clean revoked exit:\n%s", service)
	}
}

func TestInstallerConfiguresOptionalAgentLocalClashAPI(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`valid_sing_box_clash_api_url`,
		`IFS= read -r clash_api </dev/tty`,
		`IFS= read -r -s clash_secret </dev/tty`,
		`PROBE_404_SING_BOX_CLASH_API=$(quote_environment_value "${clash_api}")`,
		`PROBE_404_SING_BOX_CLASH_SECRET=$(quote_environment_value "${clash_secret}")`,
		`unset clash_secret clash_environment`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing Agent-local Clash contract %q", required)
		}
	}
	if strings.Contains(script, "--sing-box-clash-secret <") || strings.Contains(script, "--sing-box-clash-secret=") {
		t.Fatal("installer exposes the Clash API secret through command arguments")
	}
}

func TestInstallerAgentUninstallIsExplicitCompleteAndIdempotent(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`404-probe-install uninstall <server|agent>`,
		`systemctl is-active --quiet "${unit}"`,
		`systemctl disable "${unit}" >/dev/null 2>&1 || true`,
		`rm -f -- "${unit_path}" "${binary_path}"`,
		`"${CONFIG_DIRECTORY}/agent.env"`,
		`"${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock"`,
		`Re-running this command is safe.`,
		`Preserved systemd journal history`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing uninstall contract %q", required)
		}
	}
	for _, forbidden := range []string{`journalctl --vacuum`, `rm -rf -- "${STATE_DIRECTORY}"`, `systemctl disable --now "${unit}"`} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("installer uninstall violates preservation or idempotency through %q", forbidden)
		}
	}
}

func TestInstallerBootstrapsUpdaterWithoutExpandingItsAuthority(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`bootstrap_existing_agent`,
		`version --json >/dev/null 2>&1`,
		`ExecStart=${AGENT_BINARY} updater`,
		`Restart=always`,
		`RuntimeDirectory=404-probe`,
		`ReadWritePaths=${AGENT_UPDATER_STATE} /usr/local/bin /run/404-probe`,
		`NoNewPrivileges=true`,
		`ProtectSystem=strict`,
		`systemctl enable --now 404-probe-agent-updater.service`,
		`verify_agent_authentication`,
		`rm -f -- "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}"`,
		`cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing updater/bootstrap contract %q", required)
		}
	}
	for _, forbidden := range []string{`--download-url`, `--binary-path`, `--command`} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("installer expands updater authority through %q", forbidden)
		}
	}
}
