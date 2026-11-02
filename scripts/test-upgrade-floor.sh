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
mkdir -p "$(dirname "${SERVER_BINARY}")" "$(dirname "${SERVER_UNIT}")" "${CONFIG_DIRECTORY}" "${STATE_DIRECTORY}" "$(dirname "${INSTALL_HELPER}")"
printf original-unit >"${SERVER_UNIT}"
printf original-configuration >"${CONFIG_DIRECTORY}/server.env"
printf original-database >"${SERVER_DATABASE}"
printf original-helper >"${INSTALL_HELPER}"
managed="${INSTALLER_FIXTURE_ROOT}/system"

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
for identity in v1.0.9 v1.0.10; do
  require_supported_server_version "${identity}"
  printf 'PASS Server upgrade %s passes support floor\n' "${identity}"
done
