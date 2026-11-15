#!/usr/bin/env bash

set -Eeuo pipefail
set +x
umask 077

# Stable schema-2/protocol-2 releases only (source >= v1.0.1), Linux amd64/arm64.
# Both asset directories contain all seven build-release-assets.sh outputs.
# SOURCE_REPOSITORY must contain the source metadata commit; HEAD is not changed.
# Only the controlled updater is rebuilt, to isolate its account/service and use
# loopback assets. Both Agents and the Server remain exact official binaries.
# No GitHub/module downloads: populate the Go module/toolchain cache beforehand.
# Run on a test Server with an existing non-root 404-probe account. Verify the
# same UID/GID upgrade for at least 60 seconds, then restart the isolated Agent
# with same-GID-only and different UID/GID accounts to check updater rejection.
# Functional, cleanup and production snapshot results are printed separately.
[[ $# -eq 6 ]] || {
  printf 'usage: %s FROM_VERSION TO_VERSION FROM_ASSETS TO_ASSETS SOURCE_REPOSITORY GO_BINARY\n' "$0" >&2
  exit 2
}
[[ "${EUID}" -eq 0 ]] || { printf 'run as root\n' >&2; exit 1; }

readonly source_version="$1" target_version="$2"
source_assets="$3"
asset_directory="$4"
source_repository="$5"
go_binary="$6"
script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

readonly test_user="404-probe-agent-rehearsal"
readonly group_only_user="404-probe-rehearsal-gid"
readonly different_user="404-probe-rehearsal-other"
readonly production_user="404-probe"
readonly account_marker="404-probe isolated upgrade rehearsal"
readonly test_root="/var/lib/404-probe-agent-upgrade-rehearsal"
readonly server_service="404-probe-agent-upgrade-server-rehearsal.service"
readonly updater_service="404-probe-agent-upgrade-updater-rehearsal.service"
readonly agent_service="404-probe-agent-upgrade-rehearsal.service"
readonly assets_service="404-probe-agent-upgrade-assets-rehearsal.service"
readonly server_unit="/etc/systemd/system/${server_service}"
readonly updater_unit="/etc/systemd/system/${updater_service}"
readonly agent_unit="/etc/systemd/system/${agent_service}"
readonly assets_unit="/etc/systemd/system/${assets_service}"
readonly server_port="${PROBE_404_AGENT_REHEARSAL_SERVER_PORT:-33545}"
readonly assets_port="${PROBE_404_AGENT_REHEARSAL_ASSETS_PORT:-33546}"
readonly -a bind_state_targets=(/var/lib/404-probe-updater /var/lib/404-probe-security)

declare -A created_users=()
pending_account=""
created_group=""
functional_result=FAIL
created_runtime_target=0
absent_bind_state_targets=()
production_before=""
production_after=""

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

safe_remove_tree() {
  local path="$1" resolved
  [[ "${path}" == "${test_root}" && -d "${path}" && ! -L "${path}" ]] || return 0
  resolved="$(realpath -e -- "${path}")"
  [[ "${resolved}" == "${test_root}" && "$(stat -c '%u' -- "${path}")" == 0 ]] || return 1
  mountpoint -q -- "${path}" && return 1
  rm -rf --one-file-system -- "${path}"
}

production_snapshot() {
  local path
  getent passwd "${production_user}"
  getent group "${production_gid}"
  for path in \
    /usr/local/bin/404-probe-server \
    /usr/local/bin/404-probe-agent \
    /usr/local/bin/.404-probe-agent.candidate \
    /usr/local/bin/.404-probe-agent.previous \
    /usr/local/sbin/404-probe-install \
    /etc/404-probe/server.env \
    /etc/404-probe/agent.env \
    /etc/systemd/system/404-probe-server.service \
    /etc/systemd/system/404-probe-agent.service \
    /etc/systemd/system/404-probe-agent-updater.service; do
    if [[ -e "${path}" || -L "${path}" ]]; then
      stat -c "%n|%F|%U:%G:%a|%s|%Y" -- "${path}"
      [[ -f "${path}" && ! -L "${path}" ]] && sha256sum -- "${path}"
    else
      printf '%s|absent\n' "${path}"
    fi
  done
  # A running production Server writes SQLite continuously. Check its identity
  # and protection, not mutable size/mtime/content; never open it for rehearsal.
  for path in /run/404-probe /var/lib/404-probe /var/lib/404-probe/404-probe.db /var/lib/404-probe-upgrade "${bind_state_targets[@]}"; do
    if [[ -e "${path}" || -L "${path}" ]]; then
      stat -c '%n|%F|%U:%G:%a|%d:%i' -- "${path}"
    else
      printf '%s|absent\n' "${path}"
    fi
  done
  for service in 404-probe-server.service 404-probe-agent.service 404-probe-agent-updater.service; do
    printf '%s|active=%s|enabled=%s\n' "${service}" \
      "$(systemctl is-active "${service}" 2>/dev/null || true)" \
      "$(systemctl is-enabled "${service}" 2>/dev/null || true)"
    systemctl show "${service}" --property=MainPID,NRestarts --no-pager 2>/dev/null || true
  done
}

cleanup() {
  local status=$? cleanup_status=0 path account service record
  trap - EXIT
  trap '' HUP INT TERM
  set +e
  printf 'REHEARSAL_FUNCTIONAL=%s\n' "${functional_result}"
  systemctl stop "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}" >/dev/null 2>&1
  for service in "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}"; do
    case "$(systemctl show "${service}" -p ActiveState --value)" in
      inactive|failed) ;;
      *) printf 'FAIL: rehearsal unit did not stop: %s\n' "${service}" >&2; cleanup_status=1;;
    esac
  done
  if (( cleanup_status == 0 )); then
    rm -f -- "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}" || cleanup_status=1
    systemctl daemon-reload >/dev/null 2>&1 || cleanup_status=1
    safe_remove_tree "${test_root}" || cleanup_status=1
    if [[ -n "${pending_account}" ]]; then
      # Catch a signal delivered after useradd but before its result is recorded.
      record="$(getent -s files passwd "${pending_account}")"
      if [[ "${record}" == "${pending_account}:x:"*":${account_marker}:${test_root}/home:/usr/sbin/nologin" ]]; then
        created_users["${pending_account}"]="${record}"
      fi
      if [[ "${pending_account}" == "${different_user}" ]]; then
        record="$(getent -s files group "${different_user}")"
        if [[ "${record}" == "${different_user}:x:"*":" ]]; then
          created_group="${record}"
        fi
      fi
    fi
    for account in "${!created_users[@]}"; do
      # Only the exact local account entry we added is eligible. -f allows an
      # unrelated real Server process to continue using the shared numeric UID.
      if [[ "${account}" != "${production_user}" && "$(getent -s files passwd "${account}")" == "${created_users[${account}]}" ]]; then
        if ! record="$(userdel -f "${account}" 2>&1)"; then
          printf '%s\n' "${record}" >&2
          cleanup_status=1
        fi
      else
        cleanup_status=1
      fi
    done
    if [[ -n "${created_group}" ]] && getent group "${different_user}" >/dev/null; then
      if [[ "$(getent -s files group "${different_user}")" == "${created_group}" ]]; then
        groupdel "${different_user}" || cleanup_status=1
      else
        cleanup_status=1
      fi
    fi
  fi
  if (( created_runtime_target != 0 )); then
    rmdir -- /run/404-probe >/dev/null 2>&1 || true
  fi
  # systemd may create a bind destination on the host. rmdir only removes
  # empty directories. The snapshot below catches any destination left behind.
  for path in "${absent_bind_state_targets[@]}"; do
    if [[ -d "${path}" && ! -L "${path}" ]] && ! mountpoint -q -- "${path}"; then
      rmdir -- "${path}" >/dev/null 2>&1 || true
    fi
  done
  for path in "${test_root}" "${server_unit}" "${updater_unit}" "${agent_unit}" "${assets_unit}"; do
    if [[ -e "${path}" || -L "${path}" ]]; then
      printf 'FAIL: cleanup residue: %s\n' "${path}" >&2
      cleanup_status=1
    fi
  done
  for account in "${test_user}" "${group_only_user}" "${different_user}"; do
    if id "${account}" >/dev/null 2>&1 || getent group "${account}" >/dev/null 2>&1; then
      printf 'FAIL: rehearsal account or group remains: %s\n' "${account}" >&2
      cleanup_status=1
    fi
  done
  production_after="$(production_snapshot)"
  if [[ "${production_after}" == "${production_before}" ]]; then
    printf 'PRODUCTION_INVARIANTS=PASS\n'
  else
    printf 'PRODUCTION_INVARIANTS=FAIL\n' >&2
    diff -u <(printf '%s\n' "${production_before}") <(printf '%s\n' "${production_after}") >&2 || true
    status=1
  fi
  if (( cleanup_status == 0 )); then
    printf 'REHEARSAL_CLEANUP=PASS\n'
  else
    printf 'REHEARSAL_CLEANUP=FAIL\n' >&2
    status=1
  fi
  printf 'EXIT_CODE=%s\n' "${status}"
  exit "${status}"
}

for command in systemctl python3 curl install runuser sha256sum sed cmp git tar ss realpath mountpoint stat \
  useradd userdel groupdel getent uname awk seq id; do
  command -v "${command}" >/dev/null || fail "missing command: ${command}"
done
[[ -x "${go_binary}" ]] || fail "Go binary is not executable"
# Absolute, unit-safe paths avoid systemd/shell quoting ambiguity.
for path in "${source_assets}" "${asset_directory}" "${source_repository}" "${go_binary}"; do
  [[ "${path}" =~ ^/[A-Za-z0-9_./-]+$ ]] || fail "inputs require absolute paths without spaces or shell metacharacters"
done
source_repository="$(realpath -e -- "${source_repository}")"
readonly -a source_git=(git -c "safe.directory=${source_repository}" -C "${source_repository}")
for port in "${server_port}" "${assets_port}"; do
  [[ "${port}" =~ ^[1-9][0-9]{0,4}$ ]] && (( port <= 65535 )) || fail "invalid rehearsal port"
done
[[ "${server_port}" != "${assets_port}" ]] || fail "rehearsal ports must differ"
commits="$(python3 "${script_directory}/agent-upgrade-assets.py" "${source_version}" "${target_version}" \
  "${source_assets}" "${asset_directory}" "${go_binary}")" || fail "release assets rejected; no rehearsal resources created"
read -r old_commit target_commit <<<"${commits}"
[[ "$("${source_git[@]}" rev-parse --verify "${old_commit}^{commit}")" == "${old_commit}" ]] \
  || fail "source repository does not contain the metadata commit"
case "$(uname -m)" in
  x86_64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) fail "unsupported host architecture" ;;
esac
old_agent="${source_assets}/404-probe-agent-linux-${architecture}"
current_server="${asset_directory}/404-probe-server-linux-${architecture}"
candidate_agent="${asset_directory}/404-probe-agent-linux-${architecture}"
check_version() {
  local binary="$1" version="$2" commit="$3"
  "${binary}" version --json | python3 -c '
import json, sys
info = json.load(sys.stdin)
if info != {"version": sys.argv[1], "commit": sys.argv[2], "dirty": False}:
    raise SystemExit("binary runtime version/commit mismatch")
' "${version}" "${commit}"
}
check_version "${old_agent}" "${source_version}" "${old_commit}"
check_version "${current_server}" "${target_version}" "${target_commit}"
check_version "${candidate_agent}" "${target_version}" "${target_commit}"
printf 'RELEASE_PREFLIGHT=PASS FROM=%s TO=%s\n' "${source_version}" "${target_version}"

production_uid="$(id -u "${production_user}")" || fail "existing ${production_user} account required"
production_gid="$(id -g "${production_user}")" || fail "existing ${production_user} group required"
[[ "${production_uid}" != 0 && "${production_gid}" != 0 ]] || fail "production UID/GID must be non-root"
production_group="$(getent group "${production_gid}" | cut -d: -f1)"
[[ -n "${production_group}" ]] || fail "production primary group could not be resolved"
production_before="$(production_snapshot)"
for path in "${bind_state_targets[@]}"; do
  if [[ ! -e "${path}" && ! -L "${path}" ]]; then
    absent_bind_state_targets+=("${path}")
  fi
done
for path in "${test_root}" "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}"; do
  [[ ! -e "${path}" && ! -L "${path}" ]] || fail "isolated rehearsal path already exists: ${path}"
done
for account in "${test_user}" "${group_only_user}" "${different_user}"; do
  id "${account}" >/dev/null 2>&1 && fail "isolated rehearsal user already exists: ${account}"
  getent group "${account}" >/dev/null 2>&1 && fail "isolated rehearsal group already exists: ${account}"
done
[[ -z "$(ss -H -ltn "sport = :${server_port}")" ]] || fail "server rehearsal port is in use"
[[ -z "$(ss -H -ltn "sport = :${assets_port}")" ]] || fail "asset rehearsal port is in use"

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
create_user() {
  local account="$1"
  shift
  pending_account="${account}"
  useradd --system --no-create-home --no-log-init --comment "${account_marker}" --home-dir "${test_root}/home" --shell /usr/sbin/nologin "$@" "${account}"
  created_users["${account}"]="$(getent -s files passwd "${account}")"
  [[ -n "${created_users[${account}]}" ]] || fail "failed to verify local rehearsal account"
  if [[ "${account}" == "${different_user}" ]]; then
    created_group="$(getent -s files group "${different_user}")"
  fi
  pending_account=""
}
create_user "${test_user}" --non-unique --uid "${production_uid}" --gid "${production_gid}"
[[ "$(id -u "${test_user}")" == "${production_uid}" && "$(id -g "${test_user}")" == "${production_gid}" ]] || fail "UID/GID mismatch"

install -d -m 0710 -o root -g "${production_group}" "${test_root}"
install -d -m 0700 -o "${test_user}" -g "${production_group}" "${test_root}/server" "${test_root}/home"
install -d -m 0755 -o root -g root \
  "${test_root}/rootfs/usr/local/bin" \
  "${test_root}/rootfs/etc/404-probe" \
  "${test_root}/rootfs/var/lib/404-probe" \
  "${test_root}/rootfs/var/lib/404-probe-updater" \
  "${test_root}/rootfs/var/lib/404-probe-security" \
  "${test_root}/rootfs/run/404-probe" \
  "${test_root}/assets/${target_version}" \
  "${test_root}/controlled-source" \
  "${test_root}/build-cache" \
  "${test_root}/build-tmp"
chown -R "${test_user}:${production_group}" "${test_root}/rootfs/var/lib/404-probe"
chmod 0700 "${test_root}/rootfs/var/lib/404-probe-updater"
chmod 0755 "${test_root}/rootfs/run/404-probe"
install -m 0755 -o root -g root "${old_agent}" "${test_root}/rootfs/usr/local/bin/404-probe-agent"
install -m 0755 -o root -g root "${current_server}" "${test_root}/current-server"
for name in RELEASE-METADATA.json SHA256SUMS "404-probe-agent-linux-${architecture}"; do
  install -m 0644 -o root -g root "${asset_directory}/${name}" "${test_root}/assets/${target_version}/${name}"
done
chmod 0755 "${test_root}/assets/${target_version}/404-probe-agent-linux-${architecture}"

# Extract the exact metadata commit, ignoring dirty/ignored files and the
# repository's current branch. Transform only this disposable source copy.
"${source_git[@]}" -c core.autocrlf=false archive "${old_commit}" \
  | tar -xf - -C "${test_root}/controlled-source"
python3 - "${test_root}/controlled-source/internal/updater/paths_linux.go" "${test_user}" "${agent_service}" <<'PY'
from pathlib import Path
import re, sys
path = Path(sys.argv[1])
text = path.read_text()
for key, before, after in (("serviceUserName", "404-probe", sys.argv[2]),
                           ("agentServiceName", "404-probe-agent.service", sys.argv[3])):
    text, count = re.subn(r'(\b' + key + r'\s*=\s*)"' + re.escape(before) + '"',
                          lambda m: m[1] + '"' + after + '"', text)
    if count != 1:
        raise SystemExit("unsupported updater source layout: " + key)
path.write_text(text)
PY
(
  cd "${test_root}/controlled-source"
  env GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" \
    GOCACHE="${test_root}/build-cache" GOTMPDIR="${test_root}/build-tmp" TMPDIR="${test_root}/build-tmp" \
    "${go_binary}" build -buildvcs=false -trimpath \
    -ldflags="-s -w -X 404-probe/internal/buildinfo.Version=${source_version} -X 404-probe/internal/buildinfo.Commit=${old_commit} -X 404-probe/internal/updater.officialReleaseBase=http://127.0.0.1:${assets_port}/ -X 404-probe/internal/updater.officialReleaseAPIBase=http://127.0.0.1:${assets_port}/api/" \
    -o "${test_root}/controlled-updater" ./cmd/agent
)
check_version "${test_root}/controlled-updater" "${source_version}" "${old_commit}"

created="$(runuser -u "${test_user}" -- "${test_root}/current-server" agent add exact-official-upgrade --db "${test_root}/server/404-probe.db")"
agent_id="$(sed -n 's/^Agent ID: //p' <<<"${created}")"
agent_token="$(sed -n 's/^Token: //p' <<<"${created}")"
[[ "${agent_id}" =~ ^[0-9a-f]{32}$ && "${agent_token}" =~ ^[A-Za-z0-9_-]{43}$ ]] || fail "could not create isolated Agent"
cat >"${test_root}/rootfs/etc/404-probe/agent.env" <<EOF
PROBE_404_SERVER=http://127.0.0.1:${assets_port}
PROBE_404_AGENT_ID=${agent_id}
PROBE_404_TOKEN=${agent_token}
PROBE_404_STATE=/var/lib/404-probe/agent.epoch
PROBE_404_UPDATER_SOCKET=/run/404-probe/agent-updater.sock
PROBE_404_SECURITY_EXPORT=/var/lib/404-probe-security/export
PROBE_404_SECURITY_ACKS=/var/lib/404-probe/agent.security-acks.json
EOF
chown "${test_user}:${production_group}" "${test_root}/rootfs/etc/404-probe/agent.env"
chmod 0600 "${test_root}/rootfs/etc/404-probe/agent.env"
unset created agent_token

if [[ ! -e /run/404-probe ]]; then
  created_runtime_target=1
  install -d -m 0755 -o root -g root /run/404-probe
fi

# Updater health is held in memory by the Server. Observe every accepted
# report on loopback, retaining health, version and arrival time only.
cat >"${test_root}/rehearsal-http.py" <<'PY'
from http.client import HTTPConnection
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import sys
import time

port, server_port = map(int, sys.argv[1:3])
reports = sys.argv[3]
hop_headers = {"connection", "transfer-encoding", "keep-alive", "upgrade", "trailer"}

class RehearsalHTTP(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path.startswith("/api/v1/agent/"):
            self.forward()
        else:
            super().do_GET()

    def do_POST(self):
        if self.path == "/api/v1/report" or self.path.startswith("/api/v1/agent/"):
            self.forward()
        else:
            self.send_error(404)

    def forward(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        headers = {key: value for key, value in self.headers.items()
                   if key.lower() not in hop_headers}
        connection = HTTPConnection("127.0.0.1", server_port, timeout=10)
        try:
            connection.request(self.command, self.path, body, headers)
            response = connection.getresponse()
            result = response.read()
            if self.path == "/api/v1/report" and response.status == 200 and json.loads(result).get("accepted"):
                report = json.loads(body)
                health = report.get("agent_updater_health") or {}
                observation = [time.monotonic(), report.get("agent_version"),
                               int(bool(report.get("agent_upgrade_capable"))), health.get("status")]
                with open(reports, "a") as stream:
                    stream.write(json.dumps(observation) + "\n")
            self.send_response(response.status)
            for key, value in response.getheaders():
                if key.lower() not in hop_headers | {"content-length", "server", "date"}:
                    self.send_header(key, value)
            self.send_header("Content-Length", str(len(result)))
            self.end_headers()
            self.wfile.write(result)
        finally:
            connection.close()

ThreadingHTTPServer(("127.0.0.1", port), RehearsalHTTP).serve_forever()
PY
chmod 0644 "${test_root}/rehearsal-http.py"

cat >"${server_unit}" <<EOF
[Unit]
Description=404-probe isolated Agent upgrade Server rehearsal

[Service]
Type=simple
User=${test_user}
Group=${production_group}
ExecStart=${test_root}/current-server serve --listen 127.0.0.1:${server_port} --db ${test_root}/server/404-probe.db --offline-timeout 30s
Restart=on-failure
ReadWritePaths=${test_root}/server

[Install]
WantedBy=multi-user.target
EOF

cat >"${assets_unit}" <<EOF
[Unit]
Description=404-probe isolated upgrade asset server rehearsal

[Service]
Type=simple
User=${test_user}
Group=${production_group}
WorkingDirectory=${test_root}/assets
UMask=0077
ExecStart=/usr/bin/python3 ${test_root}/rehearsal-http.py ${assets_port} ${server_port} ${test_root}/server/reports.jsonl
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

common_bind_paths="BindPaths=${test_root}/rootfs/usr/local/bin:/usr/local/bin ${test_root}/rootfs/etc/404-probe:/etc/404-probe ${test_root}/rootfs/var/lib/404-probe:/var/lib/404-probe ${test_root}/rootfs/var/lib/404-probe-updater:/var/lib/404-probe-updater ${test_root}/rootfs/var/lib/404-probe-security:/var/lib/404-probe-security ${test_root}/rootfs/run/404-probe:/run/404-probe"
cat >"${updater_unit}" <<EOF
[Unit]
Description=404-probe controlled ${source_version} updater rehearsal
After=network-online.target ${assets_service}
Wants=network-online.target ${assets_service}

[Service]
Type=simple
User=root
Group=root
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin
Environment=HTTP_PROXY= HTTPS_PROXY= ALL_PROXY= NO_PROXY=127.0.0.1
ExecStart=${test_root}/controlled-updater updater
Restart=always
RestartSec=1s
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
${common_bind_paths}
ReadWritePaths=/var/lib/404-probe-updater /usr/local/bin /run/404-probe

[Install]
WantedBy=multi-user.target
EOF

# Let the first versioned report and every later report cross the 10s health
# cache. The initial capability-negotiation report carries no Agent version.
cat >"${agent_unit}" <<EOF
[Unit]
Description=404-probe exact official ${source_version} Agent upgrade rehearsal
After=network-online.target ${updater_service} ${server_service}
Wants=network-online.target ${updater_service} ${server_service}

[Service]
Type=simple
User=${test_user}
Group=${production_group}
EnvironmentFile=${test_root}/rootfs/etc/404-probe/agent.env
ExecStart=/usr/local/bin/404-probe-agent --interval 12s --job-interval 1s --timeout 3s --allow-insecure-http
Restart=on-failure
RestartSec=1s
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
${common_bind_paths}
ReadWritePaths=/var/lib/404-probe /run/404-probe

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${server_unit}" "${assets_unit}" "${updater_unit}" "${agent_unit}"
systemctl daemon-reload
systemctl start "${assets_service}" "${server_service}" "${updater_service}" "${agent_service}"

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${assets_port}/${target_version}/RELEASE-METADATA.json" >/dev/null 2>&1 \
    && python3 - "${test_root}/server/404-probe.db" "${agent_id}" "${source_version}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
db.close()
raise SystemExit(0 if row == (sys.argv[3], 1) else 1)
PY
  then
    break
  fi
  sleep 0.5
done
python3 - "${test_root}/server/404-probe.db" "${agent_id}" "${source_version}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
db.close()
if row != (sys.argv[3], 1):
    raise SystemExit(f"old Agent did not report upgrade readiness: {row!r}")
print("OLD_AGENT_READY=PASS")
PY

operation_id="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
)"
python3 - "${test_root}/server/404-probe.db" "${operation_id}" "${agent_id}" \
  "${source_version}" "${target_version}" "${target_commit}" <<'PY'
import sqlite3, sys, time
stamp = int(time.time() * 1000)
db = sqlite3.connect(sys.argv[1])
with db:
    db.execute("INSERT INTO agent_upgrade_operations(operation_id,agent_id,from_version,target_version,status,created_at,updated_at,channel,required_protocol,target_commit,server_version,beta_confirmed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
               (sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5], "requested", stamp, stamp,
                "stable", 2, sys.argv[6], sys.argv[5], 0))
db.close()
PY

final_status=""
for _ in $(seq 1 180); do
  final_status="$(python3 - "${test_root}/server/404-probe.db" "${operation_id}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT status FROM agent_upgrade_operations WHERE operation_id=?", (sys.argv[2],)).fetchone()
db.close()
print(row[0] if row else "missing")
PY
)"
  [[ "${final_status}" == succeeded ]] && break
  [[ "${final_status}" == failed || "${final_status}" == rolled_back || "${final_status}" == missing ]] \
    && fail "upgrade ended in ${final_status}"
  sleep 0.5
done
[[ "${final_status}" == succeeded ]] || fail "upgrade timed out in ${final_status}"

observe_reports() {
  python3 - "${test_root}/server/reports.jsonl" "${target_version}" "$1" "$2" "${3:-0}" <<'PY'
import json
from pathlib import Path
import sys
import time

log, version, label, expected, since = sys.argv[1:]
positive = expected == "healthy"
expiry = time.monotonic() + (90 if positive else 30)
while time.monotonic() < expiry:
    path = Path(log)
    data = path.read_bytes() if path.exists() else b""
    reports = [json.loads(line) for line in data.split(b"\n")[:-1]]
    reports = [row for row in reports if row[0] > float(since) and row[1] == version]
    if reports and positive:
        first = reports[0][0]
        spaced = [first]
        for stamp, _, capable, health in reports:
            if (capable, health) != (1, expected):
                raise SystemExit(f"{label}: at {stamp-first:.2f}s capable={capable}, updater_health={health}")
            if stamp - spaced[-1] > 10:
                spaced.append(stamp)
        elapsed = reports[-1][0] - first
        if elapsed >= 60 and len(spaced) >= 4:
            print(f"{label}=PASS elapsed={elapsed:.2f}s reports={len(reports)} "
                  f"spaced_reports={[round(stamp-first, 2) for stamp in spaced]} "
                  "agent_upgrade_capable=1 updater_health=healthy")
            break
    elif reports:
        _, _, capable, health = reports[-1]
        if health is not None:
            if (capable, health) != (0, expected):
                raise SystemExit(f"{label}: capable={capable}, updater_health={health}, expected={expected}")
            print(f"{label}=PASS agent_upgrade_capable=0 updater_health={health}")
            break
    time.sleep(0.25)
else:
    raise SystemExit(f"{label}: insufficient fresh target-version reports")
PY
}

observe_reports SAME_UID_GID healthy

python3 - "${test_root}/server/404-probe.db" "${agent_id}" "${operation_id}" "${source_version}" "${target_version}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
state = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
operation = db.execute("SELECT from_version,target_version,status,failure_code,failure_message FROM agent_upgrade_operations WHERE operation_id=?", (sys.argv[3],)).fetchone()
agent = db.execute("SELECT id,name,revoked FROM agents WHERE id=?", (sys.argv[2],)).fetchone()
db.close()
if state != (sys.argv[5], 1):
    raise SystemExit(f"unexpected final Agent state: {state!r}")
if operation != (sys.argv[4], sys.argv[5], "succeeded", None, None):
    raise SystemExit(f"unexpected final operation: {operation!r}")
if agent != (sys.argv[2], "exact-official-upgrade", 0):
    raise SystemExit(f"Agent identity changed: {agent!r}")
print("SERVER_OPERATION=PASS")
print("AGENT_IDENTITY=PASS")
PY

systemctl is-active --quiet "${agent_service}" || fail "upgraded Agent service is not active"
systemctl is-active --quiet "${updater_service}" || fail "updater service is not active"
updater_pid="$(systemctl show "${updater_service}" --property=MainPID --value)"
[[ "${updater_pid}" =~ ^[1-9][0-9]*$ ]] && cmp -s -- "/proc/${updater_pid}/exe" "${candidate_agent}" ||
  fail "live updater did not hand off to the official target asset"
printf 'OFFICIAL_UPDATER_HANDOFF=PASS\n'
check_version "${test_root}/rootfs/usr/local/bin/404-probe-agent" "${target_version}" "${target_commit}"
cmp -s -- "${test_root}/rootfs/usr/local/bin/404-probe-agent" "${candidate_agent}" \
  || fail "live Agent binary does not match the release asset"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.candidate" ]] || fail "candidate residue remains"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.previous" ]] || fail "previous residue remains"

create_user "${group_only_user}" --gid "${production_gid}"
create_user "${different_user}" --user-group
[[ "$(id -u "${group_only_user}")" != "${production_uid}" && "$(id -g "${group_only_user}")" == "${production_gid}" ]] ||
  fail "group-only control requires a distinct uid and the production gid"
[[ "$(id -u "${different_user}")" != "${production_uid}" && "$(id -g "${different_user}")" != "${production_gid}" && -n "${created_group}" ]] ||
  fail "wrong-identity control requires a distinct uid and gid"

assert_control() {
  local account="$1" group="$2" label="$3" expected="$4" since
  systemctl stop "${agent_service}"
  chown -R --no-dereference "${account}:${group}" "${test_root}/rootfs/var/lib/404-probe"
  sed -i -e "s/^User=.*/User=${account}/" -e "s/^Group=.*/Group=${group}/" "${agent_unit}"
  systemctl daemon-reload
  since="$(python3 -c 'import time; print(time.monotonic())')"
  systemctl start "${agent_service}"
  observe_reports "${label}" "${expected}" "${since}"
}
assert_control "${group_only_user}" "${production_group}" SAME_GID_ONLY unreachable
assert_control "${different_user}" "${different_user}" DIFFERENT_UID_GID identity_mismatch

printf 'OFFICIAL_OLD_AGENT_SHA256=%s\n' "$(sha256sum "${old_agent}" | awk '{print $1}')"
printf 'CANDIDATE_AGENT_SHA256=%s\n' "$(sha256sum "${candidate_agent}" | awk '{print $1}')"
printf 'SOURCE_COMMIT=%s\nTARGET_COMMIT=%s\n' "${old_commit}" "${target_commit}"
printf 'AGENT_ID=%s\n' "${agent_id}"
printf 'OPERATION_ID=%s\n' "${operation_id}"
printf 'FINAL_STATUS=%s\n' "${final_status}"
printf 'LIVE_VERSION=%s\n' "$("${test_root}/rootfs/usr/local/bin/404-probe-agent" version --json)"
functional_result=PASS
printf 'AGENT_UPGRADE_REHEARSAL=PASS\n'
