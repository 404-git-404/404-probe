package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerTwoDigitUpgradeAndNonUpgradePreserveInstallation(t *testing.T) {
	for _, row := range []struct {
		from, to         string
		success, changed bool
	}{{"v1.0.9", "v1.0.10", true, true}, {"v1.0.10", "v1.0.9", false, false}, {"v1.0.10", "v1.0.10", true, false}} {
		t.Run(row.from+"-"+row.to, func(t *testing.T) {
			rootNative := t.TempDir()
			root := shellTestPath(rootNative)
			source := m12InstallerSource(t)
			source = strings.TrimSuffix(strings.TrimSpace(source), `main "$@"`)
			source = installerFixtureSource(source, root)
			// The test owns confirmation. The actual transaction and version gate
			// remain intact; its terminal is a private file, never the host tty.
			source = strings.ReplaceAll(source, "/dev/tty", root+"/tty")
			if err := os.WriteFile(filepath.Join(rootNative, "installer.sh"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			harness := `set -Eeuo pipefail
task_root='` + root + `'
source "${task_root}/installer.sh"
CURRENT='` + row.from + `'
export PROBE_404_VERSION='` + row.to + `'
mkdir -p "$(dirname "$SERVER_BINARY")" "$(dirname "$INSTALL_HELPER")" "$(dirname "$SERVER_UNIT")" "$CONFIG_DIRECTORY" "$STATE_DIRECTORY"
printf original-binary >"$SERVER_BINARY"; chmod 755 "$SERVER_BINARY"
printf original-helper >"$INSTALL_HELPER"; chmod 755 "$INSTALL_HELPER"
printf original-database >"$SERVER_DATABASE"
printf private-config >"$CONFIG_DIRECTORY/server.env"
printf '[Service]\nUser=%s\nExecStart=%s serve --listen 127.0.0.1:8080 --db %s\n' "$SERVICE_USER" "$SERVER_BINARY" "$SERVER_DATABASE" >"$SERVER_UNIT"
chmod 644 "$SERVER_UNIT"
sha256sum "$SERVER_BINARY" "$SERVER_DATABASE" "$CONFIG_DIRECTORY/server.env" "$INSTALL_HELPER" >"$task_root/before"
require_root_linux_systemd(){ :; }
reject_openrc_deferred(){ :; }
validate_service_user(){ :; }
# Only answer the confirmation prompt; version parsing and backup loops need
# Bash's real read builtin and must not be intercepted by this fixture.
read(){ if [[ "$#" == 2 && "$1" == -r && "$2" == answer ]]; then answer=y; else builtin read "$@"; fi; }
note(){ printf '%s\n' "$*" >>"$task_root/notes"; }
die(){ printf '%s\n' "$*" >&2; exit 1; }
stat(){
 if [[ "$1" == -c && "$2" == '%U:%G:%a' ]]; then printf 'root:root:%s\n' "$(command stat -c %a "${@: -1}")";
 else command stat "$@"; fi
}
install(){
 local args=()
 while (($#)); do case "$1" in -o|-g) shift 2;; *) args+=("$1");shift;; esac;done
 command install "${args[@]}"
}
chown(){ :; }
identify_installed_server_version(){ printf '%s\n' "$CURRENT"; }
download_server_upgrade_candidate(){
 printf '#!/usr/bin/env bash\nprintf '\''{"version":"%s","dirty":false}\n'\''\n' "$1" >"$SERVER_UPGRADE_CANDIDATE"
 chmod 755 "$SERVER_UPGRADE_CANDIDATE"
}
render_local_helper(){ printf '#!/usr/bin/env bash\nexit 0\n'; }
runuser(){
 printf '%s\n' "$*" >>"$task_root/events"
 [[ "$*" != *migrate-protected* ]] || printf migrated >"$SERVER_DATABASE"
}
systemctl(){
 printf '%s\n' "$*" >>"$task_root/events"
 case "$1" in is-active) [[ "${2:-}" != --quiet ]] && printf active || return 1;; is-enabled) printf enabled;; esac
 return 0
}
database_has_open_handles(){ return 1; }
existing_regular_files_kb(){ printf 1; }
df(){ printf 'Filesystem 1024-blocks Used Available Capacity Mounted\nfixture 999999999 0 999999999 0%% /\n'; }
create_server_upgrade_backup_manifest(){ :; }
verify_server_upgrade_backup(){ :; }
write_server_upgrade_state(){ printf '%s\n' "$*" >>"$task_root/events"; }
clear_server_upgrade_state(){ printf committed >>"$task_root/events"; }
server_upgrade_trap(){ :; }
verify_server_stable_readiness(){ :; }
status=0
(upgrade_existing_server) || status=$?
if [[ "$CURRENT" == v1.0.9 ]]; then
 [[ "$status" == 0 ]]
 grep -Fq '"version":"v1.0.10"' "$SERVER_BINARY"
 [[ "$(cat "$SERVER_DATABASE")" == migrated ]]
 grep -Fq 'stop 404-probe-server.service' "$task_root/events"
 grep -Fq committed "$task_root/events"
 [[ "$(cat "$CONFIG_DIRECTORY/server.env")" == private-config ]]
else
 sha256sum -c "$task_root/before"
 [[ ! -e "$task_root/events" ]]
 if [[ "$PROBE_404_VERSION" == v1.0.9 ]]; then [[ "$status" != 0 ]]; else [[ "$status" == 0 ]]; fi
fi
`
			path := filepath.Join(rootNative, "digits.sh")
			if err := os.WriteFile(path, []byte(harness), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := installerFixtureCommand(t, "bash", shellTestPath(path)).CombinedOutput()
			if err != nil {
				t.Fatalf("installer boundary: %v\n%s", err, output)
			}
		})
	}
}
