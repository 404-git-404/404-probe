#!/usr/bin/env bash
# Source before any fixture setup or installer extraction. No production code
# sources this file. A forged/inherited flag alone cannot bypass the boundary.
if [[ -n "${INSTALLER_FIXTURE_ACTIVE:-}" ]]; then
  [[ "${INSTALLER_FIXTURE_ACTIVE}" == "${INSTALLER_FIXTURE_ROOT:-}" ]] || exit 87
  [[ -r /proc/self/mountinfo && -r /proc/self/status ]] || exit 87
  grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status || exit 87
  for fixture_mount in / /etc /run /var/lib /var/log /usr/local /home /root; do
    awk -v path="${fixture_mount}" '$5==path && $6 ~ /(^|,)ro(,|$)/ {ok=1} END {exit !ok}' /proc/self/mountinfo || exit 87
  done
else
  fixture_runner="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture.py"
  if ! command -v python3 >/dev/null 2>&1; then
    printf 'SKIP installer execution: Python3 unavailable\n'
    exit 77
  fi
  exec python3 "${fixture_runner}" --script "$0" -- "$@"
fi
