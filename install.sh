#!/usr/bin/env bash

set -Eeuo pipefail

readonly REPOSITORY="404-git-404/404-probe"
readonly DEFAULT_VERSION="v0.8.0"
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
readonly SERVICE_USER="404-probe"
readonly ENROLLMENT_PREFIX="404p1_"

INSTALL_TRANSACTION_ACTIVE=0
INSTALL_TRANSACTION_ROLE=""
INSTALL_TRANSACTION_UNIT=""
INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED=0
INSTALL_TRANSACTION_USER_CREATED=0
INSTALL_TRANSACTION_UPDATER_STATE_CREATED=0
INSTALL_TRANSACTION_PATHS=()

die() {
  printf '404-probe installer: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '\n%s\n' "$*"
}

usage() {
  cat <<'EOF'
Usage:
  404-probe-install                 interactive Server/Agent installation
  404-probe-install agent --server <origin>
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
  for command_name in awk base64 cmp curl getent install mktemp runuser sha256sum stat; do
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
    rm -f -- "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock"
  fi
  for (( index=${#INSTALL_TRANSACTION_PATHS[@]}-1; index>=0; index-- )); do
    rm -f -- "${INSTALL_TRANSACTION_PATHS[index]}"
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  (( INSTALL_TRANSACTION_CONFIG_DIRECTORY_CREATED == 0 )) || rmdir -- "${CONFIG_DIRECTORY}" 2>/dev/null || true
  (( INSTALL_TRANSACTION_STATE_DIRECTORY_CREATED == 0 )) || rmdir -- "${STATE_DIRECTORY}" 2>/dev/null || true
  (( INSTALL_TRANSACTION_UPDATER_STATE_CREATED == 0 )) || rmdir -- "${AGENT_UPDATER_STATE}" 2>/dev/null || true
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
  version="${PROBE_404_VERSION:-${DEFAULT_VERSION}}"
  asset="404-probe-${role}-linux-${architecture}"
  release_base="${PROBE_404_RELEASE_BASE_URL:-https://github.com/${REPOSITORY}/releases/download/${version}}"
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
readonly CONFIG_DIRECTORY="/etc/404-probe"
readonly STATE_DIRECTORY="/var/lib/404-probe"
readonly SERVICE_USER="404-probe"
readonly ENROLLMENT_PREFIX="404p1_"

die() { printf '404-probe helper: %s\n' "$*" >&2; exit 1; }
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
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" \
	  "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
	  "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
	rm -rf -- "${AGENT_UPDATER_STATE}"
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

require_root
case "${1:-}" in
  enroll) shift; enroll "$@" ;;
  uninstall) shift; uninstall "$@" ;;
  *) printf 'Usage: 404-probe-install enroll <name> | uninstall <server|agent>\n' >&2; exit 2 ;;
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
      http://127.0.0.1:8080/api/v1/agent/jobs/claim || true)"
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

  local web_origin web_password_hash control_token insecure_option
  web_origin="$(prompt_web_origin)"
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

valid_sing_box_clash_api_url() {
  local value="$1"
  [[ "${value}" =~ ^https?://(127\.0\.0\.1|localhost)(:[0-9]{1,5})?/?$ \
    || "${value}" =~ ^https?://\[::1\](:[0-9]{1,5})?/?$ ]]
}

quote_environment_value() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "${value}"
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
Restart=on-failure
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
RuntimeDirectoryMode=0750
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

[Install]
WantedBy=multi-user.target
EOF
}

bootstrap_existing_agent() (
  local candidate="/usr/local/bin/.404-probe-agent.bootstrap"
  local previous="/usr/local/bin/.404-probe-agent.previous"
  local backup_directory server_url agent_token insecure_option rollback_active=0
  [[ -f "${AGENT_UNIT}" && ! -L "${AGENT_UNIT}" && -f "${AGENT_BINARY}" && ! -L "${AGENT_BINARY}" && -f "${CONFIG_DIRECTORY}/agent.env" && ! -L "${CONFIG_DIRECTORY}/agent.env" ]] \
    || die "existing Agent installation is incomplete or unsafe; no files were changed"
  [[ "$(stat -c '%U:%G:%a' "${AGENT_UNIT}")" == "root:root:644" ]] \
    || die "existing Agent unit must be root:root mode 0644"
  [[ "$(stat -c '%U:%G:%a' "${AGENT_BINARY}")" == "root:root:755" ]] \
    || die "existing Agent binary must be root:root mode 0755"
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
  trap 'status=$?; if (( rollback_active != 0 )); then systemctl stop 404-probe-agent.service >/dev/null 2>&1 || true; systemctl stop 404-probe-agent-updater.service >/dev/null 2>&1 || true; systemctl disable 404-probe-agent-updater.service >/dev/null 2>&1 || true; rm -f -- "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}"; rm -rf -- "${AGENT_UPDATER_STATE}"; if [[ -f "${previous}" && ! -L "${previous}" ]]; then mv -f -- "${previous}" "${AGENT_BINARY}"; fi; cp --preserve=mode,ownership,timestamps -- "${backup_directory}/agent.service" "${AGENT_UNIT}"; systemctl daemon-reload >/dev/null 2>&1 || true; systemctl restart 404-probe-agent.service >/dev/null 2>&1 || true; fi; rm -f -- "${candidate}"; rm -rf -- "${backup_directory}"; exit "${status}"' EXIT HUP INT TERM
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
  local clash_api clash_secret clash_environment
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

  printf 'Local sing-box Clash API URL (optional, for example http://127.0.0.1:9090): ' >/dev/tty
  IFS= read -r clash_api </dev/tty
  clash_environment=""
  if [[ -n "${clash_api}" ]]; then
    valid_sing_box_clash_api_url "${clash_api}" \
      || die "sing-box Clash API URL must be an HTTP(S) loopback origin without credentials, path, query, or fragment"
    clash_api="${clash_api%/}"
    printf 'sing-box Clash API secret (optional, hidden): ' >/dev/tty
    IFS= read -r -s clash_secret </dev/tty
    printf '\n' >/dev/tty
    clash_environment=$'\n'"PROBE_404_SING_BOX_CLASH_API=$(quote_environment_value "${clash_api}")"
    if [[ -n "${clash_secret}" ]]; then
      clash_environment+=$'\n'"PROBE_404_SING_BOX_CLASH_SECRET=$(quote_environment_value "${clash_secret}")"
    fi
  fi

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
PROBE_404_STATE=${STATE_DIRECTORY}/agent.epoch${clash_environment}"
  unset clash_secret clash_environment

  insecure_option=""
  if [[ "${server_url}" == http://* ]]; then
    insecure_option=" --allow-insecure-http"
  fi
  track_install_path "${AGENT_UNIT}"
  track_install_path "${AGENT_UPDATER_UNIT}"
  [[ -d "${AGENT_UPDATER_STATE}" ]] || INSTALL_TRANSACTION_UPDATER_STATE_CREATED=1
  INSTALL_TRANSACTION_UNIT="404-probe-agent.service"
  render_agent_unit "${insecure_option}" >"${AGENT_UNIT}"
  chmod 0644 "${AGENT_UNIT}"
  install_agent_updater_unit
  systemctl daemon-reload
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
      "${STATE_DIRECTORY}/agent.epoch" "${STATE_DIRECTORY}/agent.epoch.lock" \
      "${AGENT_UPDATER_UNIT}" "${AGENT_UPDATER_SOCKET}" \
      "/usr/local/bin/.404-probe-agent.bootstrap" "/usr/local/bin/.404-probe-agent.candidate" "/usr/local/bin/.404-probe-agent.previous"
    rm -rf -- "${AGENT_UPDATER_STATE}"
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

interactive_install() {
  require_root_linux_systemd
  local choice
  cat >/dev/tty <<'EOF'
Install 404-probe V0.8.0

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

main() {
  case "${1:-}" in
    agent)
      shift
      install_agent_command "$@"
      ;;
    enroll)
      shift
      enroll_agent "$@"
      ;;
    uninstall)
      shift
      uninstall_role "$@"
      ;;
    -h|--help|help)
      usage
      ;;
    "")
      interactive_install
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
