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

created_user=0
created_runtime_target=0
production_before=""
production_after=""

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

safe_remove_tree() {
  local path="$1" resolved owner
  [[ "${path}" == "${test_root}" && -d "${path}" && ! -L "${path}" ]] || return 0
  resolved="$(realpath -e -- "${path}")"
  [[ "${resolved}" == "${test_root}" ]] || fail "rehearsal root changed identity"
  mountpoint -q -- "${path}" && fail "rehearsal root is a mountpoint"
  owner="$(stat -c '%U' -- "${path}")"
  [[ "${owner}" == root || "${owner}" == "${test_user}" ]] || fail "unexpected rehearsal root owner: ${owner}"
  rm -rf -- "${path}"
}

production_snapshot() {
  local path
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
  for path in /var/lib/404-probe /var/lib/404-probe/404-probe.db; do
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
  local status=$?
  trap - EXIT HUP INT TERM
  systemctl stop "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}" >/dev/null 2>&1 || true
  systemctl disable "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}" >/dev/null 2>&1 || true
  rm -f -- "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}"
  systemctl daemon-reload >/dev/null 2>&1 || true
  safe_remove_tree "${test_root}" || status=1
  if (( created_runtime_target != 0 )); then
    rmdir -- /run/404-probe >/dev/null 2>&1 || true
  fi
  if (( created_user != 0 )); then
    userdel "${test_user}" >/dev/null 2>&1 || status=1
    getent group "${test_user}" >/dev/null 2>&1 && groupdel "${test_user}" >/dev/null 2>&1 || true
  fi
  for path in "${test_root}" "${server_unit}" "${updater_unit}" "${agent_unit}" "${assets_unit}"; do
    if [[ -e "${path}" || -L "${path}" ]]; then
      printf 'FAIL: cleanup residue: %s\n' "${path}" >&2
      status=1
    fi
  done
  if id "${test_user}" >/dev/null 2>&1 || getent group "${test_user}" >/dev/null 2>&1; then
    printf 'FAIL: rehearsal account remains\n' >&2
    status=1
  fi
  production_after="$(production_snapshot)"
  if [[ "${production_after}" == "${production_before}" ]]; then
    printf 'PRODUCTION_INVARIANTS=PASS\n'
  else
    printf 'PRODUCTION_INVARIANTS=FAIL\n' >&2
    diff -u <(printf '%s\n' "${production_before}") <(printf '%s\n' "${production_after}") >&2 || true
    status=1
  fi
  (( status != 0 )) || printf 'REHEARSAL_CLEANUP=PASS\n'
  printf 'EXIT_CODE=%s\n' "${status}"
  exit "${status}"
}

for command in systemctl python3 curl install runuser sha256sum sed cmp git tar ss realpath mountpoint stat \
  useradd userdel groupdel getent uname awk seq; do
  command -v "${command}" >/dev/null || fail "missing command: ${command}"
done
[[ -x "${go_binary}" ]] || fail "Go binary is not executable"
# Absolute, unit-safe paths avoid systemd/shell quoting ambiguity.
for path in "${source_assets}" "${asset_directory}" "${source_repository}" "${go_binary}"; do
  [[ "${path}" =~ ^/[A-Za-z0-9_./-]+$ ]] || fail "inputs require absolute paths without spaces or shell metacharacters"
done
for port in "${server_port}" "${assets_port}"; do
  [[ "${port}" =~ ^[1-9][0-9]{0,4}$ ]] && (( port <= 65535 )) || fail "invalid rehearsal port"
done
[[ "${server_port}" != "${assets_port}" ]] || fail "rehearsal ports must differ"
commits="$(python3 "${script_directory}/agent-upgrade-assets.py" "${source_version}" "${target_version}" \
  "${source_assets}" "${asset_directory}" "${go_binary}")" || fail "release assets rejected; no rehearsal resources created"
read -r old_commit target_commit <<<"${commits}"
[[ "$(git -C "${source_repository}" rev-parse --verify "${old_commit}^{commit}")" == "${old_commit}" ]] \
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

production_before="$(production_snapshot)"
for path in "${test_root}" "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}"; do
  [[ ! -e "${path}" && ! -L "${path}" ]] || fail "isolated rehearsal path already exists: ${path}"
done
id "${test_user}" >/dev/null 2>&1 && fail "isolated rehearsal user already exists"
getent group "${test_user}" >/dev/null 2>&1 && fail "isolated rehearsal group already exists"
[[ -z "$(ss -H -ltn "sport = :${server_port}")" ]] || fail "server rehearsal port is in use"
[[ -z "$(ss -H -ltn "sport = :${assets_port}")" ]] || fail "asset rehearsal port is in use"

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
useradd --system --user-group --home-dir "${test_root}/home" --shell /usr/sbin/nologin "${test_user}"
created_user=1

install -d -m 0710 -o root -g "${test_user}" "${test_root}"
install -d -m 0700 -o "${test_user}" -g "${test_user}" "${test_root}/server" "${test_root}/home"
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
chown -R "${test_user}:${test_user}" "${test_root}/rootfs/var/lib/404-probe"
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
git -c core.autocrlf=false -C "${source_repository}" archive "${old_commit}" \
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
PROBE_404_SERVER=http://127.0.0.1:${server_port}
PROBE_404_AGENT_ID=${agent_id}
PROBE_404_TOKEN=${agent_token}
PROBE_404_STATE=/var/lib/404-probe/agent.epoch
PROBE_404_UPDATER_SOCKET=/run/404-probe/agent-updater.sock
PROBE_404_SECURITY_EXPORT=/var/lib/404-probe-security/export
PROBE_404_SECURITY_ACKS=/var/lib/404-probe/agent.security-acks.json
EOF
chown "${test_user}:${test_user}" "${test_root}/rootfs/etc/404-probe/agent.env"
chmod 0600 "${test_root}/rootfs/etc/404-probe/agent.env"
unset created agent_token

if [[ ! -e /run/404-probe ]]; then
  install -d -m 0755 -o root -g root /run/404-probe
  created_runtime_target=1
fi

cat >"${server_unit}" <<EOF
[Unit]
Description=404-probe isolated Agent upgrade Server rehearsal

[Service]
Type=simple
User=${test_user}
Group=${test_user}
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
Group=${test_user}
WorkingDirectory=${test_root}/assets
ExecStart=/usr/bin/python3 -m http.server ${assets_port} --bind 127.0.0.1
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

cat >"${agent_unit}" <<EOF
[Unit]
Description=404-probe exact official ${source_version} Agent upgrade rehearsal
After=network-online.target ${updater_service} ${server_service}
Wants=network-online.target ${updater_service} ${server_service}

[Service]
Type=simple
User=${test_user}
Group=${test_user}
EnvironmentFile=${test_root}/rootfs/etc/404-probe/agent.env
ExecStart=/usr/local/bin/404-probe-agent --interval 1s --job-interval 1s --timeout 3s --allow-insecure-http
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

for _ in $(seq 1 60); do
  if python3 - "${test_root}/server/404-probe.db" "${agent_id}" "${target_version}" <<'PY'
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
check_version "${test_root}/rootfs/usr/local/bin/404-probe-agent" "${target_version}" "${target_commit}"
cmp -s -- "${test_root}/rootfs/usr/local/bin/404-probe-agent" "${candidate_agent}" \
  || fail "live Agent binary does not match the release asset"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.candidate" ]] || fail "candidate residue remains"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.previous" ]] || fail "previous residue remains"

printf 'OFFICIAL_OLD_AGENT_SHA256=%s\n' "$(sha256sum "${old_agent}" | awk '{print $1}')"
printf 'CANDIDATE_AGENT_SHA256=%s\n' "$(sha256sum "${candidate_agent}" | awk '{print $1}')"
printf 'SOURCE_COMMIT=%s\nTARGET_COMMIT=%s\n' "${old_commit}" "${target_commit}"
printf 'AGENT_ID=%s\n' "${agent_id}"
printf 'OPERATION_ID=%s\n' "${operation_id}"
printf 'FINAL_STATUS=%s\n' "${final_status}"
printf 'LIVE_VERSION=%s\n' "$("${test_root}/rootfs/usr/local/bin/404-probe-agent" version --json)"
printf 'AGENT_UPGRADE_REHEARSAL=PASS\n'
