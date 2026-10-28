#!/usr/bin/env bash
# Source before any fixture setup or installer extraction. No production code
# sources this file. A forged/inherited flag alone cannot bypass the boundary.
fixture_runner="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture.py"
fixture_python=
for fixture_candidate in python3 python; do
  if "${fixture_candidate}" -c pass >/dev/null 2>&1; then
    fixture_python="${fixture_candidate}"
    break
  fi
done
if [[ -z "${fixture_python}" ]]; then
  printf 'INSTALLER-FIXTURE-SKIPPED: usable Python3 unavailable\n' >&2
  [[ "${INSTALLER_FIXTURE_REQUIRED:-0}" != 1 ]] || exit 1
  exit 77
fi
# Python performs the full canary in place when already isolated; flags alone
# can only request that proof, never skip it or authorize installer execution.
exec "${fixture_python}" -B "${fixture_runner}" --script "$0" -- "$@"
