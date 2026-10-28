#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Pure function extraction only; no main, file writes, or fixture required.
eval "$(awk '/^readonly DEFAULT_VERSION=/{print} /^(die|release_version|canonical_version|target_version|print_install_banner)\(\) \{/{copy= !seen[$1]++} copy{print} copy && /^\}/{copy=0}' "${root}/install.sh")"

check_banner() {
  local expected="$1" actual
  actual="$(print_install_banner)"
  [[ "${actual}" == "${expected}" ]] || {
    printf 'unexpected install banner: %s\n' "${actual}" >&2
    return 1
  }
}
unset PROBE_404_VERSION
check_banner $'Install 404-probe v1.0.9\n\n  1) Server\n  2) Agent'
printf 'PASS default stable banner\n'
PROBE_404_VERSION=v1.2.3 check_banner $'Install 404-probe v1.2.3\n\n  1) Server\n  2) Agent'
printf 'PASS explicit stable banner\n'
PROBE_404_VERSION=v1.0.1-beta.1 check_banner $'Install 404-probe v1.0.1-beta.1 (Pre-release)\n\n  1) Server\n  2) Agent'
printf 'PASS beta.1 banner\n'
PROBE_404_VERSION=v1.0.1-beta.12 check_banner $'Install 404-probe v1.0.1-beta.12 (Pre-release)\n\n  1) Server\n  2) Agent'
printf 'PASS complete beta ordinal banner\n'
if error="$(PROBE_404_VERSION=v1.0.1-rc.0 print_install_banner 2>&1)"; then
  printf 'unvalidated target was displayed\n' >&2
  exit 1
fi
[[ "${error}" == '404-probe installer: target version must be stable or explicit beta.N/rc.N' ]]
printf 'PASS invalid target rejected before banner\n'
