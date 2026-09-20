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
candidate_version_json="$("${asset_directory}/404-probe-server-linux-amd64" version --json)"
candidate_version="$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' <<<"${candidate_version_json}")"
expected_schema="${PROBE_404_REHEARSAL_EXPECTED_SCHEMA:-16}"
old_helper_sha=""

readonly test_user="404-probe-rehearsal"
readonly test_root="/var/lib/404-probe-upgrade-rehearsal"
readonly test_config="/etc/404-probe-upgrade-rehearsal"
readonly test_binary_directory="/usr/local/lib/404-probe-upgrade-rehearsal"
readonly test_binary="${test_binary_directory}/404-probe-server"
readonly test_unit="/etc/systemd/system/404-probe-upgrade-rehearsal.service"
readonly test_service="404-probe-upgrade-rehearsal.service"
readonly test_upgrade="/var/lib/404-probe-upgrade-rehearsal-transaction"
readonly test_lock="/run/404-probe-upgrade-rehearsal"
readonly test_agent_updater_state="/var/lib/404-probe-agent-updater-rehearsal"
readonly test_security_state="/var/lib/404-probe-security-rehearsal"
readonly test_runtime="/run/404-probe-agent-rehearsal"
readonly test_agent_unit="/etc/systemd/system/404-probe-agent-rehearsal.service"
readonly test_agent_updater_unit="/etc/systemd/system/404-probe-agent-updater-rehearsal.service"
readonly test_security_service_unit="/etc/systemd/system/404-probe-security-collect-rehearsal.service"
readonly test_security_timer_unit="/etc/systemd/system/404-probe-security-collect-rehearsal.timer"
readonly test_port="${PROBE_404_REHEARSAL_PORT:-33444}"
readonly transformed="/tmp/404-probe-upgrade-rehearsal.install.sh"
readonly crash_transformed="/tmp/404-probe-upgrade-rehearsal.crash.sh"
test_user_created=0

[[ "${candidate_version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || { printf 'candidate has no canonical version: %s\n' "${candidate_version_json}" >&2; exit 1; }
[[ "${expected_schema}" =~ ^[1-9][0-9]*$ ]] \
  || { printf 'invalid expected schema: %s\n' "${expected_schema}" >&2; exit 1; }

safe_remove_tree() {
  local path="$1" resolved owner
  [[ "${path}" == /* && "${path}" != / && ! -L "${path}" ]] \
    || { printf 'unsafe rehearsal cleanup target: %s\n' "${path}" >&2; return 1; }
  [[ -e "${path}" ]] || return 0
  resolved="$(realpath -e -- "${path}")"
  [[ "${resolved}" == "${path}" && -d "${path}" ]] \
    || { printf 'rehearsal cleanup target changed identity: %s\n' "${path}" >&2; return 1; }
  mountpoint -q -- "${path}" && { printf 'rehearsal cleanup target is mounted: %s\n' "${path}" >&2; return 1; }
  owner="$(stat -c '%U' -- "${path}")"
  [[ "${owner}" == root || "${owner}" == "${test_user}" ]] \
    || { printf 'unexpected rehearsal cleanup owner for %s: %s\n' "${path}" "${owner}" >&2; return 1; }
  rm -rf -- "${path}"
}

cleanup() {
  trap - EXIT HUP INT TERM
  systemctl stop "${test_service}" >/dev/null 2>&1 || true
  systemctl disable "${test_service}" >/dev/null 2>&1 || true
  rm -f -- "${test_unit}" "${test_agent_unit}" "${test_agent_updater_unit}" \
    "${test_security_service_unit}" "${test_security_timer_unit}" "${transformed}" "${crash_transformed}"
  safe_remove_tree "${test_root}" || true
  safe_remove_tree "${test_config}" || true
  safe_remove_tree "${test_binary_directory}" || true
  safe_remove_tree "${test_upgrade}" || true
  safe_remove_tree "${test_lock}" || true
  safe_remove_tree "${test_agent_updater_state}" || true
  safe_remove_tree "${test_security_state}" || true
  safe_remove_tree "${test_runtime}" || true
  systemctl daemon-reload >/dev/null 2>&1 || true
  if (( test_user_created != 0 )); then
    userdel "${test_user}" >/dev/null 2>&1 || true
    getent group "${test_user}" >/dev/null 2>&1 && groupdel "${test_user}" >/dev/null 2>&1 || true
  fi
}
for path in "${test_root}" "${test_config}" "${test_binary_directory}" "${test_unit}" "${test_upgrade}" "${test_lock}" \
  "${test_agent_updater_state}" "${test_security_state}" "${test_runtime}" "${test_agent_unit}" \
  "${test_agent_updater_unit}" "${test_security_service_unit}" "${test_security_timer_unit}"; do
  [[ ! -e "${path}" && ! -L "${path}" ]] || { printf 'isolated rehearsal path already exists: %s\n' "${path}" >&2; exit 1; }
done
id "${test_user}" >/dev/null 2>&1 && { printf 'isolated rehearsal user already exists: %s\n' "${test_user}" >&2; exit 1; }
getent group "${test_user}" >/dev/null 2>&1 && { printf 'isolated rehearsal group already exists: %s\n' "${test_user}" >&2; exit 1; }
ss -H -ltn "sport = :${test_port}" | grep -q . \
  && { printf 'isolated rehearsal port is already in use: %s\n' "${test_port}" >&2; exit 1; }
trap cleanup EXIT HUP INT TERM

transform_installer() {
  sed \
    -e "s|readonly INSTALL_HELPER=\"/usr/local/sbin/404-probe-install\"|readonly INSTALL_HELPER=\"${test_binary_directory}/install-helper\"|" \
    -e "s|readonly SERVER_BINARY=\"/usr/local/bin/404-probe-server\"|readonly SERVER_BINARY=\"${test_binary}\"|" \
    -e "s|readonly AGENT_BINARY=\"/usr/local/bin/404-probe-agent\"|readonly AGENT_BINARY=\"${test_binary_directory}/agent-absent\"|" \
    -e "s|readonly SERVER_DATABASE=\"/var/lib/404-probe/404-probe.db\"|readonly SERVER_DATABASE=\"${test_root}/404-probe.db\"|" \
    -e "s|readonly CONFIG_DIRECTORY=\"/etc/404-probe\"|readonly CONFIG_DIRECTORY=\"${test_config}\"|" \
    -e "s|readonly STATE_DIRECTORY=\"/var/lib/404-probe\"|readonly STATE_DIRECTORY=\"${test_root}\"|" \
    -e "s|readonly SERVER_UNIT=\"/etc/systemd/system/404-probe-server.service\"|readonly SERVER_UNIT=\"${test_unit}\"|" \
    -e "s|readonly AGENT_UNIT=\"/etc/systemd/system/404-probe-agent.service\"|readonly AGENT_UNIT=\"${test_agent_unit}\"|" \
    -e "s|readonly AGENT_UPDATER_UNIT=\"/etc/systemd/system/404-probe-agent-updater.service\"|readonly AGENT_UPDATER_UNIT=\"${test_agent_updater_unit}\"|" \
    -e "s|readonly AGENT_UPDATER_STATE=\"/var/lib/404-probe-updater\"|readonly AGENT_UPDATER_STATE=\"${test_agent_updater_state}\"|" \
    -e "s|readonly AGENT_UPDATER_SOCKET=\"/run/404-probe/agent-updater.sock\"|readonly AGENT_UPDATER_SOCKET=\"${test_runtime}/agent-updater.sock\"|" \
    -e "s|readonly SECURITY_STATE_DIRECTORY=\"/var/lib/404-probe-security\"|readonly SECURITY_STATE_DIRECTORY=\"${test_security_state}\"|" \
    -e "s|readonly SECURITY_SERVICE_UNIT=\"/etc/systemd/system/404-probe-security-collect.service\"|readonly SECURITY_SERVICE_UNIT=\"${test_security_service_unit}\"|" \
    -e "s|readonly SECURITY_TIMER_UNIT=\"/etc/systemd/system/404-probe-security-collect.timer\"|readonly SECURITY_TIMER_UNIT=\"${test_security_timer_unit}\"|" \
    -e "s|readonly SERVICE_USER=\"404-probe\"|readonly SERVICE_USER=\"${test_user}\"|" \
    -e "s|readonly SERVER_UPGRADE_DIRECTORY=\"/var/lib/404-probe-upgrade\"|readonly SERVER_UPGRADE_DIRECTORY=\"${test_upgrade}\"|" \
    -e "s|readonly SERVER_UPGRADE_CANDIDATE=\"/usr/local/bin/.404-probe-server.candidate\"|readonly SERVER_UPGRADE_CANDIDATE=\"${test_binary_directory}/.candidate\"|" \
    -e "s|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"/run/404-probe-upgrade\"|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"${test_lock}\"|" \
    -e "s/404-probe-server\.service/${test_service}/g" \
    -e 's/404-probe-agent\.service/404-probe-agent-rehearsal.service/g' \
    -e 's/404-probe-agent-updater\.service/404-probe-agent-updater-rehearsal.service/g' \
    -e 's/404-probe-security-collect\.service/404-probe-security-collect-rehearsal.service/g' \
    -e 's/404-probe-security-collect\.timer/404-probe-security-collect-rehearsal.timer/g' \
    -e "s| /run/404-probe;| ${test_runtime};|g" \
    -e "s|remove_owned_tree /run/404-probe|remove_owned_tree ${test_runtime}|g" \
    -e "s|/usr/local/bin/.404-probe-agent|${test_binary_directory}/.404-probe-agent|g" \
    -e "s|/usr/local/bin/.404-probe-server|${test_binary_directory}/.404-probe-server|g" \
    -e "s|/usr/local/sbin/404-probe-install.candidate|${test_binary_directory}/install-helper.candidate|g" \
    -e "s|rm -f -- /usr/local/sbin/404-probe-install|rm -f -- ${test_binary_directory}/install-helper|g" \
    "${installer_source}" >"${transformed}"
  chmod 0700 "${transformed}"
  bash -n "${transformed}"
  for forbidden in \
    'readonly SERVICE_USER="404-probe"' \
    '404-probe-server.service' \
    '404-probe-agent.service' \
    '404-probe-agent-updater.service' \
    '404-probe-security-collect.service' \
    '404-probe-security-collect.timer' \
    '"/etc/404-probe"' \
    '"/var/lib/404-probe"' \
    '"/var/lib/404-probe/404-probe.db"' \
    '"/run/404-probe"' \
    '"/usr/local/bin/404-probe-server"' \
    '"/usr/local/bin/404-probe-agent"' \
    '"/usr/local/sbin/404-probe-install"'; do
    ! grep -Fq -- "${forbidden}" "${transformed}" \
      || { printf 'transformed installer retained production resource: %s\n' "${forbidden}" >&2; exit 1; }
  done
}

setup_old_server() {
  local created first_id
  systemctl stop "${test_service}" >/dev/null 2>&1 || true
  systemctl disable "${test_service}" >/dev/null 2>&1 || true
  rm -f -- "${test_unit}"
  safe_remove_tree "${test_root}"
  safe_remove_tree "${test_config}"
  safe_remove_tree "${test_binary_directory}"
  safe_remove_tree "${test_upgrade}"
  safe_remove_tree "${test_lock}"
  install -d -m 0700 -o "${test_user}" -g "${test_user}" "${test_root}"
  install -d -m 0750 -o root -g "${test_user}" "${test_config}"
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
    chown "${test_user}:${test_user}" "${test_root}/404-probe.db"
    chmod 0600 "${test_root}/404-probe.db"
  else
    runuser -u "${test_user}" -- "${test_binary}" agent add isolated-active-a --db "${test_root}/404-probe.db" >/dev/null
    runuser -u "${test_user}" -- "${test_binary}" agent add isolated-active-b --db "${test_root}/404-probe.db" >/dev/null
    created="$(runuser -u "${test_user}" -- "${test_binary}" agent add isolated-revoked --db "${test_root}/404-probe.db")"
    first_id="$(printf '%s\n' "${created}" | sed -n 's/^Agent ID: //p')"
    unset created
    runuser -u "${test_user}" -- "${test_binary}" agent revoke "${first_id}" --db "${test_root}/404-probe.db" >/dev/null
    unset first_id
  fi
  expected_agent_listing="$(runuser -u "${test_user}" -- "${test_binary}" agent list --db "${test_root}/404-probe.db")"
  original_schema="$(database_schema)"
  cat >"${test_unit}" <<EOF
[Unit]
Description=404-probe isolated upgrade rehearsal

[Service]
Type=simple
User=${test_user}
Group=${test_user}
EnvironmentFile=${test_config}/server.env
ExecStart=${test_binary} serve --listen 127.0.0.1:${test_port} --db ${test_root}/404-probe.db --offline-timeout 30s
Restart=on-failure
ReadWritePaths=${test_root}

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${test_unit}"
  systemctl daemon-reload
  systemctl enable --now "${test_service}" >/dev/null
  for _ in $(seq 1 20); do
    [[ "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${test_port}/api/v1/agent/jobs/claim" || true)" == 401 ]] && return
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
  printf 'y\n' | script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null
}

assert_upgraded() {
  systemctl is-active --quiet "${test_service}"
  systemctl is-enabled --quiet "${test_service}"
  "${test_binary}" version --json | grep -Fq "\"version\":\"${candidate_version}\""
  runuser -u "${test_user}" -- "${test_binary}" database verify --db "${test_root}/404-probe.db" | grep -Fq '"integrity":"ok"'
  [[ "$(database_schema)" == "${expected_schema}" ]]
  [[ "$(runuser -u "${test_user}" -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
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
if [[ "${PROBE_404_REHEARSAL_CHECK_ONLY:-0}" == 1 ]]; then
  printf 'isolated installer transform checks passed\n'
  exit 0
fi
useradd --system --user-group --home-dir "${test_root}" --shell /usr/sbin/nologin "${test_user}"
test_user_created=1
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
"${test_binary}" version --json | grep -Fq "\"version\":\"${candidate_version}\""
runuser -u "${test_user}" -- "${test_binary}" database verify --db "${test_root}/404-probe.db" | grep -Fq '"integrity":"ok"'
[[ "$(database_schema)" == "${expected_schema}" ]]
[[ "$(runuser -u "${test_user}" -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
printf 'isolated inactive+disabled upgrade passed migrated_schema=%s\n' "$(database_schema)"

setup_old_server
cp -- "${transformed}" "${crash_transformed}"
sed -i '/write_server_upgrade_state binary-replaced .*could not persist binary replacement state/a\  if [[ "${PROBE_404_TEST_CRASH_PHASE:-}" == binary-replaced ]]; then kill -KILL "${BASHPID}"; fi' "${crash_transformed}"
chmod 0700 "${crash_transformed}"
if printf 'y\n' | script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=binary-replaced bash ${crash_transformed}" /dev/null; then
  printf 'SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
[[ -f "${test_upgrade}/pending" ]] || { printf 'SIGKILL did not preserve pending state\n' >&2; exit 1; }
if script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
  printf 'recovery rerun unexpectedly continued past conservative recovery\n' >&2
  exit 1
fi
systemctl is-active --quiet "${test_service}"
systemctl is-enabled --quiet "${test_service}"
[[ "$("${test_binary}" version --json)" == "${old_server_version_json}" ]] || { printf 'old Server version was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(database_schema)" == "${original_schema}" ]] || { printf 'pre-migration schema was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(sha256sum "${test_binary}" | awk '{print $1}')" == "${old_server_sha}" ]] || { printf 'old binary SHA was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(sha256sum "${test_binary_directory}/install-helper" | awk '{print $1}')" == "${old_helper_sha}" ]] || { printf 'old helper SHA was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$(runuser -u "${test_user}" -- "${test_binary}" agent list --db "${test_root}/404-probe.db")" == "${expected_agent_listing}" ]]
[[ ! -e "${test_upgrade}/pending" ]] || { printf 'pending state survived conservative recovery\n' >&2; exit 1; }
run_upgrade
assert_upgraded
printf 'isolated SIGKILL recovery passed restored_schema=%s old_sha=%s; fresh retry schema=%s\n' \
  "${original_schema}" "${old_server_sha}" "$(database_schema)"

setup_old_server
cp -- "${transformed}" "${crash_transformed}"
sed -i '/write_server_upgrade_state helper-replaced .*could not persist local helper replacement state/a\  if [[ "${PROBE_404_TEST_CRASH_PHASE:-}" == helper-replaced ]]; then kill -KILL "${BASHPID}"; fi' "${crash_transformed}"
chmod 0700 "${crash_transformed}"
if printf 'y\n' | script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=helper-replaced bash ${crash_transformed}" /dev/null; then
  printf 'helper-replaced SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
[[ -f "${test_upgrade}/pending" ]] || { printf 'helper SIGKILL did not preserve pending state\n' >&2; exit 1; }
if script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
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
if printf 'y\n' | script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} PROBE_404_TEST_CRASH_PHASE=helper-replaced bash ${crash_transformed}" /dev/null; then
  printf 'missing-helper SIGKILL injection unexpectedly succeeded\n' >&2
  exit 1
fi
if script -qec "env PROBE_404_VERSION=${candidate_version} PROBE_404_LOCAL_ASSET_DIRECTORY=${asset_directory} bash ${transformed}" /dev/null; then
  printf 'missing-helper recovery rerun unexpectedly continued past conservative recovery\n' >&2
  exit 1
fi
[[ ! -e "${test_binary_directory}/install-helper" ]] || { printf 'pre-upgrade helper absence was not restored after SIGKILL\n' >&2; exit 1; }
[[ "$("${test_binary}" version --json)" == "${old_server_version_json}" ]] || { printf 'old Server version was not restored for missing-helper recovery\n' >&2; exit 1; }
[[ "$(database_schema)" == "${original_schema}" ]] || { printf 'schema was not restored for missing-helper recovery\n' >&2; exit 1; }
run_upgrade
assert_upgraded
printf 'isolated missing-helper SIGKILL recovery and fresh retry passed\n'
