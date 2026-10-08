#!/usr/bin/env bash

set -Eeuo pipefail

readonly REPOSITORY="404-git-404/404-probe"
readonly DEFAULT_VERSION="v1.0.0"
readonly INSTALL_HELPER="/usr/local/sbin/404-probe-install"
readonly SERVER_BINARY="/usr/local/bin/404-probe-server"
readonly AGENT_BINARY="/usr/local/bin/404-probe-agent"
readonly CONFIG_DIRECTORY="/etc/404-probe"
readonly STATE_DIRECTORY="/var/lib/404-probe"
readonly SELECTOR_ORDER_FILE="${CONFIG_DIRECTORY}/selector-order.json"
readonly SERVER_DATABASE="${STATE_DIRECTORY}/404-probe.db"
readonly SERVER_UNIT="/etc/systemd/system/404-probe-server.service"
readonly AGENT_UNIT="/etc/systemd/system/404-probe-agent.service"
readonly AGENT_UPDATER_UNIT="/etc/systemd/system/404-probe-agent-updater.service"
readonly AGENT_UPDATER_STATE="/var/lib/404-probe-updater"
readonly AGENT_UPDATER_SOCKET="/run/404-probe/agent-updater.sock"
readonly AGENT_REMOVAL_WORKER_UNIT="/etc/systemd/system/404-probe-agent-removal.service"
readonly AGENT_REMOVAL_FINALIZER_UNIT="/etc/systemd/system/404-probe-agent-removal-finalize.service"
readonly AGENT_REMOVAL_STATE_DIRECTORY="${AGENT_UPDATER_STATE}/removal"
readonly AGENT_REMOVAL_REQUEST_FILE="${AGENT_REMOVAL_STATE_DIRECTORY}/request.json"
readonly AGENT_REMOVAL_FINISHED_MARKER="${AGENT_REMOVAL_STATE_DIRECTORY}/agent-uninstalled"
readonly AGENT_REMOVAL_WORKER_BINARY="/usr/local/bin/.404-probe-agent-removal-worker"
readonly SECURITY_STATE_DIRECTORY="/var/lib/404-probe-security"
readonly SECURITY_EXPORT_DIRECTORY="${SECURITY_STATE_DIRECTORY}/export"
readonly SECURITY_SERVICE_UNIT="/etc/systemd/system/404-probe-security-collect.service"
readonly SECURITY_TIMER_UNIT="/etc/systemd/system/404-probe-security-collect.timer"
readonly SERVICE_USER="404-probe"
readonly ENROLLMENT_PREFIX="404p1_"
readonly SERVER_UPGRADE_DIRECTORY="/var/lib/404-probe-upgrade"
readonly SERVER_UPGRADE_STATE="${SERVER_UPGRADE_DIRECTORY}/pending"
readonly SERVER_UPGRADE_BACKUP="${SERVER_UPGRADE_DIRECTORY}/backup"
readonly SERVER_UPGRADE_CANDIDATE="/usr/local/bin/.404-probe-server.candidate"
readonly SERVER_UPGRADE_HELPER_CANDIDATE="${INSTALL_HELPER}.candidate"
readonly SERVER_UPGRADE_LOCK_DIRECTORY="/run/404-probe-upgrade"
readonly SERVER_UPGRADE_LOCK="${SERVER_UPGRADE_LOCK_DIRECTORY}/server.lock"
readonly DOWNLOAD_CONNECT_TIMEOUT_SECONDS=10
readonly DOWNLOAD_ATTEMPT_TIMEOUT_SECONDS=60
readonly DOWNLOAD_RETRY_MAX_SECONDS=180
readonly DOWNLOAD_RETRIES=4

INSTALL_TRANSACTION_ACTIVE=0
INSTALL_TRANSACTION_ROLE=""
INSTALL_TRANSACTION_UNIT=""
INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_USER_CREATED=0
INSTALL_TRANSACTION_UPDATER_STATE_CREATED=0
INSTALL_TRANSACTION_SECURITY_STATE_CREATED=0
INSTALL_TRANSACTION_AGENT_SBIN_CREATED=0
INSTALL_TRANSACTION_PATHS=()

die() {
  printf '404-probe installer: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '\n%s\n' "$*"
}

curl_with_retry() {
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --connect-timeout "${DOWNLOAD_CONNECT_TIMEOUT_SECONDS}" \
    --max-time "${DOWNLOAD_ATTEMPT_TIMEOUT_SECONDS}" \
    --retry "${DOWNLOAD_RETRIES}" --retry-all-errors \
    --retry-max-time "${DOWNLOAD_RETRY_MAX_SECONDS}" "$@"
}

# Download to a sibling partial file so curl can resume bytes received before a
# transient timeout. The rename is atomic and callers authenticate the finished
# file before use. Alternate URLs are fixed official entry points for the same
# versioned asset; they never weaken the release identity checks.
download_to_file() (
  local destination="$1" url partial deadline remaining
  local -a extra_args=()
  shift
  partial="${destination}.partial"
	deadline="${DOWNLOAD_DEADLINE_SECONDS:-$((SECONDS + DOWNLOAD_RETRY_MAX_SECONDS))}"
	[[ -z "${DOWNLOAD_ACCEPT_HEADER:-}" ]] || extra_args+=(--header "Accept: ${DOWNLOAD_ACCEPT_HEADER}")
  [[ ! -e "${partial}" || ( -f "${partial}" && ! -L "${partial}" ) ]] \
    || die "unsafe partial download path: ${partial}"
  for url in "$@"; do
	remaining=$((deadline - SECONDS))
	(( remaining > 0 )) || break
    if curl_with_retry --retry-max-time "${remaining}" "${extra_args[@]}" --continue-at - --output "${partial}" "${url}"; then
      [[ -f "${partial}" && ! -L "${partial}" ]] || die "download did not create a regular file"
      mv -f -- "${partial}" "${destination}"
      return 0
    fi
    note "Download entry failed; trying the next official entry for the same asset."
  done
	[[ "${DOWNLOAD_PRESERVE_PARTIAL:-0}" == 1 ]] || rm -f -- "${partial}"
  return 1
)

release_version() {
  [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.[1-9][0-9]*)?$ ]]
}

download_release_asset() {
  local version="$1" name="$2" destination="$3" api_document api_url tag deadline remaining
  release_version "${version}" || die "release asset version must be stable or explicit beta.N"
  [[ "${name}" =~ ^[A-Za-z0-9._-]+$ ]] || die "release asset name is invalid"
	deadline=$((SECONDS + DOWNLOAD_RETRY_MAX_SECONDS))
	DOWNLOAD_DEADLINE_SECONDS="${deadline}" DOWNLOAD_PRESERVE_PARTIAL=1 download_to_file "${destination}" \
	  "https://github.com/${REPOSITORY}/releases/download/${version}/${name}" && return 0
	api_document="${destination}.release-api.json"
	remaining=$((deadline - SECONDS))
	(( remaining > 0 )) || { rm -f -- "${destination}.partial"; return 1; }
	curl_with_retry --retry-max-time "${remaining}" --header 'Accept: application/vnd.github+json' \
	  --output "${api_document}" "https://api.github.com/repos/${REPOSITORY}/releases/tags/${version}" \
	  || { rm -f -- "${destination}.partial" "${api_document}"; return 1; }
	tag="$(sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' "${api_document}")"
	[[ "${tag}" == "${version}" ]] || { rm -f -- "${destination}.partial" "${api_document}"; return 1; }
	api_url="$(awk -v wanted="${name}" '
	  /^[[:space:]]*"url":[[:space:]]*"https:\/\/api\.github\.com\/repos\/404-git-404\/404-probe\/releases\/assets\/[0-9]+"/ {
	    candidate=$0; sub(/^[^"]*"url":[[:space:]]*"/, "", candidate); sub(/".*/, "", candidate)
	  }
	  /^[[:space:]]*"name":[[:space:]]*"/ {
	    asset=$0; sub(/^[^"]*"name":[[:space:]]*"/, "", asset); sub(/".*/, "", asset)
	    if (asset == wanted && candidate != "") { count++; result=candidate }
	  }
	  END { if (count == 1) print result }
	' "${api_document}")"
	rm -f -- "${api_document}"
	[[ "${api_url}" =~ ^https://api\.github\.com/repos/404-git-404/404-probe/releases/assets/[0-9]+$ ]] \
	  || { rm -f -- "${destination}.partial"; return 1; }
	DOWNLOAD_DEADLINE_SECONDS="${deadline}" DOWNLOAD_ACCEPT_HEADER='application/octet-stream' \
	  download_to_file "${destination}" "${api_url}"
}

canonical_version() {
  [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

target_version() {
  local version="${PROBE_404_VERSION:-${DEFAULT_VERSION}}"
  release_version "${version}" || die "target version must be stable or explicit beta.N"
  printf '%s\n' "${version}"
}

bootstrap_latest_installer() (
  local effective latest release_base temporary_directory installer checksum_line status
  if [[ -n "${PROBE_404_VERSION:-}" ]]; then
    latest="${PROBE_404_VERSION}"
    release_version "${latest}" || die "explicit target version must be stable or beta.N"
  else
    effective="$(curl_with_retry \
      --output /dev/null --write-out '%{url_effective}' "https://github.com/${REPOSITORY}/releases/latest")" \
      || die "could not resolve the latest stable release"
    latest="${effective##*/}"
    canonical_version "${latest}" || die "latest stable release returned a non-canonical version"
    [[ "${effective}" == "https://github.com/${REPOSITORY}/releases/tag/${latest}" ]] \
      || die "latest stable release redirected outside the official repository"
  fi
  release_base="https://github.com/${REPOSITORY}/releases/download/${latest}"
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf -- "${temporary_directory}"' EXIT
  installer="${temporary_directory}/install.sh"
  download_to_file "${installer}" \
    "${release_base}/install.sh" \
    "https://raw.githubusercontent.com/${REPOSITORY}/${latest}/install.sh" \
    || die "could not download the ${latest} installer from official entries"
  download_release_asset "${latest}" SHA256SUMS "${temporary_directory}/SHA256SUMS" \
    || die "could not download the ${latest} checksum manifest from official entries"
  checksum_line="$(grep -E '^[[:xdigit:]]{64}  install\.sh$' "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "latest release does not authenticate install.sh exactly once"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "latest release installer checksum verification failed"
  if LC_ALL=C grep -q $'\r' "${installer}"; then
    die "latest release installer contains carriage returns"
  fi
  bash -n "${installer}" || die "latest release installer failed syntax validation"
  PROBE_404_INSTALLER_BOOTSTRAPPED=1 PROBE_404_BOOTSTRAP_SHA256SUMS="${temporary_directory}/SHA256SUMS" \
    PROBE_404_VERSION="${latest}" bash "${installer}" "$@"
  status=$?
  exit "${status}"
)

usage() {
  cat <<'EOF'
Usage:
  404-probe-install                 install, or offer a protected existing-Server upgrade
  404-probe-install agent --server <origin>
  404-probe-install setup-security  enable V0.9 local security audit on an existing Agent
  404-probe-install selector-order <absolute-sing-box-config.json>
  404-probe-install enroll <name>   create one Agent enrollment token
  404-probe-install uninstall <server|agent> [--confirm-delete-data]

Uninstall permanently removes the selected role's services, binaries,
configuration, credentials, database/state, updater, and managed backups.
Systemd journal history and unrecognized files are preserved.
EOF
}

# OpenRC template structure: Komari Agent (MIT), fixed commit
# 828afafe7fb5b1b58c1232094e1e46320719016e. Standard supervision:
# OpenRC aec5dedb1ce30f8188b8ef1042e518651cc0fe33 (BSD-2-Clause).
# Full notices: docs/os1b-openrc-third-party-notices-20261004.md.
detect_install_platform() {
  [[ "${EUID}" -eq 0 && "$(uname -s)" == Linux ]] || die "root on Linux is required"
  local id version runlevel pid1
  [[ -f /etc/os-release && "$(stat -Lc '%U' /etc/os-release)" == root ]] || die "a root-managed OS identity file is required"
  case "$(readlink -f /etc/os-release)" in /etc/os-release|/usr/lib/os-release) ;; *) die "unexpected OS identity link target" ;; esac
  id="$(awk -F= '$1 == "ID" { gsub(/["\047]/,"",$2); print $2 }' /etc/os-release)"
  case "${id}" in
    alpine)
      [[ -f /etc/alpine-release && ! -L /etc/alpine-release ]] || die "Alpine identity is unavailable"
      version="$(</etc/alpine-release)"
      [[ "${version}" =~ ^3\.24\.[0-9]+$ ]] || die "Alpine v1.0 requires stable 3.24.x"
      for command_name in rc-service rc-update rc-status openrc-run supervise-daemon; do
        command -v "${command_name}" >/dev/null || die "OpenRC runtime is required: ${command_name}"
      done
      [[ -d /run/openrc && ! -L /run/openrc && -r /run/openrc/softlevel ]] || die "OpenRC has not booted"
      runlevel="$(rc-status --runlevel)" || die "cannot query the running OpenRC runlevel"
      [[ "${runlevel}" =~ ^[a-zA-Z0-9_-]+$ && "$(</run/openrc/softlevel)" == "${runlevel}" ]] || die "OpenRC runlevel is inconsistent"
      pid1="$(</proc/1/comm)"
      [[ "${pid1}" == init || "${pid1}" == openrc-init ]] || die "a complete OpenRC boot environment is required, not a process-only container"
      rc-status sysinit >/dev/null || die "OpenRC sysinit state is unavailable"
      INSTALL_INIT=openrc
      ;;
    debian)
      version="$(awk -F= '$1 == "VERSION_ID" { gsub(/["\047]/,"",$2); print $2 }' /etc/os-release)"
      [[ "${version}" == 12 || "${version}" == 13 ]] || die "supported Debian versions are 12 and 13"
      [[ ! -e /etc/alpine-release ]] || die "contradictory platform identity"
      INSTALL_INIT=systemd
      ;;
    *) die "supported platforms are Debian 12/13 and Alpine 3.24.x" ;;
  esac
}

require_openrc_tools() {
  local command_name
  (( BASH_VERSINFO[0] >= 4 )) || die "Bash 4 or newer is required"
  for command_name in bash awk base64 basename cmp cp curl df dirname find flock getent grep head id install mktemp mountpoint readlink rm rmdir runuser sed sha256sum sort stat sync tar tr useradd userdel groupdel; do
    command -v "${command_name}" >/dev/null || die "missing ${command_name}; install bash, coreutils, util-linux, shadow, curl, ca-certificates and GNU tar in the isolated Alpine guest first"
  done
  for command_name in cp stat sync sha256sum install readlink; do
    "${command_name}" --version 2>/dev/null | grep 'GNU coreutils' >/dev/null || die "GNU coreutils ${command_name} is required; BusyBox aliases are not sufficient"
  done
  sha256sum --help | grep -- '--strict' >/dev/null || die "strict checksum verification is required"
  cp --help | grep -- '--preserve' >/dev/null || die "metadata-preserving copy is required"
  sync --help | grep -- '--file-system' >/dev/null || die "filesystem sync support is required"
  tar --version | grep 'GNU tar' >/dev/null || die "GNU tar is required"
  runuser --version | grep 'util-linux' >/dev/null || die "util-linux runuser is required"
  flock --version | grep 'util-linux' >/dev/null || die "util-linux flock is required"
  [[ -r /etc/ssl/certs/ca-certificates.crt ]] || die "CA certificates are required"
}

reject_openrc_deferred() {
  [[ "${INSTALL_INIT:-systemd}" != openrc ]] || die "$1 is unsupported on Alpine v1.0 (deferred to v1.1); no managed files were changed"
}
reject_deferred_command_on_host() {
  case "$1" in setup-security|finalize-agent-removal-account|recover-agent-removal)
    local id
    id="$(awk -F= '$1 == "ID" { gsub(/["\047]/,"",$2); print $2 }' /etc/os-release 2>/dev/null || true)"
    [[ "${id}" != alpine ]] || die "$1 is unsupported on Alpine v1.0 (deferred to v1.1); no managed files were changed"
    ;;
  esac
}

openrc_unit() { printf '/etc/init.d/404-probe-%s\n' "$1"; }
business_unit() {
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_unit "$1"
  elif [[ "$1" == server ]]; then printf '%s\n' "${SERVER_UNIT}"
  else printf '%s\n' "${AGENT_UNIT}"; fi
}
openrc_wrapper() { printf '/usr/local/sbin/404-probe-%s-openrc\n' "$1"; }
openrc_role_residue() {
  local path
  for path in "$(openrc_unit "$1")" "$(openrc_wrapper "$1")" "/run/404-probe-$1-openrc" "/var/log/404-probe/$1.log" "/var/log/404-probe/$1.log.1" "/var/log/404-probe/$1.log.lock"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || return 0
  done
  return 1
}
openrc_regular_or_absent() {
  local path="$1" owner="${2:-root}" permissions="${3:-755}"
  [[ -e "${path}" || -L "${path}" ]] || return 0
  [[ -f "${path}" && ! -L "${path}" && "$(stat -c '%U:%a' -- "${path}")" == "${owner}:${permissions}" ]] || die "unsafe managed regular file; no deletion was performed"
}
openrc_safe_parents() {
  local path="${1%/*}" permissions
  while [[ "${path}" != / ]]; do
    [[ -d "${path}" && ! -L "${path}" && "$(stat -c '%U' -- "${path}")" == root ]] || die "unsafe managed path parent"
    permissions="$(stat -c '%a' -- "${path}")"
    [[ "${permissions}" =~ ^[0-7]{3,4}$ ]] && (( (8#${permissions} & 022) == 0 )) || die "writable managed path parent"
    path="${path%/*}"; [[ -n "${path}" ]] || path=/
  done
}
openrc_origin_valid() {
  local origin="$1" port
  [[ "${origin}" =~ ^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$ ]] || return 1
  port="${BASH_REMATCH[2]#:}"
  if [[ -n "${port}" ]]; then (( 10#${port} > 0 && 10#${port} <= 65535 )) || return 1; fi
  [[ "${origin}" != http://* || "${origin}" =~ ^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$ ]]
}
load_openrc_env() {
  local role="$1" file="${CONFIG_DIRECTORY}/$1.env" line key value count=0 name carriage_return
  printf -v carriage_return '\r'
  local -A seen=()
  [[ "${role}" == server || "${role}" == agent ]] || die "invalid wrapper role"
  [[ -f "${file}" && ! -L "${file}" && "$(stat -c '%U:%G:%a' -- "${file}")" == "root:${SERVICE_USER}:640" ]] || die "unsafe service environment"
  [[ "$(stat -c '%s' -- "${file}")" -le 16384 ]] || die "service environment is oversized"
  cmp -s "${file}" <(LC_ALL=C tr -d '\000' <"${file}") || die "invalid service environment bytes"
  for name in $(compgen -A variable PROBE_404_); do unset "${name}"; done
  while IFS= read -r line || [[ -n "${line}" ]]; do
    count=$((count+1)); (( count <= 32 && ${#line} <= 1024 )) || die "service environment exceeds limits"
    [[ "${line}" == *=* && "${line}" != *"${carriage_return}"* ]] || die "invalid service environment record"
    key="${line%%=*}"; value="${line#*=}"
    [[ "${key}" =~ ^PROBE_404_[A-Z_]+$ && -z "${seen[${key}]:-}" ]] || die "unknown or duplicate service environment key"
    seen["${key}"]=1
    case "${role}:${key}" in
      server:PROBE_404_WEB_PUBLIC_ORIGIN|agent:PROBE_404_SERVER) openrc_origin_valid "${value}" || die "invalid service origin" ;;
      agent:PROBE_404_AGENT_ID) [[ "${value}" =~ ^[0-9a-f]{32}$ ]] || die "invalid Agent identity" ;;
      agent:PROBE_404_TOKEN) [[ "${value}" =~ ^[A-Za-z0-9_-]{43}$ ]] || die "invalid Agent credential" ;;
      agent:PROBE_404_STATE) [[ "${value}" == "${STATE_DIRECTORY}/agent.epoch" ]] || die "invalid state path" ;;
      agent:PROBE_404_COUNTRY_CODE) [[ -z "${value}" || "${value}" =~ ^[A-Z]{2}$ ]] || die "invalid country code" ;;
      agent:PROBE_404_SELECTOR_ORDER) [[ "${value}" == "${SELECTOR_ORDER_FILE}" ]] || die "invalid selector path" ;;
      agent:PROBE_404_SECURITY_EXPORT) [[ "${value}" == "${SECURITY_EXPORT_DIRECTORY}" ]] || die "invalid legacy export path" ;;
      agent:PROBE_404_SECURITY_ACKS) [[ "${value}" == "${STATE_DIRECTORY}/agent.security-acks.json" ]] || die "invalid legacy ack path" ;;
      agent:PROBE_404_SING_BOX_CLASH_API) [[ "${value}" =~ ^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$ ]] && openrc_origin_valid "${value}" || die "Clash API must be valid loopback HTTP" ;;
      *) die "unrecognized service environment key" ;;
    esac
    printf -v "${key}" '%s' "${value}"
    export "${key}"
  done <"${file}"
  if [[ "${role}" == server ]]; then
    [[ -n "${seen[PROBE_404_WEB_PUBLIC_ORIGIN]:-}" ]] || die "missing Server origin"
  else
    for key in PROBE_404_SERVER PROBE_404_AGENT_ID PROBE_404_TOKEN PROBE_404_STATE PROBE_404_COUNTRY_CODE PROBE_404_SELECTOR_ORDER; do
      [[ -n "${seen[${key}]:-}" ]] || die "missing required Agent environment key"
    done
  fi
}
openrc_business_pids() {
  local role="$1" binary uid process executable owner record start_before start_after readable
  local -a fields=()
  binary="/usr/local/bin/404-probe-${role}"; uid="$(id -u "${SERVICE_USER}" 2>/dev/null || true)"
  for process in /proc/[0-9]*; do
    [[ -d "${process}" ]] || continue
    executable="$(readlink "${process}/exe" 2>/dev/null || true)"
    if [[ "${role}" == agent && -z "${executable}" && -n "${uid}" ]]; then
      owner="$(stat -c '%u' -- "${process}" 2>/dev/null || true)"
      if [[ "${owner}" == "${uid}" ]]; then
        if ! IFS= read -r record <"${process}/stat" 2>/dev/null; then
          [[ ! -d "${process}" ]] && continue
          die "cannot verify Agent process identity"
        fi
        read -r -a fields <<<"${record##*) }"; start_before="${fields[19]:-}"
        [[ "${start_before}" =~ ^[0-9]+$ ]] || die "invalid Agent process identity"
        readable=1
        executable="$(runuser -u "${SERVICE_USER}" -- readlink "${process}/exe" 2>/dev/null)" || readable=0
        owner="$(stat -c '%u' -- "${process}" 2>/dev/null || true)"
        if ! IFS= read -r record <"${process}/stat" 2>/dev/null; then
          [[ ! -d "${process}" ]] && continue
          die "cannot recheck Agent process identity"
        fi
        read -r -a fields <<<"${record##*) }"; start_after="${fields[19]:-}"
        [[ "${owner}" == "${uid}" && "${start_after}" == "${start_before}" ]] || die "Agent process identity changed during executable verification"
        (( readable != 0 )) && [[ -n "${executable}" ]] || die "cannot verify live Agent executable"
      fi
    fi
    if [[ "${executable}" == "${binary}" || "${executable}" == "${binary} (deleted)" ]]; then
      [[ -n "${uid}" && "$(stat -c '%u' -- "${process}")" == "${uid}" ]] || die "business binary has an unexpected process owner"
      printf '%s\n' "${process##*/}"
    fi
  done
}
openrc_wait() {
  local role="$1" attempts=20 pids
  while (( attempts > 0 )); do
    if rc-service "404-probe-${role}" status >/dev/null 2>&1; then
      pids="$(openrc_business_pids "${role}")" || die "cannot verify business process"
      [[ -z "${pids}" ]] || return 0
    fi
    attempts=$((attempts-1)); (( attempts == 0 )) || sleep 0.5
  done
  die "OpenRC service did not establish a verified business process"
}
openrc_stop() {
  local role="$1" service="404-probe-$1" pids attempts=20
  if [[ -e "$(openrc_unit "${role}")" ]]; then
    openrc_regular_or_absent "$(openrc_unit "${role}")"
    rc-service "${service}" stop || die "could not stop OpenRC service; nothing was removed"
    rc-service "${service}" status >/dev/null 2>&1 && die "service remains active; nothing was removed"
  fi
  if [[ -r "/run/404-probe-${role}-openrc/supervisor.pid" ]]; then
    local supervisor_pid
    [[ "$(stat -c '%s' "/run/404-probe-${role}-openrc/supervisor.pid")" -le 16 ]] || die "supervisor PID file exceeds limits"
    IFS= read -r supervisor_pid <"/run/404-probe-${role}-openrc/supervisor.pid"
    [[ "${supervisor_pid}" =~ ^[1-9][0-9]{0,9}$ ]] || die "invalid supervisor PID"
    kill -0 "${supervisor_pid}" 2>/dev/null && die "supervisor remains; nothing was removed"
  fi
  while (( attempts > 0 )); do
      pids="$(openrc_business_pids "${role}")" || die "cannot verify process exit"
      [[ -n "${pids}" ]] || break
      attempts=$((attempts-1)); (( attempts == 0 )) || sleep 0.5
  done
  [[ -z "${pids}" ]] || die "business process remains; nothing was removed"
  if [[ -e "/etc/runlevels/default/${service}" || -L "/etc/runlevels/default/${service}" ]]; then
    [[ -L "/etc/runlevels/default/${service}" && "$(readlink -f "/etc/runlevels/default/${service}")" == "$(openrc_unit "${role}")" ]] || die "unrecognized runlevel entry"
    rc-update del "${service}" default || die "could not disable OpenRC service"
  fi
}
openrc_append_log() {
  local file="$1" line="$2" newline="$3"
  flock -x 9 || die "log lock failed"
  if (( $(stat -c '%s' -- "${file}") + ${#line} + newline > 1048576 )); then
    cp -- "${file}" "${file}.1" || die "log rotation failed"
    : >"${file}"
  fi
  printf '%s' "${line}" >>"${file}"
  (( newline == 0 )) || printf '\n' >>"${file}"
  flock -u 9
}
openrc_log() {
  export LC_ALL=C
  local role="$1" file="/var/log/404-probe/$1.log" line pending="" secret="${PROBE_404_TOKEN:-}" control="" cut eof
  [[ "${EUID}" -eq "$(id -u "${SERVICE_USER}")" ]] || die "logger must use the service account"
  if [[ "${role}" == server ]]; then
    openrc_regular_or_absent "${CONFIG_DIRECTORY}/control.token" "${SERVICE_USER}" 400
    [[ -f "${CONFIG_DIRECTORY}/control.token" && "$(stat -c '%s' "${CONFIG_DIRECTORY}/control.token")" -le 44 ]] || die "invalid control credential file"
    IFS= read -r control <"${CONFIG_DIRECTORY}/control.token"
    [[ "${control}" =~ ^[A-Za-z0-9_-]{43}$ ]] || die "invalid control credential"
  fi
  local path
  for path in "${file}" "${file}.1" "${file}.lock"; do openrc_regular_or_absent "${path}" "${SERVICE_USER}" 600; [[ -f "${path}" ]] || die "log file missing"; done
  exec 9>>"${file}.lock"
  # Retain 42 bytes so a 43-byte credential spanning read boundaries cannot leak.
  # -n returns promptly on newline. A newline is a safe credential boundary;
  # only full chunks need a carried tail. Preserve newlines and final EOF bytes.
  while true; do
    line=""; eof=0
    IFS= read -r -n 4096 line || eof=1
    pending+="${line}"
    [[ -z "${secret}" ]] || pending="${pending//"${secret}"/[REDACTED]}"
    [[ -z "${control}" ]] || pending="${pending//"${control}"/[REDACTED]}"
    if (( ${#line} < 4096 || eof != 0 )); then
      if [[ -n "${pending}" ]] || (( eof == 0 )); then openrc_append_log "${file}" "${pending}" "$((1-eof))"; fi
      pending=""
    else
      cut=$(( ${#pending} - 42 ))
      if (( cut > 0 )); then
        openrc_append_log "${file}" "${pending:0:cut}" 0
        pending="${pending:cut}"
      fi
    fi
    (( eof == 0 )) || break
  done
}
render_openrc_wrapper() {
  local role="$1" insecure="$2"
  [[ "${role}" == server || "${role}" == agent ]] || die "invalid role"
  [[ "${insecure}" == 0 || "${insecure}" == 1 ]] || die "invalid wrapper mode"
  printf '#!/usr/bin/env bash\nset -Eeuo pipefail\n'
  printf 'readonly ROLE=%q INSECURE=%q\n' "${role}" "${insecure}"
  printf 'readonly CONFIG_DIRECTORY=%q STATE_DIRECTORY=%q SELECTOR_ORDER_FILE=%q SECURITY_EXPORT_DIRECTORY=%q SERVICE_USER=%q\n' "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" "${SELECTOR_ORDER_FILE}" "${SECURITY_EXPORT_DIRECTORY}" "${SERVICE_USER}"
  printf 'die() { printf "404-probe service: %%s\\n" "$*" >&2; exit 1; }\n'
  declare -f load_openrc_env openrc_origin_valid openrc_regular_or_absent openrc_append_log openrc_log
  cat <<'WRAPPER'
[[ $# -le 1 && ( "${1:-run}" == run || "${1:-run}" == log || "${1:-run}" == log-user ) ]] || die "unsupported wrapper mode"
if [[ "${1:-run}" == log && "${EUID}" == 0 ]]; then
  exec runuser -u "${SERVICE_USER}" -- "/usr/local/sbin/404-probe-${ROLE}-openrc" log-user
fi
[[ "${EUID}" -eq "$(id -u "${SERVICE_USER}")" && "${EUID}" -ne 0 ]] || die "ordinary service must not run as root"
load_openrc_env "${ROLE}"
if [[ "${1:-run}" == log || "${1:-run}" == log-user ]]; then openrc_log "${ROLE}"; exit; fi
umask 0077
if [[ "${ROLE}" == agent ]]; then
  args=(--interval 10s --job-interval 10s --timeout 8s)
  (( INSECURE == 0 )) || args+=(--allow-insecure-http)
  exec /usr/local/bin/404-probe-agent "${args[@]}"
else
  args=(serve --listen 127.0.0.1:8080 --db "${STATE_DIRECTORY}/404-probe.db" --offline-timeout 30s --control-token-file "${CONFIG_DIRECTORY}/control.token" --web-password-hash-file "${CONFIG_DIRECTORY}/web-password.hash" --web-public-origin "${PROBE_404_WEB_PUBLIC_ORIGIN}")
  (( INSECURE == 0 )) || args+=(--web-allow-insecure-http)
  exec /usr/local/bin/404-probe-server "${args[@]}"
fi
WRAPPER
}
render_openrc_service() {
  local role="$1"
  [[ "${role}" == server || "${role}" == agent ]] || die "invalid role"
  cat <<EOF
#!/sbin/openrc-run
# Portions copyright (c) 2025 komari-monitor, MIT. See installer notices.
description="404-probe ${role} (Alpine v1.0 basic support)"
command="$(openrc_wrapper "${role}")"
command_user="${SERVICE_USER}:${SERVICE_USER}"
directory="${STATE_DIRECTORY}"
pidfile="/run/404-probe-${role}-openrc/supervisor.pid"
supervisor=supervise-daemon
respawn_delay=5
respawn_max=3
# No resetting window: finite restarts across the supervisor lifetime.
respawn_period=0
retry="SIGTERM/15/SIGKILL/5"
stopgroup=yes
output_logger="$(openrc_wrapper "${role}") log"
error_logger="$(openrc_wrapper "${role}") log"
depend() { need net; after network; }
start_pre() {
  [ ! -L /run/404-probe-${role}-openrc ] || return 1
  if [ -e /run/404-probe-${role}-openrc ]; then
    [ "\$(stat -c '%U:%G:%a' /run/404-probe-${role}-openrc)" = root:root:755 ] || return 1
  fi
  checkpath --directory --mode 0755 --owner root:root /run/404-probe-${role}-openrc || return 1
  [ "\$(stat -c '%U:%G:%a' /run/404-probe-${role}-openrc)" = root:root:755 ] || return 1
  [ ! -L /usr/local/bin/404-probe-${role} ] && [ ! -L $(openrc_wrapper "${role}") ] || return 1
  [ "\$(stat -c '%U:%G:%a' /usr/local/bin/404-probe-${role})" = root:root:755 ] || return 1
  [ "\$(stat -c '%U:%G:%a' $(openrc_wrapper "${role}"))" = root:root:755 ] || return 1
}
EOF
}
install_openrc_business() {
  local role="$1" insecure="$2" path unit wrapper
  unit="$(openrc_unit "${role}")"; wrapper="$(openrc_wrapper "${role}")"
  for path in "${unit}" "${wrapper}"; do
    openrc_safe_parents "${path}"
    [[ ! -e "${path}" && ! -L "${path}" ]] || die "OpenRC managed path already exists"
    track_install_path "${path}"
  done
  [[ ! -e /var/log/404-probe || ( -d /var/log/404-probe && ! -L /var/log/404-probe && "$(stat -c '%U:%G:%a' /var/log/404-probe)" == "root:${SERVICE_USER}:750" ) ]] || die "unsafe log directory"
  openrc_safe_parents /var/log/404-probe
  openrc_safe_parents "/run/404-probe-${role}-openrc"
  [[ ! -e "/run/404-probe-${role}-openrc" && ! -L "/run/404-probe-${role}-openrc" ]] || die "runtime residue already exists"
  install -d -m 0750 -o root -g "${SERVICE_USER}" /var/log/404-probe
  install -d -m 0755 -o root -g root "/run/404-probe-${role}-openrc"
  for path in "/var/log/404-probe/${role}.log" "/var/log/404-probe/${role}.log.1" "/var/log/404-probe/${role}.log.lock"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || die "log residue already exists"
    track_install_path "${path}"; install -m 0600 -o "${SERVICE_USER}" -g "${SERVICE_USER}" /dev/null "${path}"
  done
  render_openrc_wrapper "${role}" "${insecure}" >"${wrapper}"
  render_openrc_service "${role}" >"${unit}"
  chown root:root "${wrapper}" "${unit}"; chmod 0755 "${wrapper}" "${unit}"
  bash -n "${wrapper}" || die "wrapper syntax failed"
  (load_openrc_env "${role}")
  INSTALL_TRANSACTION_UNIT="404-probe-${role}"
  rc-update add "404-probe-${role}" default || die "could not enable service"
  rc-service "404-probe-${role}" start || die "could not start service"
  openrc_wait "${role}"
}

openrc_uninstall() {
  local role="$1" binary="/usr/local/bin/404-probe-$1" path owner permissions
  confirm_uninstall "${role}" "${2:-}"
  path="${AGENT_UNIT}"; [[ "${role}" != server ]] || path="${SERVER_UNIT}"
  [[ ! -e "${path}" && ! -L "${path}" ]] || die "mixed systemd/OpenRC role residue; nothing was removed"
  if id "${SERVICE_USER}" >/dev/null 2>&1; then
    if declare -F validate_service_user >/dev/null; then validate_service_user; else validate_removable_service_user; fi
  fi
  preflight_uninstall_trees "${role}"
  validate_owned_tree "/run/404-probe-${role}-openrc"
  validate_owned_tree /var/log/404-probe
  [[ ! -d "/run/404-probe-${role}-openrc" || "$(stat -c '%U:%G:%a' "/run/404-probe-${role}-openrc")" == root:root:755 ]] || die "unsafe runtime directory"
  [[ ! -d /var/log/404-probe || "$(stat -c '%U:%G:%a' /var/log/404-probe)" == "root:${SERVICE_USER}:750" ]] || die "unsafe log directory"
  # Delayed v1.1 resources are never adopted or silently wiped by basic support.
  if [[ "${role}" == agent ]]; then
    for path in "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_STATE}" "${AGENT_REMOVAL_WORKER_UNIT}" "${AGENT_REMOVAL_FINALIZER_UNIT}" "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" "${SECURITY_STATE_DIRECTORY}"; do
      [[ ! -e "${path}" && ! -L "${path}" ]] || die "deferred resource residue requires manual review; nothing was removed"
    done
  else
    [[ ! -e "${SERVER_UPGRADE_DIRECTORY}" && ! -L "${SERVER_UPGRADE_DIRECTORY}" ]] || die "deferred upgrade residue requires manual review"
  fi
  for path in "$(openrc_unit "${role}")" "$(openrc_wrapper "${role}")" "${binary}"; do
    if [[ "${role}" == agent && "${path}" == "$(openrc_wrapper agent)" && ! -e /usr/local/sbin && ! -L /usr/local/sbin && ! -e "${INSTALL_HELPER:-/usr/local/sbin/404-probe-install}" && ! -L "${INSTALL_HELPER:-/usr/local/sbin/404-probe-install}" ]] \
      && ! agent_residue_exists && ! server_residue_exists; then
      openrc_safe_parents /usr/local/sbin
    else
      openrc_safe_parents "${path}"
    fi
    openrc_regular_or_absent "${path}"
  done
  for path in "${CONFIG_DIRECTORY}/${role}.env"; do openrc_regular_or_absent "${path}" root 640; done
  if [[ "${role}" == server ]]; then
    for path in "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash"; do openrc_regular_or_absent "${path}" "${SERVICE_USER}" 400; done
    for path in "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"; do
      [[ ! -e "${path}" && ! -L "${path}" ]] || [[ -f "${path}" && ! -L "${path}" && "$(stat -c '%U' -- "${path}")" == "${SERVICE_USER}" ]] || die "unsafe database residue"
    done
  else
    for path in "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock"; do
      [[ ! -e "${path}" && ! -L "${path}" ]] || [[ -f "${path}" && ! -L "${path}" && "$(stat -c '%U' -- "${path}")" == "${SERVICE_USER}" ]] || die "unsafe epoch residue"
    done
    openrc_regular_or_absent "${SELECTOR_ORDER_FILE}" root 640
  fi
  for path in "/var/log/404-probe/${role}.log" "/var/log/404-probe/${role}.log.1" "/var/log/404-probe/${role}.log.lock"; do openrc_regular_or_absent "${path}" "${SERVICE_USER}" 600; done
  path="/run/404-probe-${role}-openrc/supervisor.pid"
  if [[ -e "${path}" || -L "${path}" ]]; then
    [[ -f "${path}" && ! -L "${path}" && "$(stat -c '%U' "${path}")" == root ]] || die "unsafe supervisor PID file"
    permissions="$(stat -c '%a' "${path}")"; (( (8#${permissions} & 022) == 0 )) || die "writable supervisor PID file"
  fi
  openrc_stop "${role}"
  rm -f -- "$(openrc_unit "${role}")" "$(openrc_wrapper "${role}")" "${binary}" "${CONFIG_DIRECTORY}/${role}.env" "/run/404-probe-${role}-openrc/supervisor.pid" "/var/log/404-probe/${role}.log" "/var/log/404-probe/${role}.log.1" "/var/log/404-probe/${role}.log.lock"
  if [[ "${role}" == server ]]; then
    rm -f -- "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash"
  else
    rm -f -- "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${SELECTOR_ORDER_FILE}"
  fi
  rmdir --ignore-fail-on-non-empty "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" "/run/404-probe-${role}-openrc" /var/log/404-probe 2>/dev/null || true
  if ! server_residue_exists && ! agent_residue_exists && [[ ! -d "${CONFIG_DIRECTORY}" && ! -d "${STATE_DIRECTORY}" && ! -d /var/log/404-probe ]]; then
    remove_service_account_for_uninstall
    if [[ -e "${INSTALL_HELPER:-/usr/local/sbin/404-probe-install}" ]]; then
      openrc_regular_or_absent "${INSTALL_HELPER:-/usr/local/sbin/404-probe-install}"
      rm -f -- "${INSTALL_HELPER:-/usr/local/sbin/404-probe-install}"
    fi
  fi
  printf 'Uninstalled %s basic OpenRC resources; unknown files and other-role residue were preserved.\n' "${role}"
}

openrc_validate_agent_install() {
  openrc_regular_or_absent "${AGENT_BINARY}"; [[ -f "${AGENT_BINARY}" ]] || die "Agent binary missing"
  openrc_regular_or_absent "$(openrc_unit agent)"; [[ -f "$(openrc_unit agent)" ]] || die "Agent service missing"
  openrc_regular_or_absent "$(openrc_wrapper agent)"; [[ -f "$(openrc_wrapper agent)" ]] || die "Agent wrapper missing"
  cmp -s "$(openrc_unit agent)" <(render_openrc_service agent) || die "unrecognized OpenRC Agent service"
  (load_openrc_env agent)
}

render_platform_functions() {
  declare -f detect_install_platform require_openrc_tools reject_openrc_deferred reject_deferred_command_on_host openrc_unit business_unit openrc_wrapper openrc_role_residue openrc_regular_or_absent openrc_safe_parents openrc_origin_valid load_openrc_env openrc_business_pids openrc_wait openrc_stop openrc_uninstall openrc_validate_agent_install render_openrc_service agent_residue_exists
}

require_root_linux_systemd() {
  [[ "${EUID}" -eq 0 ]] || die "run this installer as root (for example, with sudo)"
  [[ "$(uname -s)" == "Linux" ]] || die "only Linux is supported"
  detect_install_platform
  if [[ "${INSTALL_INIT}" == openrc ]]; then require_openrc_tools; return; fi
  command -v systemctl >/dev/null 2>&1 || die "systemd is required"
  [[ -d /run/systemd/system ]] || die "systemd is not running"
  for command_name in awk base64 basename cmp curl df dirname flock getent install mktemp readlink runuser sha256sum sort stat sync tar; do
    command -v "${command_name}" >/dev/null 2>&1 || die "required command not found: ${command_name}"
  done
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) die "unsupported architecture: $(uname -m) (supported: amd64, arm64)" ;;
  esac
}

validate_service_user() {
  local passwd_entry shadow_entry group_entry uid_min
  local name _ uid gid home shell password group_name members other_primary_users
  passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
  [[ -n "${passwd_entry}" ]] || die "existing ${SERVICE_USER} account has no passwd entry"
  IFS=: read -r name _ uid gid _ home shell <<<"${passwd_entry}"
  [[ "${name}" == "${SERVICE_USER}" && "${uid}" =~ ^[0-9]+$ && "${gid}" =~ ^[0-9]+$ ]] \
    || die "existing ${SERVICE_USER} account has an invalid passwd entry"
  uid_min="$(awk '$1 == "UID_MIN" { print $2; exit }' /etc/login.defs 2>/dev/null || true)"
  [[ "${uid_min}" =~ ^[0-9]+$ ]] || uid_min=1000
  (( uid > 0 && uid < uid_min )) \
    || die "existing ${SERVICE_USER} account UID ${uid} is not a system UID below ${uid_min}"
  case "${shell##*/}" in
    nologin|false) ;;
    *) die "existing ${SERVICE_USER} account shell ${shell} is interactive" ;;
  esac
  [[ "${home}" == "${STATE_DIRECTORY}" ]] \
    || die "existing ${SERVICE_USER} account home must be ${STATE_DIRECTORY}, not ${home}"

  shadow_entry="$(getent shadow "${SERVICE_USER}" || true)"
  [[ -n "${shadow_entry}" ]] || die "cannot verify that the existing ${SERVICE_USER} password is locked"
  IFS=: read -r _ password _ <<<"${shadow_entry}"
  [[ "${password}" == '!'* || "${password}" == '*'* ]] \
    || die "existing ${SERVICE_USER} account password is not locked"

  group_entry="$(getent group "${gid}" || true)"
  [[ -n "${group_entry}" ]] || die "existing ${SERVICE_USER} primary group was not found"
  IFS=: read -r group_name _ _ members <<<"${group_entry}"
  [[ "${group_name}" == "${SERVICE_USER}" ]] \
    || die "existing ${SERVICE_USER} primary group must be named ${SERVICE_USER}"
  [[ -z "${members}" ]] || die "existing ${SERVICE_USER} primary group has supplementary members"
  other_primary_users="$(getent passwd | awk -F: -v gid="${gid}" -v user="${SERVICE_USER}" '$4 == gid && $1 != user { print $1 }')"
  [[ -z "${other_primary_users}" ]] \
    || die "existing ${SERVICE_USER} primary group is also used by: ${other_primary_users//$'\n'/, }"
}

remove_service_account_for_uninstall() {
  local passwd_entry group_entry name _ uid gid group_name group_gid members other_primary_users
  passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
  if [[ -n "${passwd_entry}" ]]; then
    validate_service_user
    IFS=: read -r name _ uid gid _ _ _ <<<"${passwd_entry}"
    userdel "${SERVICE_USER}" || die "managed files were removed but the service account could not be removed"
    id "${SERVICE_USER}" >/dev/null 2>&1 && die "service account remains after userdel"
  else
    group_entry="$(getent group "${SERVICE_USER}" || true)"
    [[ -z "${group_entry}" ]] || die "orphan or unrecognized service group remains; refusing to remove it"
    return 0
  fi
  group_entry="$(getent group "${SERVICE_USER}" || true)"
  if [[ -n "${group_entry}" ]]; then
    IFS=: read -r group_name _ group_gid members <<<"${group_entry}"
    [[ "${group_name}" == "${SERVICE_USER}" && "${group_gid}" == "${gid}" && -z "${members}" ]] \
      || die "service group changed or is shared during account cleanup"
    other_primary_users="$(getent passwd | awk -F: -v gid="${gid}" '$4 == gid { print $1 }')"
    [[ -z "${other_primary_users}" ]] || die "service group remains in use by another account"
    groupdel "${SERVICE_USER}" || die "service group could not be removed"
  fi
  ! id "${SERVICE_USER}" >/dev/null 2>&1 || die "service account remains after removal"
  ! getent group "${SERVICE_USER}" >/dev/null 2>&1 || die "service group remains after removal"
}

create_service_user() {
  if id "${SERVICE_USER}" >/dev/null 2>&1; then
    validate_service_user
    return
  fi
  command -v useradd >/dev/null 2>&1 || die "useradd is required"
  command -v userdel >/dev/null 2>&1 || die "userdel is required"
  command -v groupdel >/dev/null 2>&1 || die "groupdel is required"
  if getent group "${SERVICE_USER}" >/dev/null 2>&1; then
    die "group ${SERVICE_USER} already exists without a compatible ${SERVICE_USER} account"
  fi
  local nologin_shell
  nologin_shell="$(command -v nologin || true)"
  [[ -n "${nologin_shell}" ]] || nologin_shell="/usr/sbin/nologin"
  useradd --system --user-group --home-dir "${STATE_DIRECTORY}" --shell "${nologin_shell}" --password '!' "${SERVICE_USER}"
  INSTALL_TRANSACTION_USER_CREATED=1
  if ! (validate_service_user); then
    userdel "${SERVICE_USER}" >/dev/null 2>&1 || true
    groupdel "${SERVICE_USER}" >/dev/null 2>&1 || true
    INSTALL_TRANSACTION_USER_CREATED=0
    die "new ${SERVICE_USER} account failed service-account validation and was removed"
  fi
}

begin_install_transaction() {
  INSTALL_TRANSACTION_ACTIVE=1
  INSTALL_TRANSACTION_AGENT_SBIN_CREATED=0
  INSTALL_TRANSACTION_ROLE="$1"
  [[ -d "${CONFIG_DIRECTORY}" ]] || INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=1
  [[ -d "${STATE_DIRECTORY}" ]] || INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=1
  trap 'rollback_install_transaction $?' EXIT
  trap 'exit 130' HUP INT TERM
}

track_install_path() {
  INSTALL_TRANSACTION_PATHS+=("$1")
}

openrc_agent_sbin_unmounted() {
  if awk -v directory=/usr/local/sbin '
    NF < 10 || $1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+:[0-9]+$/ || $4 !~ /^\// || $5 !~ /^\// { bad=1 }
    { separator=0; for (i=7; i<=NF; i++) if ($i == "-") { separator=i; break }; if (!separator || separator+3>NF) bad=1 }
    $5 == directory { mounted=1 }
    END { if (NR == 0 || bad) exit 2; if (mounted) exit 10 }
  ' /proc/self/mountinfo; then
    return 0
  else
    local status="$?"
    [[ "${status}" != 10 ]] || die "mounted Agent helper directory"
    die "cannot verify Agent helper directory mounts"
  fi
}

prepare_openrc_agent_sbin() {
  [[ "${INSTALL_INIT:-systemd}" == openrc ]] || return 0
  openrc_safe_parents /usr/local/sbin
  openrc_agent_sbin_unmounted
  if [[ -e /usr/local/sbin || -L /usr/local/sbin ]]; then
    openrc_safe_parents "${INSTALL_HELPER}"
    return 0
  fi
  install -d -m 0755 -o root -g root /usr/local/sbin
  INSTALL_TRANSACTION_AGENT_SBIN_CREATED=1
  [[ ! -L /usr/local/sbin && "$(stat -c '%U:%G:%a' /usr/local/sbin)" == root:root:755 ]] \
    || die "unsafe newly created Agent helper directory"
}

rollback_install_transaction() {
  local status="$1"
  local index
  trap - EXIT HUP INT TERM
  if (( INSTALL_TRANSACTION_ACTIVE == 0 )); then
    exit "${status}"
  fi
  printf '\n404-probe installer: installation failed; rolling back new artifacts...\n' >&2
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then
    if [[ -n "${INSTALL_TRANSACTION_UNIT}" ]]; then openrc_stop "${INSTALL_TRANSACTION_ROLE}"; fi
    for (( index=${#INSTALL_TRANSACTION_PATHS[@]}-1; index>=0; index-- )); do rm -f -- "${INSTALL_TRANSACTION_PATHS[index]}"; done
    if [[ "${INSTALL_TRANSACTION_ROLE}" == server ]]; then rm -f -- "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"
    else rm -f -- "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock"; fi
    rm -f -- "/run/404-probe-${INSTALL_TRANSACTION_ROLE}-openrc/supervisor.pid"
    rmdir --ignore-fail-on-non-empty "/run/404-probe-${INSTALL_TRANSACTION_ROLE}-openrc" /var/log/404-probe 2>/dev/null || true
    (( INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED == 0 )) || rmdir -- "${CONFIG_DIRECTORY}" 2>/dev/null || true
    (( INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED == 0 )) || rmdir -- "${STATE_DIRECTORY}" 2>/dev/null || true
    if (( INSTALL_TRANSACTION_AGENT_SBIN_CREATED != 0 )) && [[ -d /usr/local/sbin && ! -L /usr/local/sbin && "$(stat -c '%U:%G:%a' /usr/local/sbin)" == root:root:755 ]] \
      && (openrc_agent_sbin_unmounted) && (openrc_safe_parents /usr/local/sbin); then
      rmdir -- /usr/local/sbin 2>/dev/null || true
    fi
    (( INSTALL_TRANSACTION_USER_CREATED == 0 )) || printf 'Preserved locked service account for a safe retry.\n' >&2
    exit "${status}"
  fi
  if [[ -n "${INSTALL_TRANSACTION_UNIT}" ]]; then
    systemctl stop "${INSTALL_TRANSACTION_UNIT}" >/dev/null 2>&1 || true
    if systemctl is-active --quiet "${INSTALL_TRANSACTION_UNIT}"; then
      printf '404-probe installer: service is still active; unit and binary were preserved for recovery\n' >&2
      exit "${status}"
    fi
    systemctl disable "${INSTALL_TRANSACTION_UNIT}" >/dev/null 2>&1 || true
  fi
  if [[ "${INSTALL_TRANSACTION_ROLE}" == "server" ]]; then
    rm -f -- "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"
  elif [[ "${INSTALL_TRANSACTION_ROLE}" == "agent" ]]; then
	 systemctl stop 404-probe-agent-updater.service >/dev/null 2>&1 || true
	 systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true
    rm -f -- "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" "${AGENT_REMOVAL_WORKER_BINARY}"
	 systemctl stop 404-probe-security-collect.timer 404-probe-security-collect.service >/dev/null 2>&1 || true
	 systemctl disable 404-probe-security-collect.timer >/dev/null 2>&1 || true
  fi
  for (( index=${#INSTALL_TRANSACTION_PATHS[@]}-1; index>=0; index-- )); do
    rm -f -- "${INSTALL_TRANSACTION_PATHS[index]}"
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  (( INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED == 0 )) || rmdir -- "${CONFIG_DIRECTORY}" 2>/dev/null || true
  (( INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED == 0 )) || rmdir -- "${STATE_DIRECTORY}" 2>/dev/null || true
  (( INSTALL_TRANSACTION_UPDATER_STATE_CREATED == 0 )) || rmdir -- "${AGENT_UPDATER_STATE}" 2>/dev/null || true
  (( INSTALL_TRANSACTION_SECURITY_STATE_CREATED == 0 )) || rm -rf -- "${SECURITY_STATE_DIRECTORY}"
  if (( INSTALL_TRANSACTION_USER_CREATED != 0 )); then
    printf '404-probe installer: preserved the new locked service account for a safe retry\n' >&2
  fi
  exit "${status}"
}

commit_install_transaction() {
  INSTALL_TRANSACTION_ACTIVE=0
  INSTALL_TRANSACTION_AGENT_SBIN_CREATED=0
  INSTALL_TRANSACTION_PATHS=()
  trap - EXIT HUP INT TERM
}

prepare_directories() {
  local actual
  if [[ -e "${CONFIG_DIRECTORY}" ]]; then
    [[ -d "${CONFIG_DIRECTORY}" ]] || die "${CONFIG_DIRECTORY} exists and is not a directory"
    actual="$(stat -c '%U:%G:%a' "${CONFIG_DIRECTORY}")"
    [[ "${actual}" == "root:${SERVICE_USER}:750" ]] \
      || die "existing ${CONFIG_DIRECTORY} must be root:${SERVICE_USER} mode 0750, found ${actual}"
  else
    install -d -m 0750 -o root -g "${SERVICE_USER}" "${CONFIG_DIRECTORY}"
  fi
  if [[ -e "${STATE_DIRECTORY}" ]]; then
    [[ -d "${STATE_DIRECTORY}" ]] || die "${STATE_DIRECTORY} exists and is not a directory"
    actual="$(stat -c '%U:%G:%a' "${STATE_DIRECTORY}")"
    [[ "${actual}" == "${SERVICE_USER}:${SERVICE_USER}:700" ]] \
      || die "existing ${STATE_DIRECTORY} must be ${SERVICE_USER}:${SERVICE_USER} mode 0700, found ${actual}"
  else
    install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${STATE_DIRECTORY}"
  fi
}

download_binary() (
  local role="$1"
  local destination="$2"
  local architecture version asset temporary_directory checksum_line
  architecture="$(detect_architecture)"
  version="$(target_version)"
  asset="404-probe-${role}-linux-${architecture}"
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf -- "${temporary_directory}"' EXIT

  note "Downloading ${asset} (${version})..."
  if [[ -n "${PROBE_404_LOCAL_ASSET_DIRECTORY:-}" ]]; then
    [[ -f "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${asset}" ]] || die "local release asset not found: ${asset}"
    [[ -f "${PROBE_404_LOCAL_ASSET_DIRECTORY}/SHA256SUMS" ]] || die "local SHA256SUMS not found"
    cp -- "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${asset}" "${temporary_directory}/${asset}"
    cp -- "${PROBE_404_LOCAL_ASSET_DIRECTORY}/SHA256SUMS" "${temporary_directory}/SHA256SUMS"
  else
    download_release_asset "${version}" "${asset}" "${temporary_directory}/${asset}" \
      || die "could not download ${asset} (${version}) from official entries"
		if [[ "${PROBE_404_INSTALLER_BOOTSTRAPPED:-}" == 1 && -n "${PROBE_404_BOOTSTRAP_SHA256SUMS:-}" ]]; then
		  [[ -f "${PROBE_404_BOOTSTRAP_SHA256SUMS}" && ! -L "${PROBE_404_BOOTSTRAP_SHA256SUMS}" ]] \
		    || die "verified bootstrap checksum manifest is unavailable"
		  cp -- "${PROBE_404_BOOTSTRAP_SHA256SUMS}" "${temporary_directory}/SHA256SUMS"
		else
		  download_release_asset "${version}" SHA256SUMS "${temporary_directory}/SHA256SUMS" \
		    || die "could not download SHA256SUMS (${version}) from official entries"
		fi
  fi

  checksum_line="$(grep -E "^[[:xdigit:]]{64}  ${asset}$" "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "SHA256SUMS does not contain exactly one checksum for ${asset}"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "SHA256 verification failed for ${asset}"
  install -m 0755 "${temporary_directory}/${asset}" "${destination}"
)

render_local_helper() {
  printf '#!/usr/bin/env bash\n'
  render_platform_functions
  cat <<'HELPER'
#!/usr/bin/env bash
set -Eeuo pipefail

readonly SERVER_BINARY="/usr/local/bin/404-probe-server"
readonly AGENT_BINARY="/usr/local/bin/404-probe-agent"
readonly SERVER_DATABASE="/var/lib/404-probe/404-probe.db"
readonly SERVER_UNIT="/etc/systemd/system/404-probe-server.service"
readonly AGENT_UNIT="/etc/systemd/system/404-probe-agent.service"
readonly AGENT_UPDATER_UNIT="/etc/systemd/system/404-probe-agent-updater.service"
readonly AGENT_UPDATER_STATE="/var/lib/404-probe-updater"
readonly AGENT_UPDATER_SOCKET="/run/404-probe/agent-updater.sock"
readonly AGENT_REMOVAL_WORKER_UNIT="/etc/systemd/system/404-probe-agent-removal.service"
readonly AGENT_REMOVAL_FINALIZER_UNIT="/etc/systemd/system/404-probe-agent-removal-finalize.service"
readonly AGENT_REMOVAL_STATE_DIRECTORY="${AGENT_UPDATER_STATE}/removal"
readonly AGENT_REMOVAL_REQUEST_FILE="${AGENT_REMOVAL_STATE_DIRECTORY}/request.json"
readonly AGENT_REMOVAL_FINISHED_MARKER="${AGENT_REMOVAL_STATE_DIRECTORY}/agent-uninstalled"
readonly AGENT_REMOVAL_WORKER_BINARY="/usr/local/bin/.404-probe-agent-removal-worker"
readonly SECURITY_STATE_DIRECTORY="/var/lib/404-probe-security"
readonly SECURITY_EXPORT_DIRECTORY="${SECURITY_STATE_DIRECTORY}/export"
readonly SECURITY_SERVICE_UNIT="/etc/systemd/system/404-probe-security-collect.service"
readonly SECURITY_TIMER_UNIT="/etc/systemd/system/404-probe-security-collect.timer"
readonly SERVER_UPGRADE_DIRECTORY="/var/lib/404-probe-upgrade"
readonly SERVER_UPGRADE_LOCK_DIRECTORY="/run/404-probe-upgrade"
readonly SERVER_UPGRADE_CANDIDATE="/usr/local/bin/.404-probe-server.candidate"
readonly SERVER_UPGRADE_PREVIOUS="/usr/local/bin/.404-probe-server.previous"
readonly SERVER_UPGRADE_HELPER_CANDIDATE="/usr/local/sbin/404-probe-install.candidate"
readonly SERVER_UPGRADE_LOCK="${SERVER_UPGRADE_LOCK_DIRECTORY}/server.lock"
readonly CONFIG_DIRECTORY="/etc/404-probe"
readonly STATE_DIRECTORY="/var/lib/404-probe"
readonly SELECTOR_ORDER_FILE="${CONFIG_DIRECTORY}/selector-order.json"
readonly SERVICE_USER="404-probe"
readonly ENROLLMENT_PREFIX="404p1_"

die() { printf '404-probe helper: %s\n' "$*" >&2; exit 1; }
server_residue_exists() {
  openrc_role_residue server && return 0
  local path
  for path in "${SERVER_UNIT}" "${SERVER_BINARY}" "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" \
    "${CONFIG_DIRECTORY}/server.env" "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash" \
    "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_CANDIDATE}" "${SERVER_UPGRADE_PREVIOUS}" \
    "${SERVER_UPGRADE_HELPER_CANDIDATE}" "${SERVER_UPGRADE_LOCK_DIRECTORY}" "${SERVER_UPGRADE_LOCK}"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || return 0
  done
  return 1
}
usage() {
  printf 'Usage: 404-probe-install enroll <name> | domains [list|add|remove|disable] | selector-order <sing-box-config.json> | setup-security | uninstall <server|agent> [--confirm-delete-data] | recover-agent-removal\n'
}
require_root() {
  [[ "${EUID}" -eq 0 ]] || die "run this helper as root"
  detect_install_platform
  if [[ "${INSTALL_INIT}" == openrc ]]; then require_openrc_tools; return; fi
  command -v systemctl >/dev/null 2>&1 || die "systemd is required"
}
validate_removable_service_user() {
  local passwd_entry shadow_entry group_entry name _ uid gid home shell password group_name members other_primary_users uid_min
  id "${SERVICE_USER}" >/dev/null 2>&1 || return 0
  passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
  shadow_entry="$(getent shadow "${SERVICE_USER}" || true)"
  [[ -n "${passwd_entry}" && -n "${shadow_entry}" ]] || die "cannot safely verify the service account"
  IFS=: read -r name _ uid gid _ home shell <<<"${passwd_entry}"
  IFS=: read -r _ password _ <<<"${shadow_entry}"
  [[ "${name}" == "${SERVICE_USER}" && "${uid}" =~ ^[0-9]+$ && "${gid}" =~ ^[0-9]+$ \
    && "${home}" == "${STATE_DIRECTORY}" ]] \
    || die "service account identity is not owned by this installation"
  uid_min="$(awk '$1 == "UID_MIN" { print $2; exit }' /etc/login.defs 2>/dev/null || true)"
  [[ "${uid_min}" =~ ^[0-9]+$ ]] || uid_min=1000
  (( uid > 0 && uid < uid_min && gid > 0 )) \
    || die "service account UID/GID is outside the expected system-account range"
  case "${shell##*/}" in nologin|false) ;; *) die "service account has an interactive shell" ;; esac
  [[ "${password}" == '!'* || "${password}" == '*'* ]] || die "service account password is not locked"
  group_entry="$(getent group "${gid}" || true)"
  IFS=: read -r group_name _ _ members <<<"${group_entry}"
  [[ "${group_name}" == "${SERVICE_USER}" && -z "${members}" ]] || die "service group is shared or unexpected"
  other_primary_users="$(getent passwd | awk -F: -v gid="${gid}" -v user="${SERVICE_USER}" '$4 == gid && $1 != user { print $1 }')"
  [[ -z "${other_primary_users}" ]] || die "service group is used by another account"
}
remove_service_account_for_uninstall() {
  local passwd_entry group_entry name _ uid gid group_name group_gid members other_primary_users
  passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
  if [[ -n "${passwd_entry}" ]]; then
    validate_removable_service_user
    IFS=: read -r name _ uid gid _ _ _ <<<"${passwd_entry}"
    userdel "${SERVICE_USER}" || die "managed files were removed but the service account could not be removed"
    id "${SERVICE_USER}" >/dev/null 2>&1 && die "service account remains after userdel"
  else
    group_entry="$(getent group "${SERVICE_USER}" || true)"
    [[ -z "${group_entry}" ]] || die "orphan or unrecognized service group remains; refusing to remove it"
    return 0
  fi
  group_entry="$(getent group "${SERVICE_USER}" || true)"
  if [[ -n "${group_entry}" ]]; then
    IFS=: read -r group_name _ group_gid members <<<"${group_entry}"
    [[ "${group_name}" == "${SERVICE_USER}" && "${group_gid}" == "${gid}" && -z "${members}" ]] \
      || die "service group changed or is shared during account cleanup"
    other_primary_users="$(getent passwd | awk -F: -v gid="${gid}" '$4 == gid { print $1 }')"
    [[ -z "${other_primary_users}" ]] || die "service group remains in use by another account"
    groupdel "${SERVICE_USER}" || die "service group could not be removed"
  fi
  ! id "${SERVICE_USER}" >/dev/null 2>&1 || die "service account remains after removal"
  ! getent group "${SERVICE_USER}" >/dev/null 2>&1 || die "service group remains after removal"
}
finalize_agent_removal_account() {
  [[ $# -eq 2 && "$1" =~ ^[1-9][0-9]{0,9}$ && "$2" =~ ^[1-9][0-9]{0,9}$ ]] \
    || die "usage: finalize-agent-removal-account <recorded-uid> <recorded-gid>"
  local expected_uid="$1" expected_gid="$2" passwd_entry group_entry name _ uid gid home shell members other_primary_users
  if server_residue_exists; then
    passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
    group_entry="$(getent group "${SERVICE_USER}" || true)"
    [[ -n "${passwd_entry}" && -n "${group_entry}" ]] \
      || die "protected Server residue exists but its shared service account is missing"
    IFS=: read -r name _ uid gid _ home shell <<<"${passwd_entry}"
    [[ "${name}" == "${SERVICE_USER}" && "${uid}" == "${expected_uid}" && "${gid}" == "${expected_gid}" ]] \
      || die "protected Server service account no longer matches the recorded UID/GID"
    IFS=: read -r name _ gid members <<<"${group_entry}"
    [[ "${name}" == "${SERVICE_USER}" && "${gid}" == "${expected_gid}" ]] \
      || die "protected Server service group no longer matches the recorded GID"
    return 0
  fi

  local path other_users other_groups
  for path in "${AGENT_UNIT}" "${AGENT_BINARY}" "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
    "${SECURITY_SERVICE_UNIT}" \
    "${SECURITY_TIMER_UNIT}" "${CONFIG_DIRECTORY}/agent.env" "${STATE_DIRECTORY}/agent.epoch" \
    "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" \
    "${SELECTOR_ORDER_FILE}" "${SECURITY_STATE_DIRECTORY}"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || die "Agent business resource remains before account cleanup: ${path}"
  done

  passwd_entry="$(getent passwd "${SERVICE_USER}" || true)"
  if [[ -n "${passwd_entry}" ]]; then
    validate_removable_service_user
    IFS=: read -r name _ uid gid _ home shell <<<"${passwd_entry}"
    [[ "${name}" == "${SERVICE_USER}" && "${uid}" == "${expected_uid}" && "${gid}" == "${expected_gid}" ]] \
      || die "service account does not match the recorded UID/GID"
    userdel "${SERVICE_USER}" || die "service account removal failed; no success receipt will be sent"
  else
    other_users="$(getent passwd | awk -F: -v uid="${expected_uid}" '$3 == uid { print $1 }')"
    [[ -z "${other_users}" ]] || die "recorded service UID now belongs to another account"
  fi
  id "${SERVICE_USER}" >/dev/null 2>&1 && die "service account remains after userdel"

  group_entry="$(getent group "${SERVICE_USER}" || true)"
  if [[ -n "${group_entry}" ]]; then
    IFS=: read -r name _ gid members <<<"${group_entry}"
    [[ "${name}" == "${SERVICE_USER}" && "${gid}" == "${expected_gid}" && -z "${members}" ]] \
      || die "service group is shared or no longer matches the recorded GID"
    other_primary_users="$(getent passwd | awk -F: -v gid="${expected_gid}" '$4 == gid { print $1 }')"
    [[ -z "${other_primary_users}" ]] || die "service group is still a primary group for another account"
    groupdel "${SERVICE_USER}" || die "service group removal failed; no success receipt will be sent"
  else
    other_groups="$(getent group | awk -F: -v gid="${expected_gid}" '$3 == gid { print $1 }')"
    [[ -z "${other_groups}" ]] || die "recorded service GID now belongs to another group"
  fi
  ! getent group "${SERVICE_USER}" >/dev/null 2>&1 || die "service group remains after groupdel"
  other_groups="$(getent group | awk -F: -v gid="${expected_gid}" '$3 == gid { print $1 }')"
  [[ -z "${other_groups}" ]] || die "recorded service GID remains in use after groupdel"
}
recover_agent_removal() {
  [[ $# -eq 0 ]] || die "usage: 404-probe-install recover-agent-removal"
  local path
  [[ ! -L "${AGENT_UPDATER_STATE}" ]] || die "Agent updater recovery state is a symlink: ${AGENT_UPDATER_STATE}"
  for path in "${AGENT_REMOVAL_REQUEST_FILE}" "${AGENT_REMOVAL_WORKER_BINARY}"; do
    [[ ! -L "${path}" ]] || die "Agent removal recovery path is a symlink: ${path}"
  done
  if [[ -e "${AGENT_REMOVAL_REQUEST_FILE}" ]]; then
    [[ -f "${AGENT_REMOVAL_REQUEST_FILE}" && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_REQUEST_FILE}")" == root:root:600 ]] \
      || die "Agent removal recovery state is unsafe"
    if [[ -x "${AGENT_REMOVAL_WORKER_BINARY}" ]]; then
      systemctl reset-failed "${AGENT_REMOVAL_WORKER_UNIT##*/}" "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
      if [[ -f "${AGENT_REMOVAL_WORKER_UNIT}" ]]; then
        systemctl start "${AGENT_REMOVAL_WORKER_UNIT##*/}" || die "fixed receipt worker could not resume; state was preserved"
      else
        systemctl enable "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
        systemctl start "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" || die "fixed finalizer could not resume; state was preserved"
      fi
      return 0
    fi
    die "fixed removal executor is missing; preserve state and reinstall the same official version before retrying"
  fi
  for path in "${AGENT_UNIT}" "${AGENT_UPDATER_UNIT}" "${AGENT_BINARY}" \
    "${CONFIG_DIRECTORY}/agent.env" "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" \
    "${STATE_DIRECTORY}/agent.security-acks.json" "${SECURITY_STATE_DIRECTORY}"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || die "business or protocol state remains; fixed finalizer must run first: ${path}"
  done
  if [[ -e "${AGENT_UPDATER_STATE}" || -e "${AGENT_REMOVAL_WORKER_UNIT}" \
    || -e "${AGENT_REMOVAL_FINALIZER_UNIT}" || -e "${AGENT_REMOVAL_WORKER_BINARY}" ]]; then
    [[ -x "${AGENT_REMOVAL_WORKER_BINARY}" ]] || die "recovery units remain but their fixed executor is missing"
    "${AGENT_REMOVAL_WORKER_BINARY}" updater removal-finalize \
      || die "fixed local finalizer could not clear its own recovery tail"
    return 0
  fi
  if ! server_residue_exists; then
    ! id "${SERVICE_USER}" >/dev/null 2>&1 || die "service account remains after Agent removal"
    ! getent group "${SERVICE_USER}" >/dev/null 2>&1 || die "service group remains after Agent removal"
    rm -f -- "${INSTALL_HELPER}"
  fi
  printf 'Agent removal recovery found no unfinished managed cleanup.\n'
}
enroll() {
  [[ $# -eq 1 ]] || die "usage: 404-probe-install enroll <agent-name>"
  local name="$1" output agent_id agent_token payload
  [[ -n "${name//[[:space:]]/}" && "${name}" != -* && "${name}" != *$'\n'* && "${name}" != *$'\r'* ]] \
    || die "Agent name must be non-empty, contain no newline, and not begin with a hyphen"
  [[ -x "${SERVER_BINARY}" && -f "${SERVER_DATABASE}" && -f "$(business_unit server)" ]] \
    || die "a local 404-probe Server installation was not found"
  output="$(runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" agent add "${name}" --db "${SERVER_DATABASE}")" \
    || die "could not create Agent"
  agent_id="$(printf '%s\n' "${output}" | sed -n 's/^Agent ID: //p')"
  agent_token="$(printf '%s\n' "${output}" | sed -n 's/^Token: //p')"
  [[ "${agent_id}" =~ ^[0-9a-f]{32}$ && "${agent_token}" =~ ^[A-Za-z0-9_-]{43}$ ]] \
    || die "Server returned unexpected Agent credentials"
  payload="$(printf '%s:%s' "${agent_id}" "${agent_token}" | base64 | tr '+/' '-_' | tr -d '=\n')"
  unset output agent_id agent_token
  printf '\nAgent %s was created.\n' "${name}"
  printf 'Enrollment token (shown once; store it securely):\n%s%s\n' "${ENROLLMENT_PREFIX}" "${payload}"
  printf '\nPaste this value only at the hidden Agent installer prompt.\n'
  unset payload
}
server_origin() {
  local count
  [[ -f "${CONFIG_DIRECTORY}/server.env" && ! -L "${CONFIG_DIRECTORY}/server.env" ]] \
    || die "a safe local Server environment was not found"
  count="$(grep -c '^PROBE_404_WEB_PUBLIC_ORIGIN=' "${CONFIG_DIRECTORY}/server.env" || true)"
  [[ "${count}" -eq 1 ]] || die "the local Server public origin is missing or ambiguous"
  sed -n 's/^PROBE_404_WEB_PUBLIC_ORIGIN=//p' "${CONFIG_DIRECTORY}/server.env"
}
run_domain_command() {
  local action="$1"
  shift
  [[ -x "${SERVER_BINARY}" && -f "${SERVER_DATABASE}" && -f "${SERVER_UNIT}" ]] \
    || die "a local 404-probe Server installation was not found"
  runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" web-domain "${action}" --db "${SERVER_DATABASE}" "$@"
}
show_domain_policy() {
  printf 'Configured exact origin: %s\n' "$(server_origin)"
  run_domain_command list
}
https_suffix_mode_supported() {
  [[ "$(server_origin)" == https://* ]] || {
    printf 'Domain suffix mode requires an HTTPS public origin.\n' >&2
    return 1
  }
}
require_https_suffix_mode() {
  https_suffix_mode_supported || die "domain suffix mode is unavailable"
}
menu_domain_command() {
  if ! "$@"; then
    printf 'Domain policy was not changed; choose another action or exit.\n' >&2
    return 0
  fi
}
domains_menu() {
  local choice suffix answer
  while true; do
    cat >/dev/tty <<'EOF'

404-probe login domain management

  1) View policy and registered suffixes
  2) Add a root domain suffix
  3) Remove a root domain suffix
  4) Disable suffix mode and return to the exact origin
  5) Exit

EOF
    printf 'Choose [1-5]: ' >/dev/tty
    IFS= read -r choice </dev/tty
    case "${choice}" in
      1) menu_domain_command show_domain_policy ;;
      2)
        printf 'Root domain suffix to add: ' >/dev/tty
        IFS= read -r suffix </dev/tty
        if https_suffix_mode_supported; then
          menu_domain_command run_domain_command add "${suffix}"
        fi
        ;;
      3)
        printf 'Root domain suffix to remove: ' >/dev/tty
        IFS= read -r suffix </dev/tty
        printf 'Removing it immediately rejects Web requests and sessions on that suffix. Continue? [y/N]: ' >/dev/tty
        IFS= read -r answer </dev/tty
        [[ "${answer}" == y || "${answer}" == Y ]] || { printf 'No change.\n'; continue; }
        menu_domain_command run_domain_command remove "${suffix}"
        ;;
      4) menu_domain_command domains_disable ;;
      5) return ;;
      *) printf 'Choose a number from 1 to 5.\n' >&2 ;;
    esac
  done
}
domains_disable() {
  local origin answer
  origin="$(server_origin)"
  printf 'Disabling suffix mode will allow only the exact origin %s. Continue? [y/N]: ' "${origin}" >/dev/tty
  IFS= read -r answer </dev/tty
  [[ "${answer}" == y || "${answer}" == Y ]] || { printf 'No change.\n'; return; }
  run_domain_command disable
}
domains() {
  if [[ $# -eq 0 ]]; then
    domains_menu
    return
  fi
  case "$1" in
    list) [[ $# -eq 1 ]] || die "usage: 404-probe-install domains list"; show_domain_policy ;;
    add) [[ $# -eq 2 ]] || die "usage: 404-probe-install domains add <root-domain>"; require_https_suffix_mode; run_domain_command add "$2" ;;
    remove) [[ $# -eq 2 ]] || die "usage: 404-probe-install domains remove <root-domain>"; run_domain_command remove "$2" ;;
    disable) [[ $# -eq 1 ]] || die "usage: 404-probe-install domains disable"; domains_disable ;;
    *) die "usage: 404-probe-install domains [list|add <root-domain>|remove <root-domain>|disable]" ;;
  esac
}
selector_order() {
  [[ $# -eq 1 ]] || die "usage: 404-probe-install selector-order <sing-box-config.json>"
  local config="$1" size owner permissions
  [[ -d "${CONFIG_DIRECTORY}" && ! -L "${CONFIG_DIRECTORY}" \
    && "$(stat -c '%U:%G:%a' "${CONFIG_DIRECTORY}")" == "root:${SERVICE_USER}:750" ]] \
    || die "selector metadata directory must be root:${SERVICE_USER} mode 0750"
  if [[ -e "${SELECTOR_ORDER_FILE}" || -L "${SELECTOR_ORDER_FILE}" ]]; then
    [[ -f "${SELECTOR_ORDER_FILE}" && ! -L "${SELECTOR_ORDER_FILE}" \
      && "$(stat -c '%U:%G:%a' "${SELECTOR_ORDER_FILE}")" == "root:${SERVICE_USER}:640" ]] \
      || die "existing selector order metadata is unsafe"
  fi
  [[ "${config}" == /* && -f "${config}" && ! -L "${config}" ]] \
    || die "sing-box config must be an absolute regular non-symlink file"
  size="$(stat -c '%s' "${config}")"
  owner="$(stat -c '%U' "${config}")"
  permissions="$(stat -c '%a' "${config}")"
  [[ "${size}" =~ ^[0-9]+$ && "${size}" -gt 0 && "${size}" -le 8388608 ]] \
    || die "sing-box config must be non-empty and no larger than 8 MiB"
  [[ "${owner}" == root && "${permissions}" =~ ^[0-7]{3,4}$ ]] \
    || die "sing-box config must be root-owned with ordinary file permissions"
  (( (8#${permissions} & 022) == 0 )) || die "sing-box config must not be group- or world-writable"
  [[ -x "${AGENT_BINARY}" && -f "$(business_unit agent)" && ! -L "$(business_unit agent)" ]] \
    || die "a safe local 404-probe Agent installation was not found"
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_validate_agent_install; fi
  "${AGENT_BINARY}" selector-order extract --config "${config}" --output "${SELECTOR_ORDER_FILE}" \
    || die "could not extract selector order; only standard JSON with a top-level outbounds array is supported"
  [[ -f "${SELECTOR_ORDER_FILE}" && ! -L "${SELECTOR_ORDER_FILE}" ]] \
    || die "selector order extractor did not create a regular file"
  chown -h root:"${SERVICE_USER}" "${SELECTOR_ORDER_FILE}"
  chmod 0640 "${SELECTOR_ORDER_FILE}"
  [[ "$(stat -c '%U:%G:%a' "${SELECTOR_ORDER_FILE}")" == "root:${SERVICE_USER}:640" ]] \
    || die "selector order metadata ownership or mode is unsafe"
  runuser -u "${SERVICE_USER}" -- test -r "${SELECTOR_ORDER_FILE}" \
    || die "Agent service account cannot read selector order metadata"
  if runuser -u "${SERVICE_USER}" -- test -w "${CONFIG_DIRECTORY}" \
    || runuser -u "${SERVICE_USER}" -- test -w "${SELECTOR_ORDER_FILE}"; then
    die "Agent service account can modify selector order metadata"
  fi
  printf 'Selector order metadata refreshed. The Agent will use it on its next discovery interval.\n'
}
confirm_uninstall() {
  local role="$1" confirmation="${2:-}" typed
  [[ -z "${confirmation}" || "${confirmation}" == "--confirm-delete-data" ]] \
    || die "unknown uninstall confirmation option: ${confirmation}"
  if [[ "${confirmation}" == "--confirm-delete-data" ]]; then return; fi
  [[ -r /dev/tty && -w /dev/tty ]] \
    || die "uninstall deletes all ${role} data; rerun with --confirm-delete-data for non-interactive use"
  printf 'This permanently deletes all 404-probe %s data. Type DELETE 404-probe %s: ' "${role}" "${role}" >/dev/tty
  IFS= read -r typed </dev/tty
  [[ "${typed}" == "DELETE 404-probe ${role}" ]] || die "uninstall confirmation did not match; nothing was removed"
}
remove_owned_tree() {
  local path="$1"
	validate_owned_tree "${path}"
  [[ ! -d "${path}" ]] || rm -rf -- "${path}"
}
validate_owned_tree() {
  local path="$1" component="" part owner
  local -a parts=()
  [[ "${path}" == /* && "${path}" != "/" ]] || die "managed directory path is unsafe: ${path}"
  IFS=/ read -r -a parts <<<"${path#/}"
  for part in "${parts[@]}"; do
    [[ -n "${part}" && "${part}" != "." && "${part}" != ".." ]] || die "managed directory path is unsafe: ${path}"
    component="${component}/${part}"
    [[ ! -L "${component}" ]] || die "refusing to traverse symlinked managed path: ${component}"
  done
  [[ ! -e "${path}" || -d "${path}" ]] || die "managed directory path is not a directory: ${path}"
  if [[ -d "${path}" ]]; then
    mountpoint -q -- "${path}" && die "refusing to remove mounted managed directory: ${path}"
    owner="$(stat -c '%U' -- "${path}")"
    [[ "${owner}" == root || "${owner}" == "${SERVICE_USER}" ]] || die "managed directory has unexpected owner: ${path}"
  fi
}
preflight_uninstall_trees() {
  local role="$1" path
	for path in "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}"; do validate_owned_tree "${path}"; done
  if [[ "${role}" == agent ]]; then
    for path in "${AGENT_UPDATER_STATE}" "${AGENT_REMOVAL_STATE_DIRECTORY}" "${SECURITY_STATE_DIRECTORY}" /run/404-probe; do validate_owned_tree "${path}"; done
  else
    for path in "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_LOCK_DIRECTORY}"; do validate_owned_tree "${path}"; done
  fi
}
uninstall() {
  [[ $# -ge 1 && $# -le 2 && ( "$1" == "server" || "$1" == "agent" ) ]] \
    || die "usage: 404-probe-install uninstall <server|agent> [--confirm-delete-data]"
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_uninstall "$@"; return; fi
  local role="$1" unit="404-probe-$1.service" unit_path binary_path removal_pending=0
  confirm_uninstall "${role}" "${2:-}"
  validate_removable_service_user
	preflight_uninstall_trees "${role}"
  if [[ "${role}" == agent && ( -e "${AGENT_REMOVAL_REQUEST_FILE}" || -L "${AGENT_REMOVAL_REQUEST_FILE}" ) ]]; then
    [[ -d "${AGENT_REMOVAL_STATE_DIRECTORY}" && ! -L "${AGENT_REMOVAL_STATE_DIRECTORY}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_STATE_DIRECTORY}")" == "root:root:700" \
      && -f "${AGENT_REMOVAL_REQUEST_FILE}" && ! -L "${AGENT_REMOVAL_REQUEST_FILE}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_REQUEST_FILE}")" == "root:root:600" ]] \
      || die "Agent removal receipt state is unsafe; nothing was removed"
    removal_pending=1
  fi
  if [[ "${role}" == "server" ]]; then
    unit_path="${SERVER_UNIT}"; binary_path="${SERVER_BINARY}"
  else
    unit_path="${AGENT_UNIT}"; binary_path="${AGENT_BINARY}"
  fi
	if [[ "${role}" == "agent" ]]; then
	  if (( removal_pending == 0 )); then
	    if systemctl is-active --quiet "${AGENT_REMOVAL_WORKER_UNIT##*/}" \
	      || systemctl is-active --quiet "${AGENT_REMOVAL_FINALIZER_UNIT##*/}"; then
	      die "Agent removal executor is active without a receipt request; run recover-agent-removal first"
	    fi
	    systemctl disable "${AGENT_REMOVAL_WORKER_UNIT##*/}" "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
	  fi
	  systemctl stop 404-probe-security-collect.timer 404-probe-security-collect.service >/dev/null 2>&1 || true
	  systemctl disable 404-probe-security-collect.timer >/dev/null 2>&1 || true
	  if systemctl is-active --quiet 404-probe-agent-updater.service; then
	    systemctl stop 404-probe-agent-updater.service || die "could not stop Agent updater; nothing was removed"
	  fi
	  systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true
	fi
  if systemctl is-active --quiet "${unit}"; then
    systemctl stop "${unit}" || die "could not stop ${unit}; nothing was removed"
  fi
  systemctl disable "${unit}" >/dev/null 2>&1 || true
  if systemctl is-active --quiet "${unit}"; then
    die "${unit} is still active; nothing was removed"
  fi
  rm -f -- "${unit_path}"
  rm -f -- "${binary_path}"
  if [[ "${role}" == "agent" ]]; then
    rm -f -- "${CONFIG_DIRECTORY}/agent.env" \
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" "${SELECTOR_ORDER_FILE}" \
	  "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
	  "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" \
	  "/usr/local/bin/.404-probe-agent.bootstrap" "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
	if (( removal_pending == 0 )); then
	  systemctl disable "${AGENT_REMOVAL_WORKER_UNIT##*/}" "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
	  rm -f -- "${AGENT_REMOVAL_WORKER_UNIT}" "${AGENT_REMOVAL_FINALIZER_UNIT}" "${AGENT_REMOVAL_WORKER_BINARY}"
	  remove_owned_tree "${AGENT_UPDATER_STATE}"
	fi
	remove_owned_tree "${SECURITY_STATE_DIRECTORY}"
	remove_owned_tree /run/404-probe
  else
    rm -f -- "${CONFIG_DIRECTORY}/server.env" "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash" \
      "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" \
      /usr/local/bin/.404-probe-server.candidate /usr/local/bin/.404-probe-server.previous \
      /usr/local/sbin/404-probe-install.candidate
    remove_owned_tree "${SERVER_UPGRADE_DIRECTORY}"
    remove_owned_tree "${SERVER_UPGRADE_LOCK_DIRECTORY}"
  fi
  rmdir --ignore-fail-on-non-empty "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" 2>/dev/null || true
  systemctl daemon-reload
    if ! server_residue_exists && [[ ! -e "${AGENT_UNIT}" && ! -d "${CONFIG_DIRECTORY}" && ! -d "${STATE_DIRECTORY}" \
      && ! -d "${AGENT_UPDATER_STATE}" && ! -d "${SECURITY_STATE_DIRECTORY}" ]]; then
      remove_service_account_for_uninstall
  fi
	if [[ ! -e "${SERVER_UNIT}" && ! -e "${AGENT_UNIT}" ]] && (( removal_pending == 0 )); then
	  rm -f -- /usr/local/sbin/404-probe-install
	fi
  printf 'Uninstalled %s and removed its managed data. Re-running the official version-pinned uninstall is safe.\n' "${role}"
  printf 'Preserved systemd journal history and any unrecognized files.\n'
}
setup_security() {
  [[ $# -eq 0 ]] || die "usage: 404-probe-install setup-security"
  [[ -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" && -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" ]] \
    || die "a safe existing Agent installation was not found"
  [[ -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
    || die "a safe existing Agent environment was not found"
  [[ -f "${SECURITY_SERVICE_UNIT}" && ! -L "${SECURITY_SERVICE_UNIT}" && -f "${SECURITY_TIMER_UNIT}" && ! -L "${SECURITY_TIMER_UNIT}" ]] \
    || die "Security units are not installed; rerun the version-pinned V0.9 install.sh setup-security command"
  grep -Fxq "PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}" "${CONFIG_DIRECTORY}/agent.env" \
    && grep -Fxq "PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json" "${CONFIG_DIRECTORY}/agent.env" \
    && grep -Fxq "ReadOnlyPaths=-${SECURITY_EXPORT_DIRECTORY}" "${AGENT_UNIT}" \
    || die "Security configuration is incomplete; rerun the version-pinned V0.9 install.sh setup-security command"
  local agent_was_active=0
  systemctl is-active --quiet 404-probe-agent.service && agent_was_active=1
  systemctl daemon-reload
  systemctl start 404-probe-security-collect.service
  [[ -f "${SECURITY_EXPORT_DIRECTORY}/current.json" && ! -L "${SECURITY_EXPORT_DIRECTORY}/current.json" ]] \
    || die "security collector did not create its aggregate export"
  runuser -u "${SERVICE_USER}" -- test -r "${SECURITY_EXPORT_DIRECTORY}/current.json" \
    || die "Agent service account cannot read the security aggregate export"
  if runuser -u "${SERVICE_USER}" -- test -w "${SECURITY_EXPORT_DIRECTORY}/current.json"; then
    die "Agent service account can write the security aggregate export"
  fi
  systemctl enable --now 404-probe-security-collect.timer
  (( agent_was_active == 0 )) || systemctl restart 404-probe-agent.service
  printf '404-probe local security audit is enabled.\n'
}

reject_deferred_command_on_host "${1:-}"
require_root
case "${1:-}" in setup-security|finalize-agent-removal-account|recover-agent-removal) reject_openrc_deferred "${1}" ;; esac
case "${1:-}" in
  -h|--help|help) usage ;;
  enroll) shift; enroll "$@" ;;
  domains) shift; domains "$@" ;;
  selector-order) shift; selector_order "$@" ;;
  setup-security) shift; setup_security "$@" ;;
  uninstall) shift; uninstall "$@" ;;
  finalize-agent-removal-account) shift; finalize_agent_removal_account "$@" ;;
  recover-agent-removal) shift; recover_agent_removal "$@" ;;
  *) usage >&2; exit 2 ;;
esac
HELPER
}

install_local_helper() (
  local temporary_file
  temporary_file="$(mktemp)"
  trap 'rm -f -- "${temporary_file}"' EXIT
  render_local_helper >"${temporary_file}"
  chmod 0600 "${temporary_file}"
  bash -n "${temporary_file}" || die "generated installer helper failed its shell syntax check"
  if [[ -e "${INSTALL_HELPER}" ]]; then
    [[ -f "${INSTALL_HELPER}" && ! -L "${INSTALL_HELPER}" ]] \
      || die "existing ${INSTALL_HELPER} is not a regular non-symlink file"
    [[ "$(stat -c '%U:%G:%a' "${INSTALL_HELPER}")" == "root:root:755" ]] \
      || die "existing ${INSTALL_HELPER} must be root:root mode 0755"
    cmp --silent "${temporary_file}" "${INSTALL_HELPER}" \
      || die "existing ${INSTALL_HELPER} does not match this reviewed installer"
    return
  fi
  install -m 0755 "${temporary_file}" "${INSTALL_HELPER}"
)

write_private_file() (
  local path="$1"
  local value="$2"
  local temporary_file
  temporary_file="$(mktemp "${CONFIG_DIRECTORY}/.404-probe.XXXXXX")"
  trap 'rm -f -- "${temporary_file}"' EXIT
  chmod 0600 "${temporary_file}"
  printf '%s\n' "${value}" >"${temporary_file}"
  if [[ "${INSTALL_INIT:-systemd}" == openrc && "${path}" != "${CONFIG_DIRECTORY}/control.token" && "${path}" != "${CONFIG_DIRECTORY}/web-password.hash" ]]; then
    chown "root:${SERVICE_USER}" "${temporary_file}"
    chmod 0640 "${temporary_file}"
  else
    chown "${SERVICE_USER}:${SERVICE_USER}" "${temporary_file}"
    chmod 0400 "${temporary_file}"
  fi
  mv -f -- "${temporary_file}" "${path}"
)

generate_token() {
  head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n'
}

prompt_web_origin() {
  local value
  printf 'Web public origin [http://127.0.0.1:8080]: ' >/dev/tty
  IFS= read -r value </dev/tty
  value="${value:-http://127.0.0.1:8080}"
  if [[ "${value}" == "http://127.0.0.1:8080" ]]; then
    printf '%s\n' "${value}"
    return
  fi
  [[ "${value}" =~ ^https://(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9._-]+)(:[0-9]{1,5})?$ ]] \
    || die "Web origin must be an HTTPS origin without credentials, path, query, or fragment (or the loopback default)"
  printf '%s\n' "${value}"
}

prompt_web_domain_suffixes() {
  local value
  printf 'Allowed root domain suffixes, comma-separated [exact origin only]: ' >/dev/tty
  IFS= read -r value </dev/tty
  [[ -z "${value}" || "${value}" =~ ^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+(,[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+)*$ ]] \
    || die "Domain suffixes must be comma-separated ASCII root domains without spaces, scheme, port, or path"
  printf '%s\n' "${value}"
}

show_service_diagnostics() {
  local unit="$1"
  systemctl --no-pager --full status "${unit}" || true
  if command -v journalctl >/dev/null 2>&1; then
    journalctl --no-pager -u "${unit}" -n 50 || true
  fi
}

wait_for_service() {
  local unit="$1"
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_wait "${unit#404-probe-}"; return; fi
  local attempts=20
  local service_state
  while (( attempts > 0 )); do
    service_state="$(systemctl is-active "${unit}" 2>/dev/null || true)"
    if [[ "${service_state}" == "active" ]]; then
      return
    fi
    case "${service_state}" in
      activating|reloading) ;;
      *)
        show_service_diagnostics "${unit}"
        die "${unit} entered terminal state ${service_state:-unknown}"
        ;;
    esac
    attempts=$((attempts - 1))
    if (( attempts > 0 )); then
      sleep 1
    fi
  done
  show_service_diagnostics "${unit}"
  die "${unit} did not become active"
}

wait_for_server_readiness() {
  local unit="$1"
  local origin="${2:-http://127.0.0.1:8080}"
  local attempts=20
  local service_state status_code
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then
    openrc_wait server
    while (( attempts > 0 )); do
      [[ -n "$(openrc_business_pids server)" ]] || die "Server exited before HTTP readiness"
      status_code="$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 1 --request POST "${origin}/api/v1/agent/jobs/claim" || true)"
      case "${status_code}" in 401) return ;; ""|000) ;; *) die "Server HTTP verification failed (expected HTTP 401, received ${status_code})" ;; esac
      attempts=$((attempts-1)); (( attempts == 0 )) || sleep 0.5
    done
    die "Server application readiness timed out"
  fi
  while (( attempts > 0 )); do
    service_state="$(systemctl is-active "${unit}" 2>/dev/null || true)"
    case "${service_state}" in
      active|activating|reloading) ;;
      *)
        show_service_diagnostics "${unit}"
        die "${unit} entered terminal state ${service_state:-unknown} before application readiness"
        ;;
    esac

    status_code="$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 1 --request POST \
      "${origin}/api/v1/agent/jobs/claim" || true)"
    case "${status_code}" in
      401) return ;;
      ""|000) ;;
      *)
        show_service_diagnostics "${unit}"
        die "Server HTTP verification failed (expected HTTP 401, received ${status_code})"
        ;;
    esac

    attempts=$((attempts - 1))
    if (( attempts > 0 )); then
      sleep 0.5
    fi
  done
  show_service_diagnostics "${unit}"
  die "Server application readiness timed out (expected HTTP 401)"
}

server_residue_exists() {
  openrc_role_residue server && return 0
  local path
  for path in "${SERVER_UNIT}" "${SERVER_BINARY}" "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" \
    "${CONFIG_DIRECTORY}/server.env" "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash" \
    "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_CANDIDATE}" "/usr/local/bin/.404-probe-server.previous" \
    "${SERVER_UPGRADE_HELPER_CANDIDATE}" "${SERVER_UPGRADE_LOCK_DIRECTORY}" "${SERVER_UPGRADE_LOCK}"; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || return 0
  done
  return 1
}

agent_residue_exists() {
  openrc_role_residue agent && return 0
  local path
  for path in "${AGENT_UNIT}" "${AGENT_BINARY}" "${CONFIG_DIRECTORY}/agent.env" "${STATE_DIRECTORY}/agent.epoch" \
    "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" "${SELECTOR_ORDER_FILE}" \
    "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_STATE}" "${AGENT_UPDATER_SOCKET}" "${AGENT_REMOVAL_WORKER_UNIT}" \
    "${AGENT_REMOVAL_FINALIZER_UNIT}" \
    "${AGENT_REMOVAL_WORKER_BINARY}" "${SECURITY_SERVICE_UNIT}" \
    "${SECURITY_TIMER_UNIT}" "${SECURITY_STATE_DIRECTORY}" /usr/local/bin/.404-probe-agent.bootstrap \
    /usr/local/bin/.404-probe-agent.candidate /usr/local/bin/.404-probe-agent.previous; do
    [[ ! -e "${path}" && ! -L "${path}" ]] || return 0
  done
  return 1
}

install_server() {
  ! agent_residue_exists \
    || die "an Agent installation or stale Agent residue exists; run the current official uninstall agent command before switching roles"
  [[ ! -e "${SERVER_UNIT}" ]] || die "Server installation already exists; no files were changed"
  ! openrc_role_residue server || die "OpenRC Server residue exists; refusing to overwrite it"
  [[ ! -e "${CONFIG_DIRECTORY}/server.env" && ! -e "${CONFIG_DIRECTORY}/control.token" && ! -e "${CONFIG_DIRECTORY}/web-password.hash" ]] \
    || die "existing Server configuration was found; refusing to overwrite it"
  [[ ! -e "${SERVER_DATABASE}" ]] || die "existing Server database was found; refusing to alter it"
  [[ ! -e "${SERVER_BINARY}" ]] || die "${SERVER_BINARY} already exists; refusing to overwrite it"

  local web_origin web_suffixes web_origin_host web_password_hash control_token insecure_option suffix suffix_matches_origin=0
  local -a requested_suffixes=()
  web_origin="$(prompt_web_origin)"
  web_suffixes="$(prompt_web_domain_suffixes)"
  if [[ -n "${web_suffixes}" ]]; then
    web_origin_host="${web_origin#*://}"
    web_origin_host="${web_origin_host%%:*}"
    web_origin_host="${web_origin_host,,}"
    IFS=, read -r -a requested_suffixes <<<"${web_suffixes}"
    for suffix in "${requested_suffixes[@]}"; do
      suffix="${suffix,,}"
      [[ "${web_origin_host}" == "${suffix}" || "${web_origin_host}" == *."${suffix}" ]] && suffix_matches_origin=1
    done
    (( suffix_matches_origin != 0 )) || die "At least one domain suffix must contain the configured Web public origin host"
  fi
  note "Set the Web administrator password. The password hasher reads it without echoing it."

  begin_install_transaction server
  create_service_user
  prepare_directories
  if [[ ! -e "${INSTALL_HELPER}" ]]; then
    track_install_path "${INSTALL_HELPER}"
  fi
  install_local_helper
  track_install_path "${SERVER_BINARY}"
  download_binary server "${SERVER_BINARY}"
  if [[ -n "${web_suffixes}" ]]; then
    track_install_path "${SERVER_DATABASE}"
    track_install_path "${SERVER_DATABASE}-wal"
    track_install_path "${SERVER_DATABASE}-shm"
    IFS=, read -r -a requested_suffixes <<<"${web_suffixes}"
    for suffix in "${requested_suffixes[@]}"; do
      runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" web-domain add --db "${SERVER_DATABASE}" "${suffix}" >/dev/null \
        || die "could not register Web domain suffix ${suffix}"
    done
  fi
  if ! web_password_hash="$("${SERVER_BINARY}" web password-hash </dev/tty)"; then
    die "Web password setup failed"
  fi
  [[ -n "${web_password_hash}" ]] || die "Web password setup returned an empty hash"
  control_token="$(generate_token)"
  [[ "${#control_token}" -eq 43 ]] || die "could not generate the Control token"

  track_install_path "${CONFIG_DIRECTORY}/control.token"
  write_private_file "${CONFIG_DIRECTORY}/control.token" "${control_token}"
  track_install_path "${CONFIG_DIRECTORY}/web-password.hash"
  write_private_file "${CONFIG_DIRECTORY}/web-password.hash" "${web_password_hash}"
  track_install_path "${CONFIG_DIRECTORY}/server.env"
  write_private_file "${CONFIG_DIRECTORY}/server.env" "PROBE_404_WEB_PUBLIC_ORIGIN=${web_origin}"
  unset control_token web_password_hash

  insecure_option=""
  if [[ "${web_origin}" == "http://127.0.0.1:8080" ]]; then
    insecure_option=" --web-allow-insecure-http"
  fi
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then
    local openrc_insecure=0
    [[ -z "${insecure_option}" ]] || openrc_insecure=1
    install_openrc_business server "${openrc_insecure}"
    wait_for_server_readiness 404-probe-server
  else
  track_install_path "${SERVER_UNIT}"
  INSTALL_TRANSACTION_UNIT="404-probe-server.service"
  cat >"${SERVER_UNIT}" <<EOF
[Unit]
Description=404-probe Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
EnvironmentFile=${CONFIG_DIRECTORY}/server.env
UMask=0077
ExecStart=${SERVER_BINARY} serve --listen 127.0.0.1:8080 --db ${SERVER_DATABASE} --offline-timeout 30s --control-token-file ${CONFIG_DIRECTORY}/control.token --web-password-hash-file ${CONFIG_DIRECTORY}/web-password.hash --web-public-origin \${PROBE_404_WEB_PUBLIC_ORIGIN}${insecure_option}
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=${STATE_DIRECTORY}

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${SERVER_UNIT}"
  systemctl daemon-reload
  systemctl enable --now 404-probe-server.service
  wait_for_server_readiness 404-probe-server.service
  fi
  commit_install_transaction

  note "404-probe Server is installed and running."
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then rc-service 404-probe-server status || true
  else systemctl --no-pager --full status 404-probe-server.service || true; fi
  printf '\nLocal listen URL:  http://127.0.0.1:8080\n'
  printf 'Web login URL:    %s\n' "${web_origin}"
  printf 'Tunnel origin:    http://127.0.0.1:8080\n'
  printf '\nCreate an Agent enrollment token when needed:\n  sudo 404-probe-install enroll <agent-name>\n'
  printf '\nThe enrollment token is displayed only by that command; it is not placed in an example command.\n'
}

valid_agent_server_url() {
  local value="$1"
  if [[ "${value}" =~ ^https://(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9._-]+)(:[0-9]{1,5})?/?$ ]]; then
    return 0
  fi
  [[ "${value}" =~ ^http://(127\.0\.0\.1|localhost)(:[0-9]{1,5})?/?$ \
    || "${value}" =~ ^http://\[::1\](:[0-9]{1,5})?/?$ ]]
}

decode_enrollment_token() {
  local enrollment="$1"
  local encoded padding decoded agent_id agent_token
  [[ "${enrollment}" == "${ENROLLMENT_PREFIX}"* ]] || die "invalid enrollment token prefix"
  encoded="${enrollment#"${ENROLLMENT_PREFIX}"}"
  [[ "${encoded}" =~ ^[A-Za-z0-9_-]+$ ]] || die "invalid enrollment token encoding"
  case $((${#encoded} % 4)) in
    0) padding="" ;;
    2) padding="==" ;;
    3) padding="=" ;;
    *) die "invalid enrollment token length" ;;
  esac
  decoded="$(printf '%s' "${encoded}${padding}" | tr '_-' '/+' | base64 --decode 2>/dev/null)" \
    || die "could not decode enrollment token"
  [[ "${decoded}" == *:* && "${decoded}" != *:*:* ]] || die "invalid enrollment token payload"
  agent_id="${decoded%%:*}"
  agent_token="${decoded#*:}"
  [[ "${agent_id}" =~ ^[0-9a-f]{32}$ ]] || die "invalid Agent ID in enrollment token"
  [[ "${agent_token}" =~ ^[A-Za-z0-9_-]{43}$ ]] || die "invalid Agent credential in enrollment token"
  printf '%s\n%s\n' "${agent_id}" "${agent_token}"
}

verify_agent_authentication() {
  local server_url="$1"
  local agent_token="$2"
  local status_code
  status_code="$(printf 'Authorization: Bearer %s\n' "${agent_token}" | \
    curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
      --connect-timeout 5 --max-time 15 --request POST --header @- \
      "${server_url}/api/v1/agent/jobs/claim")" || true
  [[ "${status_code}" == "415" ]] \
    || die "Agent authentication verification failed (expected HTTP 415 after authentication, received ${status_code:-no response})"
}

install_agent_updater_unit() {
  install -d -m 0700 -o root -g root "${AGENT_UPDATER_STATE}"
  cat >"${AGENT_UPDATER_UNIT}" <<EOF
[Unit]
Description=404-probe restricted Agent updater
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
UMask=0077
ExecStart=${AGENT_BINARY} updater
Restart=always
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RuntimeDirectory=404-probe
RuntimeDirectoryMode=0755
ReadWritePaths=${AGENT_UPDATER_STATE} /usr/local/bin /run/404-probe

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${AGENT_UPDATER_UNIT}"
}

install_agent_removal_worker_unit() {
  local expected
  expected="$(cat <<'UNIT'
[Unit]
Description=404-probe Agent removal receipt worker
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=30min
StartLimitBurst=10

[Service]
Type=oneshot
User=root
Group=root
UMask=0077
ExecStart=/usr/local/bin/.404-probe-agent-removal-worker updater removal-worker
Restart=on-failure
RestartSec=30s
TimeoutStartSec=5min
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=/var/lib/404-probe-updater /etc/systemd/system

[Install]
WantedBy=multi-user.target
UNIT
)"
  if [[ -e "${AGENT_REMOVAL_WORKER_UNIT}" || -L "${AGENT_REMOVAL_WORKER_UNIT}" ]]; then
    [[ -f "${AGENT_REMOVAL_WORKER_UNIT}" && ! -L "${AGENT_REMOVAL_WORKER_UNIT}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_WORKER_UNIT}")" == "root:root:644" \
      && "$(<"${AGENT_REMOVAL_WORKER_UNIT}")" == "${expected}" ]] \
      || die "existing Agent removal worker unit is not the fixed managed unit"
    return
  fi
  printf '%s\n' "${expected}" >"${AGENT_REMOVAL_WORKER_UNIT}"
  chmod 0644 "${AGENT_REMOVAL_WORKER_UNIT}"
}

install_agent_removal_finalizer_unit() {
  local expected
  expected="$(cat <<'UNIT'
[Unit]
Description=404-probe fixed local Agent removal finalizer
After=local-fs.target
StartLimitIntervalSec=10min
StartLimitBurst=5

[Service]
Type=oneshot
User=root
Group=root
UMask=0077
ExecStart=/usr/local/bin/.404-probe-agent-removal-worker updater removal-finalize
Restart=on-failure
RestartSec=30s
TimeoutStartSec=10min
NoNewPrivileges=true
PrivateTmp=true
PrivateNetwork=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX
ReadWritePaths=/etc /var/lib /run /usr/local/bin /usr/local/sbin

[Install]
WantedBy=multi-user.target
UNIT
)"
  if [[ -e "${AGENT_REMOVAL_FINALIZER_UNIT}" || -L "${AGENT_REMOVAL_FINALIZER_UNIT}" ]]; then
    [[ -f "${AGENT_REMOVAL_FINALIZER_UNIT}" && ! -L "${AGENT_REMOVAL_FINALIZER_UNIT}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_FINALIZER_UNIT}")" == "root:root:644" \
      && "$(<"${AGENT_REMOVAL_FINALIZER_UNIT}")" == "${expected}" ]] \
      || die "existing Agent removal finalizer unit is not the fixed managed unit"
    return
  fi
  printf '%s\n' "${expected}" >"${AGENT_REMOVAL_FINALIZER_UNIT}"
  chmod 0644 "${AGENT_REMOVAL_FINALIZER_UNIT}"
}

render_agent_unit() {
  local insecure_option="$1"
  cat <<EOF
[Unit]
Description=404-probe Agent
After=network-online.target 404-probe-agent-updater.service
Wants=network-online.target 404-probe-agent-updater.service

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
EnvironmentFile=${CONFIG_DIRECTORY}/agent.env
UMask=0077
ExecStart=${AGENT_BINARY} --interval 10s --job-interval 10s --timeout 8s${insecure_option}
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=CAP_NET_RAW
AmbientCapabilities=CAP_NET_RAW
ReadWritePaths=${STATE_DIRECTORY} /run/404-probe
ReadOnlyPaths=-${SECURITY_EXPORT_DIRECTORY}

[Install]
WantedBy=multi-user.target
EOF
}

install_security_collector() {
  install -d -m 0750 -o root -g "${SERVICE_USER}" "${SECURITY_STATE_DIRECTORY}"
  install -d -m 0700 -o root -g root "${SECURITY_STATE_DIRECTORY}/private" "${SECURITY_STATE_DIRECTORY}/private/outbox"
  install -d -m 0750 -o root -g "${SERVICE_USER}" "${SECURITY_EXPORT_DIRECTORY}"
  cat >"${SECURITY_SERVICE_UNIT}" <<EOF
[Unit]
Description=404-probe local sing-box security audit
After=systemd-journald.service

[Service]
Type=oneshot
User=root
Group=${SERVICE_USER}
UMask=0077
ExecStart=${AGENT_BINARY} security-collect
PrivateNetwork=true
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX
CapabilityBoundingSet=
AmbientCapabilities=
ReadWritePaths=${SECURITY_STATE_DIRECTORY}
ReadOnlyPaths=-/var/log/journal -/run/log/journal
EOF
  cat >"${SECURITY_TIMER_UNIT}" <<EOF
[Unit]
Description=Daily 404-probe local security audit

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=15m
AccuracySec=1m
Unit=404-probe-security-collect.service

[Install]
WantedBy=timers.target
EOF
  chmod 0644 "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}"
}

setup_security_existing() (
  detect_install_platform
  reject_openrc_deferred setup-security
  require_root_linux_systemd
  [[ $# -eq 0 ]] || die "usage: 404-probe-install setup-security"
  local backup_directory="" environment_candidate="" unit_candidate="" helper_candidate="" version_json expected_version item key count path expected
  local service_existed=0 timer_existed=0 security_state_existed=0 helper_existed=0 committed=0
  local agent_was_active=0 timer_was_active=0 timer_was_enabled=0 collector_was_active=0
  [[ -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" && "$(stat -c '%U:%G:%a' "${AGENT_BINARY}")" == "root:root:755" ]] \
    || die "a safe existing Agent binary was not found"
  [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && "$(stat -c '%U:%G:%a' "${AGENT_UNIT}")" == "root:root:644" ]] \
    || die "a safe existing Agent unit was not found"
  [[ -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" && "$(stat -c '%U:%G:%a' "${CONFIG_DIRECTORY}/agent.env")" == "${SERVICE_USER}:${SERVICE_USER}:400" ]] \
    || die "a safe existing Agent environment was not found"
  validate_service_user
  grep -Fxq "User=${SERVICE_USER}" "${AGENT_UNIT}" \
    && grep -Fxq "Group=${SERVICE_USER}" "${AGENT_UNIT}" \
    && grep -Fq "ExecStart=${AGENT_BINARY} " "${AGENT_UNIT}" \
    && grep -Fxq "EnvironmentFile=${CONFIG_DIRECTORY}/agent.env" "${AGENT_UNIT}" \
    || die "existing Agent unit is not a supported 404-probe installation"
  for item in \
    "${SECURITY_STATE_DIRECTORY}|root:${SERVICE_USER}:750" \
    "${SECURITY_STATE_DIRECTORY}/private|root:root:700" \
    "${SECURITY_STATE_DIRECTORY}/private/outbox|root:root:700" \
    "${SECURITY_EXPORT_DIRECTORY}|root:${SERVICE_USER}:750"; do
    path="${item%%|*}"
    expected="${item#*|}"
    if [[ -e "${path}" || -L "${path}" ]]; then
      [[ -d "${path}" && ! -L "${path}" && "$(stat -c '%U:%G:%a' "${path}")" == "${expected}" ]] \
        || die "existing Security path is unsafe: ${path}"
    fi
  done
  expected_version="$(target_version)"
  version_json="$("${AGENT_BINARY}" version --json)" || die "could not verify the installed Agent build"
  grep -Fq "\"version\":\"${expected_version}\"" <<<"${version_json}" \
    && grep -Eq '"commit":"[^"]+"' <<<"${version_json}" \
    && grep -Fq '"dirty":false' <<<"${version_json}" \
    || die "installed Agent is not the verified ${expected_version} build; upgrade it before setup-security"

  for item in \
    "PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}" \
    "PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json"; do
    key="${item%%=*}"
    count="$(grep -c "^${key}=" "${CONFIG_DIRECTORY}/agent.env" || true)"
    (( count <= 1 )) || die "existing Agent environment has duplicate ${key} entries"
    (( count == 0 )) || grep -Fxq "${item}" "${CONFIG_DIRECTORY}/agent.env" \
      || die "existing Agent environment has a conflicting ${key} value"
  done
  grep -Fq "ReadWritePaths=${SECURITY_EXPORT_DIRECTORY}" "${AGENT_UNIT}" \
    && die "existing Agent unit grants write access to the security export"

  systemctl is-active --quiet 404-probe-agent.service && agent_was_active=1
  systemctl is-active --quiet 404-probe-security-collect.timer && timer_was_active=1
  systemctl is-enabled --quiet 404-probe-security-collect.timer && timer_was_enabled=1
  systemctl is-active --quiet 404-probe-security-collect.service && collector_was_active=1

  backup_directory="$(mktemp -d)"
  cp --preserve=mode,ownership,timestamps -- "${CONFIG_DIRECTORY}/agent.env" "${backup_directory}/agent.env"
  cp --preserve=mode,ownership,timestamps -- "${AGENT_UNIT}" "${backup_directory}/agent.service"
  if [[ -e "${INSTALL_HELPER}" ]]; then
    [[ -f "${INSTALL_HELPER}" && ! -L "${INSTALL_HELPER}" && "$(stat -c '%U:%G:%a' "${INSTALL_HELPER}")" == "root:root:755" ]] || die "unsafe installer helper path"
    cp --preserve=mode,ownership,timestamps -- "${INSTALL_HELPER}" "${backup_directory}/install-helper"
    helper_existed=1
  fi
  if [[ -e "${SECURITY_SERVICE_UNIT}" ]]; then
    [[ -f "${SECURITY_SERVICE_UNIT}" && ! -L "${SECURITY_SERVICE_UNIT}" && "$(stat -c '%U:%G:%a' "${SECURITY_SERVICE_UNIT}")" == "root:root:644" ]] || die "unsafe security service unit path"
    cp --preserve=mode,ownership,timestamps -- "${SECURITY_SERVICE_UNIT}" "${backup_directory}/security.service"
    service_existed=1
  fi
  if [[ -e "${SECURITY_TIMER_UNIT}" ]]; then
    [[ -f "${SECURITY_TIMER_UNIT}" && ! -L "${SECURITY_TIMER_UNIT}" && "$(stat -c '%U:%G:%a' "${SECURITY_TIMER_UNIT}")" == "root:root:644" ]] || die "unsafe security timer unit path"
    cp --preserve=mode,ownership,timestamps -- "${SECURITY_TIMER_UNIT}" "${backup_directory}/security.timer"
    timer_existed=1
  fi
  [[ -d "${SECURITY_STATE_DIRECTORY}" ]] && security_state_existed=1
  trap 'status=$?; if (( committed == 0 )); then [[ -z "${environment_candidate}" ]] || rm -f -- "${environment_candidate}"; [[ -z "${unit_candidate}" ]] || rm -f -- "${unit_candidate}"; [[ -z "${helper_candidate}" ]] || rm -f -- "${helper_candidate}"; systemctl stop 404-probe-security-collect.timer 404-probe-security-collect.service >/dev/null 2>&1 || true; systemctl disable 404-probe-security-collect.timer >/dev/null 2>&1 || true; cp --preserve=mode,ownership,timestamps -- "${backup_directory}/agent.env" "${CONFIG_DIRECTORY}/agent.env"; cp --preserve=mode,ownership,timestamps -- "${backup_directory}/agent.service" "${AGENT_UNIT}"; if (( helper_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"; else rm -f -- "${INSTALL_HELPER}"; fi; if (( service_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/security.service" "${SECURITY_SERVICE_UNIT}"; else rm -f -- "${SECURITY_SERVICE_UNIT}"; fi; if (( timer_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/security.timer" "${SECURITY_TIMER_UNIT}"; else rm -f -- "${SECURITY_TIMER_UNIT}"; fi; (( security_state_existed != 0 )) || rm -rf -- "${SECURITY_STATE_DIRECTORY}"; systemctl daemon-reload >/dev/null 2>&1 || true; if (( timer_was_enabled != 0 )); then systemctl enable 404-probe-security-collect.timer >/dev/null 2>&1 || true; else systemctl disable 404-probe-security-collect.timer >/dev/null 2>&1 || true; fi; if (( timer_was_active != 0 )); then systemctl start 404-probe-security-collect.timer >/dev/null 2>&1 || true; else systemctl stop 404-probe-security-collect.timer >/dev/null 2>&1 || true; fi; (( collector_was_active == 0 )) || systemctl start 404-probe-security-collect.service >/dev/null 2>&1 || true; if (( agent_was_active != 0 )); then systemctl restart 404-probe-agent.service >/dev/null 2>&1 || true; else systemctl stop 404-probe-agent.service >/dev/null 2>&1 || true; fi; fi; rm -rf -- "${backup_directory}"; exit "${status}"' EXIT HUP INT TERM

  environment_candidate="$(mktemp "${CONFIG_DIRECTORY}/.agent-security-env.XXXXXX")"
  cp -- "${CONFIG_DIRECTORY}/agent.env" "${environment_candidate}"
  grep -q '^PROBE_404_SECURITY_EXPORT=' "${environment_candidate}" || printf '%s\n' "PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}" >>"${environment_candidate}"
  grep -q '^PROBE_404_SECURITY_ACKS=' "${environment_candidate}" || printf '%s\n' "PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json" >>"${environment_candidate}"
  chown "${SERVICE_USER}:${SERVICE_USER}" "${environment_candidate}"
  chmod 0400 "${environment_candidate}"

  unit_candidate="$(mktemp "$(dirname "${AGENT_UNIT}")/.agent-security-unit.XXXXXX")"
  if grep -Fxq "ReadOnlyPaths=-${SECURITY_EXPORT_DIRECTORY}" "${AGENT_UNIT}"; then
    cp -- "${AGENT_UNIT}" "${unit_candidate}"
  else
    awk -v line="ReadOnlyPaths=-${SECURITY_EXPORT_DIRECTORY}" '/^\[Install\]$/ && !done { print line; done=1 } { print } END { if (!done) exit 1 }' "${AGENT_UNIT}" >"${unit_candidate}" \
      || die "could not add read-only security export to Agent unit"
  fi
  chown root:root "${unit_candidate}"
  chmod 0644 "${unit_candidate}"

  helper_candidate="$(mktemp)"
  render_local_helper >"${helper_candidate}"
  chmod 0600 "${helper_candidate}"
  bash -n "${helper_candidate}" || die "generated installer helper failed its shell syntax check"
  chown root:root "${helper_candidate}"
  chmod 0755 "${helper_candidate}"

  mv -f -- "${environment_candidate}" "${CONFIG_DIRECTORY}/agent.env"
  mv -f -- "${unit_candidate}" "${AGENT_UNIT}"
  mv -f -- "${helper_candidate}" "${INSTALL_HELPER}"
  install_security_collector
  systemctl daemon-reload
  systemctl start 404-probe-security-collect.service
  [[ -f "${SECURITY_EXPORT_DIRECTORY}/current.json" && ! -L "${SECURITY_EXPORT_DIRECTORY}/current.json" ]] \
    || die "security collector did not create its aggregate export"
  runuser -u "${SERVICE_USER}" -- test -r "${SECURITY_EXPORT_DIRECTORY}/current.json" \
    || die "Agent service account cannot read the security aggregate export"
  if runuser -u "${SERVICE_USER}" -- test -w "${SECURITY_EXPORT_DIRECTORY}/current.json"; then
    die "Agent service account can write the security aggregate export"
  fi
  systemctl enable --now 404-probe-security-collect.timer
  if (( agent_was_active != 0 )); then
    systemctl restart 404-probe-agent.service
    wait_for_service 404-probe-agent.service
  else
    systemctl stop 404-probe-agent.service
  fi
  committed=1
  trap - EXIT HUP INT TERM
  rm -rf -- "${backup_directory}"
  note "404-probe ${expected_version} local security audit is enabled; Agent identity, credential, and epoch state were preserved."
)

setup_selector_order_existing() (
  require_root_linux_systemd
  [[ $# -eq 1 ]] || die "usage: 404-probe-install selector-order <sing-box-config.json>"
  local config="$1" size owner permissions version_json expected_version backup_directory helper_candidate="" metadata_existed=0 helper_existed=0 committed=0
  [[ -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" && "$(stat -c '%U:%G:%a' "${AGENT_BINARY}")" == "root:root:755" ]] \
    || die "a safe existing Agent binary was not found"
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_validate_agent_install
  else
    [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && "$(stat -c '%U:%G:%a' "${AGENT_UNIT}")" == "root:root:644" ]] \
      || die "a safe existing Agent unit was not found"
  fi
  [[ -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
    || die "a safe existing Agent environment was not found"
  validate_service_user
  [[ -d "${CONFIG_DIRECTORY}" && ! -L "${CONFIG_DIRECTORY}" \
    && "$(stat -c '%U:%G:%a' "${CONFIG_DIRECTORY}")" == "root:${SERVICE_USER}:750" ]] \
    || die "selector metadata directory must be root:${SERVICE_USER} mode 0750"
  if [[ "${INSTALL_INIT:-systemd}" != openrc ]]; then
  grep -Fxq "User=${SERVICE_USER}" "${AGENT_UNIT}" \
    && grep -Fxq "Group=${SERVICE_USER}" "${AGENT_UNIT}" \
    && grep -Fq "ExecStart=${AGENT_BINARY} " "${AGENT_UNIT}" \
    && grep -Fxq "EnvironmentFile=${CONFIG_DIRECTORY}/agent.env" "${AGENT_UNIT}" \
    || die "existing Agent unit is not a supported 404-probe installation"
  fi
  expected_version="$(target_version)"
  version_json="$("${AGENT_BINARY}" version --json)" || die "could not verify the installed Agent build"
  grep -Fq "\"version\":\"${expected_version}\"" <<<"${version_json}" \
    && grep -Eq '"commit":"[^"]+"' <<<"${version_json}" \
    && grep -Fq '"dirty":false' <<<"${version_json}" \
    || die "installed Agent is not the verified ${expected_version} build; upgrade it before selector-order setup"
  [[ "${config}" == /* && -f "${config}" && ! -L "${config}" ]] \
    || die "sing-box config must be an absolute regular non-symlink file"
  size="$(stat -c '%s' "${config}")"
  owner="$(stat -c '%U' "${config}")"
  permissions="$(stat -c '%a' "${config}")"
  [[ "${size}" =~ ^[0-9]+$ && "${size}" -gt 0 && "${size}" -le 8388608 ]] \
    || die "sing-box config must be non-empty and no larger than 8 MiB"
  [[ "${owner}" == root && "${permissions}" =~ ^[0-7]{3,4}$ ]] \
    || die "sing-box config must be root-owned with ordinary file permissions"
  (( (8#${permissions} & 022) == 0 )) || die "sing-box config must not be group- or world-writable"
  if [[ -e "${SELECTOR_ORDER_FILE}" || -L "${SELECTOR_ORDER_FILE}" ]]; then
    [[ -f "${SELECTOR_ORDER_FILE}" && ! -L "${SELECTOR_ORDER_FILE}" \
      && "$(stat -c '%U:%G:%a' "${SELECTOR_ORDER_FILE}")" == "root:${SERVICE_USER}:640" ]] \
      || die "existing selector order metadata is unsafe"
    metadata_existed=1
  fi
  if [[ -e "${INSTALL_HELPER}" || -L "${INSTALL_HELPER}" ]]; then
    [[ -f "${INSTALL_HELPER}" && ! -L "${INSTALL_HELPER}" \
      && "$(stat -c '%U:%G:%a' "${INSTALL_HELPER}")" == "root:root:755" ]] \
      || die "existing installer helper is unsafe"
    helper_existed=1
  fi

  backup_directory="$(mktemp -d)"
  (( metadata_existed == 0 )) || cp --preserve=mode,ownership,timestamps -- "${SELECTOR_ORDER_FILE}" "${backup_directory}/selector-order.json"
  (( helper_existed == 0 )) || cp --preserve=mode,ownership,timestamps -- "${INSTALL_HELPER}" "${backup_directory}/install-helper"
  trap 'status=$?; if (( committed == 0 )); then if (( metadata_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/selector-order.json" "${SELECTOR_ORDER_FILE}"; else rm -f -- "${SELECTOR_ORDER_FILE}"; fi; if (( helper_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"; else rm -f -- "${INSTALL_HELPER}"; fi; fi; [[ -z "${helper_candidate}" ]] || rm -f -- "${helper_candidate}"; rm -rf -- "${backup_directory}"; exit "${status}"' EXIT HUP INT TERM

  "${AGENT_BINARY}" selector-order extract --config "${config}" --output "${SELECTOR_ORDER_FILE}" \
    || die "could not extract selector order; only standard JSON with a top-level outbounds array is supported"
  [[ -f "${SELECTOR_ORDER_FILE}" && ! -L "${SELECTOR_ORDER_FILE}" ]] \
    || die "selector order extractor did not create a regular file"
  chown -h root:"${SERVICE_USER}" "${SELECTOR_ORDER_FILE}"
  chmod 0640 "${SELECTOR_ORDER_FILE}"
  [[ "$(stat -c '%U:%G:%a' "${SELECTOR_ORDER_FILE}")" == "root:${SERVICE_USER}:640" ]] \
    || die "selector order metadata ownership or mode is unsafe"
  runuser -u "${SERVICE_USER}" -- test -r "${SELECTOR_ORDER_FILE}" \
    || die "Agent service account cannot read selector order metadata"
  if runuser -u "${SERVICE_USER}" -- test -w "${CONFIG_DIRECTORY}" \
    || runuser -u "${SERVICE_USER}" -- test -w "${SELECTOR_ORDER_FILE}"; then
    die "Agent service account can modify selector order metadata"
  fi

  helper_candidate="$(mktemp "$(dirname "${INSTALL_HELPER}")/.404-probe-install.selector.XXXXXX")"
  render_local_helper >"${helper_candidate}"
  bash -n "${helper_candidate}" || die "generated installer helper failed its shell syntax check"
  chown root:root "${helper_candidate}"
  chmod 0755 "${helper_candidate}"
  mv -f -- "${helper_candidate}" "${INSTALL_HELPER}"
  helper_candidate=""
  committed=1
  trap - EXIT HUP INT TERM
  rm -rf -- "${backup_directory}"
  note "Selector order metadata is enabled for ${expected_version}; Agent identity and service state were preserved."
)

bootstrap_existing_agent() (
  reject_openrc_deferred bootstrap-agent-updater
  local candidate="/usr/local/bin/.404-probe-agent.bootstrap"
  local previous="/usr/local/bin/.404-probe-agent.previous"
  local backup_directory server_url agent_token insecure_option rollback_active=0 helper_existed=0
  [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" && -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
    || die "existing Agent installation is incomplete or unsafe; no files were changed"
  [[ "$(stat -c '%U:%G:%a' "${AGENT_UNIT}")" == "root:root:644" ]] \
    || die "existing Agent unit must be root:root mode 0644"
  [[ "$(stat -c '%U:%G:%a' "${AGENT_BINARY}")" == "root:root:755" ]] \
    || die "existing Agent binary must be root:root mode 0755"
  if [[ -e "${INSTALL_HELPER}" ]]; then
    [[ -f "${INSTALL_HELPER}" && ! -L "${INSTALL_HELPER}" && "$(stat -c '%U:%G:%a' "${INSTALL_HELPER}")" == "root:root:755" ]] \
      || die "existing installer helper is unsafe; no files were changed"
    helper_existed=1
  fi
  if "${AGENT_BINARY}" version --json >/dev/null 2>&1; then
    die "existing Agent already supports versioned upgrades; use the Web upgrade action"
  fi
  [[ ! -e "${AGENT_UPDATER_UNIT}" && ! -e "${AGENT_UPDATER_SOCKET}" && ! -e "${AGENT_UPDATER_STATE}" ]] \
    || die "existing updater installation is partial or unmanaged; no files were changed"
  [[ ! -e "${AGENT_REMOVAL_WORKER_UNIT}" && ! -e "${AGENT_REMOVAL_FINALIZER_UNIT}" \
    && ! -e "${AGENT_REMOVAL_WORKER_BINARY}" ]] \
    || die "existing Agent removal worker residue is partial or unmanaged; no files were changed"
  grep -Fxq "User=${SERVICE_USER}" "${AGENT_UNIT}" \
    && grep -Fq "ExecStart=${AGENT_BINARY} " "${AGENT_UNIT}" \
    && grep -Fxq "EnvironmentFile=${CONFIG_DIRECTORY}/agent.env" "${AGENT_UNIT}" \
    || die "existing Agent unit is not a supported 404-probe v0.7 installation"
  server_url="$(sed -n 's/^PROBE_404_SERVER=//p' "${CONFIG_DIRECTORY}/agent.env")"
  agent_token="$(sed -n 's/^PROBE_404_TOKEN=//p' "${CONFIG_DIRECTORY}/agent.env")"
  [[ "$(grep -c '^PROBE_404_SERVER=' "${CONFIG_DIRECTORY}/agent.env")" -eq 1 && "$(grep -c '^PROBE_404_TOKEN=' "${CONFIG_DIRECTORY}/agent.env")" -eq 1 ]] \
    || die "existing Agent environment is ambiguous"
  valid_agent_server_url "${server_url}" || die "existing Agent Server URL is invalid"
  [[ "${agent_token}" =~ ^[A-Za-z0-9_-]{43}$ ]] || die "existing Agent credential is invalid"
  [[ ! -e "${candidate}" ]] || { [[ -f "${candidate}" && ! -L "${candidate}" ]] || die "unsafe bootstrap candidate path"; rm -f -- "${candidate}"; }
  [[ ! -e "${previous}" ]] || { [[ -f "${previous}" && ! -L "${previous}" ]] || die "unsafe previous binary path"; rm -f -- "${previous}"; }
  download_binary agent "${candidate}"
  backup_directory="$(mktemp -d)"
  cp --preserve=mode,ownership,timestamps -- "${AGENT_UNIT}" "${backup_directory}/agent.service"
  if (( helper_existed != 0 )); then
    cp --preserve=mode,ownership,timestamps -- "${INSTALL_HELPER}" "${backup_directory}/install-helper"
  fi
  trap 'status=$?; if (( rollback_active != 0 )); then systemctl stop 404-probe-agent.service >/dev/null 2>&1 || true; systemctl stop 404-probe-agent-updater.service >/dev/null 2>&1 || true; systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true; rm -f -- "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" "${AGENT_REMOVAL_WORKER_UNIT}" "${AGENT_REMOVAL_FINALIZER_UNIT}" "${AGENT_REMOVAL_WORKER_BINARY}"; rm -rf -- "${AGENT_UPDATER_STATE}"; if [[ -f "${previous}" && ! -L "${previous}" ]]; then mv -f -- "${previous}" "${AGENT_BINARY}"; fi; cp --preserve=mode,ownership,timestamps -- "${backup_directory}/agent.service" "${AGENT_UNIT}"; if (( helper_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"; else rm -f -- "${INSTALL_HELPER}"; fi; systemctl daemon-reload >/dev/null 2>&1 || true; systemctl restart 404-probe-agent.service >/dev/null 2>&1 || true; fi; rm -f -- "${candidate}"; rm -rf -- "${backup_directory}"; exit "${status}"' EXIT HUP INT TERM
  systemctl stop 404-probe-agent.service || die "could not stop existing Agent"
  rollback_active=1
  ln -- "${AGENT_BINARY}" "${previous}"
  mv -f -- "${candidate}" "${AGENT_BINARY}"
  insecure_option=""
  [[ "${server_url}" != http://* ]] || insecure_option=" --allow-insecure-http"
  render_agent_unit "${insecure_option}" >"${AGENT_UNIT}"
  chmod 0644 "${AGENT_UNIT}"
  install_agent_updater_unit
  install_agent_removal_worker_unit
  install_agent_removal_finalizer_unit
  systemctl daemon-reload
  systemctl enable --now 404-probe-agent-updater.service
  wait_for_service 404-probe-agent-updater.service
  systemctl enable --now 404-probe-agent.service
  wait_for_service 404-probe-agent.service
  verify_agent_authentication "${server_url}" "${agent_token}"
  rm -f -- "${INSTALL_HELPER}"
  install_local_helper
  rm -f -- "${previous}"
  rollback_active=0
  trap - EXIT HUP INT TERM
  rm -rf -- "${backup_directory}"
  unset agent_token
  note "404-probe Agent v0.8 bootstrap completed; identity, credential, configuration, and epoch state were preserved."
)

install_agent() {
  ! openrc_role_residue agent || die "OpenRC Agent already exists; Alpine automatic upgrade is deferred to v1.1"
  ! server_residue_exists \
    || die "a Server installation or stale Server residue exists; run the current official uninstall server command before switching roles"
  if [[ -e "${AGENT_UNIT}" || -e "${CONFIG_DIRECTORY}/agent.env" || -e "${AGENT_BINARY}" \
    || -e "${AGENT_REMOVAL_WORKER_UNIT}" || -e "${AGENT_REMOVAL_FINALIZER_UNIT}" || -e "${AGENT_REMOVAL_WORKER_BINARY}" ]]; then
    [[ -e "${AGENT_UNIT}" && -e "${CONFIG_DIRECTORY}/agent.env" && -e "${AGENT_BINARY}" ]] \
      || die "conflicting partial Agent installation found; no files were changed"
    bootstrap_existing_agent
    return
  fi
  [[ ! -e "${CONFIG_DIRECTORY}/agent.env" ]] || die "existing Agent configuration was found; refusing to overwrite it"
  [[ ! -e "${STATE_DIRECTORY}/agent.epoch" && ! -e "${STATE_DIRECTORY}/agent.epoch.lock" ]] \
    || die "existing Agent state was found; refusing to alter it"
  [[ ! -e "${AGENT_BINARY}" ]] || die "${AGENT_BINARY} already exists; refusing to overwrite it"

  local server_url="${1:-}" enrollment credentials agent_id agent_token insecure_option permissions country_code
  if [[ -z "${server_url}" ]]; then
    printf 'Server URL: ' >/dev/tty
    IFS= read -r server_url </dev/tty
  fi
  valid_agent_server_url "${server_url}" \
    || die "Server URL must be an HTTPS origin, or loopback HTTP, without credentials, path, query, or fragment"
  server_url="${server_url%/}"
  printf 'Enrollment value: ' >/dev/tty
  IFS= read -r -s enrollment </dev/tty
  printf '\n' >/dev/tty
  credentials="$(decode_enrollment_token "${enrollment}")"
  agent_id="${credentials%%$'\n'*}"
  agent_token="${credentials#*$'\n'}"
  unset enrollment credentials
  verify_agent_authentication "${server_url}" "${agent_token}"

  begin_install_transaction agent
  prepare_openrc_agent_sbin
  create_service_user
  prepare_directories
  if [[ ! -e "${INSTALL_HELPER}" ]]; then
    track_install_path "${INSTALL_HELPER}"
  fi
  install_local_helper
  track_install_path "${AGENT_BINARY}"
  download_binary agent "${AGENT_BINARY}"
	country_code=""
	if country_code="$("${AGENT_BINARY}" country-code lookup 2>/dev/null)"; then
		note "Agent egress country/region was identified once during installation as ${country_code}."
	else
		country_code=""
		note "Agent egress country/region could not be identified; installation will continue with an unknown location."
	fi
	if [[ -f /etc/sing-box/config.json && ! -L /etc/sing-box/config.json && "$(stat -c '%U' /etc/sing-box/config.json)" == root ]]; then
	  permissions="$(stat -c '%a' /etc/sing-box/config.json)"
	  if [[ "${permissions}" =~ ^[0-7]{3,4}$ ]] && (( (8#${permissions} & 022) == 0 )); then
	    track_install_path "${SELECTOR_ORDER_FILE}"
	    if "${AGENT_BINARY}" selector-order extract --config /etc/sing-box/config.json --output "${SELECTOR_ORDER_FILE}"; then
	      [[ -f "${SELECTOR_ORDER_FILE}" && ! -L "${SELECTOR_ORDER_FILE}" ]] \
	        || die "selector order extractor did not create a regular file"
	      chown -h root:"${SERVICE_USER}" "${SELECTOR_ORDER_FILE}"
	      chmod 0640 "${SELECTOR_ORDER_FILE}"
	      [[ "$(stat -c '%U:%G:%a' "${SELECTOR_ORDER_FILE}")" == "root:${SERVICE_USER}:640" ]] \
	        || die "selector order metadata ownership or mode is unsafe"
	      runuser -u "${SERVICE_USER}" -- test -r "${SELECTOR_ORDER_FILE}" \
	        || die "Agent service account cannot read selector order metadata"
	      if runuser -u "${SERVICE_USER}" -- test -w "${CONFIG_DIRECTORY}" \
	        || runuser -u "${SERVICE_USER}" -- test -w "${SELECTOR_ORDER_FILE}"; then
	        die "Agent service account can modify selector order metadata"
	      fi
	    else
	      rm -f -- "${SELECTOR_ORDER_FILE}"
	      note "sing-box selector order could not be extracted; Agent will use deterministic name order until configured locally."
	    fi
	  fi
	fi
  track_install_path "${CONFIG_DIRECTORY}/agent.env"
  write_private_file "${CONFIG_DIRECTORY}/agent.env" "PROBE_404_SERVER=${server_url}
PROBE_404_AGENT_ID=${agent_id}
PROBE_404_TOKEN=${agent_token}
PROBE_404_STATE=${STATE_DIRECTORY}/agent.epoch
PROBE_404_COUNTRY_CODE=${country_code}
PROBE_404_SELECTOR_ORDER=${SELECTOR_ORDER_FILE}
PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}
PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json"

  insecure_option=""
  if [[ "${server_url}" == http://* ]]; then
    insecure_option=" --allow-insecure-http"
  fi
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then
    local openrc_insecure=0
    [[ -z "${insecure_option}" ]] || openrc_insecure=1
    install_openrc_business agent "${openrc_insecure}"
  else
  track_install_path "${AGENT_UNIT}"
  track_install_path "${AGENT_UPDATER_UNIT}"
	track_install_path "${AGENT_REMOVAL_WORKER_UNIT}"
	track_install_path "${AGENT_REMOVAL_FINALIZER_UNIT}"
	track_install_path "${SECURITY_SERVICE_UNIT}"
	track_install_path "${SECURITY_TIMER_UNIT}"
  [[ -d "${AGENT_UPDATER_STATE}" ]] || INSTALL_TRANSACTION_UPDATER_STATE_CREATED=1
  [[ -d "${SECURITY_STATE_DIRECTORY}" ]] || INSTALL_TRANSACTION_SECURITY_STATE_CREATED=1
  INSTALL_TRANSACTION_UNIT="404-probe-agent.service"
  render_agent_unit "${insecure_option}" >"${AGENT_UNIT}"
  chmod 0644 "${AGENT_UNIT}"
  install_agent_updater_unit
	install_agent_removal_worker_unit
	install_agent_removal_finalizer_unit
	install_security_collector
  systemctl daemon-reload
	systemctl start 404-probe-security-collect.service
	systemctl enable --now 404-probe-security-collect.timer
  systemctl enable --now 404-probe-agent-updater.service
  wait_for_service 404-probe-agent-updater.service
  systemctl enable --now 404-probe-agent.service
  wait_for_service 404-probe-agent.service
  fi

  verify_agent_authentication "${server_url}" "${agent_token}"
  unset agent_token
  commit_install_transaction

  note "404-probe Agent is installed, running, and authenticated to the Server."
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then rc-service 404-probe-agent status || true
  else systemctl --no-pager --full status 404-probe-agent.service || true; fi
}

install_agent_command() {
  require_root_linux_systemd
  [[ $# -eq 2 && "$1" == "--server" ]] \
    || die "usage: 404-probe-install agent --server <origin>"
  install_agent "$2"
}

enroll_agent() {
  require_root_linux_systemd
  [[ $# -eq 1 ]] || die "usage: 404-probe-install enroll <agent-name>"
  local agent_name="$1"
  [[ -n "${agent_name//[[:space:]]/}" && "${agent_name}" != *$'\n'* && "${agent_name}" != *$'\r'* ]] \
    || die "Agent name must not be empty or contain a newline"
  [[ "${agent_name}" != -* ]] || die "Agent name must not begin with a hyphen"
  [[ -x "${SERVER_BINARY}" && -f "${SERVER_DATABASE}" && -f "$(business_unit server)" ]] \
    || die "a local 404-probe Server installation was not found"

  local output agent_id agent_token payload enrollment
  output="$(runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" agent add "${agent_name}" --db "${SERVER_DATABASE}")" \
    || die "could not create Agent"
  agent_id="$(printf '%s\n' "${output}" | sed -n 's/^Agent ID: //p')"
  agent_token="$(printf '%s\n' "${output}" | sed -n 's/^Token: //p')"
  [[ "${agent_id}" =~ ^[0-9a-f]{32}$ && "${agent_token}" =~ ^[A-Za-z0-9_-]{43}$ ]] \
    || die "Server returned unexpected Agent credentials"
  payload="$(printf '%s:%s' "${agent_id}" "${agent_token}" | base64 | tr '+/' '-_' | tr -d '=\n')"
  enrollment="${ENROLLMENT_PREFIX}${payload}"
  unset output agent_id agent_token payload

  printf '\nAgent %s was created.\n' "${agent_name}"
  printf 'Enrollment token (shown once; store it securely):\n%s\n' "${enrollment}"
  printf '\nPaste this value only at the hidden Agent installer prompt.\n'
  unset enrollment
}

confirm_uninstall() {
  local role="$1" confirmation="${2:-}" typed
  [[ -z "${confirmation}" || "${confirmation}" == "--confirm-delete-data" ]] \
    || die "unknown uninstall confirmation option: ${confirmation}"
  if [[ "${confirmation}" == "--confirm-delete-data" ]]; then return; fi
  [[ -r /dev/tty && -w /dev/tty ]] \
    || die "uninstall deletes all ${role} data; rerun with --confirm-delete-data for non-interactive use"
  printf 'This permanently deletes all 404-probe %s data. Type DELETE 404-probe %s: ' "${role}" "${role}" >/dev/tty
  IFS= read -r typed </dev/tty
  [[ "${typed}" == "DELETE 404-probe ${role}" ]] || die "uninstall confirmation did not match; nothing was removed"
}

remove_owned_tree() {
  local path="$1"
  validate_owned_tree "${path}"
  [[ ! -d "${path}" ]] || rm -rf -- "${path}"
}
validate_owned_tree() {
  local path="$1" component="" part owner
  local -a parts=()
  [[ "${path}" == /* && "${path}" != "/" ]] || die "managed directory path is unsafe: ${path}"
  IFS=/ read -r -a parts <<<"${path#/}"
  for part in "${parts[@]}"; do
    [[ -n "${part}" && "${part}" != "." && "${part}" != ".." ]] || die "managed directory path is unsafe: ${path}"
    component="${component}/${part}"
    [[ ! -L "${component}" ]] || die "refusing to traverse symlinked managed path: ${component}"
  done
  [[ ! -e "${path}" || -d "${path}" ]] || die "managed directory path is not a directory: ${path}"
  if [[ -d "${path}" ]]; then
    mountpoint -q -- "${path}" && die "refusing to remove mounted managed directory: ${path}"
    owner="$(stat -c '%U' -- "${path}")"
    [[ "${owner}" == root || "${owner}" == "${SERVICE_USER}" ]] || die "managed directory has unexpected owner: ${path}"
  fi
}
preflight_uninstall_trees() {
  local role="$1" path
	for path in "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}"; do validate_owned_tree "${path}"; done
  if [[ "${role}" == agent ]]; then
    for path in "${AGENT_UPDATER_STATE}" "${AGENT_REMOVAL_STATE_DIRECTORY}" "${SECURITY_STATE_DIRECTORY}" /run/404-probe; do validate_owned_tree "${path}"; done
  else
    for path in "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_LOCK_DIRECTORY}"; do validate_owned_tree "${path}"; done
  fi
}

uninstall_role() {
  require_root_linux_systemd
  [[ $# -ge 1 && $# -le 2 && ( "$1" == "server" || "$1" == "agent" ) ]] \
    || die "usage: 404-probe-install uninstall <server|agent> [--confirm-delete-data]"
  if [[ "${INSTALL_INIT:-systemd}" == openrc ]]; then openrc_uninstall "$@"; return; fi
  local role="$1"
  local unit="404-probe-${role}.service"
  local unit_path binary_path removal_pending=0
  confirm_uninstall "${role}" "${2:-}"
  id "${SERVICE_USER}" >/dev/null 2>&1 && validate_service_user
  preflight_uninstall_trees "${role}"
  if [[ "${role}" == agent && ( -e "${AGENT_REMOVAL_REQUEST_FILE}" || -L "${AGENT_REMOVAL_REQUEST_FILE}" ) ]]; then
    [[ -d "${AGENT_REMOVAL_STATE_DIRECTORY}" && ! -L "${AGENT_REMOVAL_STATE_DIRECTORY}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_STATE_DIRECTORY}")" == "root:root:700" \
      && -f "${AGENT_REMOVAL_REQUEST_FILE}" && ! -L "${AGENT_REMOVAL_REQUEST_FILE}" \
      && "$(stat -c '%U:%G:%a' "${AGENT_REMOVAL_REQUEST_FILE}")" == "root:root:600" ]] \
      || die "Agent removal receipt state is unsafe; nothing was removed"
    removal_pending=1
  fi
  if [[ "${role}" == "server" ]]; then
    unit_path="${SERVER_UNIT}"
    binary_path="${SERVER_BINARY}"
  else
    unit_path="${AGENT_UNIT}"
    binary_path="${AGENT_BINARY}"
  fi
  if [[ "${role}" == "agent" ]]; then
	if (( removal_pending == 0 )); then
	  if systemctl is-active --quiet "${AGENT_REMOVAL_WORKER_UNIT##*/}" \
	    || systemctl is-active --quiet "${AGENT_REMOVAL_FINALIZER_UNIT##*/}"; then
	    die "Agent removal executor is active without a receipt request; run recover-agent-removal first"
	  fi
	  systemctl disable "${AGENT_REMOVAL_WORKER_UNIT##*/}" "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
	fi
	if systemctl is-active --quiet 404-probe-security-collect.timer || systemctl is-active --quiet 404-probe-security-collect.service; then
	  systemctl stop 404-probe-security-collect.timer 404-probe-security-collect.service || die "could not stop security collector; nothing was removed"
	fi
	systemctl disable 404-probe-security-collect.timer >/dev/null 2>&1 || true
    if systemctl is-active --quiet 404-probe-agent-updater.service; then
      systemctl stop 404-probe-agent-updater.service || die "could not stop Agent updater; nothing was removed"
    fi
    systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true
  fi
  if systemctl is-active --quiet "${unit}"; then
    systemctl stop "${unit}" || die "could not stop ${unit}; nothing was removed"
  fi
  systemctl disable "${unit}" >/dev/null 2>&1 || true
  if systemctl is-active --quiet "${unit}"; then
    die "${unit} is still active; nothing was removed"
  fi
  rm -f -- "${unit_path}"
  rm -f -- "${binary_path}"
  if [[ "${role}" == "agent" ]]; then
    rm -f -- "${CONFIG_DIRECTORY}/agent.env" \
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" \
      "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
	  "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" \
      "/usr/local/bin/.404-probe-agent.bootstrap" "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
	if (( removal_pending == 0 )); then
	  systemctl disable "${AGENT_REMOVAL_WORKER_UNIT##*/}" "${AGENT_REMOVAL_FINALIZER_UNIT##*/}" >/dev/null 2>&1 || true
	  rm -f -- "${AGENT_REMOVAL_WORKER_UNIT}" "${AGENT_REMOVAL_FINALIZER_UNIT}" "${AGENT_REMOVAL_WORKER_BINARY}"
	  remove_owned_tree "${AGENT_UPDATER_STATE}"
	fi
    remove_owned_tree "${SECURITY_STATE_DIRECTORY}"
    remove_owned_tree /run/404-probe
    rm -f -- "${SELECTOR_ORDER_FILE}"
  else
    rm -f -- "${CONFIG_DIRECTORY}/server.env" "${CONFIG_DIRECTORY}/control.token" "${CONFIG_DIRECTORY}/web-password.hash" \
      "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" \
      "${SERVER_UPGRADE_CANDIDATE}" /usr/local/bin/.404-probe-server.previous \
      "${SERVER_UPGRADE_HELPER_CANDIDATE}"
    remove_owned_tree "${SERVER_UPGRADE_DIRECTORY}"
    remove_owned_tree "${SERVER_UPGRADE_LOCK_DIRECTORY}"
  fi
  rmdir --ignore-fail-on-non-empty "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" 2>/dev/null || true
  systemctl daemon-reload
  if ! server_residue_exists && [[ ! -e "${AGENT_UNIT}" && ! -d "${CONFIG_DIRECTORY}" && ! -d "${STATE_DIRECTORY}" \
    && ! -d "${AGENT_UPDATER_STATE}" && ! -d "${SECURITY_STATE_DIRECTORY}" ]]; then
    remove_service_account_for_uninstall
  fi
	if ! server_residue_exists && [[ ! -e "${SERVER_UNIT}" && ! -e "${AGENT_UNIT}" ]] && (( removal_pending == 0 )); then
	  rm -f -- "${INSTALL_HELPER}"
	fi
  note "Uninstalled ${role} and removed its managed data. Re-running the official version-pinned uninstall is safe."
  printf 'Preserved systemd journal history and any unrecognized files.\n'
}

json_string_field() {
  local json="$1" field="$2"
  printf '%s\n' "${json}" | sed -n "s/.*\"${field}\":\"\([^\"]*\)\".*/\1/p"
}

compare_versions() {
  local left="$1" right="$2" lmajor lminor lpatch rmajor rminor rpatch lbeta rbeta lbase rbase
  release_version "${left}" && release_version "${right}" || return 2
  lbase="${left%%-beta.*}"; rbase="${right%%-beta.*}"
  IFS=. read -r lmajor lminor lpatch <<<"${lbase#v}"
  IFS=. read -r rmajor rminor rpatch <<<"${rbase#v}"
  for pair in "${lmajor}:${rmajor}" "${lminor}:${rminor}" "${lpatch}:${rpatch}"; do
    if (( 10#${pair%%:*} < 10#${pair#*:} )); then printf '%s\n' -1; return; fi
    if (( 10#${pair%%:*} > 10#${pair#*:} )); then printf '%s\n' 1; return; fi
  done
  [[ "${left}" == *-beta.* || "${right}" == *-beta.* ]] || { printf '%s\n' 0; return; }
  [[ "${left}" == *-beta.* ]] || { printf '%s\n' 1; return; }
  [[ "${right}" == *-beta.* ]] || { printf '%s\n' -1; return; }
  lbeta="${left##*-beta.}"; rbeta="${right##*-beta.}"
  if (( 10#${lbeta} < 10#${rbeta} )); then printf '%s\n' -1
  elif (( 10#${lbeta} > 10#${rbeta} )); then printf '%s\n' 1
  else printf '%s\n' 0; fi
}

download_server_upgrade_candidate() (
  local target="$1" architecture asset temporary_directory checksum_line version_json candidate_version candidate_commit release_json release_commit
  architecture="$(detect_architecture)"
  asset="404-probe-server-linux-${architecture}"
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf -- "${temporary_directory}"' EXIT
  if [[ -n "${PROBE_404_LOCAL_ASSET_DIRECTORY:-}" ]]; then
    for name in "${asset}" SHA256SUMS RELEASE-METADATA.json; do
      [[ -f "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${name}" && ! -L "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${name}" ]] \
        || die "local release asset not found or unsafe: ${name}"
      cp -- "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${name}" "${temporary_directory}/${name}"
    done
  else
    for name in "${asset}" SHA256SUMS RELEASE-METADATA.json; do
      download_release_asset "${target}" "${name}" "${temporary_directory}/${name}" \
        || die "could not download ${name} (${target}) from official entries"
    done
  fi
  checksum_line="$(grep -E "^[[:xdigit:]]{64}  ${asset}$" "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "SHA256SUMS does not authenticate ${asset} exactly once"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "SHA256 verification failed for ${asset}"
  install -m 0755 -o root -g root "${temporary_directory}/${asset}" "${SERVER_UPGRADE_CANDIDATE}"
  release_json="$("${SERVER_UPGRADE_CANDIDATE}" release verify --version "${target}" \
    --metadata "${temporary_directory}/RELEASE-METADATA.json" --checksums "${temporary_directory}/SHA256SUMS" \
    --asset "${temporary_directory}/${asset}")" || die "strict release metadata verification failed"
  release_commit="$(json_string_field "${release_json}" commit)"
  version_json="$("${SERVER_UPGRADE_CANDIDATE}" version --json)" || die "candidate does not provide Server version JSON"
  candidate_version="$(json_string_field "${version_json}" version)"
  candidate_commit="$(json_string_field "${version_json}" commit)"
  [[ "${candidate_version}" == "${target}" && "${candidate_commit}" =~ ^[0-9a-f]{40}$ && "${version_json}" == *'"dirty":false'* ]] \
    || die "candidate build identity is invalid"
  [[ "${release_commit}" == "${candidate_commit}" ]] \
    || die "candidate commit does not match release metadata"
)

identify_installed_server_version() {
  local version_json version inspected commit
  if version_json="$("${SERVER_BINARY}" version --json 2>/dev/null)"; then
    version="$(json_string_field "${version_json}" version)"
    release_version "${version}" && [[ "${version_json}" == *'"dirty":false'* ]] \
      || die "installed Server returned an untrusted build identity; no files were changed"
    printf '%s\n' "${version}"
    return
  fi
  inspected="$("${SERVER_UPGRADE_CANDIDATE}" version inspect --json "${SERVER_BINARY}" 2>/dev/null)" \
    || die "installed Server build cannot be identified safely; no files were changed"
  commit="$(json_string_field "${inspected}" commit)"
  [[ "${inspected}" == *'"dirty":false'* ]] || die "installed Server is a modified build; no files were changed"
  case "${commit}" in
    e3a614d54eb868e14a1382455579f3872e26463d) printf '%s\n' v0.8.0 ;;
    4dfbd3e9018123d9e8a43a5857a5f0f6758abe35) printf '%s\n' v0.8.1 ;;
    05d566e41063764c3322cf5264eb8329366d1240) printf '%s\n' v0.9.0 ;;
    *) die "installed legacy Server is unknown (commit ${commit:-unavailable}); refusing to guess its version" ;;
  esac
}

write_server_upgrade_state() {
  local phase="$1" active="$2" enabled="$3" target="$4" temporary
  temporary="${SERVER_UPGRADE_STATE}.new"
  printf 'phase=%s\noriginal_active=%s\noriginal_enabled=%s\ntarget=%s\n' \
    "${phase}" "${active}" "${enabled}" "${target}" >"${temporary}" || return 1
  chmod 0600 "${temporary}" || return 1
  sync -f "${temporary}" || return 1
  mv -f -- "${temporary}" "${SERVER_UPGRADE_STATE}" || return 1
  sync -f "${SERVER_UPGRADE_STATE}" || return 1
  sync -f "${SERVER_UPGRADE_DIRECTORY}" || return 1
}

clear_server_upgrade_state() {
  local content
  content="$(<"${SERVER_UPGRADE_STATE}")" || return 1
  rm -f -- "${SERVER_UPGRADE_STATE}" || return 1
  if ! sync -f "${SERVER_UPGRADE_DIRECTORY}"; then
    printf '%s\n' "${content}" >"${SERVER_UPGRADE_STATE}" || true
    chmod 0600 "${SERVER_UPGRADE_STATE}" || true
    sync -f "${SERVER_UPGRADE_STATE}" >/dev/null 2>&1 || true
    return 1
  fi
}

server_upgrade_state_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "${SERVER_UPGRADE_STATE}"
}

database_has_open_handles() {
  local fd resolved target
  for fd in /proc/[0-9]*/fd/*; do
    [[ -e "${fd}" ]] || continue
    resolved="$(readlink -f "${fd}" 2>/dev/null || true)"
    for target in "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"; do
      [[ "${resolved}" != "${target}" ]] || return 0
    done
  done
  return 1
}

existing_regular_files_kb() {
  local path bytes=0 size
  for path in "$@"; do
    [[ -e "${path}" ]] || continue
    [[ -f "${path}" && ! -L "${path}" ]] || return 1
    size="$(stat -c '%s' "${path}")" || return 1
    [[ "${size}" =~ ^[0-9]+$ ]] || return 1
    bytes=$((bytes + size))
  done
  printf '%s\n' "$(((bytes + 1023) / 1024))"
}

create_server_upgrade_backup_manifest() {
  local backup="$1" file
  tar -cpf "${backup}/config.tar" -C "${backup}" config || return 1
  : >"${backup}/database-files" || return 1
  for file in 404-probe.db 404-probe.db-wal 404-probe.db-shm; do
    [[ ! -e "${backup}/database/${file}" ]] || printf '%s\n' "${file}" >>"${backup}/database-files" || return 1
  done
  (
    cd "${backup}" || exit 1
    sha256sum server-binary server.service config.tar database-files helper-state || exit 1
    [[ "$(<helper-state)" != 1 ]] || sha256sum install-helper || exit 1
    while IFS= read -r file; do sha256sum "database/${file}" || exit 1; done <database-files
  ) >"${backup}/SHA256SUMS" || return 1
  chmod 0600 "${backup}/config.tar" "${backup}/database-files" "${backup}/helper-state" "${backup}/SHA256SUMS" || return 1
  sync -f "${backup}/server-binary" "${backup}/server.service" "${backup}/config.tar" \
    "${backup}/database-files" "${backup}/helper-state" "${backup}/SHA256SUMS" "${backup}/database/404-probe.db" || return 1
  [[ "$(<"${backup}/helper-state")" != 1 ]] || sync -f "${backup}/install-helper" || return 1
  sync -f "${backup}" "${backup}/database" || return 1
}

verify_server_upgrade_backup() {
  local backup="$1" file expected_lines=5 actual_lines expected_names actual_names helper_state
  [[ -d "${backup}" && ! -L "${backup}" && "$(stat -c '%U:%G:%a' "${backup}")" == root:root:700 ]] || return 1
  [[ -d "${backup}/database" && ! -L "${backup}/database" ]] || return 1
  for file in complete server-binary server.service config.tar database-files helper-state SHA256SUMS database/404-probe.db; do
    [[ -f "${backup}/${file}" && ! -L "${backup}/${file}" ]] || return 1
  done
  helper_state="$(<"${backup}/helper-state")" || return 1
  [[ "${helper_state}" =~ ^[01]$ ]] || return 1
  if [[ "${helper_state}" == 1 ]]; then
    [[ -f "${backup}/install-helper" && ! -L "${backup}/install-helper" ]] || return 1
    expected_lines=$((expected_lines + 1))
  else
    [[ ! -e "${backup}/install-helper" ]] || return 1
  fi
  [[ "$(sed -n '1p' "${backup}/database-files")" == 404-probe.db ]] || return 1
  [[ "$(grep -Ec '^404-probe\.db(-wal|-shm)?$' "${backup}/database-files")" -eq "$(grep -c . "${backup}/database-files")" ]] || return 1
  [[ "$(sort -u "${backup}/database-files" | grep -c .)" -eq "$(grep -c . "${backup}/database-files")" ]] || return 1
  for file in 404-probe.db 404-probe.db-wal 404-probe.db-shm; do
    if [[ -e "${backup}/database/${file}" ]]; then
      grep -Fxq "${file}" "${backup}/database-files" || return 1
    elif grep -Fxq "${file}" "${backup}/database-files"; then
      return 1
    fi
  done
  while IFS= read -r file; do
    [[ -f "${backup}/database/${file}" && ! -L "${backup}/database/${file}" ]] || return 1
    expected_lines=$((expected_lines + 1))
  done <"${backup}/database-files"
  for file in "${backup}/database"/*; do
    [[ -e "${file}" ]] || continue
    [[ -f "${file}" && ! -L "${file}" ]] || return 1
    grep -Fxq "$(basename "${file}")" "${backup}/database-files" || return 1
  done
  actual_lines="$(grep -Ec '^[[:xdigit:]]{64} [ *](server-binary|server\.service|config\.tar|database-files|helper-state|install-helper|database/404-probe\.db(-wal|-shm)?)$' "${backup}/SHA256SUMS")"
  [[ "${actual_lines}" -eq "${expected_lines}" && "${actual_lines}" -eq "$(grep -c . "${backup}/SHA256SUMS")" ]] || return 1
  expected_names="$({ printf '%s\n' server-binary server.service config.tar database-files helper-state; [[ "${helper_state}" != 1 ]] || printf '%s\n' install-helper; while IFS= read -r file; do printf 'database/%s\n' "${file}"; done <"${backup}/database-files"; } | sort)" || return 1
  actual_names="$(awk '{name=$2; sub(/^\*/, "", name); print name}' "${backup}/SHA256SUMS" | sort)" || return 1
  [[ "${actual_names}" == "${expected_names}" ]] || return 1
  (cd "${backup}" && sha256sum --check --strict SHA256SUMS >/dev/null) || return 1
}

verify_legacy_server_upgrade_backup() {
  local backup="$1" file expected_lines=4 actual_lines expected_names actual_names
  [[ -d "${backup}" && ! -L "${backup}" && "$(stat -c '%U:%G:%a' "${backup}")" == root:root:700 ]] || return 1
  [[ -d "${backup}/database" && ! -L "${backup}/database" ]] || return 1
  [[ ! -e "${backup}/helper-state" && ! -e "${backup}/install-helper" ]] || return 1
  for file in complete server-binary server.service config.tar database-files SHA256SUMS database/404-probe.db; do
    [[ -f "${backup}/${file}" && ! -L "${backup}/${file}" ]] || return 1
  done
  [[ "$(sed -n '1p' "${backup}/database-files")" == 404-probe.db ]] || return 1
  [[ "$(grep -Ec '^404-probe\.db(-wal|-shm)?$' "${backup}/database-files")" -eq "$(grep -c . "${backup}/database-files")" ]] || return 1
  [[ "$(sort -u "${backup}/database-files" | grep -c .)" -eq "$(grep -c . "${backup}/database-files")" ]] || return 1
  for file in 404-probe.db 404-probe.db-wal 404-probe.db-shm; do
    if [[ -e "${backup}/database/${file}" ]]; then
      grep -Fxq "${file}" "${backup}/database-files" || return 1
    elif grep -Fxq "${file}" "${backup}/database-files"; then
      return 1
    fi
  done
  while IFS= read -r file; do
    [[ -f "${backup}/database/${file}" && ! -L "${backup}/database/${file}" ]] || return 1
    expected_lines=$((expected_lines + 1))
  done <"${backup}/database-files"
  for file in "${backup}/database"/*; do
    [[ -e "${file}" ]] || continue
    [[ -f "${file}" && ! -L "${file}" ]] || return 1
    grep -Fxq "$(basename "${file}")" "${backup}/database-files" || return 1
  done
  actual_lines="$(grep -Ec '^[[:xdigit:]]{64} [ *](server-binary|server\.service|config\.tar|database-files|database/404-probe\.db(-wal|-shm)?)$' "${backup}/SHA256SUMS")"
  [[ "${actual_lines}" -eq "${expected_lines}" && "${actual_lines}" -eq "$(grep -c . "${backup}/SHA256SUMS")" ]] || return 1
  expected_names="$({ printf '%s\n' server-binary server.service config.tar database-files; while IFS= read -r file; do printf 'database/%s\n' "${file}"; done <"${backup}/database-files"; } | sort)" || return 1
  actual_names="$(awk '{name=$2; sub(/^\*/, "", name); print name}' "${backup}/SHA256SUMS" | sort)" || return 1
  [[ "${actual_names}" == "${expected_names}" ]] || return 1
  (cd "${backup}" && sha256sum --check --strict SHA256SUMS >/dev/null) || return 1
}

restore_server_upgrade() {
  reject_openrc_deferred Server-upgrade-recovery
  local phase active enabled target restore_temporary helper_restore_temporary helper_state legacy_backup=0
  [[ -f "${SERVER_UPGRADE_STATE}" && ! -L "${SERVER_UPGRADE_STATE}" ]] || return 1
  phase="$(server_upgrade_state_value phase)" || return 1
  active="$(server_upgrade_state_value original_active)" || return 1
  enabled="$(server_upgrade_state_value original_enabled)" || return 1
  target="$(server_upgrade_state_value target)" || return 1
  [[ "${active}" =~ ^[01]$ && "${enabled}" =~ ^[01]$ ]] || return 1
  release_version "${target}" || return 1
  systemctl stop 404-probe-server.service >/dev/null 2>&1 || true
  if systemctl is-active --quiet 404-probe-server.service; then
    printf '404-probe installer: recovery stopped because the Server could not be stopped\n' >&2
    return 1
  fi
  case "${phase}" in
    prepared|stopped-unbacked) ;;
    backup-complete|migration-started|binary-replaced|candidate-started|helper-replaced)
      if [[ -e "${SERVER_UPGRADE_BACKUP}/helper-state" ]]; then
        verify_server_upgrade_backup "${SERVER_UPGRADE_BACKUP}"
      else
        [[ "${phase}" != helper-replaced ]] && verify_legacy_server_upgrade_backup "${SERVER_UPGRADE_BACKUP}" && legacy_backup=1
      fi || {
          printf '404-probe installer: backup is incomplete; leaving the Server stopped and preserving recovery files\n' >&2
          return 1
        }
      if [[ "${phase}" == "migration-started" || "${phase}" == "binary-replaced" || "${phase}" == "candidate-started" || "${phase}" == "helper-replaced" ]]; then
        rm -f -- "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm" || return 1
        while IFS= read -r file; do
          cp -a -- "${SERVER_UPGRADE_BACKUP}/database/${file}" "${STATE_DIRECTORY}/${file}" || return 1
        done <"${SERVER_UPGRADE_BACKUP}/database-files"
        restore_temporary="${SERVER_BINARY}.restore"
        cp -a -- "${SERVER_UPGRADE_BACKUP}/server-binary" "${restore_temporary}" || return 1
        mv -f -- "${restore_temporary}" "${SERVER_BINARY}" || return 1
        sync -f "${SERVER_BINARY}" "${SERVER_DATABASE}" || return 1
        sync -f "${STATE_DIRECTORY}" "$(dirname "${SERVER_BINARY}")" || return 1
      fi
      if (( legacy_backup == 0 )); then
        helper_state="$(<"${SERVER_UPGRADE_BACKUP}/helper-state")" || return 1
        if [[ "${helper_state}" == 1 ]]; then
          helper_restore_temporary="${INSTALL_HELPER}.restore"
          cp -a -- "${SERVER_UPGRADE_BACKUP}/install-helper" "${helper_restore_temporary}" || return 1
          mv -f -- "${helper_restore_temporary}" "${INSTALL_HELPER}" || return 1
          sync -f "${INSTALL_HELPER}" || return 1
        else
          rm -f -- "${INSTALL_HELPER}" || return 1
        fi
        sync -f "$(dirname "${INSTALL_HELPER}")" || return 1
      fi
      ;;
    *) return 1 ;;
  esac
  if (( enabled != 0 )); then systemctl enable 404-probe-server.service >/dev/null || return 1
  else systemctl disable 404-probe-server.service >/dev/null || return 1; fi
  if (( active != 0 )); then systemctl start 404-probe-server.service || return 1
  else systemctl stop 404-probe-server.service >/dev/null 2>&1 || true; fi
  clear_server_upgrade_state || return 1
  rm -f -- "${SERVER_UPGRADE_CANDIDATE}" || return 1
  rm -f -- "${SERVER_UPGRADE_HELPER_CANDIDATE}" || return 1
  rm -rf -- "${STATE_DIRECTORY}/.404-probe-upgrade-staging" || return 1
  return 0
}

server_upgrade_trap() {
  local status="$1"
  trap - EXIT HUP INT TERM
  if (( status != 0 )) && [[ -f "${SERVER_UPGRADE_STATE}" ]]; then
    printf '\n404-probe installer: upgrade failed; restoring the pre-upgrade Server state...\n' >&2
    if ! restore_server_upgrade; then
      printf '404-probe installer: automatic recovery is incomplete. Leave the Server stopped and preserve %s.\n' "${SERVER_UPGRADE_DIRECTORY}" >&2
    fi
  fi
  exit "${status}"
}

upgrade_existing_server() (
  reject_openrc_deferred Server-upgrade
  local target current comparison active=0 enabled=0 helper_existed=0 active_state enabled_state answer required_kb available_kb database_kb pid executable version_json listen origin
  if [[ -e "${SERVER_UPGRADE_LOCK_DIRECTORY}" ]]; then
    [[ -d "${SERVER_UPGRADE_LOCK_DIRECTORY}" && ! -L "${SERVER_UPGRADE_LOCK_DIRECTORY}" \
      && "$(stat -c '%U:%G:%a' "${SERVER_UPGRADE_LOCK_DIRECTORY}")" == "root:root:700" ]] \
      || die "Server upgrade lock directory is unsafe"
  else
    install -d -m 0700 -o root -g root "${SERVER_UPGRADE_LOCK_DIRECTORY}"
  fi
  if [[ -e "${SERVER_UPGRADE_LOCK}" ]]; then
    [[ -f "${SERVER_UPGRADE_LOCK}" && ! -L "${SERVER_UPGRADE_LOCK}" \
      && "$(stat -c '%U:%G:%a' "${SERVER_UPGRADE_LOCK}")" == "root:root:600" ]] \
      || die "Server upgrade lock file is unsafe"
  else
    install -m 0600 -o root -g root /dev/null "${SERVER_UPGRADE_LOCK}"
  fi
  exec 9<>"${SERVER_UPGRADE_LOCK}"
  flock -n 9 || die "another Server upgrade is already running"
  trap 'rm -f -- "${SERVER_UPGRADE_CANDIDATE}" "${SERVER_UPGRADE_HELPER_CANDIDATE}"' EXIT
  if [[ -e "${SERVER_UPGRADE_DIRECTORY}" ]]; then
    [[ -d "${SERVER_UPGRADE_DIRECTORY}" && ! -L "${SERVER_UPGRADE_DIRECTORY}" \
      && "$(stat -c '%U:%G:%a' "${SERVER_UPGRADE_DIRECTORY}")" == "root:root:700" ]] \
      || die "Server upgrade directory is unsafe; no files were changed"
  else
    install -d -m 0700 -o root -g root "${SERVER_UPGRADE_DIRECTORY}"
  fi
  if [[ -e "${SERVER_UPGRADE_STATE}" ]]; then
    note "An interrupted Server upgrade was found; restoring its recorded pre-upgrade state first."
    restore_server_upgrade || die "interrupted upgrade recovery requires manual attention; recovery files were preserved"
    die "interrupted upgrade was recovered; run the installer again to start a fresh upgrade"
  fi
  [[ -f "${SERVER_BINARY}" && ! -L "${SERVER_BINARY}" && -f "${SERVER_UNIT}" && ! -L "${SERVER_UNIT}" \
    && -d "${CONFIG_DIRECTORY}" && ! -L "${CONFIG_DIRECTORY}" && -f "${SERVER_DATABASE}" && ! -L "${SERVER_DATABASE}" ]] \
    || die "existing Server installation is incomplete or unsupported; no files were changed"
  [[ ! -e "${AGENT_UNIT}" && ! -e "${AGENT_BINARY}" ]] || die "mixed Server/Agent installation is unsupported"
  [[ "$(stat -c '%U:%G:%a' "${SERVER_BINARY}")" == "root:root:755" ]] || die "installed Server binary ownership or mode is unsafe"
  [[ "$(stat -c '%U:%G:%a' "${SERVER_UNIT}")" == "root:root:644" ]] || die "installed Server unit ownership or mode is unsafe"
  if [[ -e "${INSTALL_HELPER}" ]]; then
    [[ -f "${INSTALL_HELPER}" && ! -L "${INSTALL_HELPER}" && "$(stat -c '%U:%G:%a' "${INSTALL_HELPER}")" == "root:root:755" ]] \
      || die "installed helper ownership or mode is unsafe"
    helper_existed=1
  fi
  grep -Fxq "User=${SERVICE_USER}" "${SERVER_UNIT}" \
    && grep -Fq "ExecStart=${SERVER_BINARY} serve " "${SERVER_UNIT}" \
    && grep -Fq -- "--db ${SERVER_DATABASE}" "${SERVER_UNIT}" \
    || die "installed Server unit is not a supported 404-probe layout"
  listen="$(sed -n "s#^ExecStart=${SERVER_BINARY} serve --listen \([^ ]*\) .*#\1#p" "${SERVER_UNIT}")"
  [[ "$(printf '%s\n' "${listen}" | grep -c .)" -eq 1 ]] || die "installed Server listen address is ambiguous"
  if [[ "${listen}" =~ ^(127\.0\.0\.1|localhost):([0-9]{1,5})$ || "${listen}" =~ ^\[::1\]:([0-9]{1,5})$ ]]; then
    origin="http://${listen}"
  else
    die "installed Server listen address is not a supported loopback endpoint"
  fi
  target="$(target_version)"
  rm -f -- "${SERVER_UPGRADE_CANDIDATE}"
  rm -f -- "${SERVER_UPGRADE_HELPER_CANDIDATE}"
  download_server_upgrade_candidate "${target}"
  render_local_helper >"${SERVER_UPGRADE_HELPER_CANDIDATE}" || die "could not render upgraded local helper"
  chmod 0755 "${SERVER_UPGRADE_HELPER_CANDIDATE}" || die "could not protect upgraded local helper"
  bash -n "${SERVER_UPGRADE_HELPER_CANDIDATE}" || die "upgraded local helper failed syntax validation"
  current="$(identify_installed_server_version)"
  comparison="$(compare_versions "${current}" "${target}")"
  if [[ "${comparison}" == 0 ]]; then
    rm -f -- "${SERVER_UPGRADE_CANDIDATE}" "${SERVER_UPGRADE_HELPER_CANDIDATE}"
    note "404-probe Server is already ${target}; no files were changed."
    exit 0
  fi
  [[ "${comparison}" == -1 ]] || die "refusing to downgrade Server ${current} to ${target}"
  runuser -u "${SERVICE_USER}" -- "${SERVER_UPGRADE_CANDIDATE}" database verify --db "${SERVER_DATABASE}" >/dev/null \
    || die "candidate cannot read and verify the existing database without migration"
  active_state="$(systemctl is-active 404-probe-server.service 2>/dev/null || true)"
  enabled_state="$(systemctl is-enabled 404-probe-server.service 2>/dev/null || true)"
  [[ "${active_state}" == active || "${active_state}" == inactive ]] \
    || die "Server service state ${active_state:-unknown} is not safe for an automated upgrade"
  [[ "${enabled_state}" == enabled || "${enabled_state}" == disabled ]] \
    || die "Server enablement state ${enabled_state:-unknown} is not safe for an automated upgrade"
  [[ "${active_state}" == active ]] && active=1
  [[ "${enabled_state}" == enabled ]] && enabled=1
  database_kb="$(existing_regular_files_kb "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm")" \
    || die "database, WAL, or SHM path is unsafe"
  required_kb=$(( database_kb * 3 + 131072 ))
  available_kb="$(df -Pk "${SERVER_UPGRADE_DIRECTORY}" | awk 'NR==2 {print $4}')"
  [[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge "${required_kb}" ]] \
    || die "insufficient free space for protected upgrade (need ${required_kb} KiB, have ${available_kb:-unknown} KiB)"
  cat >/dev/tty <<EOF
404-probe Server upgrade

  Role:             Server
  Current build:    ${current} ($(sha256sum "${SERVER_BINARY}" | awk '{print $1}'))
  Target release:   ${target}
  Database impact:  protected backup, migration, integrity verification, automatic rollback on failure
  Preserved:        configuration, credentials, domain/listen settings, systemd enabled/active state

EOF
  printf 'Proceed with this Server upgrade? [y/N]: ' >/dev/tty
  IFS= read -r answer </dev/tty
  [[ "${answer}" == y || "${answer}" == Y ]] || { rm -f -- "${SERVER_UPGRADE_CANDIDATE}"; die "upgrade cancelled; no files were changed"; }

  rm -rf -- "${SERVER_UPGRADE_DIRECTORY}/backup.previous" || die "could not rotate previous Server backup"
  if [[ -e "${SERVER_UPGRADE_BACKUP}" ]]; then
    [[ -d "${SERVER_UPGRADE_BACKUP}" && ! -L "${SERVER_UPGRADE_BACKUP}" ]] || die "existing Server backup path is unsafe"
    mv -- "${SERVER_UPGRADE_BACKUP}" "${SERVER_UPGRADE_DIRECTORY}/backup.previous" || die "could not rotate previous Server backup"
  fi
  install -d -m 0700 -o root -g root "${SERVER_UPGRADE_BACKUP}" "${SERVER_UPGRADE_BACKUP}/database"
  write_server_upgrade_state prepared "${active}" "${enabled}" "${target}" || die "could not persist prepared upgrade state"
  trap 'server_upgrade_trap $?' EXIT
  trap 'exit 130' HUP INT TERM
  systemctl stop 404-probe-server.service || die "could not stop Server; upgrade did not begin"
  systemctl is-active --quiet 404-probe-server.service && die "Server is still active; upgrade did not begin"
  write_server_upgrade_state stopped-unbacked "${active}" "${enabled}" "${target}" || die "could not persist stopped upgrade state"
  database_has_open_handles && die "a process still has the Server database open; refusing to take an inconsistent backup"
  cp -a -- "${SERVER_BINARY}" "${SERVER_UPGRADE_BACKUP}/server-binary" || die "could not back up Server binary"
  cp -a -- "${SERVER_UNIT}" "${SERVER_UPGRADE_BACKUP}/server.service" || die "could not back up Server unit"
  cp -a -- "${CONFIG_DIRECTORY}" "${SERVER_UPGRADE_BACKUP}/config" || die "could not back up Server configuration"
  printf '%s\n' "${helper_existed}" >"${SERVER_UPGRADE_BACKUP}/helper-state" || die "could not record local helper state"
  if (( helper_existed != 0 )); then
    cp -a -- "${INSTALL_HELPER}" "${SERVER_UPGRADE_BACKUP}/install-helper" || die "could not back up local helper"
  fi
  for path in "${SERVER_DATABASE}" "${SERVER_DATABASE}-wal" "${SERVER_DATABASE}-shm"; do
    [[ ! -e "${path}" ]] || cp -a -- "${path}" "${SERVER_UPGRADE_BACKUP}/database/" || die "could not back up $(basename "${path}")"
  done
  create_server_upgrade_backup_manifest "${SERVER_UPGRADE_BACKUP}" || die "could not create a durable Server backup manifest"
  touch "${SERVER_UPGRADE_BACKUP}/complete" || die "could not mark Server backup complete"
  chmod 0600 "${SERVER_UPGRADE_BACKUP}/complete" || die "could not protect Server backup marker"
  sync -f "${SERVER_UPGRADE_BACKUP}/complete" || die "could not persist Server backup marker"
  sync -f "${SERVER_UPGRADE_BACKUP}" || die "could not persist Server backup directory"
  verify_server_upgrade_backup "${SERVER_UPGRADE_BACKUP}" || die "new Server backup failed complete integrity verification"
  write_server_upgrade_state backup-complete "${active}" "${enabled}" "${target}" || die "could not persist completed backup state"
  rm -rf -- "${STATE_DIRECTORY}/.404-probe-upgrade-staging" || die "could not clear old migration staging"
  install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${STATE_DIRECTORY}/.404-probe-upgrade-staging" || die "could not create migration staging"
  cp -a -- "${SERVER_UPGRADE_BACKUP}/database/." "${STATE_DIRECTORY}/.404-probe-upgrade-staging/" || die "could not stage database backup"
  chown -R "${SERVICE_USER}:${SERVICE_USER}" "${STATE_DIRECTORY}/.404-probe-upgrade-staging" || die "could not protect migration staging"
  runuser -u "${SERVICE_USER}" -- "${SERVER_UPGRADE_CANDIDATE}" database migrate-copy --db "${STATE_DIRECTORY}/.404-probe-upgrade-staging/404-probe.db" >/dev/null \
    || die "candidate migration rehearsal failed on the protected database copy"
  write_server_upgrade_state migration-started "${active}" "${enabled}" "${target}" || die "could not persist production migration state"
  runuser -u "${SERVICE_USER}" -- "${SERVER_UPGRADE_CANDIDATE}" database migrate-protected --db "${SERVER_DATABASE}" >/dev/null \
    || die "production database migration failed"
  mv -f -- "${SERVER_UPGRADE_CANDIDATE}" "${SERVER_BINARY}" || die "could not atomically replace Server binary"
  write_server_upgrade_state binary-replaced "${active}" "${enabled}" "${target}" || die "could not persist binary replacement state"
  version_json="$("${SERVER_BINARY}" version --json)"
  [[ "$(json_string_field "${version_json}" version)" == "${target}" && "${version_json}" == *'"dirty":false'* ]] \
    || die "installed candidate identity check failed"
  if (( active != 0 )); then
    systemctl start 404-probe-server.service || die "candidate Server failed to start"
    write_server_upgrade_state candidate-started "${active}" "${enabled}" "${target}" || die "could not persist candidate startup state"
    wait_for_server_readiness 404-probe-server.service "${origin}"
    pid="$(systemctl show -p MainPID --value 404-probe-server.service)"
    [[ "${pid}" =~ ^[1-9][0-9]*$ ]] || die "candidate Server has no main process"
    executable="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)"
    [[ "${executable}" == "${SERVER_BINARY}" ]] || die "running Server process is not the installed candidate"
  fi
  runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" database verify --db "${SERVER_DATABASE}" >/dev/null \
    || die "post-upgrade database query and integrity verification failed"
  mv -f -- "${SERVER_UPGRADE_HELPER_CANDIDATE}" "${INSTALL_HELPER}" || die "could not atomically replace local helper"
  write_server_upgrade_state helper-replaced "${active}" "${enabled}" "${target}" || die "could not persist local helper replacement state"
  "${INSTALL_HELPER}" --help >/dev/null || die "upgraded local helper failed its help check"
  "${INSTALL_HELPER}" domains list >/dev/null || die "upgraded local helper failed its offline domain-management check"
  if (( enabled != 0 )); then systemctl enable 404-probe-server.service >/dev/null || die "could not restore enabled state"
  else systemctl disable 404-probe-server.service >/dev/null || die "could not restore disabled state"; fi
  (( active != 0 )) || systemctl stop 404-probe-server.service >/dev/null 2>&1 || true
  rm -rf -- "${STATE_DIRECTORY}/.404-probe-upgrade-staging" || die "could not clear migration staging"
  sync -f "${SERVER_BINARY}" "${SERVER_DATABASE}" "${INSTALL_HELPER}" || die "could not persist upgraded Server files"
  sync -f "${STATE_DIRECTORY}" "$(dirname "${SERVER_BINARY}")" "$(dirname "${INSTALL_HELPER}")" || die "could not persist upgraded Server directories"
  clear_server_upgrade_state || die "could not durably commit Server upgrade state"
  rm -f -- "${SERVER_UPGRADE_CANDIDATE}" "${SERVER_UPGRADE_HELPER_CANDIDATE}" || die "could not clear Server candidate staging"
  trap - EXIT HUP INT TERM
  note "404-probe Server upgraded from ${current} to ${target}. The protected backup remains at ${SERVER_UPGRADE_BACKUP}."
)

interactive_install() {
  require_root_linux_systemd
  local choice
  cat >/dev/tty <<'EOF'
Install 404-probe v1.0.0

  1) Server
  2) Agent

EOF
  printf 'Select a role [1-2]: ' >/dev/tty
  IFS= read -r choice </dev/tty
  case "${choice}" in
    1|server|Server) install_server ;;
    2|agent|Agent) install_agent ;;
    *) die "invalid role selection" ;;
  esac
}

install_or_upgrade() {
  require_root_linux_systemd
  if server_residue_exists; then
    reject_openrc_deferred Server-upgrade
    upgrade_existing_server
    return
  fi
  if agent_residue_exists; then
    reject_openrc_deferred Agent-upgrade
    [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" \
      && -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
      || die "existing Agent installation is incomplete or unsupported; no files were changed"
    note "Existing 404-probe Agent detected. Agent upgrades remain controlled by the Server Web interface."
    printf 'No Agent identity, credential, epoch, service, or security state was changed.\n'
    printf 'If this Agent predates the local security collector, run: sudo 404-probe-install setup-security\n'
    printf 'To enable config-order selectors, run the current version-pinned install.sh with: selector-order /absolute/path/to/config.json\n'
    return
  fi
  interactive_install
}

main() {
  case "${1:-}" in -h|--help|help) usage; return ;; esac
  reject_deferred_command_on_host "${1:-}"
  require_root_linux_systemd
  case "${1:-}" in setup-security) reject_openrc_deferred setup-security ;; esac
  if [[ -z "${PROBE_404_LOCAL_ASSET_DIRECTORY:-}" && -z "${PROBE_404_INSTALLER_BOOTSTRAPPED:-}" ]]; then
    bootstrap_latest_installer "$@"
    exit $?
  fi
  case "${1:-}" in
    agent)
      shift
      install_agent_command "$@"
      ;;
    enroll)
      shift
      enroll_agent "$@"
      ;;
    setup-security)
      shift
      setup_security_existing "$@"
      ;;
    selector-order)
      shift
      setup_selector_order_existing "$@"
      ;;
    uninstall)
      shift
      uninstall_role "$@"
      ;;
    -h|--help|help)
      usage
      ;;
    "")
      install_or_upgrade
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
