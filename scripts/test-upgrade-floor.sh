#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
set -Eeuo pipefail
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
source <(sed '$d' "${INSTALLER_FIXTURE_INSTALLER}")
mkdir -p "$(dirname "${SERVER_BINARY}")"

# Real product transaction must reject before creating locks, candidates or backups.
for identity in v1.0.8 unknown; do
  if [[ "${identity}" == unknown ]]; then
    printf '#!/bin/bash\nexit 1\n' >"${SERVER_BINARY}"
  else
    printf '#!/bin/bash\nprintf '\''{"version":"%s","dirty":false}\n'\''\n' "${identity}" >"${SERVER_BINARY}"
  fi
  chmod 0755 "${SERVER_BINARY}"
  before="$(find "$(dirname "${SERVER_BINARY}")" -type f -exec sha256sum {} +)"
  if upgrade_existing_server >"${temporary}/rejection" 2>&1; then
    printf 'FAIL accepted %s\n' "${identity}"; exit 1
  fi
  grep -Fq 'no files were changed' "${temporary}/rejection"
  [[ ! -e "${SERVER_UPGRADE_LOCK_DIRECTORY}" && ! -e "${SERVER_UPGRADE_DIRECTORY}" ]]
  [[ "${before}" == "$(find "$(dirname "${SERVER_BINARY}")" -type f -exec sha256sum {} +)" ]]
  printf 'PASS Server upgrade %s rejected without file changes\n' "${identity}"
done
for identity in v1.0.9 v1.0.10; do
  require_supported_server_version "${identity}"
  printf 'PASS Server upgrade %s passes support floor\n' "${identity}"
done
