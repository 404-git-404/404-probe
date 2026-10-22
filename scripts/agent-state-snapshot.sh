#!/bin/sh
# R05: the state of one Agent host, read only. Run on the host (over ssh by scripts/agent-state-snapshot.py), prints one
# "key<TAB>value" line per fact. It changes nothing, and it never prints a secret: values of TOKEN / ENROLL / SECRET /
# PASSWORD / KEY settings are reduced to "<set>" or "<empty>".
#
# SNAPSHOT_ROOT prefixes every path (the tests run this against a fixture tree). Commands are looked up in PATH.
R="${SNAPSHOT_ROOT:-}"

emit() { printf '%s\t%s\n' "$1" "$(printf '%s' "$2" | tr '\n\t' '  ')"; }
sha() { if [ -f "$1" ]; then sha256sum "$1" 2>/dev/null | cut -d' ' -f1 | tr -d '\'; else echo missing; fi; }
have() { command -v "$1" >/dev/null 2>&1; }
mode() { if [ -e "$1" ]; then stat -c '%a %U:%G' "$1" 2>/dev/null || echo unknown; else echo missing; fi; }

emit host.os "$(sed -n 's/^PRETTY_NAME=//p' "$R/etc/os-release" 2>/dev/null | tr -d '"')"
emit host.arch "$(uname -m)"
if [ -d "$R/run/systemd/system" ] && have systemctl; then INIT=systemd; elif have rc-service; then INIT=openrc; else INIT=unknown; fi
emit host.init "$INIT"

# The Agent binary.
BIN="$R/usr/local/bin/404-probe-agent"
emit binary.agent.sha256 "$(sha "$BIN")"
emit binary.agent.mode "$(mode "$BIN")"
if [ -x "$BIN" ]; then emit agent.version "$("$BIN" version 2>&1 | head -n 1)"; else emit agent.version missing; fi

# Settings: names and values, except secrets.
ENVFILE="$R/etc/404-probe/agent.env"
emit config.agent.env.mode "$(mode "$ENVFILE")"
if [ -f "$ENVFILE" ]; then
  grep -v '^[[:space:]]*#' "$ENVFILE" | grep '=' | while IFS= read -r line; do
    name=${line%%=*}; value=${line#*=}
    case "$name" in
      *TOKEN*|*ENROLL*|*SECRET*|*PASSWORD*|*KEY*) if [ -n "$value" ]; then value='<set>'; else value='<empty>'; fi ;;
    esac
    emit "config.env.$name" "$value"
  done
fi
emit config.selector_order.sha256 "$(sha "$R/etc/404-probe/selector-order.json")"
emit state.collector_unavailable_marker "$([ -e "$R/var/lib/404-probe-security/export/collector-unavailable" ] && echo present || echo absent)"

if [ "$INIT" = systemd ]; then
  for unit in 404-probe-agent.service 404-probe-agent-updater.service 404-probe-security-collect.service 404-probe-security-collect.timer 404-probe-agent-removal.service 404-probe-agent-removal-finalize.service; do
    key=${unit%.*}.${unit##*.}
    if systemctl cat "$unit" >/dev/null 2>&1; then
      # The unit with its drop-ins as systemd reads it, so a drop-in counts too.
      emit "unit.$key.sha256" "$(systemctl cat "$unit" 2>/dev/null | sha256sum | cut -d' ' -f1)"
      emit "unit.$key.file.mode" "$(mode "$R/etc/systemd/system/$unit")"
      emit "unit.$key.exec" "$(systemctl cat "$unit" 2>/dev/null | grep -E '^(ExecStart|ExecStartPre|ExecStop)=' | tr '\n' ';')"
      emit "unit.$key.enabled" "$(systemctl is-enabled "$unit" 2>/dev/null || true)"
      emit "runtime.$key.active" "$(systemctl is-active "$unit" 2>/dev/null || true)"
      # What the sandbox really is: the effective values, not the text of the file.
      systemctl show "$unit" -p User -p Group -p NoNewPrivileges -p ProtectSystem -p ProtectHome -p PrivateTmp -p PrivateDevices \
        -p CapabilityBoundingSet -p AmbientCapabilities -p ReadWritePaths -p ReadOnlyPaths -p RestrictAddressFamilies \
        -p MemoryDenyWriteExecute -p ProtectKernelTunables -p ProtectKernelModules -p ProtectControlGroups -p RestrictSUIDSGID \
        -p LockPersonality -p SystemCallFilter -p UMask 2>/dev/null | while IFS= read -r line; do
        emit "hardening.$key.${line%%=*}" "${line#*=}"
      done
      case "$unit" in
        *.service) emit "runtime.$key.main_pid" "$(systemctl show "$unit" -p MainPID 2>/dev/null | sed 's/^MainPID=//')"
                   emit "runtime.$key.restarts" "$(systemctl show "$unit" -p NRestarts 2>/dev/null | sed 's/^NRestarts=//')" ;;
      esac
    else
      emit "unit.$key.sha256" missing
    fi
  done
elif [ "$INIT" = openrc ]; then
  for svc in 404-probe-agent 404-probe-agent-updater; do
    file="$R/etc/init.d/$svc"
    emit "unit.$svc.sha256" "$(sha "$file")"
    emit "unit.$svc.file.mode" "$(mode "$file")"
    emit "unit.$svc.confd.sha256" "$(sha "$R/etc/conf.d/$svc")"
    if [ -f "$file" ]; then
      # How the service is run and with what it may do: the OpenRC counterpart of the sandbox lines.
      emit "hardening.$svc.run" "$(grep -E '^(command|command_args|command_user|command_background|capabilities|supervisor|rc_ulimit|rc_cgroup_[a-z_]+|export )' "$file" | tr '\n' ';')"
    fi
    emit "unit.$svc.enabled" "$(rc-update show default 2>/dev/null | grep -c "^ *$svc " | sed 's/^1$/yes/;s/^0$/no/')"
    emit "runtime.$svc.status" "$(rc-service "$svc" status 2>&1 | head -n 1 | sed 's/^ *\* *//')"
  done
  CG="$R/sys/fs/cgroup"
  [ -f "$CG/memory.max" ] && emit hardening.host.cgroup_memory_max "$(cat "$CG/memory.max")"
fi
exit 0
