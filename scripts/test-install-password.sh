#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
# Tests the Server installer's administrator-password choice with a stub hasher and stub terminal.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(tail -n 1 "${INSTALLER_FIXTURE_INSTALLER}")" == 'main "$@"' ]]
source <(sed '$d' "${INSTALLER_FIXTURE_INSTALLER}")
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT

# The stub hasher records its arguments and its stdin and prints a fake hash.
hasher="${temporary}/hasher"
cat >"${hasher}" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${STUB_DIR}/argv"
cat >>"${STUB_DIR}/stdin"
printf '$argon2id$stub\n'
STUB
chmod 755 "${hasher}"
run_web_password_hasher() { "${hasher}" web password-hash; }
export STUB_DIR="${temporary}"

# The stub terminal reads answers from a file and records everything shown.
tty_write() { printf '%s' "$1" >>"${temporary}/screen"; }
tty_read() { IFS= read -r "$1" <&3 || printf -v "$1" ''; }
tty_read_secret() { IFS= read -r "$1" <&3 || printf -v "$1" ''; }

run_case() { # answers...
  rm -f "${temporary}/argv" "${temporary}/stdin" "${temporary}/screen" "${temporary}/answers"
  : >"${temporary}/argv"; : >"${temporary}/stdin"; : >"${temporary}/screen"
  printf '%s\n' "$@" >"${temporary}/answers"
  exec 3<"${temporary}/answers"
  local status=0
  (obtain_web_password_hash >"${temporary}/hash" 2>"${temporary}/error") || status=$?
  exec 3<&-
  return "${status}"
}

# Choice 1 and the default (just Enter) both generate a 43-character base64url password.
for answers in "1|" "|"; do
  IFS='|' read -r choice acknowledge <<<"${answers}"
  run_case "${choice}" "${acknowledge}"
  [[ "$(cat "${temporary}/hash")" == '$argon2id$stub' ]]
  generated="$(tr -d '\n' <"${temporary}/stdin")"
  [[ "${generated}" =~ ^[A-Za-z0-9_-]{43}$ ]] || { printf 'bad generated password: %s\n' "${generated}" >&2; exit 1; }
  [[ "$(cat "${temporary}/argv")" == 'web password-hash' ]]
  grep -Fq -- "${generated}" "${temporary}/screen"
  grep -Fq 'cannot be shown again' "${temporary}/screen"
  ! grep -Fq -- "${generated}" "${temporary}/argv"
  ! grep -Fq -- "${generated}" "${temporary}/hash" "${temporary}/error"
  printf 'PASS choice "%s" generates a 43-character password sent over stdin only\n' "${choice}"
done

# Each generation is different.
run_case 1 ""; first="$(cat "${temporary}/stdin")"
run_case 1 ""; second="$(cat "${temporary}/stdin")"
[[ "${first}" != "${second}" ]]
printf 'PASS generated passwords differ\n'

# Choice 2: a valid password goes over stdin, never through argv, and is not echoed on screen.
good='correct horse battery'
run_case 2 "${good}" "${good}"
[[ "$(cat "${temporary}/stdin")" == "${good}" && "$(cat "${temporary}/argv")" == 'web password-hash' ]]
! grep -Fq -- "${good}" "${temporary}/screen"
printf 'PASS own password accepted\n'

# Too short (15 characters) is rejected and the prompt repeats; then a good one is taken.
run_case 2 "123456789012345" "${good}" "${good}"
grep -Fq 'at least 16 characters' "${temporary}/screen"
[[ "$(cat "${temporary}/stdin")" == "${good}" ]]
printf 'PASS short password rejected\n'

# Exactly 16 characters is fine.
run_case 2 "1234567890123456" "1234567890123456"
[[ "$(cat "${temporary}/stdin")" == 1234567890123456 ]]
printf 'PASS 16 characters accepted\n'

# Mismatched confirmation is rejected.
run_case 2 "${good}" "different password here" "${good}" "${good}"
grep -Fq 'do not match' "${temporary}/screen"
[[ "$(cat "${temporary}/stdin")" == "${good}" ]]
printf 'PASS mismatched confirmation rejected\n'

# Three bad attempts in a row abort and the hasher is never called.
if run_case 2 short1 short2 short3; then printf 'three short passwords were accepted\n' >&2; exit 1; fi
[[ ! -s "${temporary}/stdin" && ! -s "${temporary}/argv" ]]
printf 'PASS repeated bad input aborts without hashing\n'

# Anything else is refused.
if run_case 3; then printf 'invalid choice accepted\n' >&2; exit 1; fi
[[ ! -s "${temporary}/argv" ]]
printf 'PASS invalid choice refused\n'

# Multi-byte passwords are counted in characters, in any locale: 15 characters are 45 bytes but still too short.
export LC_ALL=C
cjk() { local n="$1" out="" i; for ((i = 0; i < n; i++)); do out+="密"; done; printf '%s' "${out}"; }
run_case 2 "$(cjk 15)" "${good}" "${good}"
grep -Fq 'at least 16 characters' "${temporary}/screen"
[[ "$(cat "${temporary}/stdin")" == "${good}" ]]
printf 'PASS 15 multi-byte characters are rejected by the installer itself\n'
run_case 2 "$(cjk 16)" "$(cjk 16)"
[[ "$(cat "${temporary}/stdin")" == "$(cjk 16)" ]]
! grep -Fq 'at least 16 characters' "${temporary}/screen"
printf 'PASS 16 multi-byte characters are accepted\n'
[[ "$(utf8_length "$(cjk 7)")" == 7 && "$(utf8_length 'abc')" == 3 && "$(utf8_length '')" == 0 ]]
printf 'PASS utf8_length counts code points\n'

# All valid UTF-8 scalar ranges, including their boundary values, stay accepted.
for value in '' 'ASCII' $'\x7f' $'\xc2\x80' $'\xdf\xbf' $'\xe0\xa0\x80' $'\xed\x9f\xbf' $'\xef\xbf\xbf' $'\xf0\x90\x80\x80' $'\xf4\x8f\xbf\xbf' '密码🙂'; do
  utf8_valid "${value}" || { printf 'valid UTF-8 was rejected\n' >&2; exit 1; }
done
printf 'PASS UTF-8 scalar boundaries are valid\n'

# Invalid input must be rejected even after enough ASCII to pass the old length check.
for invalid in $'\x80' $'\xbf' $'\xc0\x80' $'\xc1\xbf' $'\xe0\x80\x80' $'\xed\xa0\x80' $'\xf0\x80\x80\x80' $'\xf4\x90\x80\x80' $'\xf5\x80\x80\x80' $'\xff' $'\xc2' $'\xe2\x82' $'\xf0\x90\x80' $'\xc2A' $'\xe2A\x80' $'\xe2\x82A' $'\xf0A\x80\x80' $'\xf0\x90A\x80' $'\xf0\x90\x80A'; do
  if utf8_valid "${invalid}"; then printf 'invalid UTF-8 was accepted\n' >&2; exit 1; fi
  run_case 2 "${good}${invalid}" "${good}" "${good}"
  grep -Fq 'must be valid UTF-8' "${temporary}/screen"
  [[ "$(cat "${temporary}/stdin")" == "${good}" && "$(cat "${temporary}/argv")" == 'web password-hash' ]]
done
printf 'PASS malformed UTF-8 is rejected before hashing and the password prompt retries\n'

# Confirmation is validated too; three malformed attempts never reach the hasher.
run_case 2 "${good}" "${good}"$'\x80' "${good}" "${good}"
grep -Fq 'must be valid UTF-8' "${temporary}/screen"
[[ "$(cat "${temporary}/stdin")" == "${good}" ]]
if run_case 2 "${good}"$'\x80' "${good}"$'\xe2\x82' "${good}"$'\xed\xa0\x80'; then
  printf 'three invalid UTF-8 passwords were accepted\n' >&2; exit 1
fi
[[ ! -s "${temporary}/stdin" && ! -s "${temporary}/argv" ]]
printf 'PASS invalid confirmation retries and repeated invalid passwords abort without hashing\n'

# A supplementary-plane character counts as one code point, and caller locale is not changed.
unicode_password="$(printf '🙂%.0s' {1..16})"
run_case 2 "${unicode_password}" "${unicode_password}"
[[ "$(cat "${temporary}/stdin")" == "${unicode_password}" && "${LC_ALL}" == C ]]
if command -v locale >/dev/null 2>&1 && locale -a | grep -Fxi 'C.utf8' >/dev/null; then
  (
    export LC_ALL=C.utf8
    utf8_valid "${unicode_password}"
    ! utf8_valid "${good}"$'\x80'
    [[ "${LC_ALL}" == C.utf8 ]]
  )
fi
printf 'PASS valid Unicode passwords and the caller locale are preserved\n'
