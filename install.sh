#!/usr/bin/env bash

set -Eeuo pipefail

readonly REPOSITORY="404-git-404/404-probe"
readonly DEFAULT_VERSION="v0.9.2"
readonly INSTALL_HELPER="/usr/local/sbin/404-probe-install"
readonly SERVER_BINARY="/usr/local/bin/404-probe-server"
readonly AGENT_BINARY="/usr/local/bin/404-probe-agent"
readonly CONFIG_DIRECTORY="/etc/404-probe"
readonly STATE_DIRECTORY="/var/lib/404-probe"
readonly SERVER_DATABASE="${STATE_DIRECTORY}/404-probe.db"
readonly SERVER_UNIT="/etc/systemd/system/404-probe-server.service"
readonly AGENT_UNIT="/etc/systemd/system/404-probe-agent.service"
readonly AGENT_UPDATER_UNIT="/etc/systemd/system/404-probe-agent-updater.service"
readonly AGENT_UPDATER_STATE="/var/lib/404-probe-updater"
readonly AGENT_UPDATER_SOCKET="/run/404-probe/agent-updater.sock"
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

INSTALL_TRANSACTION_ACTIVE=0
INSTALL_TRANSACTION_ROLE=""
INSTALL_TRANSACTION_UNIT=""
INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_USER_CREATED=0
INSTALL_TRANSACTION_UPDATER_STATE_CREATED=0
INSTALL_TRANSACTION_SECURITY_STATE_CREATED=0
INSTALL_TRANSACTION_PATHS=()

die() {
  printf '404-probe installer: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '\n%s\n' "$*"
}

canonical_version() {
  [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

target_version() {
  local version="${PROBE_404_VERSION:-${DEFAULT_VERSION}}"
  canonical_version "${version}" || die "target version must use canonical vX.Y.Z form"
  printf '%s\n' "${version}"
}

bootstrap_latest_installer() (
  local effective latest release_base temporary_directory installer checksum_line status
  if [[ -n "${PROBE_404_VERSION:-}" ]]; then
    latest="${PROBE_404_VERSION}"
    canonical_version "${latest}" || die "explicit target version must use canonical vX.Y.Z form"
  else
    effective="$(curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
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
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "${installer}" "${release_base}/install.sh"
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "${temporary_directory}/SHA256SUMS" "${release_base}/SHA256SUMS"
  checksum_line="$(grep -E '^[[:xdigit:]]{64}  install\.sh$' "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "latest release does not authenticate install.sh exactly once"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "latest release installer checksum verification failed"
  if LC_ALL=C grep -q $'\r' "${installer}"; then
    die "latest release installer contains carriage returns"
  fi
  bash -n "${installer}" || die "latest release installer failed syntax validation"
  PROBE_404_INSTALLER_BOOTSTRAPPED=1 PROBE_404_VERSION="${latest}" bash "${installer}" "$@"
  status=$?
  exit "${status}"
)

usage() {
  cat <<'EOF'
Usage:
  404-probe-install                 install, or offer a protected existing-Server upgrade
  404-probe-install agent --server <origin>
  404-probe-install setup-security  enable V0.9 local security audit on an existing Agent
  404-probe-install enroll <name>   create one Agent enrollment token
  404-probe-install uninstall <server|agent>

Agent uninstall removes its service unit, binary, private environment, and
epoch/state files. Server uninstall preserves its configuration and database.
Systemd journal history is preserved for both roles.
EOF
}

require_root_linux_systemd() {
  [[ "${EUID}" -eq 0 ]] || die "run this installer as root (for example, with sudo)"
  [[ "$(uname -s)" == "Linux" ]] || die "only Linux is supported"
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
  INSTALL_TRANSACTION_ROLE="$1"
  [[ -d "${CONFIG_DIRECTORY}" ]] || INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=1
  [[ -d "${STATE_DIRECTORY}" ]] || INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=1
  trap 'rollback_install_transaction $?' EXIT
  trap 'exit 130' HUP INT TERM
}

track_install_path() {
  INSTALL_TRANSACTION_PATHS+=("$1")
}

rollback_install_transaction() {
  local status="$1"
  local index
  trap - EXIT HUP INT TERM
  if (( INSTALL_TRANSACTION_ACTIVE == 0 )); then
    exit "${status}"
  fi
  printf '\n404-probe installer: installation failed; rolling back new artifacts...\n' >&2
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
    rm -f -- "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json"
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
  local architecture version asset release_base temporary_directory checksum_line
  architecture="$(detect_architecture)"
  version="$(target_version)"
  asset="404-probe-${role}-linux-${architecture}"
  release_base="https://github.com/${REPOSITORY}/releases/download/${version}"
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf -- "${temporary_directory}"' EXIT

  note "Downloading ${asset} (${version})..."
  if [[ -n "${PROBE_404_LOCAL_ASSET_DIRECTORY:-}" ]]; then
    [[ -f "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${asset}" ]] || die "local release asset not found: ${asset}"
    [[ -f "${PROBE_404_LOCAL_ASSET_DIRECTORY}/SHA256SUMS" ]] || die "local SHA256SUMS not found"
    cp -- "${PROBE_404_LOCAL_ASSET_DIRECTORY}/${asset}" "${temporary_directory}/${asset}"
    cp -- "${PROBE_404_LOCAL_ASSET_DIRECTORY}/SHA256SUMS" "${temporary_directory}/SHA256SUMS"
  else
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
      --output "${temporary_directory}/${asset}" "${release_base}/${asset}"
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
      --output "${temporary_directory}/SHA256SUMS" "${release_base}/SHA256SUMS"
  fi

  checksum_line="$(grep -E "^[[:xdigit:]]{64}  ${asset}$" "${temporary_directory}/SHA256SUMS" || true)"
  [[ "$(printf '%s\n' "${checksum_line}" | grep -c .)" -eq 1 ]] || die "SHA256SUMS does not contain exactly one checksum for ${asset}"
  (cd "${temporary_directory}" && printf '%s\n' "${checksum_line}" | sha256sum --check --strict -) \
    || die "SHA256 verification failed for ${asset}"
  install -m 0755 "${temporary_directory}/${asset}" "${destination}"
)

render_local_helper() {
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
readonly SECURITY_STATE_DIRECTORY="/var/lib/404-probe-security"
readonly SECURITY_EXPORT_DIRECTORY="${SECURITY_STATE_DIRECTORY}/export"
readonly SECURITY_SERVICE_UNIT="/etc/systemd/system/404-probe-security-collect.service"
readonly SECURITY_TIMER_UNIT="/etc/systemd/system/404-probe-security-collect.timer"
readonly CONFIG_DIRECTORY="/etc/404-probe"
readonly STATE_DIRECTORY="/var/lib/404-probe"
readonly SERVICE_USER="404-probe"
readonly ENROLLMENT_PREFIX="404p1_"

die() { printf '404-probe helper: %s\n' "$*" >&2; exit 1; }
usage() {
  printf 'Usage: 404-probe-install enroll <name> | domains [list|add|remove|disable] | setup-security | uninstall <server|agent>\n'
}
require_root() {
  [[ "${EUID}" -eq 0 ]] || die "run this helper as root"
  command -v systemctl >/dev/null 2>&1 || die "systemd is required"
}
enroll() {
  [[ $# -eq 1 ]] || die "usage: 404-probe-install enroll <agent-name>"
  local name="$1" output agent_id agent_token payload
  [[ -n "${name//[[:space:]]/}" && "${name}" != -* && "${name}" != *$'\n'* && "${name}" != *$'\r'* ]] \
    || die "Agent name must be non-empty, contain no newline, and not begin with a hyphen"
  [[ -x "${SERVER_BINARY}" && -f "${SERVER_DATABASE}" && -f "${SERVER_UNIT}" ]] \
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
uninstall() {
  [[ $# -eq 1 && ( "$1" == "server" || "$1" == "agent" ) ]] \
    || die "usage: 404-probe-install uninstall <server|agent>"
  local role="$1" unit="404-probe-$1.service" unit_path binary_path
  if [[ "${role}" == "server" ]]; then
    unit_path="${SERVER_UNIT}"; binary_path="${SERVER_BINARY}"
  else
    unit_path="${AGENT_UNIT}"; binary_path="${AGENT_BINARY}"
  fi
	if [[ "${role}" == "agent" ]]; then
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
  rm -f -- "${unit_path}" "${binary_path}"
  if [[ "${role}" == "agent" ]]; then
    rm -f -- "${CONFIG_DIRECTORY}/agent.env" \
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" \
	  "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
	  "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" \
	  "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
	rm -rf -- "${AGENT_UPDATER_STATE}"
	rm -rf -- "${SECURITY_STATE_DIRECTORY}"
  fi
  systemctl daemon-reload
  printf 'Uninstalled %s. Re-running this command is safe.\n' "${role}"
  if [[ "${role}" == "agent" ]]; then
    printf 'Removed the Agent unit, binary, private environment, and epoch/state files.\n'
  else
    printf 'Preserved Server configuration, credentials, and database.\n'
  fi
  printf 'Preserved systemd journal history, service user, and this installer helper.\n'
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

require_root
case "${1:-}" in
  -h|--help|help) usage ;;
  enroll) shift; enroll "$@" ;;
  domains) shift; domains "$@" ;;
  setup-security) shift; setup_security "$@" ;;
  uninstall) shift; uninstall "$@" ;;
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
  chown "${SERVICE_USER}:${SERVICE_USER}" "${temporary_file}"
  chmod 0400 "${temporary_file}"
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

install_server() {
  [[ ! -e "${AGENT_UNIT}" && ! -e "${CONFIG_DIRECTORY}/agent.env" ]] \
    || die "an Agent installation already exists; Server and Agent roles are kept on separate hosts"
  [[ ! -e "${SERVER_UNIT}" ]] || die "Server installation already exists; no files were changed"
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
  commit_install_transaction

  note "404-probe Server is installed and running."
  systemctl --no-pager --full status 404-probe-server.service || true
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

bootstrap_existing_agent() (
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
  trap 'status=$?; if (( rollback_active != 0 )); then systemctl stop 404-probe-agent.service >/dev/null 2>&1 || true; systemctl stop 404-probe-agent-updater.service >/dev/null 2>&1 || true; systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true; rm -f -- "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}"; rm -rf -- "${AGENT_UPDATER_STATE}"; if [[ -f "${previous}" && ! -L "${previous}" ]]; then mv -f -- "${previous}" "${AGENT_BINARY}"; fi; cp --preserve=mode,ownership,timestamps -- "${backup_directory}/agent.service" "${AGENT_UNIT}"; if (( helper_existed != 0 )); then cp --preserve=mode,ownership,timestamps -- "${backup_directory}/install-helper" "${INSTALL_HELPER}"; else rm -f -- "${INSTALL_HELPER}"; fi; systemctl daemon-reload >/dev/null 2>&1 || true; systemctl restart 404-probe-agent.service >/dev/null 2>&1 || true; fi; rm -f -- "${candidate}"; rm -rf -- "${backup_directory}"; exit "${status}"' EXIT HUP INT TERM
  systemctl stop 404-probe-agent.service || die "could not stop existing Agent"
  rollback_active=1
  ln -- "${AGENT_BINARY}" "${previous}"
  mv -f -- "${candidate}" "${AGENT_BINARY}"
  insecure_option=""
  [[ "${server_url}" != http://* ]] || insecure_option=" --allow-insecure-http"
  render_agent_unit "${insecure_option}" >"${AGENT_UNIT}"
  chmod 0644 "${AGENT_UNIT}"
  install_agent_updater_unit
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
  [[ ! -e "${SERVER_UNIT}" && ! -e "${CONFIG_DIRECTORY}/server.env" ]] \
    || die "a Server installation already exists; Server and Agent roles are kept on separate hosts"
  if [[ -e "${AGENT_UNIT}" || -e "${CONFIG_DIRECTORY}/agent.env" || -e "${AGENT_BINARY}" ]]; then
    [[ -e "${AGENT_UNIT}" && -e "${CONFIG_DIRECTORY}/agent.env" && -e "${AGENT_BINARY}" ]] \
      || die "conflicting partial Agent installation found; no files were changed"
    bootstrap_existing_agent
    return
  fi
  [[ ! -e "${CONFIG_DIRECTORY}/agent.env" ]] || die "existing Agent configuration was found; refusing to overwrite it"
  [[ ! -e "${STATE_DIRECTORY}/agent.epoch" && ! -e "${STATE_DIRECTORY}/agent.epoch.lock" ]] \
    || die "existing Agent state was found; refusing to alter it"
  [[ ! -e "${AGENT_BINARY}" ]] || die "${AGENT_BINARY} already exists; refusing to overwrite it"

  local server_url="${1:-}" enrollment credentials agent_id agent_token insecure_option
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
  create_service_user
  prepare_directories
  if [[ ! -e "${INSTALL_HELPER}" ]]; then
    track_install_path "${INSTALL_HELPER}"
  fi
  install_local_helper
  track_install_path "${AGENT_BINARY}"
  download_binary agent "${AGENT_BINARY}"
  track_install_path "${CONFIG_DIRECTORY}/agent.env"
  write_private_file "${CONFIG_DIRECTORY}/agent.env" "PROBE_404_SERVER=${server_url}
PROBE_404_AGENT_ID=${agent_id}
PROBE_404_TOKEN=${agent_token}
PROBE_404_STATE=${STATE_DIRECTORY}/agent.epoch
PROBE_404_SECURITY_EXPORT=${SECURITY_EXPORT_DIRECTORY}
PROBE_404_SECURITY_ACKS=${STATE_DIRECTORY}/agent.security-acks.json"

  insecure_option=""
  if [[ "${server_url}" == http://* ]]; then
    insecure_option=" --allow-insecure-http"
  fi
  track_install_path "${AGENT_UNIT}"
  track_install_path "${AGENT_UPDATER_UNIT}"
	track_install_path "${SECURITY_SERVICE_UNIT}"
	track_install_path "${SECURITY_TIMER_UNIT}"
  [[ -d "${AGENT_UPDATER_STATE}" ]] || INSTALL_TRANSACTION_UPDATER_STATE_CREATED=1
  [[ -d "${SECURITY_STATE_DIRECTORY}" ]] || INSTALL_TRANSACTION_SECURITY_STATE_CREATED=1
  INSTALL_TRANSACTION_UNIT="404-probe-agent.service"
  render_agent_unit "${insecure_option}" >"${AGENT_UNIT}"
  chmod 0644 "${AGENT_UNIT}"
  install_agent_updater_unit
	install_security_collector
  systemctl daemon-reload
	systemctl start 404-probe-security-collect.service
	systemctl enable --now 404-probe-security-collect.timer
  systemctl enable --now 404-probe-agent-updater.service
  wait_for_service 404-probe-agent-updater.service
  systemctl enable --now 404-probe-agent.service
  wait_for_service 404-probe-agent.service

  verify_agent_authentication "${server_url}" "${agent_token}"
  unset agent_token
  commit_install_transaction

  note "404-probe Agent is installed, running, and authenticated to the Server."
  systemctl --no-pager --full status 404-probe-agent.service || true
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
  [[ -x "${SERVER_BINARY}" && -f "${SERVER_DATABASE}" && -f "${SERVER_UNIT}" ]] \
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

uninstall_role() {
  require_root_linux_systemd
  [[ $# -eq 1 && ( "$1" == "server" || "$1" == "agent" ) ]] \
    || die "usage: 404-probe-install uninstall <server|agent>"
  local role="$1"
  local unit="404-probe-${role}.service"
  local unit_path binary_path
  if [[ "${role}" == "server" ]]; then
    unit_path="${SERVER_UNIT}"
    binary_path="${SERVER_BINARY}"
  else
    unit_path="${AGENT_UNIT}"
    binary_path="${AGENT_BINARY}"
  fi
  if [[ "${role}" == "agent" ]]; then
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
  rm -f -- "${unit_path}" "${binary_path}"
  if [[ "${role}" == "agent" ]]; then
    rm -f -- "${CONFIG_DIRECTORY}/agent.env" \
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" "${STATE_DIRECTORY}/agent.security-acks.json" \
      "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
	  "${SECURITY_SERVICE_UNIT}" "${SECURITY_TIMER_UNIT}" \
      "/usr/local/bin/.404-probe-agent.bootstrap" "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
    rm -rf -- "${AGENT_UPDATER_STATE}"
    rm -rf -- "${SECURITY_STATE_DIRECTORY}"
  fi
  systemctl daemon-reload
  note "Uninstalled ${role}. Re-running this command is safe."
  if [[ "${role}" == "agent" ]]; then
    printf 'Removed the Agent unit, binary, private environment, and epoch/state files.\n'
  else
    printf 'Preserved Server configuration, credentials, and database.\n'
  fi
  printf 'Preserved systemd journal history, service user, and installer helper.\n'
}

json_string_field() {
  local json="$1" field="$2"
  printf '%s\n' "${json}" | sed -n "s/.*\"${field}\":\"\([^\"]*\)\".*/\1/p"
}

compare_versions() {
  local left="$1" right="$2" lmajor lminor lpatch rmajor rminor rpatch
  canonical_version "${left}" && canonical_version "${right}" || return 2
  IFS=. read -r lmajor lminor lpatch <<<"${left#v}"
  IFS=. read -r rmajor rminor rpatch <<<"${right#v}"
  for pair in "${lmajor}:${rmajor}" "${lminor}:${rminor}" "${lpatch}:${rpatch}"; do
    if (( 10#${pair%%:*} < 10#${pair#*:} )); then printf '%s\n' -1; return; fi
    if (( 10#${pair%%:*} > 10#${pair#*:} )); then printf '%s\n' 1; return; fi
  done
  printf '%s\n' 0
}

download_server_upgrade_candidate() (
  local target="$1" architecture asset release_base temporary_directory checksum_line version_json candidate_version candidate_commit release_json release_commit
  architecture="$(detect_architecture)"
  asset="404-probe-server-linux-${architecture}"
  release_base="https://github.com/${REPOSITORY}/releases/download/${target}"
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
      curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
        --output "${temporary_directory}/${name}" "${release_base}/${name}"
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
    canonical_version "${version}" && [[ "${version_json}" == *'"dirty":false'* ]] \
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
  local phase active enabled target restore_temporary helper_restore_temporary helper_state legacy_backup=0
  [[ -f "${SERVER_UPGRADE_STATE}" && ! -L "${SERVER_UPGRADE_STATE}" ]] || return 1
  phase="$(server_upgrade_state_value phase)" || return 1
  active="$(server_upgrade_state_value original_active)" || return 1
  enabled="$(server_upgrade_state_value original_enabled)" || return 1
  target="$(server_upgrade_state_value target)" || return 1
  [[ "${active}" =~ ^[01]$ && "${enabled}" =~ ^[01]$ ]] || return 1
  canonical_version "${target}" || return 1
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
Install 404-probe V0.9.2

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
  if [[ -e "${SERVER_UNIT}" || -e "${SERVER_BINARY}" || -e "${SERVER_DATABASE}" || -e "${CONFIG_DIRECTORY}/server.env" ]]; then
    upgrade_existing_server
    return
  fi
  if [[ -e "${AGENT_UNIT}" || -e "${AGENT_BINARY}" || -e "${CONFIG_DIRECTORY}/agent.env" ]]; then
    [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" \
      && -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
      || die "existing Agent installation is incomplete or unsupported; no files were changed"
    note "Existing 404-probe Agent detected. Agent upgrades remain controlled by the Server Web interface."
    printf 'No Agent identity, credential, epoch, service, or security state was changed.\n'
    printf 'If this Agent predates the local security collector, run: sudo 404-probe-install setup-security\n'
    return
  fi
  interactive_install
}

main() {
  case "${1:-}" in -h|--help|help) usage; return ;; esac
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
