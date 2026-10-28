#!/usr/bin/env bash
# Defense in depth. Kernel mounts remain the boundary if a function is replaced,
# `command rm` bypasses this wrapper, or a shell redirect writes a file.
# No mutation wrapper accepts /dev/*. Shell redirection to /dev/null remains
# available; the fd-aware outer trace and device snapshots guard bypasses.
# Debian's awk alias resolves through the deliberately hidden /etc/alternatives.
# Use the same installed implementation without exposing that host directory.
if ! command -v awk >/dev/null 2>&1 && [[ -x /usr/bin/mawk ]]; then
  awk() { /usr/bin/mawk "$@"; }
  export -f awk
fi
fixture_path() {
  local path="$1" parent
  [[ "${path}" == /* ]] || path="${PWD}/${path}"
  [[ "/${path}/" != */../* ]] || { printf 'fixture rejects parent traversal\n' >&2; return 88; }
  case "${path}" in
    "${INSTALLER_FIXTURE_ROOT}/"*|/tmp/*) ;;
    *) printf 'fixture rejects path outside root: %s\n' "${path}" >&2; return 88 ;;
  esac
  parent="${path}"
  while [[ "${parent}" != / ]]; do
    [[ ! -L "${parent}" ]] || { printf 'fixture rejects symlink: %s\n' "${parent}" >&2; return 88; }
    parent="${parent%/*}"; [[ -n "${parent}" ]] || parent=/
  done
}
fixture_mutate() {
  local operation="$1" argument; shift
  for argument in "$@"; do
    [[ "${argument}" == -* ]] && continue
    fixture_path "${argument}" || return
  done
  command "${operation}" "$@"
}
for fixture_command in rm rmdir unlink mv cp install mkdir touch tee truncate chmod chown; do
  eval "${fixture_command}() { fixture_mutate ${fixture_command} \"\$@\"; }"
  export -f "${fixture_command}"
done
fixture_forbid() { printf 'fixture requires an explicit service/account stub\n' >&2; return 89; }
for fixture_command in systemctl service rc-service useradd userdel usermod groupadd groupdel groupmod; do
  eval "${fixture_command}() { fixture_forbid; }"
  export -f "${fixture_command}"
done
export -f fixture_path fixture_mutate fixture_forbid
