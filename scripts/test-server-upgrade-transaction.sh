#!/usr/bin/env bash

set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT

server_binary="${temporary}/usr/local/bin/404-probe-server"
server_unit="${temporary}/etc/systemd/system/404-probe-server.service"
config_directory="${temporary}/etc/404-probe"
state_directory="${temporary}/var/lib/404-probe"
upgrade_directory="${temporary}/var/lib/404-probe-upgrade"
lock_directory="${temporary}/run/404-probe-upgrade"
mkdir -p "$(dirname "${server_binary}")" "$(dirname "${server_unit}")" "${config_directory}" "${state_directory}" "${upgrade_directory}" "${lock_directory}"
chmod 0700 "${upgrade_directory}" "${lock_directory}"

source <(sed -e '$d' \
  -e "s|readonly SERVER_BINARY=\"/usr/local/bin/404-probe-server\"|readonly SERVER_BINARY=\"${server_binary}\"|" \
  -e "s|readonly CONFIG_DIRECTORY=\"/etc/404-probe\"|readonly CONFIG_DIRECTORY=\"${config_directory}\"|" \
  -e "s|readonly STATE_DIRECTORY=\"/var/lib/404-probe\"|readonly STATE_DIRECTORY=\"${state_directory}\"|" \
  -e "s|readonly SERVER_UNIT=\"/etc/systemd/system/404-probe-server.service\"|readonly SERVER_UNIT=\"${server_unit}\"|" \
  -e "s|readonly SERVER_UPGRADE_DIRECTORY=\"/var/lib/404-probe-upgrade\"|readonly SERVER_UPGRADE_DIRECTORY=\"${upgrade_directory}\"|" \
  -e "s|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"/run/404-probe-upgrade\"|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"${lock_directory}\"|" \
  "${root}/install.sh")

systemctl_log="${temporary}/systemctl.log"
SYNC_FAIL=0
SHA_FAIL_MODE=""
stat() {
  if [[ "${1:-}" == -c && "${2:-}" == '%U:%G:%a' && "${3:-}" == "${SERVER_UPGRADE_BACKUP}" ]]; then
    printf 'root:root:700\n'
    return
  fi
  command stat "$@"
}
systemctl() {
  printf '%s\n' "$*" >>"${systemctl_log}"
  if [[ "$1" == is-active ]]; then return 1; fi
  return 0
}
sync() {
  if (( SYNC_FAIL != 0 )); then return 1; fi
  command sync "$@"
}
sha256sum() {
  if [[ "${SHA_FAIL_MODE}" == primary && "${1:-}" == server-binary ]]; then return 1; fi
  if [[ "${SHA_FAIL_MODE}" == sidecar && "${1:-}" == database/404-probe.db ]]; then return 1; fi
  command sha256sum "$@"
}

printf '%1500s' x >"${state_directory}/404-probe.db"
database_kb="$(existing_regular_files_kb "${state_directory}/404-probe.db" "${state_directory}/404-probe.db-wal" "${state_directory}/404-probe.db-shm")"
[[ "${database_kb}" == 2 ]] || { printf 'missing WAL/SHM size test failed: %s\n' "${database_kb}" >&2; exit 1; }

make_backup() {
  rm -rf -- "${SERVER_UPGRADE_BACKUP}"
  mkdir -p "${SERVER_UPGRADE_BACKUP}/database" "${SERVER_UPGRADE_BACKUP}/config"
  chmod 0700 "${SERVER_UPGRADE_BACKUP}"
  printf 'old-binary' >"${SERVER_UPGRADE_BACKUP}/server-binary"
  printf 'old-unit' >"${SERVER_UPGRADE_BACKUP}/server.service"
  printf 'secret' >"${SERVER_UPGRADE_BACKUP}/config/server.env"
  printf 'old-database' >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db"
  create_server_upgrade_backup_manifest "${SERVER_UPGRADE_BACKUP}" || return 1
  : >"${SERVER_UPGRADE_BACKUP}/complete"
  chmod 0600 "${SERVER_UPGRADE_BACKUP}/complete"
}

SHA_FAIL_MODE=primary
if make_backup; then
  printf 'primary backup hash failure was ignored\n' >&2
  exit 1
fi
SHA_FAIL_MODE=sidecar
if make_backup; then
  printf 'database sidecar hash failure was ignored\n' >&2
  exit 1
fi
SHA_FAIL_MODE=""

make_backup
verify_server_upgrade_backup "${SERVER_UPGRADE_BACKUP}" || { printf 'valid backup was rejected\n' >&2; exit 1; }
printf 'corruption' >>"${SERVER_UPGRADE_BACKUP}/database/404-probe.db"
printf 'new-database' >"${SERVER_DATABASE}"
printf 'new-binary' >"${SERVER_BINARY}"
write_server_upgrade_state migration-started 1 1 v0.9.1
if restore_server_upgrade; then
  printf 'corrupted backup was accepted\n' >&2
  exit 1
fi
[[ "$(<"${SERVER_DATABASE}")" == new-database ]] || { printf 'corrupted backup overwrote live DB\n' >&2; exit 1; }
! grep -q '^start ' "${systemctl_log}" || { printf 'old service started from corrupted backup\n' >&2; exit 1; }
[[ -f "${SERVER_UPGRADE_STATE}" ]] || { printf 'pending state was removed after failed recovery\n' >&2; exit 1; }

make_backup
binary_line="$(grep '[ *]server-binary$' "${SERVER_UPGRADE_BACKUP}/SHA256SUMS")"
awk -v replacement="${binary_line}" '{ if ($2 ~ /database\/404-probe\.db$/) print replacement; else print }' \
  "${SERVER_UPGRADE_BACKUP}/SHA256SUMS" >"${SERVER_UPGRADE_BACKUP}/SHA256SUMS.repeated"
mv -f -- "${SERVER_UPGRADE_BACKUP}/SHA256SUMS.repeated" "${SERVER_UPGRADE_BACKUP}/SHA256SUMS"
if verify_server_upgrade_backup "${SERVER_UPGRADE_BACKUP}"; then
  printf 'manifest with duplicate binary and omitted DB checksum was accepted\n' >&2
  exit 1
fi

make_backup
printf 'new-database' >"${SERVER_DATABASE}"
printf 'new-binary' >"${SERVER_BINARY}"
: >"${systemctl_log}"
write_server_upgrade_state migration-started 1 1 v0.9.1
SYNC_FAIL=1
if restore_server_upgrade; then
  printf 'recovery ignored a persistence barrier failure\n' >&2
  exit 1
fi
SYNC_FAIL=0
! grep -q '^start ' "${systemctl_log}" || { printf 'service started before restored files were durable\n' >&2; exit 1; }
[[ -f "${SERVER_UPGRADE_STATE}" ]] || { printf 'pending state was removed after persistence failure\n' >&2; exit 1; }
restore_server_upgrade || { printf 'valid recovery failed\n' >&2; exit 1; }
[[ "$(<"${SERVER_DATABASE}")" == old-database ]] || { printf 'old DB was not restored\n' >&2; exit 1; }
[[ "$(<"${SERVER_BINARY}")" == old-binary ]] || { printf 'old binary was not restored\n' >&2; exit 1; }
grep -q '^start 404-probe-server.service$' "${systemctl_log}" || { printf 'original active state was not restored\n' >&2; exit 1; }
[[ ! -e "${SERVER_UPGRADE_STATE}" ]] || { printf 'pending state survived successful recovery\n' >&2; exit 1; }

make_backup
printf 'new-database' >"${SERVER_DATABASE}"
printf 'new-binary' >"${SERVER_BINARY}"
: >"${systemctl_log}"
write_server_upgrade_state migration-started 0 0 v0.9.1
restore_server_upgrade || { printf 'inactive recovery failed\n' >&2; exit 1; }
grep -q '^disable 404-probe-server.service$' "${systemctl_log}" || { printf 'original disabled state was not restored\n' >&2; exit 1; }
! grep -q '^start ' "${systemctl_log}" || { printf 'originally inactive Server was started\n' >&2; exit 1; }

fixture_directory="${temporary}/release-fixture"
fake_bin="${temporary}/fake-bin"
mkdir -p "${fixture_directory}" "${fake_bin}"
cat >"${fixture_directory}/install.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s|%s\n' "${PROBE_404_VERSION:-}" "${1:-}" >"${BOOTSTRAP_RESULT}"
EOF
fixture_digest="$(sha256sum "${fixture_directory}/install.sh" | awk '{print $1}')"
printf '%s  install.sh\n' "${fixture_digest}" >"${fixture_directory}/SHA256SUMS"
cat >"${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -eu
output=""
url=""
while (( $# > 0 )); do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --write-out) shift 2 ;;
    --proto) shift 2 ;;
    --tlsv1.2|--fail|--silent|--show-error|--location) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "${url}" >>"${CURL_LOG}"
cp -- "${FIXTURE_DIRECTORY}/${url##*/}" "${output}"
EOF
chmod 0755 "${fake_bin}/curl"
export FIXTURE_DIRECTORY="${fixture_directory}"
export CURL_LOG="${temporary}/curl.log"
export BOOTSTRAP_RESULT="${temporary}/bootstrap.result"
: >"${CURL_LOG}"
PATH="${fake_bin}:${PATH}" bash "${root}/install.sh" --help >/dev/null
[[ ! -s "${CURL_LOG}" ]] || { printf 'help unexpectedly required network access\n' >&2; exit 1; }
PATH="${fake_bin}:${PATH}" PROBE_404_VERSION=v1.2.3 bash "${root}/install.sh" sentinel
[[ "$(<"${BOOTSTRAP_RESULT}")" == 'v1.2.3|sentinel' ]] || { printf 'explicit version was not forwarded to tagged installer\n' >&2; exit 1; }
grep -Fxq 'https://github.com/404-git-404/404-probe/releases/download/v1.2.3/install.sh' "${CURL_LOG}" \
  || { printf 'explicit version did not fetch its tagged installer\n' >&2; exit 1; }
grep -Fxq 'https://github.com/404-git-404/404-probe/releases/download/v1.2.3/SHA256SUMS' "${CURL_LOG}" \
  || { printf 'explicit version did not fetch its tagged manifest\n' >&2; exit 1; }
! grep -q '/releases/latest' "${CURL_LOG}" || { printf 'explicit version unexpectedly resolved latest\n' >&2; exit 1; }

printf 'Server upgrade transaction fault tests passed.\n'
