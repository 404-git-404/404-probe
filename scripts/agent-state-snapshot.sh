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

# What a value must look like to be shown. A value that is allowlisted by name but is not exactly this shape is redacted: a URL is
# only scheme, host and port (no user, path, query or fragment), a path is a path that names this project, a duration is digits.
RE_DURATION='^[0-9]{1,6}(ns|us|ms|s|m|h)?$'
RE_URL='^https?://([A-Za-z0-9.-]{1,100}|\[[0-9A-Fa-f:.]{2,45}\])(:[0-9]{1,5})?/?$'
RE_LISTEN='^([A-Za-z0-9.-]{0,100}|\[[0-9A-Fa-f:.]{2,45}\]):[0-9]{1,5}$'
RE_PROJECT_PATH='^/[A-Za-z0-9._/-]{0,150}404-probe[A-Za-z0-9._/-]{0,100}$'
RE_ANY_PATH='^/[A-Za-z0-9._/-]{1,200}$'
RE_BOOL='^(true|false|yes|no|1|0)$'
RE_NAME='^[A-Z][A-Z0-9_]{0,63}$'
# valid REGEX VALUE: the value, on a line of its own, matches the extended regular expression.
valid() { [ "${#2}" -le 400 ] && printf '%s\n' "$2" | grep -Eq "$1"; }
trim() { v=$1; v=${v#"${v%%[![:space:]]*}"}; v=${v%"${v##*[![:space:]]}"}; printf '%s' "$v"; }
# Strip the quotes of a shell assignment value (a pair; unquote_loose also takes a quote that has no partner, as on a continued line).
unquote() {
  v=$1
  case "$v" in
    \"*\") v=${v#\"}; v=${v%\"} ;;
    \'*\') v=${v#\'}; v=${v%\'} ;;
  esac
  printf '%s' "$v"
}
unquote_loose() { v=$1; v=${v#[\"\']}; v=${v%[\"\']}; printf '%s' "$v"; }

# Settings that may be shown, and the shape of the value of each. Any other name, and any other shape, is redacted.
setting_ok() {
  case "$1" in
    PROBE_404_SERVER|PROBE_404_WEB_PUBLIC_ORIGIN) valid "$RE_URL" "$2" ;;
    PROBE_404_AGENT_ID) valid '^[A-Za-z0-9._-]{1,64}$' "$2" ;;
    PROBE_404_COUNTRY_CODE) valid '^[A-Za-z]{2}$' "$2" ;;
    PROBE_404_STATE|PROBE_404_SELECTOR_ORDER|PROBE_404_SECURITY_EXPORT|PROBE_404_SECURITY_ACKS) valid "$RE_PROJECT_PATH" "$2" ;;
    PROBE_404_VERSION) valid '^v?[0-9]{1,4}(\.[0-9]{1,4}){0,3}([-+.][0-9A-Za-z.-]{1,30})?$' "$2" ;;
    PROBE_404_ALLOW_INSECURE_HTTP) valid "$RE_BOOL" "$2" ;;
    *) return 1 ;;
  esac
}
# Start-command flags that take a value, and the shape of it. The flag is shown only when this holds.
flag_takes_value() {
  case "$1" in --interval|--job-interval|--timeout|--offline-timeout|--listen|--db|--control-token-file|--web-password-hash-file|--server|--web-public-origin) return 0 ;; esac
  return 1
}
flag_known() {
  flag_takes_value "$1" && return 0
  case "$1" in --allow-insecure-http|--web-allow-insecure-http) return 0 ;; esac
  return 1
}
flag_ok() {
  case "$1" in
    --interval|--job-interval|--timeout|--offline-timeout) valid "$RE_DURATION" "$2" ;;
    --listen) valid "$RE_LISTEN" "$2" ;;
    --db|--control-token-file|--web-password-hash-file) valid "$RE_PROJECT_PATH" "$2" ;;
    --server) valid "$RE_URL" "$2" ;;
    --web-public-origin) valid "$RE_URL" "$2" || [ "$2" = '${PROBE_404_WEB_PUBLIC_ORIGIN}' ] ;;
    --allow-insecure-http|--web-allow-insecure-http) valid "$RE_BOOL" "$2" ;;
    *) return 1 ;;
  esac
}
# The words of a start command, reduced to what is recognised: an allowlisted flag whose value has the right shape is shown as
# "flag=value"; every other word - an unlisted flag, a value that starts with "-", a positional argument - is "<redacted>", the
# name of an unlisted flag included, because a word that starts with "-" can be a flag or a value and nothing here can tell.
summarize_args() {
  out=''; pending=''
  set -f
  for w in $1; do
    if [ -n "$pending" ]; then
      # A flag that takes a value takes the next word whatever it starts with.
      if flag_ok "$pending" "$w"; then out="$out $pending=$w"; else out="$out $pending=<redacted>"; fi
      pending=''; continue
    fi
    case "$w" in
      --allow-insecure-http|--web-allow-insecure-http) out="$out $w" ;;
      --*=*) name=${w%%=*}; value=${w#*=}
        if flag_known "$name"; then
          if flag_ok "$name" "$value"; then out="$out $name=$value"; else out="$out $name=<redacted>"; fi
        else out="$out <redacted>"; fi ;;
      -*) if flag_takes_value "$w"; then pending=$w; else out="$out <redacted>"; fi ;;
      serve|agent|version|licenses|run|updater|security-collect) out="$out $w" ;;
      *) out="$out <redacted>" ;;
    esac
  done
  [ -n "$pending" ] && out="$out $pending=<none>"
  set +f
  printf '%s' "${out# }"
}
# One "NAME=value" assignment (of an environment file or an init script) as facts under the prefix $1: an allowlisted name with a
# value of the right shape is shown; anything else is redacted and only its keyed digest is kept. $2 is the assignment, $3 a counter.
emit_assignment() {
  item=$(trim "$2")
  case "$item" in *=*) ;; *) return 0 ;; esac
  aname=$(trim "${item%%=*}"); avalue=$(unquote "$(trim "${item#*=}")")
  if valid "$RE_NAME" "$aname" && setting_ok "$aname" "$avalue"; then
    emit "$1.$aname" "$avalue"
  elif valid "$RE_NAME" "$aname"; then
    if [ -n "$avalue" ]; then emit "$1.$aname" '<redacted>'; else emit "$1.$aname" '<empty>'; fi
    emit "$1.$aname.mac" "$(mac "$avalue")"
  else
    # Not even a name: nothing of the line is shown, and it is counted so that a change still shows.
    emit "$1.other.$3" '<redacted>'; emit "$1.other.$3.mac" "$(mac "$item")"
  fi
}
# The Linux capability names, in their canonical upper-case form. A list is shown only when every item is one of these.
known_capability() {
  case "$1" in
    CAP_CHOWN|CAP_DAC_OVERRIDE|CAP_DAC_READ_SEARCH|CAP_FOWNER|CAP_FSETID|CAP_KILL|CAP_SETGID|CAP_SETUID|CAP_SETPCAP|CAP_LINUX_IMMUTABLE|CAP_NET_BIND_SERVICE|CAP_NET_BROADCAST|CAP_NET_ADMIN|CAP_NET_RAW|CAP_IPC_LOCK|CAP_IPC_OWNER|CAP_SYS_MODULE|CAP_SYS_RAWIO|CAP_SYS_CHROOT|CAP_SYS_PTRACE|CAP_SYS_PACCT|CAP_SYS_ADMIN|CAP_SYS_BOOT|CAP_SYS_NICE|CAP_SYS_RESOURCE|CAP_SYS_TIME|CAP_SYS_TTY_CONFIG|CAP_MKNOD|CAP_LEASE|CAP_AUDIT_WRITE|CAP_AUDIT_CONTROL|CAP_SETFCAP|CAP_MAC_OVERRIDE|CAP_MAC_ADMIN|CAP_SYSLOG|CAP_WAKE_ALARM|CAP_BLOCK_SUSPEND|CAP_AUDIT_READ|CAP_PERFMON|CAP_BPF|CAP_CHECKPOINT_RESTORE) return 0 ;;
  esac
  return 1
}
# A comma separated list with no empty item, no space and nothing but canonical names.
capabilities_ok() {
  case "$1" in ''|,*|*,|*,,*|*[[:space:]]*) return 1 ;; esac
  caps_ifs=$IFS; IFS=,; set -f
  for cap in $1; do
    if ! known_capability "$cap"; then IFS=$caps_ifs; set +f; return 1; fi
  done
  IFS=$caps_ifs; set +f
  return 0
}
# One of the plain settings of an init script, shown only when it has the shape its name promises.
run_value_ok() {
  case "$1" in
    command) valid "$RE_ANY_PATH" "$2" ;;
    command_user) valid '^[A-Za-z0-9._-]{1,32}(:[A-Za-z0-9._-]{1,32})?$' "$2" ;;
    command_background) valid "$RE_BOOL" "$2" ;;
    supervisor) valid '^[A-Za-z0-9._-]{1,40}$' "$2" ;;
    capabilities) capabilities_ok "$2" ;;
    rc_ulimit) valid '^(-[A-Za-z] ([0-9]+|unlimited)( |$))+$' "$2" ;;
    rc_cgroup_*) valid '^([a-z_.]+ [0-9]+|[0-9]+|max)$' "$2" ;;
    *) return 1 ;;
  esac
}

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
  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    line=$(trim "$(printf '%s' "$line" | tr -d '\r')")
    case "$line" in ''|'#'*) continue ;; esac
    case "$line" in export|export[[:space:]]*) line=${line#export} ;; esac
    n=$((n + 1))
    emit_assignment config.env "$line" "$n"
  done < "$ENVFILE"
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
      # The effective command: a drop-in resets ExecStart= with an empty line and sets its own, so it is the last non-empty one.
      start=$(systemctl cat "$unit" 2>/dev/null | grep -E '^ExecStart=.' | tail -n 1)
      start=${start#ExecStart=}
      while :; do case "$start" in [-@:+!]*) start=${start#?} ;; *) break ;; esac; done
      set -f; set -- $start; set +f
      if valid "$RE_ANY_PATH" "${1:-}"; then emit "unit.$key.exec.argv0" "${1:-}"; else emit "unit.$key.exec.argv0" '<redacted>'; fi
      [ $# -gt 0 ] && shift
      emit "unit.$key.exec.args" "$(summarize_args "$*")"
      emit "unit.$key.exec.args.mac" "$(mac "$*")"
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
      run=''; n=0
      while IFS= read -r line || [ -n "$line" ]; do
        line=$(trim "$(printf '%s' "$line" | tr -d '\r')")
        case "$line" in
          command=*|command_user=*|command_background=*|supervisor=*|capabilities=*|rc_ulimit=*|rc_cgroup_*=*)
            name=${line%%=*}; value=$(unquote "${line#*=}")
            if run_value_ok "$name" "$value"; then run="$run$name=$value;"; else run="$run$name=<redacted>;"; fi ;;
          command_args=*) args=$(unquote_loose "${line#*=}")
            emit "hardening.$svc.args" "$(summarize_args "$args")"; emit "hardening.$svc.args.mac" "$(mac "$args")" ;;
          export|export[[:space:]]*) n=$((n + 1)); emit_assignment "hardening.$svc.export" "${line#export}" "$n" ;;
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
