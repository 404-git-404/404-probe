#!/bin/sh
# R05: the state of one Agent host, read only. Run on the host (over ssh by scripts/agent-state-snapshot.py), prints one
# "key<TAB>value" line per fact. It changes nothing.
#
# Nothing secret is ever printed, in any form that could be turned back into the secret. The rule is an allowlist:
#   * a value is shown only for a name that is listed below (settings that are paths, addresses, switches, intervals);
#   * everything else - any other setting, any argument value of a start command, any export in an init script, the text of
#     a unit file - is reduced to "<redacted>" or to a keyed digest ("mac");
#   * a keyed digest is sha256(KEY ":" content) with a random KEY that the caller makes, keeps only in the snapshot directory
#     and hands to this script on its standard input (never in an argument or a file on the host). Without the KEY the digest
#     cannot be checked against a guess, so a low-entropy password is as safe as a token; with it, two snapshots of the same
#     directory show that a value changed without saying what it was.
#
# SNAPSHOT_ROOT prefixes every path (the tests run this against a fixture tree). Commands are looked up in PATH.
R="${SNAPSHOT_ROOT:-}"
if [ -z "$SNAPSHOT_KEY" ]; then echo "SNAPSHOT_KEY is required (the caller generates it)" >&2; exit 3; fi

emit() { printf '%s\t%s\n' "$1" "$(printf '%s' "$2" | tr '\n\t' '  ')"; }
sha() { if [ -f "$1" ]; then sha256sum "$1" 2>/dev/null | cut -d' ' -f1 | tr -d '\\'; else echo missing; fi; }
mac() { printf '%s:%s' "$SNAPSHOT_KEY" "$1" | sha256sum | cut -d' ' -f1 | tr -d '\\'; }
filemac() { if [ -f "$1" ]; then { printf '%s:' "$SNAPSHOT_KEY"; cat "$1"; } | sha256sum | cut -d' ' -f1 | tr -d '\\'; else echo missing; fi; }
have() { command -v "$1" >/dev/null 2>&1; }
mode() { if [ -e "$1" ]; then stat -c '%a %U:%G' "$1" 2>/dev/null || echo unknown; else echo missing; fi; }

# Settings whose value may be shown (paths, addresses, identifiers, switches). A value with an "@" (credentials in a URL) never is.
safe_setting() {
  case "$1" in
    PROBE_404_SERVER|PROBE_404_AGENT_ID|PROBE_404_COUNTRY_CODE|PROBE_404_STATE|PROBE_404_SELECTOR_ORDER|PROBE_404_SECURITY_EXPORT|PROBE_404_SECURITY_ACKS|PROBE_404_WEB_PUBLIC_ORIGIN|PROBE_404_VERSION|PROBE_404_ALLOW_INSECURE_HTTP) ;;
    *) return 1 ;;
  esac
  case "$2" in *@*) return 1 ;; esac
  return 0
}
# Start-command flags whose value may be shown; every other value is redacted.
safe_flag() {
  case "$1" in
    --interval|--job-interval|--timeout|--offline-timeout|--listen|--db|--control-token-file|--web-password-hash-file|--web-public-origin|--server|--allow-insecure-http|--web-allow-insecure-http) ;;
    *) return 1 ;;
  esac
  case "$2" in *@*) return 1 ;; esac
  return 0
}
# The words of a start command, without any value that is not allowlisted: "flag=value flag=<redacted> flag word <arg>".
summarize_args() {
  out=''; pending=''
  set -f
  for w in $1; do
    if [ -n "$pending" ]; then
      case "$w" in -*) out="$out $pending"; pending='' ;; *)
        if safe_flag "$pending" "$w"; then out="$out $pending=$w"; else out="$out $pending=<redacted>"; fi
        pending=''; continue ;;
      esac
    fi
    case "$w" in
      -*=*) name=${w%%=*}; value=${w#*=}; if safe_flag "$name" "$value"; then out="$out $name=$value"; else out="$out $name=<redacted>"; fi ;;
      -*) pending=$w ;;
      serve|agent|version|licenses|run) out="$out $w" ;;
      *) out="$out <arg>" ;;
    esac
  done
  [ -n "$pending" ] && out="$out $pending"
  set +f
  printf '%s' "${out# }"
}
# Strip the quotes of a shell assignment value.
unquote() { v=$1; v=${v#\"}; v=${v%\"}; v=${v#\'}; v=${v%\'}; printf '%s' "$v"; }

emit host.os "$(sed -n 's/^PRETTY_NAME=//p' "$R/etc/os-release" 2>/dev/null | tr -d '"')"
emit host.arch "$(uname -m)"
if [ -d "$R/run/systemd/system" ] && have systemctl; then INIT=systemd; elif have rc-service; then INIT=openrc; else INIT=unknown; fi
emit host.init "$INIT"

# The Agent binary (public content: its hash is not a secret).
BIN="$R/usr/local/bin/404-probe-agent"
emit binary.agent.sha256 "$(sha "$BIN")"
emit binary.agent.mode "$(mode "$BIN")"
if [ -x "$BIN" ]; then emit agent.version "$("$BIN" version 2>&1 | head -n 1)"; else emit agent.version missing; fi

# Settings: allowlisted names show their value; any other name only that it is set, with a keyed digest of the value.
ENVFILE="$R/etc/404-probe/agent.env"
emit config.agent.env.mode "$(mode "$ENVFILE")"
emit config.agent.env.mac "$(filemac "$ENVFILE")"
if [ -f "$ENVFILE" ]; then
  grep -v '^[[:space:]]*#' "$ENVFILE" | grep '=' | while IFS= read -r line; do
    name=${line%%=*}; value=${line#*=}
    if safe_setting "$name" "$value"; then
      emit "config.env.$name" "$value"
    else
      if [ -n "$value" ]; then emit "config.env.$name" '<redacted>'; else emit "config.env.$name" '<empty>'; fi
      emit "config.env.$name.mac" "$(mac "$value")"
    fi
  done
fi
emit config.selector_order.sha256 "$(sha "$R/etc/404-probe/selector-order.json")"
emit state.collector_unavailable_marker "$([ -e "$R/var/lib/404-probe-security/export/collector-unavailable" ] && echo present || echo absent)"

if [ "$INIT" = systemd ]; then
  for unit in 404-probe-agent.service 404-probe-agent-updater.service 404-probe-security-collect.service 404-probe-security-collect.timer 404-probe-agent-removal.service 404-probe-agent-removal-finalize.service; do
    key=${unit%.*}.${unit##*.}
    if systemctl cat "$unit" >/dev/null 2>&1; then
      # The unit with its drop-ins as systemd reads it, so a drop-in counts too; only a keyed digest of it is kept.
      emit "unit.$key.mac" "$(systemctl cat "$unit" 2>/dev/null | { printf '%s:' "$SNAPSHOT_KEY"; cat; } | sha256sum | cut -d' ' -f1 | tr -d '\\')"
      emit "unit.$key.file.mode" "$(mode "$R/etc/systemd/system/$unit")"
      start=$(systemctl cat "$unit" 2>/dev/null | grep -E '^ExecStart=' | head -n 1)
      start=${start#ExecStart=}; start=${start#[-@:+!]}
      set -f; set -- $start; set +f
      emit "unit.$key.exec.argv0" "${1:-}"
      [ $# -gt 0 ] && shift
      emit "unit.$key.exec.args" "$(summarize_args "$*")"
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
      emit "unit.$key.mac" missing
    fi
  done
elif [ "$INIT" = openrc ]; then
  for svc in 404-probe-agent 404-probe-agent-updater; do
    file="$R/etc/init.d/$svc"
    emit "unit.$svc.mac" "$(filemac "$file")"
    emit "unit.$svc.file.mode" "$(mode "$file")"
    emit "unit.$svc.confd.mac" "$(filemac "$R/etc/conf.d/$svc")"
    if [ -f "$file" ]; then
      # How the service is run and with what it may do: the OpenRC counterpart of the sandbox lines. Allowlisted keys only.
      run=''
      while IFS= read -r line; do
        case "$line" in
          command=*|command_user=*|command_background=*|supervisor=*|capabilities=*|rc_ulimit=*|rc_cgroup_*=*)
            name=${line%%=*}; value=$(unquote "${line#*=}"); run="$run$name=$value;" ;;
          command_args=*) emit "hardening.$svc.args" "$(summarize_args "$(unquote "${line#*=}")")" ;;
          export\ *) item=${line#export }; ename=${item%%=*}; evalue=$(unquote "${item#*=}")
            if safe_setting "$ename" "$evalue"; then emit "hardening.$svc.export.$ename" "$evalue"; else emit "hardening.$svc.export.$ename" '<redacted>'; emit "hardening.$svc.export.$ename.mac" "$(mac "$evalue")"; fi ;;
        esac
      done < "$file"
      emit "hardening.$svc.run" "$run"
    fi
    emit "unit.$svc.enabled" "$(rc-update show default 2>/dev/null | grep -c "^ *$svc " | sed 's/^1$/yes/;s/^0$/no/')"
    emit "runtime.$svc.status" "$(rc-service "$svc" status 2>&1 | head -n 1 | sed 's/^ *\* *//')"
  done
  CG="$R/sys/fs/cgroup"
  [ -f "$CG/memory.max" ] && emit hardening.host.cgroup_memory_max "$(cat "$CG/memory.max")"
fi
exit 0
