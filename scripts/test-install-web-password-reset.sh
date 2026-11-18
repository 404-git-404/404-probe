#!/usr/bin/env bash
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture-entry.sh"
set -Eeuo pipefail

[[ "${EUID}" -eq 0 && $# -eq 1 ]] || { printf 'Run the multi-user installer fixture as root with the candidate Server binary.\n' >&2; exit 1; }
candidate="$1"
[[ -x "${candidate}" ]]
source <(sed '$d' "${INSTALLER_FIXTURE_INSTALLER}")

install -d -m 0755 "$(dirname "${SERVER_BINARY}")" "$(dirname "${INSTALL_HELPER}")"
install -d -m 0750 -o root -g "${SERVICE_USER}" "${CONFIG_DIRECTORY}"
install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_USER}" "${STATE_DIRECTORY}"
install -m 0755 -o root -g root "${candidate}" "${SERVER_BINARY}"
runuser -u "${SERVICE_USER}" -- "${SERVER_BINARY}" agent list --db "${SERVER_DATABASE}" >/dev/null
file="${CONFIG_DIRECTORY}/web-password.hash"
before='synthetic first password'
new='synthetic replacement password'
last='synthetic final password'
hash="$(printf '%s' "${before}" | "${SERVER_BINARY}" web password-hash)"
write_private_file "${file}" "${hash}"
install_local_helper

# Replace only the TTY transport in the fixture. The generated helper still
# hashes on stdin, replaces the file and invokes the real runuser and CLI.
helper="${INSTALL_HELPER}.test"
awk '/^reject_deferred_command_on_host / {
  print "tty_write() { :; }"
  print "tty_read() { IFS= read -r \"$1\"; }"
  print "tty_read_secret() { IFS= read -r \"$1\"; }"
} { print }' "${INSTALL_HELPER}" >"${helper}"
chmod 0755 "${helper}"
inode="$(stat -c '%i' "${file}")"
result="$(printf '2\n%s\n%s\n' "${new}" "${new}" | "${helper}" web-password reset)"
[[ "${result}" == *'All existing Web sessions have been signed out.'* && "${result}" != *"${new}"* ]]
[[ "$(stat -c '%U:%G:%a' "${file}")" == '404-probe:404-probe:400' && "$(stat -c '%i' "${file}")" != "${inode}" ]]
runuser -u "${SERVICE_USER}" -- python3 - "${SERVER_DATABASE}" "${file}" <<'PY'
import hashlib
import sqlite3
import sys
from pathlib import Path

content = Path(sys.argv[2]).read_bytes()
with sqlite3.connect(sys.argv[1]) as db:
    value = db.execute('SELECT password_hash,file_sha256,revision FROM web_admin_credential').fetchone()
assert value == (content.decode().rstrip('\n'), hashlib.sha256(content).hexdigest(), 1)
PY
printf 'PASS rendered helper replaces private file atomically and adopts as service user\n'

inode="$(stat -c '%i' "${file}")"
runuser -u "${SERVICE_USER}" -- python3 - "${SERVER_DATABASE}" <<'PY'
import sqlite3
import sys

with sqlite3.connect(sys.argv[1]) as db:
    db.execute('DROP TABLE web_admin_credential')
PY
if result="$(printf '2\n%s\n%s\n' "${last}" "${last}" | "${helper}" web-password reset 2>&1)"; then
  printf 'FAIL expected service-account adoption failure\n' >&2
  exit 1
fi
[[ "${result}" == *'restart 404-probe-server.service to apply it'* && "${result}" != *"${last}"* ]]
[[ "$(stat -c '%U:%G:%a' "${file}")" == '404-probe:404-probe:400' && "$(stat -c '%i' "${file}")" != "${inode}" ]]
printf 'PASS adoption failure preserves new private file and reports restart recovery\n'
