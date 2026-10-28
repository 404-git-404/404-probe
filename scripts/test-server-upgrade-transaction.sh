#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"

set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT

server_binary="${temporary}/usr/local/bin/404-probe-server"
install_helper="${temporary}/usr/local/sbin/404-probe-install"
server_unit="${temporary}/etc/systemd/system/404-probe-server.service"
config_directory="${temporary}/etc/404-probe"
state_directory="${temporary}/var/lib/404-probe"
upgrade_directory="${temporary}/var/lib/404-probe-upgrade"
lock_directory="${temporary}/run/404-probe-upgrade"
mkdir -p "$(dirname "${server_binary}")" "$(dirname "${install_helper}")" "$(dirname "${server_unit}")" "${config_directory}" "${state_directory}" "${upgrade_directory}" "${lock_directory}"
chmod 0700 "${upgrade_directory}" "${lock_directory}"

source <(sed -e '$d' \
	-e "s|readonly INSTALL_HELPER=\"/usr/local/sbin/404-probe-install\"|readonly INSTALL_HELPER=\"${install_helper}\"|" \
  -e "s|readonly SERVER_BINARY=\"/usr/local/bin/404-probe-server\"|readonly SERVER_BINARY=\"${server_binary}\"|" \
  -e "s|readonly CONFIG_DIRECTORY=\"/etc/404-probe\"|readonly CONFIG_DIRECTORY=\"${config_directory}\"|" \
  -e "s|readonly STATE_DIRECTORY=\"/var/lib/404-probe\"|readonly STATE_DIRECTORY=\"${state_directory}\"|" \
  -e "s|readonly SERVER_UNIT=\"/etc/systemd/system/404-probe-server.service\"|readonly SERVER_UNIT=\"${server_unit}\"|" \
  -e "s|readonly SERVER_UPGRADE_DIRECTORY=\"/var/lib/404-probe-upgrade\"|readonly SERVER_UPGRADE_DIRECTORY=\"${upgrade_directory}\"|" \
  -e "s|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"/run/404-probe-upgrade\"|readonly SERVER_UPGRADE_LOCK_DIRECTORY=\"${lock_directory}\"|" \
  "${INSTALLER_FIXTURE_INSTALLER}")

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
  if [[ "$1" == start && -n "${START_HOOK:-}" ]]; then eval "${START_HOOK}"; fi
  return 0
}
# Recovery tests simulate the five-second health check; its real behaviour is tested separately below.
real_stable_readiness="$(declare -f verify_server_stable_readiness)"
READINESS_FAIL=0
verify_server_stable_readiness() { return "${READINESS_FAIL}"; }
RUNUSER_FAIL=0
runuser() {
  while [[ $# -gt 0 && "$1" != -- ]]; do shift; done
  shift
  if [[ "$(basename -- "${1:-}")" == "$(basename -- "${SERVER_BINARY}")" && "${2:-}" == database && "${3:-}" == verify ]]; then
    return "${RUNUSER_FAIL}"
  fi
  return 69
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
  printf '1\n' >"${SERVER_UPGRADE_BACKUP}/helper-state"
  printf 'old-helper' >"${SERVER_UPGRADE_BACKUP}/install-helper"
  printf 'secret' >"${SERVER_UPGRADE_BACKUP}/config/server.env"
  printf 'old-database' >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db"
  create_server_upgrade_backup_manifest "${SERVER_UPGRADE_BACKUP}" || return 1
  : >"${SERVER_UPGRADE_BACKUP}/complete"
  chmod 0600 "${SERVER_UPGRADE_BACKUP}/complete"
}

make_legacy_backup() {
  rm -rf -- "${SERVER_UPGRADE_BACKUP}"
  mkdir -p "${SERVER_UPGRADE_BACKUP}/config" "${SERVER_UPGRADE_BACKUP}/database"
  chmod 0700 "${SERVER_UPGRADE_BACKUP}"
  printf 'old-binary' >"${SERVER_UPGRADE_BACKUP}/server-binary"
  printf 'old-unit' >"${SERVER_UPGRADE_BACKUP}/server.service"
  printf 'secret' >"${SERVER_UPGRADE_BACKUP}/config/server.env"
  printf 'old-database' >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db"
  tar -cpf "${SERVER_UPGRADE_BACKUP}/config.tar" -C "${SERVER_UPGRADE_BACKUP}" config
  printf '404-probe.db\n' >"${SERVER_UPGRADE_BACKUP}/database-files"
  (
    cd "${SERVER_UPGRADE_BACKUP}"
    sha256sum server-binary server.service config.tar database-files database/404-probe.db
  ) >"${SERVER_UPGRADE_BACKUP}/SHA256SUMS"
  chmod 0600 "${SERVER_UPGRADE_BACKUP}/config.tar" "${SERVER_UPGRADE_BACKUP}/database-files" "${SERVER_UPGRADE_BACKUP}/SHA256SUMS"
  : >"${SERVER_UPGRADE_BACKUP}/complete"
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
printf 'new-helper' >"${INSTALL_HELPER}"
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
[[ "$(<"${INSTALL_HELPER}")" == old-helper ]] || { printf 'old helper was not restored\n' >&2; exit 1; }
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

make_backup
printf '0\n' >"${SERVER_UPGRADE_BACKUP}/helper-state"
rm -f -- "${SERVER_UPGRADE_BACKUP}/install-helper"
create_server_upgrade_backup_manifest "${SERVER_UPGRADE_BACKUP}"
: >"${SERVER_UPGRADE_BACKUP}/complete"
printf 'new-database' >"${SERVER_DATABASE}"
printf 'new-binary' >"${SERVER_BINARY}"
printf 'new-helper' >"${INSTALL_HELPER}"
write_server_upgrade_state helper-replaced 0 0 v0.9.1
restore_server_upgrade || { printf 'missing-helper recovery failed\n' >&2; exit 1; }
[[ ! -e "${INSTALL_HELPER}" ]] || { printf 'helper absence was not restored\n' >&2; exit 1; }

make_legacy_backup
printf 'new-database' >"${SERVER_DATABASE}"
printf 'new-binary' >"${SERVER_BINARY}"
printf 'old-helper' >"${INSTALL_HELPER}"
write_server_upgrade_state binary-replaced 1 1 v0.9.1
restore_server_upgrade || { printf 'legacy v0.9.1 recovery failed\n' >&2; exit 1; }
[[ "$(<"${SERVER_DATABASE}")" == old-database ]] || { printf 'legacy old DB was not restored\n' >&2; exit 1; }
[[ "$(<"${SERVER_BINARY}")" == old-binary ]] || { printf 'legacy old binary was not restored\n' >&2; exit 1; }
[[ "$(<"${INSTALL_HELPER}")" == old-helper ]] || { printf 'legacy recovery changed the helper\n' >&2; exit 1; }


# --- #3: a recovery that started the old Server (which wrote to the database) and was then interrupted or
# failed its health check must be retryable. In backup-complete nothing used to be copied back, so the retry
# compared the now-modified files with the backup and could never succeed.
make_backup
printf 'old-database' >"${SERVER_DATABASE}"
printf 'old-binary' >"${SERVER_BINARY}"
printf 'old-helper' >"${INSTALL_HELPER}"
rm -f -- "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"
write_server_upgrade_state backup-complete 1 1 v0.9.1
START_HOOK='printf "-written-by-old-server" >>"${SERVER_DATABASE}"; printf wal >"${SERVER_DATABASE}-wal"; printf shm >"${SERVER_DATABASE}-shm"'
READINESS_FAIL=1
if restore_server_upgrade 2>/dev/null; then printf 'recovery with a failing health check unexpectedly succeeded\n' >&2; exit 1; fi
[[ -f "${SERVER_UPGRADE_STATE}" ]] || { printf 'pending state was cleared after a failed health check\n' >&2; exit 1; }
START_HOOK=""
READINESS_FAIL=0
restore_server_upgrade || { printf 'REPRODUCED #3: backup-complete recovery cannot be retried after the old Server wrote the database\n' >&2; exit 1; }
[[ "$(<"${SERVER_DATABASE}")" == old-database && ! -e "${SERVER_DATABASE}-wal" && ! -e "${SERVER_DATABASE}-shm" ]] || { printf 'retry did not restore the database files\n' >&2; exit 1; }
[[ ! -e "${SERVER_UPGRADE_STATE}" ]] || { printf 'pending state survived the retried recovery\n' >&2; exit 1; }

# --- verify_server_stable_readiness: shared by recovery and the normal upgrade ---------------------
# A real process is started from a copy of sleep installed as SERVER_BINARY so that /proc/<pid>/exe,
# the executable path and its hash are genuine. systemctl and curl are driven from small state files.
stable_directory="${temporary}/stable"
mkdir -p "${stable_directory}"
printf '[Service]\nExecStart=%s serve --listen 127.0.0.1:8080 --db /x\n' "${SERVER_BINARY}" >"${SERVER_UNIT}"
real_systemctl="$(declare -f systemctl)"
systemctl() {
  case "$1" in
    is-active) local state; state="$(<"${stable_directory}/state")"; [[ "${2:-}" == --quiet ]] || printf '%s\n' "${state}"; [[ "${state}" == active ]] ;;
    show) cat "${stable_directory}/pid" ;;
    status) return 0 ;;
    *) return 0 ;;
  esac
}
journalctl() { return 0; }
curl() { printf '%s' "$(<"${stable_directory}/http")"; }
eval "${real_stable_readiness}"
start_stable_server() {
  rm -f -- "${SERVER_BINARY}"
  cp -- "$(command -v sleep)" "${SERVER_BINARY}"
  chmod 0755 "${SERVER_BINARY}"
  "${SERVER_BINARY}" 120 &
  stable_pid=$!
  printf '%s\n' "${stable_pid}" >"${stable_directory}/pid"
  printf 'active\n' >"${stable_directory}/state"
  printf '401' >"${stable_directory}/http"
}
stop_stable_server() { kill "${stable_pid}" 2>/dev/null || true; wait "${stable_pid}" 2>/dev/null || true; }
expect_stable() { # name, expected status, minimum seconds, maximum seconds, background mutation command
  local name="$1" expected="$2" minimum="$3" maximum="$4" status=0 started elapsed
  start_stable_server
  if [[ -n "${5:-}" ]]; then ( eval "$5" ) >/dev/null 2>&1 & fi
  started=${SECONDS}
  verify_server_stable_readiness >/dev/null 2>&1 || status=$?
  elapsed=$(( SECONDS - started ))
  stop_stable_server
  if (( expected == 0 )) && (( status != 0 )) || (( expected != 0 )) && (( status == 0 )); then
    printf 'stable readiness: %s returned %s\n' "${name}" "${status}" >&2; exit 1
  fi
  if (( elapsed < minimum || elapsed > maximum )); then
    printf 'stable readiness: %s took %ss, expected %s-%ss\n' "${name}" "${elapsed}" "${minimum}" "${maximum}" >&2; exit 1
  fi
  printf 'stable readiness: %s ok (%ss)\n' "${name}" "${elapsed}"
}
sd="${stable_directory}"
expect_stable 'healthy candidate passes after the full observation window' 0 5 8
expect_stable 'candidate that never becomes active is rejected' 1 0 4 "printf 'inactive\n' >${sd}/state"
expect_stable 'candidate that dies after two seconds is rejected' 1 2 4 "sleep 2; printf 'inactive\n' >${sd}/state; kill \${stable_pid}"
expect_stable 'candidate that is restarted (PID change) is rejected' 1 2 4 "sleep 2; ${SERVER_BINARY} 120 & printf '%s\n' \$! >${sd}/pid; wait"
expect_stable 'candidate whose HTTP stops answering 401 is rejected' 1 2 4 "sleep 2; printf '500' >${sd}/http"
expect_stable 'candidate whose executable was swapped is rejected' 1 2 4 "sleep 2; cp -- /bin/true ${SERVER_BINARY}.swap; mv -f -- ${SERVER_BINARY}.swap ${SERVER_BINARY}"
unset -f systemctl journalctl curl
eval "${real_systemctl}"
verify_server_stable_readiness() { return "${READINESS_FAIL}"; }
rm -f -- "${SERVER_BINARY}" "${SERVER_UNIT}" "${server_unit}"

# Both paths use the one function.
[[ "$(grep -c 'verify_server_stable_readiness' "${INSTALLER_FIXTURE_INSTALLER}")" -ge 3 ]] || { printf 'stable readiness is not shared\n' >&2; exit 1; }
! grep -q 'verify_restored_server_readiness' "${INSTALLER_FIXTURE_INSTALLER}" || { printf 'old readiness function name still used\n' >&2; exit 1; }
candidate_block="$(sed -n '/write_server_upgrade_state candidate-started/,/systemctl enable 404-probe-server.service >\/dev\/null || die "could not restore enabled state"/p' "${INSTALLER_FIXTURE_INSTALLER}")"
grep -q 'verify_server_stable_readiness || die' <<<"${candidate_block}" || { printf 'normal upgrade does not run the stability check after candidate-started\n' >&2; exit 1; }
! grep -q 'readlink -f "/proc/${pid}/exe"' <<<"${candidate_block}" || { printf 'normal upgrade still has the one-shot PID check\n' >&2; exit 1; }

# --- restore_server_upgrade must be repeatable after an interruption at ANY step --------------------------
# cp, mv, rm, sync, systemctl and the database check are wrapped; the Nth call of the chosen command inside
# restore_server_upgrade fails (cp-partial leaves half a copy behind). After one or two interrupted attempts a
# clean retry must give the same end state as an uninterrupted recovery.
sweep_state="${temporary}/sweep-state"
mkdir -p "${sweep_state}"
real_systemctl_simple="$(declare -f systemctl)"
real_sync="$(declare -f sync)"
real_runuser="$(declare -f runuser)"
INJECT_ARMED=0; INJECT_CMD=""; INJECT_N=0; INJECT_COUNT=0; INJECT_HIT=0
inject_point() {
  [[ "${INJECT_ARMED}" == 1 && "${INJECT_CMD%-partial}" == "$1" ]] || return 0
  INJECT_COUNT=$((INJECT_COUNT + 1))
  if (( INJECT_COUNT == INJECT_N )); then INJECT_HIT=1; return 1; fi
  return 0
}
cp() {
  if ! inject_point cp; then
    if [[ "${INJECT_CMD}" == cp-partial ]]; then
      local source="${*: -2:1}" destination="${*: -1}"
      [[ -f "${source}" ]] && head -c "$(( $(command stat -c %s -- "${source}") / 2 ))" -- "${source}" >"${destination}"
    fi
    return 1
  fi
  command cp "$@"
}
mv() { inject_point mv || return 1; command mv "$@"; }
rm() { inject_point rm || return 1; command rm "$@"; }
sync() { inject_point sync || return 1; command sync "$@"; }
eval "${real_runuser/runuser ()/base_runuser ()}"
runuser() { inject_point runuser || return 1; base_runuser "$@"; }
systemctl() {
  case "$1" in
    stop|start|enable|disable) inject_point systemctl || return 1 ;;
  esac
  printf '%s\n' "$*" >>"${systemctl_log}"
  case "$1" in
    stop) printf inactive >"${sweep_state}/active" ;;
    start) printf active >"${sweep_state}/active" ;;
    enable) printf enabled >"${sweep_state}/enabled" ;;
    disable) printf disabled >"${sweep_state}/enabled" ;;
    is-active) local state; state="$(<"${sweep_state}/active")"; [[ "${2:-}" == --quiet ]] || printf '%s\n' "${state}"; [[ "${state}" == active ]]; return ;;
  esac
  return 0
}

tree_digest() { (cd "$1" && find . -type f | sort | while IFS= read -r f; do printf '%s %s\n' "$f" "$(sha256sum "$f" | awk '{print $1}')"; done); }

# $1 phase, $2 active(0/1), $3 enabled(0/1), $4 wal(0/1): live files as left by the failed upgrade
sweep_setup() {
  local phase="$1" active="$2" enabled="$3" wal="$4"
  command rm -rf -- "${SERVER_UPGRADE_BACKUP}"
  make_backup
  if (( wal != 0 )); then
    printf 'old-wal' >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db-wal"
    printf 'old-shm' >"${SERVER_UPGRADE_BACKUP}/database/404-probe.db-shm"
    create_server_upgrade_backup_manifest "${SERVER_UPGRADE_BACKUP}"
  fi
  command rm -f -- "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" "${SERVER_BINARY}" "${INSTALL_HELPER}"
  command rm -f -- "${state_directory}"/*.restore "${server_binary}.restore" "${install_helper}.restore"
  case "${phase}" in
    backup-complete) # nothing replaced yet, but an earlier recovery attempt already ran the old Server
      printf 'old-database' >"${SERVER_DATABASE}"; printf 'old-binary' >"${SERVER_BINARY}"; printf 'old-helper' >"${INSTALL_HELPER}"
      printf 'grown-by-old-server' >>"${SERVER_DATABASE}"; printf 'new-wal' >"${SERVER_DATABASE}-wal"; printf 'new-shm' >"${SERVER_DATABASE}-shm" ;;
    *)
      printf 'new-database' >"${SERVER_DATABASE}"; printf 'new-binary' >"${SERVER_BINARY}"; printf 'new-helper' >"${INSTALL_HELPER}" ;;
  esac
  printf '%s' "$([[ ${active} == 1 ]] && echo active || echo inactive)" >"${sweep_state}/active"
  printf '%s' "$([[ ${enabled} == 1 ]] && echo enabled || echo disabled)" >"${sweep_state}/enabled"
  command rm -f -- "${SERVER_UPGRADE_STATE}"
  write_server_upgrade_state "${phase}" "${active}" "${enabled}" v0.9.1
  : >"${systemctl_log}"
}

sweep_assert_recovered() { # label active enabled wal
  local label="$1" active="$2" enabled="$3" wal="$4" leftovers
  [[ "$(<"${SERVER_DATABASE}")" == old-database ]] || { printf '%s: database not restored\n' "${label}" >&2; exit 1; }
  if (( wal != 0 )); then
    [[ "$(<"${SERVER_DATABASE}-wal")" == old-wal && "$(<"${SERVER_DATABASE}-shm")" == old-shm ]] || { printf '%s: WAL/SHM not restored\n' "${label}" >&2; exit 1; }
  else
    [[ ! -e "${SERVER_DATABASE}-wal" && ! -e "${SERVER_DATABASE}-shm" ]] || { printf '%s: stale WAL/SHM left behind\n' "${label}" >&2; exit 1; }
  fi
  [[ "$(<"${SERVER_BINARY}")" == old-binary && "$(<"${INSTALL_HELPER}")" == old-helper ]] || { printf '%s: binary/helper not restored\n' "${label}" >&2; exit 1; }
  leftovers="$(find "${temporary}/usr" "${temporary}/var" -name '*.restore*' 2>/dev/null)"
  [[ -z "${leftovers}" ]] || { printf '%s: leftover files: %s\n' "${label}" "${leftovers}" >&2; exit 1; }
  [[ ! -e "${SERVER_UPGRADE_STATE}" && ! -e "${SERVER_UPGRADE_STATE}.new" ]] || { printf '%s: pending state not cleared\n' "${label}" >&2; exit 1; }
  [[ "$(tree_digest "${SERVER_UPGRADE_BACKUP}")" == "${sweep_backup_digest}" ]] || { printf '%s: backup directory changed\n' "${label}" >&2; exit 1; }
  [[ "$(<"${sweep_state}/active")" == "$([[ ${active} == 1 ]] && echo active || echo inactive)" ]] || { printf '%s: wrong active state\n' "${label}" >&2; exit 1; }
  [[ "$(<"${sweep_state}/enabled")" == "$([[ ${enabled} == 1 ]] && echo enabled || echo disabled)" ]] || { printf '%s: wrong enabled state\n' "${label}" >&2; exit 1; }
}

run_restore_with() { # command n -> status of one armed restore attempt
  INJECT_CMD="$1"; INJECT_N="$2"; INJECT_COUNT=0; INJECT_HIT=0; INJECT_ARMED=1
  local status=0
  restore_server_upgrade >/dev/null 2>&1 || status=$?
  INJECT_ARMED=0
  return "${status}"
}

sweep_cases=0
sweep() { # phase active enabled wal
  local phase="$1" active="$2" enabled="$3" wal="$4" command n label second
  for command in cp cp-partial mv rm sync systemctl runuser; do
    for n in $(seq 1 60); do
      label="${phase} active=${active} enabled=${enabled} wal=${wal} ${command}#${n}"
      sweep_setup "${phase}" "${active}" "${enabled}" "${wal}"
      sweep_backup_digest="$(tree_digest "${SERVER_UPGRADE_BACKUP}")"
      if run_restore_with "${command}" "${n}"; then
        # Either no Nth call exists (the command is exhausted) or the failure hit a step whose errors are ignored by
        # design (stopping an already stopped Server). Either way the end state must be the complete recovery.
        sweep_assert_recovered "${label} (uninterrupted)" "${active}" "${enabled}" "${wal}"
        [[ "${INJECT_HIT}" == 1 ]] || break
        sweep_cases=$((sweep_cases + 1))
        continue
      fi
      [[ "${INJECT_HIT}" == 1 ]] || { printf '%s: recovery failed without an injected failure\n' "${label}" >&2; exit 1; }
      # Clearing the pending state is the commit point: once it is gone only disposable candidate/staging files
      # may remain, and the recovered end state must already be complete.
      if [[ ! -e "${SERVER_UPGRADE_STATE}" ]]; then
        sweep_assert_recovered "${label} (interrupted after the commit point)" "${active}" "${enabled}" "${wal}"
        sweep_cases=$((sweep_cases + 1))
        continue
      fi
      # A second interruption, earlier or at the same point, then the clean retry.
      second=$(( n > 1 ? n - 1 : 1 ))
      if run_restore_with "${command}" "${second}"; then
        # Call numbers shift once files are already restored; that attempt simply completed the recovery.
        sweep_assert_recovered "${label} (second attempt completed)" "${active}" "${enabled}" "${wal}"
        sweep_cases=$((sweep_cases + 1))
        continue
      fi
      if [[ ! -e "${SERVER_UPGRADE_STATE}" ]]; then
        sweep_assert_recovered "${label} (second attempt interrupted after the commit point)" "${active}" "${enabled}" "${wal}"
        sweep_cases=$((sweep_cases + 1))
        continue
      fi
      restore_server_upgrade >/dev/null 2>&1 || { printf '%s: clean retry after two interruptions failed\n' "${label}" >&2; exit 1; }
      sweep_assert_recovered "${label}" "${active}" "${enabled}" "${wal}"
      sweep_cases=$((sweep_cases + 1))
    done
  done
}
for phase in backup-complete migration-started binary-replaced candidate-started helper-replaced; do
  sweep "${phase}" 1 1 0
done
sweep backup-complete 1 1 1
sweep migration-started 1 1 1
sweep backup-complete 0 0 0
sweep helper-replaced 0 1 0
printf 'restore interruption sweep: %s injected interruptions each recovered by a retry\n' "${sweep_cases}"
unset -f cp mv rm runuser base_runuser
eval "${real_systemctl_simple}"
eval "${real_sync}"
eval "${real_runuser}"

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
PATH="${fake_bin}:${PATH}" bash "${INSTALLER_FIXTURE_INSTALLER}" --help >/dev/null
[[ ! -s "${CURL_LOG}" ]] || { printf 'help unexpectedly required network access\n' >&2; exit 1; }
PATH="${fake_bin}:${PATH}" PROBE_404_VERSION=v1.2.3 bash "${INSTALLER_FIXTURE_INSTALLER}" sentinel
[[ "$(<"${BOOTSTRAP_RESULT}")" == 'v1.2.3|sentinel' ]] || { printf 'explicit version was not forwarded to tagged installer\n' >&2; exit 1; }
grep -Fxq 'https://github.com/404-git-404/404-probe/releases/download/v1.2.3/install.sh' "${CURL_LOG}" \
  || { printf 'explicit version did not fetch its tagged installer\n' >&2; exit 1; }
grep -Fxq 'https://github.com/404-git-404/404-probe/releases/download/v1.2.3/SHA256SUMS' "${CURL_LOG}" \
  || { printf 'explicit version did not fetch its tagged manifest\n' >&2; exit 1; }
! grep -q '/releases/latest' "${CURL_LOG}" || { printf 'explicit version unexpectedly resolved latest\n' >&2; exit 1; }

printf 'Server upgrade transaction fault tests passed.\n'
