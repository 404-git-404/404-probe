#!/usr/bin/env bash
# Local orchestration only: no systemd, network, account or installed Agent changes.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source <(sed '$d' "${root}/install.sh")
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
export PROBE_404_VERSION=v1.0.1
reject_openrc_deferred() { :; }
install() { printf 'unexpected install\n' >>"${temporary}/mutations"; return 99; }
stat() { printf 'unexpected stat\n' >>"${temporary}/mutations"; return 99; }
systemctl() { printf 'unexpected systemctl\n' >>"${temporary}/mutations"; return 99; }
command() {
  if [[ "$*" == '-v python3' ]]; then return 1; fi
  builtin command "$@"
}
if upgrade_old_beta_agent >"${temporary}/output" 2>"${temporary}/error"; then
  printf 'missing python3 unexpectedly accepted\n' >&2; exit 1
fi
grep -Fq 'requires python3 for static verification' "${temporary}/error"
grep -Fq 'No Agent files were changed' "${temporary}/error"
[[ ! -e "${temporary}/mutations" ]]
printf 'PASS missing python3 exits before inspecting or changing Agent files/services\n'
