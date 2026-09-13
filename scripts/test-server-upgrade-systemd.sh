#!/usr/bin/env bash

set -Eeuo pipefail

[[ "${EUID}" -eq 0 ]] || { printf 'run as root\n' >&2; exit 1; }
[[ $# -ge 3 && $# -le 4 ]] || { printf 'usage: %s INSTALLER OLD_SERVER ASSET_DIRECTORY [SOURCE_DATABASE]\n' "$0" >&2; exit 2; }
installer_source="$1"
old_server_source="$2"
asset_directory="$3"
source_database="${4:-}"
expected_agent_listing=""
original_schema=""
old_server_sha="$(sha256sum "${old_server_source}" | awk '{print $1}')"
old_server_version_json="$("${old_server_source}" version --json)"
candidate_sha="$(sha256sum "${asset_directory}/404-probe-server-linux-amd64" | awk '{print $1}')"
old_helper_sha=""

readonly test_root="/var/lib/404-probe-upgrade-rehearsal"
readonly test_config="/etc/404-probe-upgrade-rehearsal"
readonly test_binary_directory="/usr/local/lib/404-probe-upgrade-rehearsal"
readonly test_binary="${test_binary_directory}/404-probe-server"
readonly test_unit="/etc/systemd/system/404-probe-upgrade-rehearsal.service"
readonly test_service="404-probe-upgrade-rehearsal.service"
readonly test_upgrade="/var/lib/404-probe-upgrade-rehearsal-transaction"
readonly test_lock="/run/404-probe-upgrade-rehearsal"
readonly transformed="/tmp/404-probe-upgrade-rehearsal.install.sh"
readonly crash_transformed="/tmp/404-probe-upgrade-rehearsal.crash.sh"

cleanup() {
  trap - EXIT HUP INT TERM
  systemctl stop "${test_service}" >/dev/null 2>&1 || true
  systemctl disable "${test_service}" >/dev/null 2>&1 || true
  rm -f -- "${test_unit}" "${transformed}" "${crash_transformed}"
  rm -rf -- "${test_root}" "${test_config}" "${test_binary_directory}" "${test_upgrade}" "${test_lock}"
  systemctl daemon-reload >/dev/null 2>&1 || true
}
for path in "${test_root}" "${test_config}" "${test_binary_directory}" "${test_unit}" "${test_upgrade}" "${test_lock}"; do
  [[ ! -e "${path}" ]] || { printf 'isolated rehearsal path already exists: %s\n' "${path}" >&2; exit 1; }
done
trap cleanup EXIT HUP INT TERM

transform_installer() {
  sed \
    -e "s|readonly INSTALL_HELPER=\"/usr/local/sbin/404-probe-install\"|readonly INSTALL_HELPER=\"${test_binary_directory}/install-helper\"|" \
    -e "s|readonly SERVER_BINARY=\"/usr/local/bin/404-probe-server\"|readonly SERVER_BINARY=\"${test_binary}\"|" \
    -e "s|readonly AGENT_BINARY=\"/usr/local/bin/404-probe-agent\"|readonly AGENT_BINARY=\"${test_binary_directory}/agent-absent\"|" \
    -e "s|readonly CONFIG_DIRECTORY=\"/etc/404-probe\"|readonly CONFIG_DIRECTORY=\"${test_config}\"|" \
    -e "s|readonly STATE_DIRECTORY=\"/var/lib/404-probe\"|readonly STATE_DIRECTORY=\"${test_root}\"|" \
    -e "s|readonly SERVER_UNIT=\"/etc/systemd/system/404-probe-server.service\"|readonly SERVER_UNIT=\"${test_unit}\"|" \
    -e "s|readonly AGENT_UNIT=\"/etc/systemd/system/404-probe-agent.service\"|readonly AGENT_UNIT=\"${test_binary_directory}/agent.service.absent\"|" \
    -e "s|readonly SERVER_UPGRADE_DIRECTORY=\"/var/lib/404-probe-upgrade\"|readonly SERVER_UPGRADE_DIRECTORY=\"${test_upgrade}\"|" \
    -e "s|readonly SERVER_UPGRADE_CANDIDATE=\"/usr/local/bin/.404-probe-server.candidate\"|readonly SERVER_UPGRADE_CANDIDATE=\"${test_binary_directory}/.candidate\"|" \
    -e "s|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"/run/404-probe-upgrade\"|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"${test_lock}\"|" \
    -e "s/404-probe-server\.service/${test_service}/g" \
    "${installer_source}" >"${transformed}"
  chmod 0700 "${transformed}"
  bash -n "${transformed}"
}

setup_old_server() {
  local created first_id
  systemctl stop "${test_service}" >/dev/null 2>&1 || true
  systemctl disable "${test_service}" >/dev/null 2>&1 || true
  rm -f -- "${test_unit}"
  rm -rf -- "${test_root}" "${test_config}" "${test_binary_directory}" "${test_upgrade}" "${test_lock}"
  install -d -m 0700 -o 404-probe -g 404-probe "${test_root}"
  install -d -m 0750 -o root -g 404-probe "${test_config}"
  install -d -m 0755 -o root -g root "${test_binary_directory}"
  install -m 0755 -o root -g root "${old_server_source}" "${test_binary}"
  printf '#!/usr/bin/env bash\nprintf "old helper fixture\\n"\n' >"${test_binary_directory}/install-helper"
  chmod 0755 "${test_binary_directory}/install-helper"
  old_helper_sha="$(sha256sum "${test_binary_directory}/install-helper" | awk '{print $1}')"
  printf 'PROBE_404_WEB_PUBLIC_ORIGIN=https://legacy.navolyn.com\n' >"${test_config}/server.env"
  chown root:404-probe "${test_config}/server.env"
  chmod 0640 "${test_config}/server.env"
  if [[ -n "${source_database}" ]]; then
    python3 - "${source_database}" "${test_root}/404-probe.db" <<'PY'
import sqlite3
import sys
source = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
destination = sqlite3.connect(sys.argv[2])
with destination:
    source.backup(destination)
destination.close()
source.close()
PY
    chown 404-probe:404-probe "${test_root}/404-probe.db"
    chmod 0600 "${test_root}/404-probe.db"
  else
    runuser -u 404-probe -- "${test_binary}" agent add isolated-active-a --db "${test_root}/404-probe.db" >/dev/null
    runuser -u 404-probe -- "${test_binary}" agent add isolated-active-b --db "${test_root}/404-probe.db" >/dev/null
    created="$(runuser -u 404-probe -- "${test_binary}" agent add isolated-revoked --db "${test_root}/404-probe.db")"
    first_id="$(printf '%s\n' "${created}" | sed -n 's/^Agent ID: //p')"
    unset created
    runuser -u 404-probe -- "${test_binary}" agent revoke "${first_id}" --db "${test_root}/404-probe.db" >/dev/null
    unset first_id
  fi
  expected_agent_listing="$(runuser -u 404-probe -- "${test_binary}" agent list --db "${test_root}/404-probe.db")"
  original_schema="$(database_schema)"
  cat >"${test_unit}" <<EOF
[Unit]
Description=404-probe isolated upgrade rehearsal

[Service]
Type=simple
User=404-probe
Group=404-probe
EnvironmentFile=${test_config}/server.env
ExecStart=${test_binary} serve --listen 127.0.0.1:33444 --db ${test_root}/404-probe.db --offline-timeout 30s
Restart=on-failure
ReadWritePaths=${test_root}

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${test_unit}"
  systemctl daemon-reload
  systemctl enable --now "${test_service}" >/dev/null
  for _ in $(seq 1 20); do
    [[ "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:33444/api/v1/agent/jobs/claim || true)" == 401 ]] && return
    sleep 0.25
  done
  printf 'isolated old Server did not become ready\n' >&2
  exit 1
}

database_schema() {
  python3 - "${test_root}/404-probe.db" <<'PY'
import sqlite3
import sys
database = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
print(database.execute("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").fetchone()[0])
database.close()
PY
}

run_upgrade() {
  printf 'y\n' | script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null
}

assert_upgraded() {
  systemctl is-active --quiet "${test_service}"
  systemctl is-enabled --quiet "${test_service}"
  "${test_binary}" version --json | grep -Fq '"version":"v0.9.3"'
  runuser -u 404-probe -- "${test_binary}" database verify --db "${test_root}/404-probe.db" | grep -Fq '"integrity":"ok"'
  [[ "$(database_schema)" == 14 ]]
  [[ "$(runuser -u 404-probe -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
  "${test_binary_directory}/install-helper" --help | grep -Fq 'domains [list|add|remove|disable]'
  "${test_binary_directory}/install-helper" domains list | grep -Fq 'Mode: exact'
}

test_domain_menu() {
  local output
  output="$(printf '2\na.navolyn.com\n2\nnavolyn.com\n3\nnavolyn.com\ny\n2\nexample.com\n1\n3\nexample.com\ny\n4\ny\n5\n' | \
    script -qec "${test_binary_directory}/install-helper domains" /dev/null)"
  printf '%s\n' "${output}" | grep -Fq '404-probe login domain management'
  printf '%s\n' "${output}" | grep -Fq 'domain suffix must be a registrable domain, not a public suffix or subdomain'
  printf '%s\n' "${output}" | grep -Fq 'cannot remove the last Web domain suffix'
  [[ "$(printf '%s\n' "${output}" | grep -Fc 'Domain policy was not changed; choose another action or exit.')" -ge 2 ]]
  printf '%s\n' "${output}" | grep -Fq 'navolyn.com'
  printf '%s\n' "${output}" | grep -Fq 'example.com'
  "${test_binary_directory}/install-helper" domains list | grep -Fq 'Mode: exact'
  "${test_binary_directory}/install-helper" domains add navolyn.com | grep -Fq 'suffix mode is active'
  if "${test_binary_directory}/install-helper" domains remove navolyn.com >/dev/null 2>&1; then
    printf 'helper removed the final suffix\n' >&2
    exit 1
  fi
  printf 'y\n' | script -qec "${test_binary_directory}/install-helper domains disable" /dev/null >/dev/null
  "${test_binary_directory}/install-helper" domains list | grep -Fq 'Mode: exact'
}

transform_installer
printf 'fixture old_sha=%s candidate_sha=%s\n' "${old_server_sha}" "${candidate_sha}"
setup_old_server
printf 'active path original_schema=%s\n' "${original_schema}"
run_upgrade
assert_upgraded
printf 'isolated active+enabled upgrade passed migrated_schema=%s\n' "$(database_schema)"

setup_old_server
systemctl disable --now "${test_service}" >/dev/null
run_upgrade
[[ "$(systemctl is-active "${test_service}" 2>/dev/null || true)" == inactive ]]
[[ "$(systemctl is-enabled "${test_service}" 2>/dev/null || true)" == disabled ]]
"${test_binary}" version --json | grep -Fq '"version":"v0.9.3"'
runuser -u 404-probe -- "${test_binary}" database verify --db "${test_root}/404-probe.db" | grep -Fq '"integrity":"ok"'
[[ "$(database_schema)" == 14 ]]
[[ "$(runuser -u 404-probe -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
printf 'isolated inactive+disabled upgrade passed migrated_schema=%s\n' "$(database_schema)"

setup_old_server
cp -- "${transformed}" "${crash_transformed}"
sed -i '/write_server_upgrade_state binary-replaced .*could not persist binary replacement state/a\  if [[ "${PROBE_404_TEST_CRASH_PHASE:-}" == binary-replaced ]]; then kill -KILL "${BASHPID}"; fi' "${crash_transformed}"
chmod 0700 "${crash_transformed}"
if printf 'y\n' | script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=binary-replaced bash ${crash_transformed}" /dev/null; then
  printf 'SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
[[ -f "${test_upgrade}/pending" ]] || { printf 'SIGKILL did not preserve pending state\n' >&2; exit 1; }
if script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
  printf 'recovery rerun unexpectedly continued past conservative recovery\n' >&2
  exit 1
fi
systemctl is-active --quiet "${test_service}"
systemctl is-enabled --quiet "${test_service}"
[[ "$("${test_binary}" version --json)" == "${old_server_version_json}" ]] || { printf 'old Server version was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(database_schema)" == "${original_schema}" ]] || { printf 'pre-migration schema was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(sha256sum "${test_binary}" | awk '{print $1}')" == "${old_server_sha}" ]] || { printf 'old binary SHA was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(sha256sum "${test_binary_directory}/install-helper" | awk '{print $1}')" == "${old_helper_sha}" ]] || { printf 'old helper SHA was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(runuser -u 404-probe -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
[[ ! -e "${test_upgrade}/pending" ]] || { printf 'pending state survived conservative recovery\n' >&2; exit 1; }
run_upgrade
assert_upgraded
printf 'isolated SIGKILL recovery passed restored_schema=%s old_sha=%s; fresh retry schema=%s\n' \
  "${original_schema}" "${old_server_sha}" "$(database_schema)"

setup_old_server
cp -- "${transformed}" "${crash_transformed}"
sed -i '/write_server_upgrade_state helper-replaced .*could not persist local helper replacement state/a\  if [[ "${PROBE_404_TEST_CRASH_PHASE:-}" == helper-replaced ]]; then kill -KILL "${BASHPID}"; fi' "${crash_transformed}"
chmod 0700 "${crash_transformed}"
if printf 'y\n' | script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=helper-replaced bash ${crash_transformed}" /dev/null; then
  printf 'helper-replaced SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
[[ -f "${test_upgrade}/pending" ]] || { printf 'helper SIGKILL did not preserve pending state\n' >&2; exit 1; }
if script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
  printf 'helper recovery rerun unexpectedly continued past conservative recovery\n' >&2
  exit 1
fi
[[ "$(sha256sum "${test_binary}" | awk '{print $1}')" == "${old_server_sha}" ]] || { printf 'old binary SHA was not restored after helper SIGKILL\n' >&2; exit 1; }
[[ "$(sha256sum "${test_binary_directory}/install-helper" | awk '{print $1}')" == "${old_helper_sha}" ]] || { printf 'old helper SHA was not restored after helper SIGKILL\n' >&2; exit 1; }
[[ "$(database_schema)" == "${original_schema}" ]] || { printf 'schema was not restored after helper SIGKILL\n' >&2; exit 1; }
run_upgrade
assert_upgraded
test_domain_menu
printf 'isolated helper-replaced SIGKILL recovery and fresh retry passed\n'

setup_old_server
rm -f -- "${test_binary_directory}/install-helper"
cp -- "${transformed}" "${crash_transformed}"
sed -i '/write_server_upgrade_state helper-replaced .*could not persist local helper replacement state/a\  if [[ "${PROBE_404_TEST_CRASH_PHASE:-}" == helper-replaced ]]; then kill -KILL "${BASHPID}"; fi' "${crash_transformed}"
chmod 0700 "${crash_transformed}"
if printf 'y\n' | script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=helper-replaced bash ${crash_transformed}" /dev/null; then
  printf 'missing-helper SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
if script -qec "env PROBE_404_VERSION=v0.9.3 PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
  printf 'missing-helper recovery rerun unexpectedly continued past conservative recovery\n' >&2
  exit 1
fi
[[ ! -e "${test_binary_directory}/install-helper" ]] || { printf 'pre-upgrade helper absence was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$("${test_binary}" version --json)" == "${old_server_version_json}" ]] || { printf 'old Server version was not restored for missing-helper recovery\n' >&2; exit 1; }
[[ "$(database_schema)" == "${original_schema}" ]] || { printf 'schema was not restored for missing-helper recovery\n' >&2; exit 1; }
run_upgrade
assert_upgraded
printf 'isolated missing-helper SIGKILL recovery and fresh retry passed\n'
