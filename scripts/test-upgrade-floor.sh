#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
set -Eeuo pipefail
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
source <(sed '$d' "${INSTALLER_FIXTURE_INSTALLER}")
# The default fixture masks /etc/passwd and /etc/group. Preserve observed
# numeric ownership/mode, translating UID/GID 0 to their production names.
stat() {
  if [[ "$1" == -c && "$2" == '%U:%G:%a' ]]; then
    local observed
    observed="$(command stat -c '%u:%g:%a' "$3")" || return
    if [[ "${observed}" == 0:0:* ]]; then
      printf 'root:root:%s\n' "${observed##*:}"
    else
      printf '%s\n' "${observed}"
    fi
  else
    command stat "$@"
  fi
}
# Account names are hidden in this fixture; files are already owned by UID/GID0.
install() {
  local args=()
  while (($#)); do
    case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
  done
  command install "${args[@]}"
}
mkdir -p "$(dirname "${SERVER_BINARY}")" "$(dirname "${SERVER_UNIT}")" "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" "$(dirname "${INSTALL_HELPER}")"
printf original-unit >"${SERVER_UNIT}"
printf original-configuration >"${CONFIG_DIRECTORY}/server.env"
printf original-database >"${SERVER_DATABASE}"
printf original-helper >"${INSTALL_HELPER}"
managed="${INSTALLER_FIXTURE_ROOT}/system"

# Recovery remains reachable even when the interrupted executable is unusable/old.
for identity in unknown v1.0.8; do
  if [[ "${identity}" == unknown ]]; then
    printf '#!/bin/bash\nexit 1\n' >"${SERVER_BINARY}"
  else
    printf '#!/bin/bash\nprintf '\''{"version":"%s","dirty":false}\n'\''\n' "${identity}" >"${SERVER_BINARY}"
  fi
  chmod 0755 "${SERVER_BINARY}"
  mkdir -p "${SERVER_UPGRADE_DIRECTORY}"
  chmod 0700 "${SERVER_UPGRADE_DIRECTORY}"
  printf pending >"${SERVER_UPGRADE_STATE}"
  rm -f -- "${temporary}/restored"
  if (
    restore_server_upgrade() { printf restored >"${temporary}/restored"; }
    upgrade_existing_server
  ) >"${temporary}/recovery" 2>&1; then
    printf 'FAIL recovery must request a fresh installer run\n'; exit 1
  fi
  [[ -f "${temporary}/restored" ]]
  grep -Fq 'interrupted upgrade was recovered' "${temporary}/recovery"
  rm -rf -- "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_LOCK_DIRECTORY}"
  printf 'PASS interrupted %s Server enters recovery before version rejection\n' "${identity}"
done

# Real product transaction must reject before creating locks, candidates or backups.
for identity in v1.0.8 unknown; do
  if [[ "${identity}" == unknown ]]; then
    printf '#!/bin/bash\nexit 1\n' >"${SERVER_BINARY}"
  else
    printf '#!/bin/bash\nprintf '\''{"version":"%s","dirty":false}\n'\''\n' "${identity}" >"${SERVER_BINARY}"
  fi
  chmod 0755 "${SERVER_BINARY}"
  before="$(find "${managed}" -type f -exec sha256sum {} + | LC_ALL=C sort)"
  if upgrade_existing_server >"${temporary}/rejection" 2>&1; then
    printf 'FAIL accepted %s\n' "${identity}"; exit 1
  fi
  grep -Fq 'no files were changed' "${temporary}/rejection"
  if [[ "${identity}" == unknown ]]; then
    grep -Fq server_version_unrecognized "${temporary}/rejection"
  else
    grep -Fq server_version_too_old "${temporary}/rejection"
  fi
  [[ ! -e "${SERVER_UPGRADE_LOCK_DIRECTORY}" && ! -e "${SERVER_UPGRADE_DIRECTORY}" ]]
  [[ "${before}" == "$(find "${managed}" -type f -exec sha256sum {} + | LC_ALL=C sort)" ]]
  printf 'PASS Server upgrade %s rejected without file changes\n' "${identity}"
done
# The real identify function runs before and after locking. Stop only at download.
for identity in v1.0.9 v1.0.10; do
  cat >"${SERVER_BINARY}" <<EOF
#!/bin/bash
[[ "\$1" == version && "\$2" == --json ]] || exit 99
printf identified'\n' >>"${temporary}/identify.log"
printf '%s\n' '{"version":"${identity}","dirty":false}'
EOF
  chmod 0755 "${SERVER_BINARY}"
  chmod 0755 "${INSTALL_HELPER}"
  printf '[Service]\nUser=%s\nExecStart=%s serve --listen 127.0.0.1:8080 --db %s\n' "${SERVICE_USER}" "${SERVER_BINARY}" "${SERVER_DATABASE}" >"${SERVER_UNIT}"
  : >"${temporary}/identify.log"
  status=0
  (
    # Simulate another installer completing immediately before this lock.
    flock() {
      command flock "$@" || return
      if [[ "${identity}" == v1.0.9 ]]; then sed -i 's/v1.0.9/v1.0.10/' "${SERVER_BINARY}"; fi
    }
    download_server_upgrade_candidate() { printf '%s\n' "${current}" >"${temporary}/download-current"; exit 88; }
    upgrade_existing_server
  ) >"${temporary}/allowed" 2>&1 || status=$?
  [[ "${status}" == 88 && "$(<"${temporary}/download-current")" == v1.0.10 ]]
  [[ "$(wc -l <"${temporary}/identify.log")" == 2 ]]
  rm -rf -- "${SERVER_UPGRADE_DIRECTORY}" "${SERVER_UPGRADE_LOCK_DIRECTORY}"
  rm -f -- "${temporary}/download-current"
  printf 'PASS Server %s reaches download with locked identity re-read\n' "${identity}"
done

# Untrusted JSON identities use the same stable, actionable error code.
for identity in '{"version":"dev","dirty":false}' '{"version":"v1.0.9","dirty":true}'; do
  printf '#!/bin/bash\nprintf '\''%%s\n'\'' '\''%s'\''\n' "${identity}" >"${SERVER_BINARY}"
  chmod 0755 "${SERVER_BINARY}"
  if upgrade_existing_server >"${temporary}/identity-rejection" 2>&1; then exit 1; fi
  grep -Fq server_version_unrecognized "${temporary}/identity-rejection"
  grep -Fq 'PROBE_404_VERSION=v1.0.9' "${temporary}/identity-rejection"
  grep -Fq 'no files were changed' "${temporary}/identity-rejection"
done
printf 'PASS untrusted identities have stable code and pinned installer guidance\n'

mkdir -p "${SERVER_UPGRADE_DIRECTORY}"
chmod 0700 "${SERVER_UPGRADE_DIRECTORY}"
printf pending >"${SERVER_UPGRADE_STATE}"
if ( restore_server_upgrade() { return 1; }; upgrade_existing_server ) >"${temporary}/old-recovery" 2>&1; then exit 1; fi
grep -Fq 'original installer' "${temporary}/old-recovery"
[[ -f "${SERVER_UPGRADE_STATE}" ]]
printf 'PASS unsupported old recovery retains state and names the original installer\n'
