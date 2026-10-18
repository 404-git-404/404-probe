#!/usr/bin/env bash
# Tests the installer's non-interactive (one-click) Agent mode with stub commands; nothing is installed.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(tail -n 1 "${root}/install.sh")" == 'main "$@"' ]]
source <(sed '$d' "${root}/install.sh")
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
export STUB_DIR="${temporary}"

# The interactive Agent prompts must be unchanged (checked on the real function before any stubbing).
declare -f install_agent | grep -Fq "Enrollment value: "
declare -f install_agent | grep -Eq 'read -r -s enrollment < /dev/tty'
printf 'PASS the interactive Agent prompts are unchanged
'

token="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"
[[ "${#token}" -eq 43 ]]
agent_id="0123456789abcdef0123456789abcdef"
agent_token="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"

# curl stub: records its arguments and stdin; answers from ${STUB_DIR}/response (body, then status line).
curl() {
  printf '%s\n' "$*" >>"${STUB_DIR}/curl.argv"
  cat >"${STUB_DIR}/curl.stdin"
  printf '%s' "$(cat "${STUB_DIR}/response")"
}
respond() { printf '%s\n%s' "$2" "$1" >"${STUB_DIR}/response"; } # status body
reset() { rm -f "${temporary}"/curl.* "${temporary}/response" "${temporary}/out"; unset PROBE_404_ROLE PROBE_404_SERVER PROBE_404_ENROLL_TOKEN PROBE_404_ENROLL_TOKEN_FILE PROBE_404_AGENT_NAME; ONECLICK_SERVER=""; ONECLICK_TOKEN=""; ONECLICK_NAME=""; }
success="{\"agent_id\":\"${agent_id}\",\"agent_token\":\"${agent_token}\",\"name\":\"x\"}"
set_complete() {
  export PROBE_404_ROLE=agent PROBE_404_SERVER=https://probe.example.com PROBE_404_ENROLL_TOKEN="${token}" PROBE_404_AGENT_NAME="${1:-web 1}"
}
# Runs a command in a subshell; sets status and captured output.
attempt() { status=0; ( "$@" ) >"${temporary}/out" 2>&1 || status=$?; output="$(cat "${temporary}/out")"; }

# 1. No variables: interactive install is untouched.
reset
if oneclick_agent_requested; then printf 'one-click mode started without variables\n' >&2; exit 1; fi
printf 'PASS no variables keeps the interactive flow\n'

# 2. Partial variables fail and list what is missing; they never fall back to interactive mode.
reset; export PROBE_404_ROLE=agent
attempt oneclick_agent_requested
[[ "${status}" -ne 0 && "${output}" == *PROBE_404_SERVER* && "${output}" == *PROBE_404_ENROLL_TOKEN* && "${output}" == *PROBE_404_AGENT_NAME* ]]
reset; export PROBE_404_ROLE=agent PROBE_404_SERVER=https://probe.example.com
attempt oneclick_agent_requested
[[ "${status}" -ne 0 && "${output}" != *"missing or invalid: PROBE_404_SERVER"* && "${output}" == *PROBE_404_ENROLL_TOKEN* ]]
reset; export PROBE_404_SERVER=https://probe.example.com PROBE_404_ENROLL_TOKEN="${token}" PROBE_404_AGENT_NAME=x
attempt oneclick_agent_requested
[[ "${status}" -ne 0 && "${output}" == *PROBE_404_ROLE* && "${output}" != *"${token}"* ]]
printf 'PASS partial variables are rejected with the missing names\n'

# 3. Token and token file together are refused; the server role is refused.
reset; set_complete; export PROBE_404_ENROLL_TOKEN_FILE="${temporary}/token"
attempt oneclick_agent_requested
[[ "${status}" -ne 0 && "${output}" == *"only one of"* && "${output}" != *"${token}"* ]]
reset; set_complete; export PROBE_404_ROLE=server
attempt oneclick_agent_requested
[[ "${status}" -ne 0 && "${output}" == *"Server"* && "${output}" == *interactive* ]]
printf 'PASS token+file together and the Server role are refused\n'

# 4. Complete variables: accepted without reading /dev/tty; the token leaves the environment.
reset; set_complete
oneclick_agent_requested
for function_name in oneclick_variables_present oneclick_agent_requested oneclick_load_inputs oneclick_redeem_token install_agent_oneclick; do
  ! declare -f "${function_name}" | grep -q '/dev/tty' || { printf '%s touches /dev/tty\n' "${function_name}" >&2; exit 1; }
done
oneclick_load_inputs
[[ -z "${PROBE_404_ENROLL_TOKEN+x}" && -z "${PROBE_404_ENROLL_TOKEN_FILE+x}" ]]
[[ "${ONECLICK_NAME}" == 'web 1' && "${ONECLICK_SERVER}" == https://probe.example.com ]]
! env | grep -Fq -- "${token}"
printf 'PASS complete variables are loaded without a terminal and the token leaves the environment\n'

# 5. Redeeming sends the token only on stdin and prints only the credentials.
respond 200 "${success}"
credentials="$(oneclick_redeem_token)"
[[ "${credentials}" == "${agent_id}"$'\n'"${agent_token}" ]]
grep -Fq -- "${token}" "${temporary}/curl.stdin"
! grep -Fq -- "${token}" "${temporary}/curl.argv"
grep -Fq 'https://probe.example.com/api/v1/agent/enroll' "${temporary}/curl.argv"
printf 'PASS the token goes to the Server on stdin, never in argv\n'

# 6. The name is JSON-escaped (quotes, backslashes, spaces, Chinese).
if command -v node >/dev/null 2>&1; then parse() { node -e 'let s="";process.stdin.on("data",c=>s+=c).on("end",()=>process.stdout.write(JSON.parse(s).name))'; }
else parse() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["name"], end="")'; }; fi
for name in 'plain' 'with space' 'say "hi"' 'back\slash' "it's" '$HOME `id`' '东京 节点 #1' 'a\"b'; do
  reset; set_complete "${name}"
  oneclick_agent_requested; oneclick_load_inputs
  respond 200 "${success}"
  oneclick_redeem_token >/dev/null
  [[ "$(parse <"${temporary}/curl.stdin")" == "${name}" ]] || { printf 'name %q was not escaped correctly: %s\n' "${name}" "$(cat "${temporary}/curl.stdin")" >&2; exit 1; }
done
printf 'PASS agent names survive JSON escaping\n'

# 7. Server refusals become clear messages and never echo the token.
for pair in 'enrollment_used|already used' 'enrollment_expired|expired' 'enrollment_revoked|revoked' 'enrollment_invalid|does not recognise' 'enrollment_name_mismatch|does not match'; do
  code="${pair%%|*}"; phrase="${pair#*|}"
  reset; set_complete; oneclick_agent_requested; oneclick_load_inputs
  respond 409 "{\"error\":{\"code\":\"${code}\",\"message\":\"x\"}}"
  attempt oneclick_redeem_token
  [[ "${status}" -ne 0 && "${output}" == *"${phrase}"* && "${output}" != *"${token}"* ]] || { printf '%s: %s\n' "${code}" "${output}" >&2; exit 1; }
done
printf 'PASS used, expired, revoked, unknown and mismatched tokens give clear errors\n'

# 8. A token file is read, trailing newline stripped, and symlinks or bad content are refused.
reset; set_complete; unset PROBE_404_ENROLL_TOKEN
printf '%s\n' "${token}" >"${temporary}/token"; export PROBE_404_ENROLL_TOKEN_FILE="${temporary}/token"
oneclick_agent_requested; oneclick_load_inputs
[[ "${ONECLICK_TOKEN}" == "${token}" && -z "${PROBE_404_ENROLL_TOKEN_FILE+x}" ]]
reset; set_complete; unset PROBE_404_ENROLL_TOKEN
ln -s "${temporary}/token" "${temporary}/link" 2>/dev/null || true
if [[ -L "${temporary}/link" ]]; then # (Git Bash on Windows cannot create real symlinks)
  export PROBE_404_ENROLL_TOKEN_FILE="${temporary}/link"
  oneclick_agent_requested; attempt oneclick_load_inputs
  [[ "${status}" -ne 0 && "${output}" == *"regular file"* ]]
fi
reset; set_complete; export PROBE_404_ENROLL_TOKEN="too-short"
oneclick_agent_requested; attempt oneclick_load_inputs
[[ "${status}" -ne 0 && "${output}" == *"format"* && "${output}" != *too-short* ]]
for bad in 'http://probe.example.com' 'https://u:p@probe.example.com' 'https://probe.example.com/path'; do
  reset; set_complete; export PROBE_404_SERVER="${bad}"
  oneclick_agent_requested; attempt oneclick_load_inputs
  [[ "${status}" -ne 0 ]]
done
for bad in ' leading' 'trailing ' $'new\nline'; do
  reset; set_complete "${bad}"
  oneclick_agent_requested; attempt oneclick_load_inputs
  [[ "${status}" -ne 0 ]]
done
printf 'PASS token file and input validation\n'

# 9. An installed Agent (or leftovers) is refused before the token is redeemed.
reset; set_complete
oneclick_agent_requested
require_root_linux_systemd() { :; }
server_residue_exists() { return 1; }
agent_residue_exists() { return 0; }
install_agent() { printf 'install_agent reached\n'; }
attempt install_agent_oneclick
[[ "${status}" -ne 0 && "${output}" == *"already installed"* && "${output}" != *"install_agent reached"* && ! -e "${temporary}/curl.argv" && "${output}" != *"${token}"* ]]
printf 'PASS an existing Agent is refused without using the token\n'

# 10. With nothing installed the flow reaches install_agent with the loaded values and never reads the terminal.
reset; set_complete
oneclick_agent_requested
agent_residue_exists() { return 1; }
install_agent() { printf 'install_agent %s server=%s name=%s\n' "$1" "${ONECLICK_SERVER}" "${ONECLICK_NAME}"; }
attempt install_agent_oneclick
[[ "${status}" -eq 0 && "${output}" == 'install_agent  server=https://probe.example.com name=web 1' ]]
printf 'PASS a clean machine proceeds to the Agent install\n'

# 12. Non-root: refused first, with the guidance and without using the token.
reset; set_complete
current_euid() { printf '1000'; }
attempt oneclick_require_root
[[ "${status}" -ne 0 && "${output}" == *"sudo -i"* && "${output}" == *"token was not used"* && "${output}" != *"${token}"* && ! -e "${temporary}/curl.argv" ]]
current_euid() { printf '0'; }
attempt oneclick_require_root
[[ "${status}" -eq 0 ]]
# main() runs the root check before any other check: with a non-root user and one-click variables it stops there.
current_euid() { printf '1000'; }
reject_deferred_command_on_host() { printf 'reached another check\n'; }
attempt main
[[ "${status}" -ne 0 && "${output}" == *"sudo -i"* && "${output}" != *"reached another check"* && ! -e "${temporary}/curl.argv" ]]
printf 'PASS non-root is refused first and the token is not used\n'
