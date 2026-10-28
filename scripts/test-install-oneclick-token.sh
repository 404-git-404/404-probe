#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
# Tests `404-probe-install oneclick-token` (test machines only) with a stub for the Server's CLI.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(tail -n 1 "${INSTALLER_FIXTURE_INSTALLER}")" == 'main "$@"' ]]
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
mkdir -p "${temporary}/bin" "${temporary}/units" "${temporary}/home" "${temporary}/db"
: >"${temporary}/db/404-probe.db"; : >"${temporary}/units/404-probe-server.service"
printf '#!/bin/sh\nexit 0\n' >"${temporary}/bin/404-probe-server"; chmod 755 "${temporary}/bin/404-probe-server"

# The installer with the Server's fixed paths moved under a temporary directory.
sed '$d' "${INSTALLER_FIXTURE_INSTALLER}" \
  | sed -e "s#/usr/local/bin/404-probe-server#${temporary}/bin/404-probe-server#g" \
        -e "s#^readonly SERVER_DATABASE=.*#readonly SERVER_DATABASE=\"${temporary}/db/404-probe.db\"#" \
        -e "s#/etc/systemd/system#${temporary}/units#g" >"${temporary}/installer.sh"
source "${temporary}/installer.sh"
chown() { return 0; }

# Stub for the Server CLI: counts calls, refuses the 11th pending token like the real limit does.
COUNT_FILE="${temporary}/count"; printf '0' >"${COUNT_FILE}"
LAST_NAME_FILE="${temporary}/last-name"
run_server_enroll_token() {
  local n; n="$(cat "${COUNT_FILE}")"
  if (( n >= 10 )); then printf 'there are already 10 unused install commands; use or revoke one, or wait for them to expire\n' >&2; return 1; fi
  printf '%s' "$((n + 1))" >"${COUNT_FILE}"
  printf '%s' "$1" >"${LAST_NAME_FILE}"
  printf 'Token: %s\nExpires: 2026-10-18T12:00:00Z\nEnrollment ID: %032d\n' "${FAKE_TOKEN}" "${n}"
}
FAKE_TOKEN="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"
[[ "${#FAKE_TOKEN}" -eq 43 ]]

attempt() { status=0; ( "$@" ) >"${temporary}/out" 2>"${temporary}/err" || status=$?; output="$(cat "${temporary}/out")"; error="$(cat "${temporary}/err")"; }

# 1. Default path: a root-only file named after the Agent; the terminal shows path, name and expiry, never the token.
SANDBOX_HOME_REWRITE="${temporary}/home"
oneclick_token_path() { printf '%s/oneclick-token-%s.token' "${temporary}/home" "$1"; }
attempt oneclick_token test-agent-de --output "$(oneclick_token_path test-agent-de)"
[[ "${status}" -eq 0 ]]
file="$(oneclick_token_path test-agent-de)"
[[ -f "${file}" && "$(cat "${file}")" == "${FAKE_TOKEN}" ]]
: >"${temporary}/modecheck"; chmod 600 "${temporary}/modecheck"
if [[ "$(stat -c '%a' "${temporary}/modecheck")" == 600 ]]; then # (Git Bash on NTFS cannot represent modes)
  [[ "$(stat -c '%a' "${file}")" == 600 ]] || { printf 'mode is %s\n' "$(stat -c '%a' "${file}")" >&2; exit 1; }
fi
declare -f oneclick_token | grep -Fq 'umask 077' && declare -f oneclick_token | grep -Fq 'chmod 600'
[[ "${output}" == *"Token file: ${file}"* && "${output}" == *"Name: test-agent-de"* && "${output}" == *"Expires: 2026-10-18T12:00:00Z"* ]]
[[ "${output}${error}" != *"${FAKE_TOKEN}"* ]] || { printf 'the token reached the terminal\n' >&2; exit 1; }
[[ "$(cat "${LAST_NAME_FILE}")" == test-agent-de ]]
printf 'PASS the token goes to a 600 file and never to the terminal\n'

# 2. The default file name is derived from the Agent name (unsafe characters replaced) under /root.
default_line="$(declare -f oneclick_token | grep -F 'output_path="/root/oneclick-token-${sanitized}.token"')"
[[ -n "${default_line}" ]]
[[ "$(printf '%s' 'web 1/東京' | tr -c 'A-Za-z0-9._-' '_')" != */* ]]
printf 'PASS the default path is /root/oneclick-token-<name>.token\n'

# 3. Existing files are never overwritten, relative paths and bad names are refused, no token is created.
before="$(cat "${COUNT_FILE}")"
attempt oneclick_token test-agent-de --output "${file}"
[[ "${status}" -ne 0 && "${error}" == *"already exists"* && "$(cat "${file}")" == "${FAKE_TOKEN}" && "$(cat "${COUNT_FILE}")" == "${before}" ]]
attempt oneclick_token x --output relative/path
[[ "${status}" -ne 0 && "${error}" == *"absolute"* && "$(cat "${COUNT_FILE}")" == "${before}" ]]
attempt oneclick_token --output "${temporary}/home/none/dir/file" x
[[ "${status}" -ne 0 && "${error}" == *"directory"* ]]
for bad in "" " lead" "trail " $'tab\tname' $'new\nline'; do
  attempt oneclick_token "${bad}" --output "${temporary}/home/bad.token"
  [[ "${status}" -ne 0 && ! -e "${temporary}/home/bad.token" && "$(cat "${COUNT_FILE}")" == "${before}" ]] || { printf 'bad name %q accepted\n' "${bad}" >&2; exit 1; }
done
attempt oneclick_token "$(printf 'n%.0s' $(seq 1 129))" --output "${temporary}/home/long.token"
[[ "${status}" -ne 0 && ! -e "${temporary}/home/long.token" ]]
attempt oneclick_token one two
[[ "${status}" -ne 0 && "${error}" == *usage* ]]
attempt oneclick_token --unknown x
[[ "${status}" -ne 0 && "${error}" == *usage* ]]
printf 'PASS overwrites, relative paths and bad names are refused before any token is made\n'

# 4. The pending limit applies: the 11th token fails with the Server's message and leaves no file.
printf '9' >"${COUNT_FILE}"
attempt oneclick_token tenth --output "${temporary}/home/tenth.token"
[[ "${status}" -eq 0 && -f "${temporary}/home/tenth.token" ]]
attempt oneclick_token eleventh --output "${temporary}/home/eleventh.token"
[[ "${status}" -ne 0 && "${error}" == *"already 10 unused install commands"* && ! -e "${temporary}/home/eleventh.token" ]]
printf 'PASS the command counts in the 10-token limit\n'

# 5. A malformed answer from the Server is rejected and no file is written.
printf '0' >"${COUNT_FILE}"
run_server_enroll_token() { printf 'Token: short\nExpires: 2026-10-18T12:00:00Z\n'; }
attempt oneclick_token garbled --output "${temporary}/home/garbled.token"
[[ "${status}" -ne 0 && ! -e "${temporary}/home/garbled.token" && "${output}${error}" != *short* ]]
printf 'PASS an unexpected Server answer writes nothing\n'

# 6. Both entry points have the command and the usage mentions it.
grep -q 'oneclick-token) shift; oneclick_token' "${INSTALLER_FIXTURE_INSTALLER}"
grep -q 'oneclick-token)$' "${INSTALLER_FIXTURE_INSTALLER}"
grep -Fq 'oneclick-token <name> [--output' "${INSTALLER_FIXTURE_INSTALLER}"
[[ "$(grep -c '^oneclick_token() {' "${INSTALLER_FIXTURE_INSTALLER}")" == 2 ]]
printf 'PASS the command is available from the installed helper and from install.sh\n'
