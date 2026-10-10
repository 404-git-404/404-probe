#!/usr/bin/env bash
# Isolated command observations only; no real systemd or installed Agent writes.
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source <(sed '$d' "${root}/install.sh")
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
sleep() { :; }
systemctl() { printf '%s\n' "${fixture}"; }
fixture=$'ActiveState=inactive\nMainPID=0\nJob=0'
wait_local_migration_updater_stopped
for fixture in $'ActiveState=active\nMainPID=123\nJob=0' $'ActiveState=inactive\nMainPID=0\nJob=1'; do
  if wait_local_migration_updater_stopped; then echo 'accepted live or queued Updater' >&2; exit 1; fi
done
fixture=$'ActiveState=inactive\nSubState=dead\nMainPID=0'
if wait_local_migration_process 404-probe-agent-updater.service; then echo 'accepted inactive service' >&2; exit 1; fi
fixture=$'ActiveState=active\nSubState=running\nMainPID=99999999'
if wait_local_migration_process 404-probe-agent-updater.service; then echo 'accepted foreign/missing executable' >&2; exit 1; fi
if [[ -e "/proc/$$/exe" ]]; then
  # Redirect only the test copy's executable reference to this real process.
  # The production fixed AGENT_BINARY constant is not changed.
  fixture_binary="/proc/$$/exe"
  eval "$(declare -f wait_local_migration_process | sed 's/AGENT_BINARY/fixture_binary/g')"
  fixture="$(printf 'ActiveState=active\nSubState=running\nMainPID=%s' "$$")"
  systemctl() { printf 'observation\n' >>"${temporary}/observations"; printf '%s\n' "${fixture}"; }
  wait_local_migration_process 404-probe-agent-updater.service
  [[ "$(wc -l <"${temporary}/observations")" == 2 ]]
fi
# The failure cleanup must report restoration failure and preserve the original
# installer exit code. The durable journal and marker are deliberately untouched.
printf 'marker\n' >"${temporary}/marker"
printf 'journal\n' >"${temporary}/journal"
trap_line="$(grep '^  trap '\''status=\$?; if (( status != 0 && repair_updater == 0 ))' "${root}/install.sh")"
[[ -n "${trap_line}" ]]
set +e
(
  repair_updater=0; helper_candidate=""; candidate="${temporary}/candidate"
  restore_local_migration_updater() { return 1; }
  eval "${trap_line}"
  exit 37
) 2>"${temporary}/failure"
status=$?
set -e
[[ "${status}" == 37 ]]
grep -Fq 'actual Updater restoration could not be confirmed' "${temporary}/failure"
[[ "$(cat "${temporary}/marker")" == marker && "$(cat "${temporary}/journal")" == journal ]]
printf 'PASS stopped/queued/PID checks and honest failed restoration exit/marker/journal preservation\n'
