#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
# Tests the installer's systemd sandbox adaptation with a stub for the transient-unit trials. Nothing is installed.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(tail -n 1 "${INSTALLER_FIXTURE_INSTALLER}")" == 'main "$@"' ]]
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
mkdir -p "${temporary}/units" "${temporary}/security/export" "${temporary}/updater" "${temporary}/etc"

# The installer with its fixed system paths moved under a temporary directory.
sed '$d' "${INSTALLER_FIXTURE_INSTALLER}" \
  | sed -e "s#/etc/systemd/system#${temporary}/units#g" \
        -e "s#/var/lib/404-probe-security#${temporary}/security#g" \
        -e "s#/var/lib/404-probe-updater#${temporary}/updater#g" \
        -e "s#/etc/404-probe#${temporary}/etc#g" >"${temporary}/installer.sh"
source "${temporary}/installer.sh"

# ---- stubs -----------------------------------------------------------------------------------------
install() { return 0; }
chown() { return 0; }
note() { printf '%s\n' "$*" >>"${temporary}/notes"; }
CALLS="${temporary}/calls"
systemctl() { printf 'systemctl %s\n' "$*" >>"${CALLS}"; case "$1" in start) [[ "${COLLECTOR_START:-ok}" == ok ]] || return 1 ;; esac; return 0; }

# Trials: each case names the directive keys that "fail" when present in a trial.
FAIL_KEYS=""
TRIALS="${temporary}/trials"
trial_ok() { # directives...
  local directive key
  printf '%s
' "$*" >>"${TRIALS}"
  for directive in "$@"; do
    key="${directive%%=*}"
    [[ " ${FAIL_KEYS} " != *" ${key} "* ]] || return 1
    if [[ "${directive}" == AmbientCapabilities=CAP_NET_RAW && " ${FAIL_KEYS} " == *" NET_RAW "* ]]; then return 1; fi
  done
  # A combination fails when it holds every key of FAIL_COMBO (directives that only fail together).
  if [[ -n "${FAIL_COMBO:-}" ]]; then
    local all=1 want
    for want in ${FAIL_COMBO}; do [[ " $* " == *" ${want}="* || " $* " == *"${want}="* ]] || all=0; done
    (( all == 0 )) || return 1
  fi
  return 0
}
BATCHES="${temporary}/batches"
sandbox_trial_batch() {
  local index entry
  SANDBOX_BATCH_RESULT=()
  printf 'batch of %s
' "${#SANDBOX_BATCH[@]}" >>"${BATCHES}"
  for index in "${!SANDBOX_BATCH[@]}"; do
    mapfile -t entry <<<"${SANDBOX_BATCH[index]}"
    if trial_ok "${entry[@]}"; then SANDBOX_BATCH_RESULT[index]=0; else SANDBOX_BATCH_RESULT[index]=1; fi
  done
}
NET_RAW_BOUND=1
sandbox_net_raw_in_bounding_set() { (( NET_RAW_BOUND == 1 )); }
# Accounts on the stubbed system, "name:uid" separated by spaces (default: a normal Debian with nobody).
PASSWD_USERS="root:0 daemon:1 nobody:65534"
getent() {
  local entry name
  [[ "$1" == passwd ]] || { command getent "$@"; return; }
  if [[ $# -eq 2 ]]; then
    for entry in ${PASSWD_USERS}; do name="${entry%%:*}"; [[ "${name}" == "$2" ]] && { echo "${name}:x:${entry#*:}:${entry#*:}::/:/usr/sbin/nologin"; return 0; }; done
    return 2
  fi
  for entry in ${PASSWD_USERS}; do name="${entry%%:*}"; echo "${name}:x:${entry#*:}:${entry#*:}::/:/usr/sbin/nologin"; done
}
sandbox_virtualization() { printf 'podman'; }

run_probe() { SANDBOX_REMOVED=""; FAIL_KEYS="$1"; FAIL_COMBO="${2:-}"; NET_RAW_BOUND=1; : >"${TRIALS}"; : >"${BATCHES}"; sandbox_probe; }
unit_text() { # generator output with the current SANDBOX_REMOVED
  render_agent_unit ""
}
all_units() { # prints the agent, updater, removal and collector units for the current SANDBOX_REMOVED
  rm -f "${temporary}"/units/*
  render_agent_unit "" >"${temporary}/units/agent.service"
  install_agent_updater_unit
  install_agent_removal_worker_unit
  install_agent_removal_finalizer_unit
  install_security_collector
}
has() { grep -Fxq -- "$2" "$1"; }

# ---- 1. full hardening works: the units are exactly what the installer always generated ----------------
run_probe ""
[[ -z "${SANDBOX_REMOVED}" ]]
[[ "$(wc -l <"${BATCHES}")" -eq 1 ]] || { printf 'a fully working machine should need a single batch (one systemd reload)
' >&2; exit 1; }
all_units
cp -r "${temporary}/units" "${temporary}/units-full"
sandbox_filter_saved="$(declare -f sandbox_filter)"
sandbox_filter() { cat; }
all_units
for f in "${temporary}"/units/*; do cmp -s "${f}" "${temporary}/units-full/$(basename "${f}")" || { printf 'unit %s differs from the unfiltered original\n' "${f}" >&2; exit 1; }; done
eval "${sandbox_filter_saved}"
for key in PrivateTmp=true ProtectSystem=strict ProtectKernelTunables=true RestrictSUIDSGID=true LockPersonality=true MemoryDenyWriteExecute=true 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' CapabilityBoundingSet=CAP_NET_RAW AmbientCapabilities=CAP_NET_RAW; do
  has "${temporary}/units-full/agent.service" "${key}" || { printf 'full agent unit lacks %s\n' "${key}" >&2; exit 1; }
done
printf 'PASS full hardening keeps every unit exactly as before\n'

# ---- 2. measured Podman result: kernel-protect and seccomp directives are out, the rest stays ----------------
podman_fail="ProtectKernelTunables ProtectKernelModules ProtectKernelLogs RestrictSUIDSGID LockPersonality MemoryDenyWriteExecute RestrictAddressFamilies"
run_probe "${podman_fail}"
[[ "${SANDBOX_REMOVED}" == "${podman_fail}" ]] || { printf 'removed=%s\n' "${SANDBOX_REMOVED}" >&2; exit 1; }
all_units
for f in "${temporary}"/units/*; do
  for key in ProtectKernelTunables ProtectKernelModules ProtectKernelLogs RestrictSUIDSGID LockPersonality MemoryDenyWriteExecute RestrictAddressFamilies; do
    ! grep -q "^${key}=" "${f}" || { printf '%s still has %s\n' "${f}" "${key}" >&2; exit 1; }
  done
done
for kept in PrivateTmp=true ProtectSystem=strict ProtectHome=true ProtectControlGroups=true NoNewPrivileges=true UMask=0077 "User=${SERVICE_USER}" CapabilityBoundingSet=CAP_NET_RAW AmbientCapabilities=CAP_NET_RAW; do
  has "${temporary}/units/agent.service" "${kept}" || { printf 'reduced agent unit lost %s\n' "${kept}" >&2; exit 1; }
done
has "${temporary}/units/404-probe-agent-removal-finalize.service" PrivateNetwork=true
has "${temporary}/units/404-probe-security-collect.service" PrivateNetwork=true
has "${temporary}/units/404-probe-agent-updater.service" 'ReadWritePaths='"${AGENT_UPDATER_STATE}"' /usr/local/bin /run/404-probe'
printf 'PASS the measured Podman set drops only what cannot run\n'

# ---- 3. namespace directives that fail drop out; everything else is kept -----------------------------------
run_probe "PrivateTmp PrivateNetwork ProtectSystem ProtectHome ProtectControlGroups"
[[ "${SANDBOX_REMOVED}" == "PrivateTmp PrivateNetwork ProtectSystem ProtectHome ProtectControlGroups" ]]
all_units
for key in PrivateTmp PrivateNetwork ProtectSystem ProtectHome ProtectControlGroups; do ! grep -q "^${key}=" "${temporary}/units/agent.service"; done
has "${temporary}/units/agent.service" RestrictSUIDSGID=true
has "${temporary}/units/agent.service" NoNewPrivileges=true
has "${temporary}/units/agent.service" 'ReadWritePaths='"${STATE_DIRECTORY}"' /run/404-probe'
printf 'PASS namespace directives are dropped on their own\n'

# ---- 4. each directive works alone but not together: the namespace group goes too ------------------------------
run_probe "" "PrivateTmp ProtectSystem"
case " ${SANDBOX_REMOVED} " in *" PrivateTmp "*" ProtectSystem "*) ;; *) printf 'combination failure not handled: %s\n' "${SANDBOX_REMOVED}" >&2; exit 1 ;; esac
for key in PrivateTmp PrivateNetwork ProtectSystem ProtectHome ProtectControlGroups; do [[ " ${SANDBOX_REMOVED} " == *" ${key} "* ]]; done
printf 'PASS a failing combination falls back to the set without namespaces\n'

# ---- 5. nothing works: exit before anything is registered, with the token message ------------------------------
for failing in "UMask" "NoNewPrivileges"; do
  FAIL_KEYS="${failing}"; : >"${TRIALS}"
  status=0; output="$( (sandbox_probe) 2>&1)" || status=$?
  [[ "${status}" -ne 0 && "${output}" == *"no registration token was used"* && "${output}" == *"nothing was installed"* ]] || { printf 'unexpected: %s\n' "${output}" >&2; exit 1; }
done
# Even the reduced set does not run together:
FAIL_KEYS=""; FAIL_COMBO="UMask NoNewPrivileges"; : >"${TRIALS}"
status=0; output="$( (sandbox_probe) 2>&1)" || status=$?
[[ "${status}" -ne 0 && "${output}" == *"no registration token was used"* ]]
printf 'PASS an unusable machine is refused before any registration\n'

# ---- 6. CAP_NET_RAW cannot be granted: only that pair is removed ----------------------------------------------
run_probe "NET_RAW"
[[ "${SANDBOX_REMOVED}" == "NET_RAW" ]]
all_units
! grep -q 'CAP_NET_RAW' "${temporary}/units/agent.service"
has "${temporary}/units/agent.service" PrivateTmp=true
has "${temporary}/units/404-probe-security-collect.service" 'CapabilityBoundingSet='
has "${temporary}/units/404-probe-security-collect.service" 'AmbientCapabilities='
description="$(sandbox_describe)"
[[ "${description}" == *"ICMP"*"unavailable"* && "${description}" != *"Omitted systemd directives"* ]]
printf 'PASS CAP_NET_RAW is dropped alone and ICMP is announced as unavailable\n'

# ---- 6b. CAP_NET_RAW is accepted by systemd but outside the bounding set (Podman default): treated as unavailable
SANDBOX_REMOVED=""; FAIL_KEYS=""; FAIL_COMBO=""; NET_RAW_BOUND=0; : >"${TRIALS}"; : >"${BATCHES}"; sandbox_probe
[[ "${SANDBOX_REMOVED}" == "NET_RAW" ]]
! grep -q "CAP_NET_RAW" "${TRIALS}"
NET_RAW_BOUND=1
printf 'PASS a CAP_NET_RAW outside the bounding set is dropped without a trial
'

# ---- 6c. a host without a "nobody" account keeps ICMP: the grant is tried as another account, or as root
for users in "root:0 daemon:1" "root:0 svc:1500" "root:0"; do
  PASSWD_USERS="${users}"; run_probe ""
  [[ -z "${SANDBOX_REMOVED}" ]] || { printf 'NET_RAW lost without nobody (%s): %s
' "${users}" "${SANDBOX_REMOVED}" >&2; exit 1; }
  case "${users}" in
    *daemon*) grep -q "User=daemon" "${TRIALS}" ;;
    *svc*) grep -q "User=svc" "${TRIALS}" ;;
    *) grep -q "User=root.*AmbientCapabilities=CAP_NET_RAW" "${TRIALS}" ;;
  esac
done
PASSWD_USERS="root:0 daemon:1 nobody:65534"
run_probe "NET_RAW"
[[ "${SANDBOX_REMOVED}" == "NET_RAW" ]]
printf 'PASS ICMP is not lost on hosts without a nobody account
'

# ---- 7. description and recorded configuration ---------------------------------------------------------------
run_probe "${podman_fail} NET_RAW"
description="$(sandbox_describe)"
[[ "${description}" == *"检测到容器环境或沙箱受限，已使用精简加固"* && "${description}" == *"podman"* && "${description}" == *"ProtectKernelTunables"* && "${description}" == *"MemoryDenyWriteExecute"* ]]
omitted_line="$(grep 'Omitted systemd directives' <<<"${description}")"
[[ "${omitted_line}" != *NET_RAW* ]]
SECURITY_COLLECTOR_STATE="unavailable:sandbox_unavailable"
sandbox_write_config
grep -Fxq 'PROBE_404_SANDBOX_MODE=reduced' "${SANDBOX_CONFIG}"
grep -Fq '仅供参考，安装和升级时都会重新探测' "${SANDBOX_CONFIG}"
grep -Fxq "PROBE_404_SANDBOX_REMOVED=\"${SANDBOX_REMOVED}\"" "${SANDBOX_CONFIG}"
grep -Fxq 'PROBE_404_SECURITY_COLLECTOR="unavailable:sandbox_unavailable"' "${SANDBOX_CONFIG}"
run_probe ""; SECURITY_COLLECTOR_STATE=ok; sandbox_write_config
grep -Fxq 'PROBE_404_SANDBOX_MODE=full' "${SANDBOX_CONFIG}"
[[ -z "$(sandbox_describe)" ]]
printf 'PASS the outcome is described and recorded\n'

# ---- 8. a collector that cannot start does not stop the install ------------------------------------------------
securityctl() { :; }
systemctl() {
  printf 'systemctl %s\n' "$*" >>"${CALLS}"
  case "$1 $2" in
    "start 404-probe-security-collect.service") [[ "${COLLECTOR_START:-ok}" == ok ]] || return 1 ;;
    "show -p") case "$3" in ExecMainStatus) printf '%s\n' "${COLLECTOR_STATUS:-0}" ;; Result) printf '%s\n' "${COLLECTOR_RESULT:-success}" ;; esac ;;
  esac
  return 0
}
rm -f "${SECURITY_EXPORT_DIRECTORY}/collector-unavailable"; : >"${CALLS}"; COLLECTOR_START=fail COLLECTOR_STATUS=217 COLLECTOR_RESULT=exit-code
COLLECTOR_START=fail; COLLECTOR_STATUS=217; COLLECTOR_RESULT=exit-code
security_collector_activate
[[ "$(cat "${SECURITY_EXPORT_DIRECTORY}/collector-unavailable")" == sandbox_unavailable && "${SECURITY_COLLECTOR_STATE}" == unavailable:sandbox_unavailable ]]
grep -Fq 'systemctl disable --now 404-probe-security-collect.timer' "${CALLS}"
! grep -Fq 'enable --now 404-probe-security-collect.timer' "${CALLS}"
grep -Fq 'systemctl reset-failed 404-probe-security-collect.service' "${CALLS}"
grep -Fq 'could not be started here' "${temporary}/notes"
COLLECTOR_STATUS=1; COLLECTOR_RESULT=exit-code; : >"${CALLS}"
security_collector_activate
[[ "$(cat "${SECURITY_EXPORT_DIRECTORY}/collector-unavailable")" == collector_start_failed ]]
COLLECTOR_START=ok; : >"${CALLS}"
security_collector_activate
[[ ! -e "${SECURITY_EXPORT_DIRECTORY}/collector-unavailable" && "${SECURITY_COLLECTOR_STATE}" == ok ]]
grep -Fq 'enable --now 404-probe-security-collect.timer' "${CALLS}"
printf 'PASS an unstartable collector leaves the Agent installed, timer off, reason recorded\n'

# ---- 9. ordering and reuse: every path that rewrites units measures first and records the result -------------
body() { declare -f "$1"; }
install_body="$(body install_agent)"
probe_at="${install_body%%sandbox_prepare*}"; redeem_at="${install_body%%oneclick_redeem_token*}"
(( ${#probe_at} < ${#redeem_at} )) || { printf 'the sandbox probe must run before the token is redeemed\n' >&2; exit 1; }
[[ "${install_body}" == *security_collector_activate* && "${install_body}" == *sandbox_write_config* ]]
for fn in bootstrap_existing_agent upgrade_old_beta_agent setup_security_existing; do
  text="$(body "${fn}")"
  [[ "${text}" == *sandbox_prepare* && "${text}" == *sandbox_write_config* ]] || { printf '%s does not reuse the sandbox adaptation\n' "${fn}" >&2; exit 1; }
done
# Every generator of a unit file passes through the filter.
for fn in render_agent_unit install_agent_updater_unit install_agent_removal_worker_unit install_agent_removal_finalizer_unit install_security_collector; do
  [[ "$(body "${fn}")" == *sandbox_filter* ]] || { printf '%s writes a unit without sandbox_filter\n' "${fn}" >&2; exit 1; }
done
# A failed install after the token was spent says so.
[[ "$(body rollback_install_transaction)" == *"generate a new command in the panel"* ]]
printf 'PASS probing precedes registration and every unit-writing path uses the result\n'

# ---- 10. OpenRC has no such options and no probe ------------------------------------------------------------
: >"${TRIALS}"; : >"${temporary}/notes"; INSTALL_INIT=openrc sandbox_prepare
[[ ! -s "${TRIALS}" && ! -s "${temporary}/notes" ]]
printf 'PASS OpenRC is untouched\n'

# The progress message must be visible before any trial begins, including on fully supported hosts.
(
  : >"${temporary}/notes"
  sandbox_probe() {
    [[ "$(cat "${temporary}/notes")" == '正在检测运行环境… (Checking the runtime environment…)' ]]
    printf 'probe started\n' >>"${temporary}/notes"
    SANDBOX_REMOVED=""
  }
  INSTALL_INIT=systemd sandbox_prepare
  [[ "$(wc -l <"${temporary}/notes")" -eq 2 ]]
)
printf 'PASS the bilingual progress message precedes the sandbox probe\n'

# ---- 11. a working collector clears an earlier "unavailable" marker on every recovery path -------------------------
for fn in setup_security_existing; do
  body="$(declare -f "${fn}")"
  enable_at="${body%%enable --now 404-probe-security-collect.timer*}"
  clear_at="${body%%collector-unavailable*}"
  [[ "${body}" == *collector-unavailable* ]] && (( ${#enable_at} < ${#clear_at} )) || { printf '%s does not clear the marker after enabling the timer
' "${fn}" >&2; exit 1; }
done
# The installed helper's own setup_security (a separate copy of the logic inside install.sh) does too.
helper_body="$(sed -n '/^setup_security() {$/,/^}$/p' "${INSTALLER_FIXTURE_INSTALLER}")"
[[ -n "${helper_body}" && "${helper_body}" == *'rm -f -- "${SECURITY_EXPORT_DIRECTORY}/collector-unavailable"'* ]]
printf 'PASS recovery paths clear the collector-unavailable marker
'
