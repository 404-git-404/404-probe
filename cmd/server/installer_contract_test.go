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
		`readonly DEFAULT_VERSION="v0.9.2"`,
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
	for _, required := range []string{
		`country_code="$("${AGENT_BINARY}" country-code lookup 2>/dev/null)"`,
		`PROBE_404_COUNTRY_CODE=${country_code}`,
		`installation will continue with an unknown location`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing one-time country lookup contract %q", required)
		}
	}
	if strings.Count(script, `country-code lookup`) != 1 {
		t.Fatalf("country lookup must occur exactly once in the fresh-install path")
	}
	installStart := strings.Index(script, "install_agent() {")
	installEnd := strings.Index(script[installStart:], "\ninstall_agent_command() {")
	if installStart < 0 || installEnd < 0 {
		t.Fatal("could not isolate install_agent")
	}
	installAgent := script[installStart : installStart+installEnd]
	existingBootstrap := strings.Index(installAgent, "bootstrap_existing_agent")
	lookup := strings.Index(installAgent, "country-code lookup")
	if existingBootstrap < 0 || lookup < 0 || existingBootstrap > lookup {
		t.Fatal("existing Agent bootstrap can reach the external country lookup")
	}
}

func TestInstallerServerUpgradeTransactionContract(t *testing.T) {
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
		`bootstrap_latest_installer`,
		`latest stable release returned a non-canonical version`,
		`SHA256SUMS does not authenticate ${asset} exactly once`,
		`RELEASE-METADATA.json`,
		`Proceed with this Server upgrade? [y/N]:`,
		`write_server_upgrade_state prepared`,
		`write_server_upgrade_state stopped-unbacked`,
		`write_server_upgrade_state backup-complete`,
		`write_server_upgrade_state migration-started`,
		`write_server_upgrade_state binary-replaced`,
		`write_server_upgrade_state helper-replaced`,
		`database_has_open_handles`,
		`database migrate-copy --db`,
		`database migrate-protected --db`,
		`database verify --db`,
		`restore_server_upgrade`,
		`original_active=`,
		`original_enabled=`,
		`running Server process is not the installed candidate`,
		`helper-state`,
		`SERVER_UPGRADE_HELPER_CANDIDATE`,
		`"${INSTALL_HELPER}" domains list`,
		`verify_legacy_server_upgrade_backup`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing Server upgrade contract %q", required)
		}
	}
	if strings.Contains(script, `PROBE_404_RELEASE_BASE_URL`) {
		t.Fatal("production installer permits an arbitrary release origin")
	}
}

func TestInstallerOfflineDomainManagementContract(t *testing.T) {
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
		`404-probe-install domains list`,
		`404-probe-install domains add <root-domain>`,
		`404-probe-install domains remove <root-domain>`,
		`404-probe-install domains disable`,
		`404-probe login domain management`,
		`runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" web-domain "${action}" --db "${SERVER_DATABASE}"`,
		`Disabling suffix mode will allow only the exact origin`,
		`Configured exact origin:`,
		`Domain policy was not changed; choose another action or exit.`,
		`Domain suffix mode requires an HTTPS public origin`,
		`prompt_web_domain_suffixes`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing domain-management contract %q", required)
		}
	}
	helperStart := strings.Index(script, "cat <<'HELPER'\n")
	helperEnd := strings.Index(script[helperStart+1:], "\nHELPER")
	if helperStart < 0 || helperEnd < 0 {
		t.Fatal("rendered local helper is missing")
	}
	helper := script[helperStart : helperStart+1+helperEnd]
	if strings.Contains(helper, "curl ") || strings.Contains(helper, "bootstrap_latest_installer") {
		t.Fatal("installed domain-management helper requires network bootstrap")
	}
}

func TestReleaseManifestAuthenticatesInstaller(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "build-release-assets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	line := `bash "${ROOT_DIRECTORY}/scripts/write-installer-checksum.sh"` + " \\\n" +
		`  "${installer_path}" "${OUTPUT_DIRECTORY}/SHA256SUMS"`
	if strings.Count(script, line) != 1 {
		t.Fatal("release manifest must authenticate install.sh exactly once")
	}
	if strings.Contains(script, `sha256sum "${installer_path}" | sed`) {
		t.Fatal("release manifest depends on platform-specific sha256sum filename markers")
	}
}

func TestReleaseBuildExportsLFInstallerFromTag(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate release build contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "build-release-assets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`git show "${tag_commit}:install.sh" >"${installer_path}"`,
		`LC_ALL=C grep -q $'\r' "${installer_path}"`,
		`bash -n "${installer_path}"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("release build missing tagged LF installer contract %q", required)
		}
	}
	if strings.Contains(script, `cp install.sh "${OUTPUT_DIRECTORY}`) || strings.Contains(script, `cp "${ROOT_DIRECTORY}/install.sh"`) {
		t.Fatal("release build copies the checkout installer instead of exporting the tagged blob")
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

func TestInstallerUsesZeroConfigurationLocalClashAPI(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, forbidden := range []string{"read -r clash_api", "read -r -s clash_secret", "PROBE_404_SING_BOX_CLASH_API=", "PROBE_404_SING_BOX_CLASH_SECRET=", "--sing-box-clash-secret"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("fresh install must not ask for or store Clash API configuration: %q", forbidden)
		}
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
		`RuntimeDirectoryMode=0755`,
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
	for _, forbidden := range []string{`--download-url`, `--binary-path`, `--command`, `AmbientCapabilities=CAP_SETUID CAP_SETGID`} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("installer expands updater authority through %q", forbidden)
		}
	}
}

func TestInstallerSecurityCollectorIsLocalLeastPrivilegeAndDurable(t *testing.T) {
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
		`install -d -m 0750 -o root -g "${SERVICE_USER}" "${SECURITY_STATE_DIRECTORY}"`,
		`install -d -m 0700 -o root -g root "${SECURITY_STATE_DIRECTORY}/private" "${SECURITY_STATE_DIRECTORY}/private/outbox"`,
		`install -d -m 0750 -o root -g "${SERVICE_USER}" "${SECURITY_EXPORT_DIRECTORY}"`,
		`ExecStart=${AGENT_BINARY} security-collect`,
		`Group=${SERVICE_USER}`,
		`PrivateNetwork=true`,
		`ProtectSystem=strict`,
		`RestrictAddressFamilies=AF_UNIX`,
		"CapabilityBoundingSet=\n",
		"AmbientCapabilities=\n",
		`ReadWritePaths=${SECURITY_STATE_DIRECTORY}`,
		`ReadOnlyPaths=-/var/log/journal -/run/log/journal`,
		`OnCalendar=daily`,
		`Persistent=true`,
		`RandomizedDelaySec=15m`,
		`systemctl start 404-probe-security-collect.service`,
		`systemctl enable --now 404-probe-security-collect.timer`,
		`PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}`,
		`PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json`,
		`404-probe-install setup-security`,
		`setup_security_existing`,
		`runuser -u "${SERVICE_USER}" -- test -r "${SECURITY_EXPORT_DIRECTORY}/current.json"`,
		`runuser -u "${SERVICE_USER}" -- test -w "${SECURITY_EXPORT_DIRECTORY}/current.json"`,
		`ReadOnlyPaths=-${SECURITY_EXPORT_DIRECTORY}`,
		`Agent identity, credential, and epoch state were preserved`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing security collector contract %q", required)
		}
	}
	serviceStart := strings.Index(script, "Description=404-probe local sing-box security audit")
	serviceEnd := strings.Index(script[serviceStart:], "\nEOF")
	if serviceStart < 0 || serviceEnd < 0 {
		t.Fatal("installer security service is missing")
	}
	service := script[serviceStart : serviceStart+serviceEnd]
	for _, forbidden := range []string{"AF_INET", "AF_INET6", "EnvironmentFile=", "--unit", "--command", "--url", "--token"} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("security collector service expands authority through %q:\n%s", forbidden, service)
		}
	}
	helperStart := strings.Index(script, "cat <<'HELPER'\n")
	if helperStart < 0 {
		t.Fatal("rendered installer helper is missing")
	}
	helperBodyStart := helperStart + len("cat <<'HELPER'\n")
	helperEnd := strings.Index(script[helperBodyStart:], "\nHELPER")
	if helperEnd < 0 {
		t.Fatal("rendered installer helper terminator is missing")
	}
	helper := script[helperStart : helperBodyStart+helperEnd]
	for _, required := range []string{
		`setup_security() {`,
		`setup-security) shift; setup_security "$@" ;;`,
		`runuser -u "${SERVICE_USER}" -- test -r "${SECURITY_EXPORT_DIRECTORY}/current.json"`,
		`runuser -u "${SERVICE_USER}" -- test -w "${SECURITY_EXPORT_DIRECTORY}/current.json"`,
	} {
		if !strings.Contains(helper, required) {
			t.Fatalf("rendered helper missing setup-security contract %q", required)
		}
	}
}

func TestInstallerSecuritySetupRollbackPreservesPriorServiceState(t *testing.T) {
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
		`agent_was_active=0 timer_was_active=0 timer_was_enabled=0 collector_was_active=0`,
		`systemctl is-active --quiet 404-probe-agent.service && agent_was_active=1`,
		`systemctl is-active --quiet 404-probe-security-collect.timer && timer_was_active=1`,
		`systemctl is-enabled --quiet 404-probe-security-collect.timer && timer_was_enabled=1`,
		`if (( timer_was_enabled != 0 )); then systemctl enable 404-probe-security-collect.timer`,
		`if (( timer_was_active != 0 )); then systemctl start 404-probe-security-collect.timer`,
		`if (( agent_was_active != 0 )); then systemctl restart 404-probe-agent.service`,
		`else systemctl stop 404-probe-agent.service`,
		`cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("setup-security rollback missing prior-state contract %q", required)
		}
	}
}
