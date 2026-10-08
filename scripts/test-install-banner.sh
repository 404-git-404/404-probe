#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Load the original functions without executing the root/system installer entry point.
[[ "$(tail -n 1 "${root}/install.sh")" == 'main "$@"' ]]
source <(sed '$d' "${root}/install.sh")
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT

check_banner() {
  local expected="$1" actual
  actual="$(print_install_banner)"
  [[ "${actual}" == "${expected}" ]] || {
    printf 'unexpected install banner: %s\n' "${actual}" >&2
    return 1
  }
}
unset PROBE_404_VERSION
check_banner $'Install 404-probe v1.0.0\n\n  1) Server\n  2) Agent'
printf 'PASS default stable banner\n'
PROBE_404_VERSION=v1.2.3 check_banner $'Install 404-probe v1.2.3\n\n  1) Server\n  2) Agent'
printf 'PASS explicit stable banner\n'
PROBE_404_VERSION=v1.0.1-beta.1 check_banner $'Install 404-probe v1.0.1-beta.1 (Pre-release)\n\n  1) Server\n  2) Agent'
printf 'PASS beta.1 banner\n'
PROBE_404_VERSION=v1.0.1-beta.12 check_banner $'Install 404-probe v1.0.1-beta.12 (Pre-release)\n\n  1) Server\n  2) Agent'
printf 'PASS complete beta ordinal banner\n'
if (PROBE_404_VERSION=v1.0.1-rc.1 print_install_banner >"${temporary}/output" 2>"${temporary}/error"); then
  printf 'unvalidated target was displayed\n' >&2
  exit 1
fi
[[ ! -s "${temporary}/output" ]]
grep -Fq 'target version must be stable or explicit beta.N' "${temporary}/error"
printf 'PASS invalid target rejected before banner\n'
