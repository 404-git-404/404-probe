#!/usr/bin/env bash
# Never capture installer terminal output or passwords.
set -u
umask 077
fail() { printf 'R31: %s\n' "$*" >&2; exit 2; }
[[ $EUID -eq 0 ]] || fail 'requires root'
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PYTHON="${R31_PYTHON:-python3}"
if [[ $# -eq 2 && "$1" == --check ]]; then
  exec "$PYTHON" -B "$SCRIPT_DIR/r31-preflight.py" --check "$2"
fi
[[ $# -eq 2 && "$1" == --config ]] || fail 'use --config CONFIG.json (or --check CONFIG.json)'
[[ -t 0 && -t 1 && -r /dev/tty ]] || fail 'requires an interactive root terminal, without a pipe'
work="$("$PYTHON" -B "$SCRIPT_DIR/r31-preflight.py" --work-dir "$2")" || fail 'invalid configuration'
[[ ! -L "$work/rehearsal.lock" ]] || fail 'unsafe lock path'
exec 9>"$work/rehearsal.lock" || fail 'cannot open lock'
flock -n 9 || fail 'another rehearsal is running'
"$PYTHON" -B "$SCRIPT_DIR/r31-preflight.py" --check "$2" || fail 'preflight failed; no rehearsal started'
(set -o noclobber; : >"$work/run-started") || fail 'attempt already started; never overwrite evidence'
"$PYTHON" -B "$SCRIPT_DIR/r31-preflight.py" --operator-run "$2" </dev/tty
exit $?
