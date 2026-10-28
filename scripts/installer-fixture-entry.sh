#!/usr/bin/env bash
# Source before any fixture setup or installer extraction. No production code
# sources this file. A forged/inherited flag alone cannot bypass the boundary.
fixture_runner="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/installer-fixture.py"
if ! command -v python3 >/dev/null 2>&1; then
  printf 'SKIP installer execution: Python3 unavailable\n'
  exit 77
fi
# Python performs the full canary in place when already isolated; flags alone
# can only request that proof, never skip it or authorize installer execution.
exec python3 "${fixture_runner}" --script "$0" -- "$@"
