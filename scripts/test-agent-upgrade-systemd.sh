#!/usr/bin/env bash

set -Eeuo pipefail

[[ "${EUID}" -eq 0 ]] || { printf 'run as root\n' >&2; exit 1; }
[[ $# -eq 5 ]] || {
  printf 'usage: %s CURRENT_SERVER OFFICIAL_V093_AGENT V093_SOURCE_DIR V100_ASSET_DIR GO_BINARY\n' "$0" >&2
  exit 2
}

current_server="$1"
old_agent="$2"
old_source="$3"
asset_directory="$4"
go_binary="$5"

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
readonly target_version="v1.0.0"
readonly old_commit="b386b5eda434cd149762c7639b06dc9a4754b83a"

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
    /var/lib/404-probe/404-probe.db \
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
  for service in 404-probe-server.service 404-probe-agent.service 404-probe-agent-updater.service; do
    printf '%s|active=%s|enabled=%s\n' "${service}" \
      "$(systemctl is-active "${service}" 2>/dev/null || true)" \
      "$(systemctl is-enabled "${service}" 2>/dev/null || true)"
  done
}

cleanup() {
  local status=$?
  trap - EXIT HUP INT TERM
  systemctl stop "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}" >/dev/null 2>&1 || true
  systemctl disable "${agent_service}" "${updater_service}" "${server_service}" "${assets_service}" >/dev/null 2>&1 || true
  rm -f -- "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}"
  systemctl daemon-reload >/dev/null 2>&1 || true
  safe_remove_tree "${test_root}" || true
  if (( created_runtime_target != 0 )); then
    rmdir -- /run/404-probe >/dev/null 2>&1 || true
  fi
  if (( created_user != 0 )); then
    userdel "${test_user}" >/dev/null 2>&1 || true
    getent group "${test_user}" >/dev/null 2>&1 && groupdel "${test_user}" >/dev/null 2>&1 || true
  fi
  production_after="$(production_snapshot)"
  if [[ "${production_after}" == "${production_before}" ]]; then
    printf 'PRODUCTION_INVARIANTS=PASS\n'
  else
    printf 'PRODUCTION_INVARIANTS=FAIL\n' >&2
    diff -u <(printf '%s\n' "${production_before}") <(printf '%s\n' "${production_after}") >&2 || true
    status=1
  fi
  printf 'EXIT_CODE=%s\n' "${status}"
  exit "${status}"
}

for command in systemctl python3 curl install runuser sha256sum sed cmp; do
  command -v "${command}" >/dev/null || fail "missing command: ${command}"
done
for path in "${current_server}" "${old_agent}" "${go_binary}"; do
  [[ -f "${path}" && ! -L "${path}" ]] || fail "required regular file is missing: ${path}"
done
[[ -d "${old_source}" && -f "${old_source}/go.mod" && -f "${old_source}/internal/updater/serve_linux.go" ]] \
  || fail "invalid v0.9.3 source tree"
for name in RELEASE-METADATA.json SHA256SUMS 404-probe-agent-linux-amd64; do
  [[ -f "${asset_directory}/${name}" && ! -L "${asset_directory}/${name}" ]] || fail "missing release asset: ${name}"
done
[[ "$("${old_agent}" version --json)" == *'"version":"v0.9.3"'* ]] || fail "old Agent is not v0.9.3"
[[ "$("${current_server}" version --json)" == *'"version":"v1.0.0"'* ]] || fail "current Server is not v1.0.0"
[[ "$("${asset_directory}/404-probe-agent-linux-amd64" version --json)" == *'"version":"v1.0.0"'* ]] \
  || fail "candidate Agent is not v1.0.0"

production_before="$(production_snapshot)"
for path in "${test_root}" "${agent_unit}" "${updater_unit}" "${server_unit}" "${assets_unit}"; do
  [[ ! -e "${path}" && ! -L "${path}" ]] || fail "isolated rehearsal path already exists: ${path}"
done
id "${test_user}" >/dev/null 2>&1 && fail "isolated rehearsal user already exists"
getent group "${test_user}" >/dev/null 2>&1 && fail "isolated rehearsal group already exists"
[[ -z "$(ss -H -ltn "sport = :${server_port}")" ]] || fail "server rehearsal port is in use"
[[ -z "$(ss -H -ltn "sport = :${assets_port}")" ]] || fail "asset rehearsal port is in use"

trap cleanup EXIT HUP INT TERM
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
  "${test_root}/systemctl-bin" \
  "${test_root}/build-cache" \
  "${test_root}/build-tmp"
chown -R "${test_user}:${test_user}" "${test_root}/rootfs/var/lib/404-probe"
chmod 0700 "${test_root}/rootfs/var/lib/404-probe-updater"
chmod 0755 "${test_root}/rootfs/run/404-probe"
install -m 0755 -o root -g root "${old_agent}" "${test_root}/rootfs/usr/local/bin/404-probe-agent"
install -m 0755 -o root -g root "${current_server}" "${test_root}/current-server"
for name in RELEASE-METADATA.json SHA256SUMS 404-probe-agent-linux-amd64; do
  install -m 0644 -o root -g root "${asset_directory}/${name}" "${test_root}/assets/${target_version}/${name}"
done
chmod 0755 "${test_root}/assets/${target_version}/404-probe-agent-linux-amd64"

cp -a -- "${old_source}/." "${test_root}/controlled-source/"
sed -i 's/serviceUserName       = "404-probe"/serviceUserName       = "404-probe-agent-rehearsal"/' \
  "${test_root}/controlled-source/internal/updater/serve_linux.go"
grep -Fq 'serviceUserName       = "404-probe-agent-rehearsal"' \
  "${test_root}/controlled-source/internal/updater/serve_linux.go" || fail "could not isolate updater service account"
(
  cd "${test_root}/controlled-source"
  env GOTOOLCHAIN=local GOCACHE="${test_root}/build-cache" GOTMPDIR="${test_root}/build-tmp" TMPDIR="${test_root}/build-tmp" \
    "${go_binary}" build -buildvcs=false -trimpath \
    -ldflags="-s -w -X 404-probe/internal/buildinfo.Version=v0.9.3 -X 404-probe/internal/buildinfo.Commit=${old_commit} -X 404-probe/internal/updater.officialReleaseBase=http://127.0.0.1:${assets_port}/" \
    -o "${test_root}/controlled-updater" ./cmd/agent
)
[[ "$("${test_root}/controlled-updater" version --json)" == *'"version":"v0.9.3"'* ]] \
  || fail "controlled updater has the wrong version"

cat >"${test_root}/systemctl-bin/systemctl" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ \$# -eq 2 && "\$2" == "404-probe-agent.service" && ( "\$1" == restart || "\$1" == stop ) ]]; then
  exec /bin/systemctl "\$1" "${agent_service}"
fi
exec /bin/systemctl "\$@"
EOF
chmod 0755 "${test_root}/systemctl-bin/systemctl"

created="$(runuser -u "${test_user}" -- "${test_root}/current-server" agent add exact-v093-upgrade --db "${test_root}/server/404-probe.db")"
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
chmod 0400 "${test_root}/rootfs/etc/404-probe/agent.env"

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
Description=404-probe controlled v0.9.3 updater rehearsal
After=network-online.target ${assets_service}
Wants=network-online.target ${assets_service}

[Service]
Type=simple
User=root
Group=root
Environment=PATH=${test_root}/systemctl-bin:/usr/sbin:/usr/bin:/sbin:/bin
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
Description=404-probe exact official v0.9.3 Agent upgrade rehearsal
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
    && python3 - "${test_root}/server/404-probe.db" "${agent_id}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
db.close()
raise SystemExit(0 if row == ("v0.9.3", 1) else 1)
PY
  then
    break
  fi
  sleep 0.5
done
python3 - "${test_root}/server/404-probe.db" "${agent_id}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
db.close()
if row != ("v0.9.3", 1):
    raise SystemExit(f"old Agent did not report upgrade readiness: {row!r}")
print("OLD_AGENT_READY=PASS")
PY

operation_id="$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
)"
python3 - "${test_root}/server/404-probe.db" "${operation_id}" "${agent_id}" <<'PY'
import sqlite3, sys, time
stamp = int(time.time() * 1000)
db = sqlite3.connect(sys.argv[1])
with db:
    db.execute("INSERT INTO agent_upgrade_operations(operation_id,agent_id,from_version,target_version,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)",
               (sys.argv[2], sys.argv[3], "v0.9.3", "v1.0.0", "requested", stamp, stamp))
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
  if python3 - "${test_root}/server/404-probe.db" "${agent_id}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
row = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
db.close()
raise SystemExit(0 if row == ("v1.0.0", 1) else 1)
PY
  then
    break
  fi
  sleep 0.5
done

python3 - "${test_root}/server/404-probe.db" "${agent_id}" "${operation_id}" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
state = db.execute("SELECT agent_version,agent_upgrade_capable FROM agent_state WHERE agent_id=?", (sys.argv[2],)).fetchone()
operation = db.execute("SELECT from_version,target_version,status,failure_code,failure_message FROM agent_upgrade_operations WHERE operation_id=?", (sys.argv[3],)).fetchone()
agent = db.execute("SELECT id,name,revoked FROM agents WHERE id=?", (sys.argv[2],)).fetchone()
db.close()
if state != ("v1.0.0", 1):
    raise SystemExit(f"unexpected final Agent state: {state!r}")
if operation != ("v0.9.3", "v1.0.0", "succeeded", None, None):
    raise SystemExit(f"unexpected final operation: {operation!r}")
if agent != (sys.argv[2], "exact-v093-upgrade", 0):
    raise SystemExit(f"Agent identity changed: {agent!r}")
print("SERVER_OPERATION=PASS")
print("AGENT_IDENTITY=PASS")
PY

systemctl is-active --quiet "${agent_service}" || fail "upgraded Agent service is not active"
systemctl is-active --quiet "${updater_service}" || fail "updater service is not active"
[[ "$("${test_root}/rootfs/usr/local/bin/404-probe-agent" version --json)" == *'"version":"v1.0.0"'* ]] \
  || fail "live Agent binary was not upgraded"
cmp -s -- "${test_root}/rootfs/usr/local/bin/404-probe-agent" "${asset_directory}/404-probe-agent-linux-amd64" \
  || fail "live Agent binary does not match the release asset"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.candidate" ]] || fail "candidate residue remains"
[[ ! -e "${test_root}/rootfs/usr/local/bin/.404-probe-agent.previous" ]] || fail "previous residue remains"

printf 'OFFICIAL_OLD_AGENT_SHA256=%s\n' "$(sha256sum "${old_agent}" | awk '{print $1}')"
printf 'CANDIDATE_AGENT_SHA256=%s\n' "$(sha256sum "${asset_directory}/404-probe-agent-linux-amd64" | awk '{print $1}')"
printf 'AGENT_ID=%s\n' "${agent_id}"
printf 'OPERATION_ID=%s\n' "${operation_id}"
printf 'FINAL_STATUS=%s\n' "${final_status}"
printf 'LIVE_VERSION=%s\n' "$("${test_root}/rootfs/usr/local/bin/404-probe-agent" version --json)"
printf 'AGENT_UPGRADE_REHEARSAL=PASS\n'
