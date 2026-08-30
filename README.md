# 404-probe

## Quick Start

On a fresh Linux VPS with systemd, run the unified installer and choose Server or Agent:

```bash
curl -fsSL https://raw.githubusercontent.com/404-git-404/404-probe/main/install.sh | sudo bash
```

For a Server, enter its public Web URL (for example, `https://probe.example.com`) and set the Web administrator password. The installer exposes `http://127.0.0.1:8080` as cloudflared's local origin, but 404-probe does not provision or manage Cloudflare Tunnel; Tunnel configuration remains external. Create each Agent's shown-once enrollment value on the Server:

```bash
sudo 404-probe-install enroll singapore-01
```

For an Agent, run the same installer and enter only the Server URL and the enrollment value. Agent names remain server-authoritative. Non-loopback connections require HTTPS. An enrollment value packages the Agent's permanent credential; it is shown once, but it is not a single-use bootstrap token. Paste it only into the installer's hidden prompt and clear any clipboard copy after use.

## Build and test

```bash
go test ./...
go build -o 404-probe-agent ./cmd/agent
go build -o 404-probe-server ./cmd/server

mkdir -p build/linux-amd64 build/linux-arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-server ./cmd/server
```

## Upgrade an existing database

Stop the server and back up the database before opening it with the new binary. V0.4 already uses schema V4, and V0.5 opens that database in place without a schema migration. V1, V2, and V3 databases are still migrated to schema V4 on open; newer schemas are rejected.

```bash
sudo systemctl stop 404-probe-server
sudo cp -a /var/lib/404-probe/404-probe.db /var/lib/404-probe/404-probe.db.pre-v0.5
sudo ./404-probe-server agent list --db /var/lib/404-probe/404-probe.db
```

V0.5 does not change the Agent report or Probe Job protocol, Control API, read-only `remote` CLI, local `--db` CLI, scheduler/materializer, or HTTP/TCP/ICMP executors. Existing Agents can continue reporting with their current IDs and credentials. Revoking an Agent preserves its telemetry, schedules, jobs, and results; an enabled schedule may be disabled later when the scheduler next materializes it for the revoked Agent.

## Configure Web authentication

The Web UI and its read APIs are disabled unless an independent administrator password hash and public origin are configured. Generate an Argon2id hash at an interactive terminal; the command reads the password without echoing it and writes only the hash to stdout:

```bash
install -d -m 700 "$PWD/run/server"
umask 077
./404-probe-server web password-hash > "$PWD/run/server/web-password.hash"
chmod 600 "$PWD/run/server/web-password.hash"
```

The browser receives only an opaque `HttpOnly`, `Secure`, `SameSite=Strict` session cookie. Web sessions expire after 30 minutes idle or 12 hours total and are invalidated by logout or Server restart. The password hash and session boundary are independent from the Control token: never put a Control token in browser storage, JavaScript, HTML, a URL, or a Web request.

Production deployments must provide an exact HTTPS origin and terminate TLS at the public edge. Bind the Server to loopback when a reverse proxy is used. Plain HTTP Web login requires both an explicit exception and a loopback public origin and is only for local development.

After login, the dashboard uses only the authenticated Web surface:

```text
GET /api/v1/web/agents
GET /api/v1/web/agents/{agent_id}
GET /api/v1/web/agents/{agent_id}/history?hours=24
GET /api/v1/web/events
GET /api/v1/web/schedules
GET /api/v1/web/schedules/{schedule_id}
GET /api/v1/web/jobs
GET /api/v1/web/jobs/{job_id}
POST /api/v1/web/agents
POST /api/v1/web/agents/{agent_id}/revoke
```

The default Agent collection contains active Agents only. It accepts `status=online|offline|revoked` for an explicit state filter; `status=revoked` provides the retained revoked records. The Schedule collection accepts `agent_id`, `enabled=true|false`, and `probe_type=http|tcp_connect|icmp_ping`. The Job collection accepts `agent_id`, `schedule_id`, `probe_type`, `status`, `success`, and the documented created/finished time bounds. Every collection accepts `limit=1..100` and an opaque endpoint/filter-bound `cursor`. History accepts `hours=1..720`.

These responses are `no-store` and use explicit browser-safe DTOs: agent tokens, the Control token, lease credentials, epochs, session IDs, report sequences, boot IDs, raw network counters, result hashes, and storage-only fields are not exposed. The SSE feed uses the same whitelist. Job collections expose only a bounded `result_summary`; Job detail exposes the target config, error text, and the complete typed HTTP, TCP, or ICMP measurement. There is no separate Web Result resource.

Schedule targets, Job errors, and measurements are sensitive operational data visible only after Web authentication. The Web Schedule and Job surfaces are strictly read-only; legacy V0.1 browser data routes are unavailable, and Web JavaScript never calls `/api/v1/control/*`.

### V0.5 Web Agent lifecycle

`Add Agent` creates the same Agent ID and credential used by the existing CLI and Agent protocol. SQLite stores only the credential hash. The no-store creation response is the only retrieval path for the enrollment value: closing the result dialog clears it from the DOM, and list, detail, history, and SSE responses never expose it. There is no credential recovery API. Losing it requires revoking that Agent and creating a replacement.

The generated Linux install command contains only the configured Server origin and a version-pinned installer URL. It does not place the enrollment value in a URL, shell argument, or command history. The installer reads the value without echo from `/dev/tty`, validates the existing `404p1_` format, and writes the resulting credential only to the private Agent environment file.

`Remove Agent` means credential revocation and removal from the default active dashboard. It is not a hard delete, remote uninstall, or remote command. Historical telemetry, schedules, Probe Jobs, and Results remain queryable; the operation does not delete or cancel those records. It also does not remove software from the Agent host.

Both Web mutations require an authenticated Web session, the exact configured Origin, `Sec-Fetch-Site: same-origin`, and the session CSRF token. Requests use bounded strict JSON bodies and no-store responses. The browser never receives the Control token.

Agent cards use a fixed five-slot layout for identity, metrics, network, metadata, and actions. Online, offline, long-name, and never-reported cards keep the same height on desktop and mobile. Long text is truncated in the card while its complete value remains available through `title`, accessible labels, and the authenticated Agent detail response. Card resizing, dragging, per-Agent layouts, and metric visibility preferences are not supported.

## Start the server

The control API is optional. Its token must be 32 random bytes encoded as unpadded base64url, stored in a private regular file.

```bash
install -d -m 700 "$PWD/run/server"
export PROBE_404_DB="$PWD/run/server/404-probe.db"
export PROBE_404_CONTROL_TOKEN_FILE="$PWD/run/server/control.token"
export PROBE_404_WEB_PASSWORD_HASH_FILE="$PWD/run/server/web-password.hash"
umask 077
head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n' > "$PROBE_404_CONTROL_TOKEN_FILE"
chmod 600 "$PROBE_404_CONTROL_TOKEN_FILE"

./404-probe-server serve \
  --listen 127.0.0.1:8080 \
  --db "$PROBE_404_DB" \
  --offline-timeout 30s \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --web-password-hash-file "$PROBE_404_WEB_PASSWORD_HASH_FILE" \
  --web-public-origin "https://probe.example.com"
```

Use HTTPS at the public edge. The Control token is a high-privilege administrator secret: the Control API can create network probes on connected Agents even though the `remote` CLI described below is read-only. Do not place this token in a URL, browser, shell argument, or log. Plain HTTP is intended only for loopback development.

## Register and start an agent

Keep the server running and use a second shell from the same directory for the remaining commands.

```bash
export PROBE_404_DB="$PWD/run/server/404-probe.db"
export PROBE_404_CONTROL_TOKEN_FILE="$PWD/run/server/control.token"
PROBE_404_AGENT_OUTPUT="$(./404-probe-server agent add singapore-01 --db "$PROBE_404_DB")"
printf '%s\n' "$PROBE_404_AGENT_OUTPUT"
PROBE_404_AGENT_ID="$(printf '%s\n' "$PROBE_404_AGENT_OUTPUT" | sed -n 's/^Agent ID: //p')"
PROBE_404_TOKEN="$(printf '%s\n' "$PROBE_404_AGENT_OUTPUT" | sed -n 's/^Token: //p')"
./404-probe-server agent list --db "$PROBE_404_DB"

install -d -m 700 "$PWD/run/agent"
export PROBE_404_AGENT_ENV="$PWD/run/agent/agent.env"
umask 077
printf '%s\n' \
  'PROBE_404_SERVER=http://127.0.0.1:8080' \
  "PROBE_404_AGENT_ID=$PROBE_404_AGENT_ID" \
  "PROBE_404_TOKEN=$PROBE_404_TOKEN" \
  "PROBE_404_STATE=$PWD/run/agent/epoch" \
  > "$PROBE_404_AGENT_ENV"

set -a
. "$PROBE_404_AGENT_ENV"
set +a
./404-probe-agent \
  --interval 10s \
  --job-interval 10s \
  --timeout 8s \
  --allow-insecure-http &
PROBE_404_AGENT_PID=$!
```

Remove `--allow-insecure-http` when `PROBE_404_SERVER` uses HTTPS.

## Run one-shot probes

HTTP:

```bash
PROBE_404_JOB_OUTPUT="$(./404-probe-server probe run \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --type http \
  --url https://example.com/ \
  --method GET \
  --expected-status 200 \
  --timeout 5s \
  --expires-in 5m)"
printf '%s\n' "$PROBE_404_JOB_OUTPUT"
PROBE_404_JOB_ID="$(printf '%s\n' "$PROBE_404_JOB_OUTPUT" | sed -n 's/^Job ID: //p')"
```

TCP connect:

```bash
./404-probe-server probe run \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --type tcp_connect \
  --host example.com \
  --port 443
```

ICMP ping (requires an ICMP-capable agent platform and socket permissions):

```bash
./404-probe-server probe run \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --type icmp_ping \
  --target 1.1.1.1 \
  --count 4
```

List recent jobs and inspect one job/result:

```bash
./404-probe-server probe list \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --limit 20

./404-probe-server probe get "$PROBE_404_JOB_ID" \
  --db "$PROBE_404_DB"
```

## Manage fixed-interval schedules

Intervals must be between 30 seconds and 7 days.

```bash
PROBE_404_SCHEDULE_OUTPUT="$(./404-probe-server schedule add \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --name homepage \
  --type http \
  --url https://example.com/ \
  --method GET \
  --expected-status 200 \
  --timeout 5s \
  --interval 1m)"
printf '%s\n' "$PROBE_404_SCHEDULE_OUTPUT"
PROBE_404_SCHEDULE_ID="$(printf '%s\n' "$PROBE_404_SCHEDULE_OUTPUT" | sed -n 's/^Schedule ID: //p')"

./404-probe-server schedule list \
  --db "$PROBE_404_DB" \
  --agent-id "$PROBE_404_AGENT_ID"

./404-probe-server schedule disable "$PROBE_404_SCHEDULE_ID" --db "$PROBE_404_DB"
./404-probe-server schedule enable "$PROBE_404_SCHEDULE_ID" --db "$PROBE_404_DB"
./404-probe-server schedule delete "$PROBE_404_SCHEDULE_ID" --db "$PROBE_404_DB"
```

## Observe a server remotely

The explicit `remote` namespace is a read-only Control API client. It never opens SQLite and does not provide remote `run`, `add`, `enable`, `disable`, `delete`, or `revoke` operations. Existing commands using `--db` remain the local compatibility and management path; their filters intentionally do not match every remote filter in V0.3.

The remote client accepts the Control credential only through `--control-token-file`. The file must contain the same canonical 32-byte unpadded base64url token accepted by `serve`, must be a regular file, and on Unix must not be accessible by group or other users.

Set the HTTPS origin and the private token file available on the administrator machine:

```bash
export PROBE_404_SERVER=https://probe.example.com
export PROBE_404_CONTROL_TOKEN_FILE="$PWD/run/admin/control.token"
chmod 600 "$PROBE_404_CONTROL_TOKEN_FILE"
```

`--server` must be an origin such as `https://host` or `https://host:port`; credentials, path prefixes, queries, and fragments are rejected. The client uses the system CA store and normal hostname verification. It refuses every redirect, has a fixed 15-second total timeout, accepts at most 2 MiB of JSON response data, and does not retry.

For local development only, loopback HTTP requires an explicit exception:

```bash
./404-probe-server remote agent list \
  --server http://127.0.0.1:8080 \
  --allow-insecure-http \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE"
```

`--allow-insecure-http` does not permit private-LAN or other non-loopback HTTP servers.

### Remote Agent queries

```bash
./404-probe-server remote agent list \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --status online \
  --limit 50

./404-probe-server remote agent get "$PROBE_404_AGENT_ID" \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --json
```

`--status` accepts `online`, `offline`, or `revoked`.

### Remote Schedule queries

```bash
./404-probe-server remote schedule list \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --enabled true \
  --probe-type http \
  --limit 50

./404-probe-server remote schedule get "$PROBE_404_SCHEDULE_ID" \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --json
```

`--enabled` accepts `true` or `false`; `--probe-type` accepts `http`, `tcp_connect`, or `icmp_ping`. Schedule detail contains its probe target/configuration and is sensitive operational data.

### Remote Probe queries

```bash
./404-probe-server remote probe list \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --agent-id "$PROBE_404_AGENT_ID" \
  --schedule-id "$PROBE_404_SCHEDULE_ID" \
  --probe-type http \
  --status finished \
  --success false \
  --limit 50

./404-probe-server remote probe get "$PROBE_404_JOB_ID" \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --json
```

Probe filters are:

- `--agent-id` and `--schedule-id`;
- `--probe-type http|tcp_connect|icmp_ping`;
- `--status queued|leased|finished|expired`;
- `--success true|false`;
- `--created-after`, `--created-before`, `--finished-after`, and `--finished-before`, expressed as positive Unix millisecond timestamps.

An after timestamp must be lower than its matching before timestamp. Success and finished-time filters may be combined only with `--status finished` or with no status filter. Probe list output contains only a bounded result summary; `remote probe get` returns the complete typed HTTP, TCP, or ICMP measurement and sensitive target configuration. There is no separate `/results` resource.

### Pagination and JSON output

Every remote list command requests exactly one page. The default limit is 50 and the accepted range is 1 through 100. If the table has another page, it ends with:

```text
next_cursor: <opaque cursor>
```

Pass that value back with the same command and filters:

```bash
./404-probe-server remote probe list \
  --server "$PROBE_404_SERVER" \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" \
  --status finished \
  --limit 50 \
  --cursor "$PROBE_404_NEXT_CURSOR"
```

Cursors are opaque and are bound to their endpoint and filters. The CLI does not decode them and does not provide `--all` or automatic pagination. With `--json`, a list retains the pagination envelope:

```json
{"items":[],"next_cursor":null}
```

A `get --json` command emits one allowlisted resource object. Normal table/JSON output and `next_cursor` are written to stdout; errors are written to stderr with a nonzero exit status.

### Remote diagnostic workflow

Use the read path from broad state to the typed result without SSH or direct SQLite access:

```bash
./404-probe-server remote agent list --server "$PROBE_404_SERVER" --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" --status offline
./404-probe-server remote schedule list --server "$PROBE_404_SERVER" --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" --agent-id "$PROBE_404_AGENT_ID"
./404-probe-server remote probe list --server "$PROBE_404_SERVER" --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" --agent-id "$PROBE_404_AGENT_ID" --status finished --success false
./404-probe-server remote probe get "$PROBE_404_JOB_ID" --server "$PROBE_404_SERVER" --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE" --json
```

Agent hostnames, internal addresses, probe targets, errors, and measurements are sensitive operational data even when they are not credentials. Do not expose remote output publicly or place the Control token in a browser; V0.5 Web authentication uses a separate password and server-side session boundary.

## Capacity runbook

V0.5 continues the V0.4 behavior of keeping Probe Jobs and Results without automatic retention or purge. Fixed-interval schedules therefore grow the SQLite database continuously. Check capacity regularly on the Server host:

```bash
du -h "$PROBE_404_DB"

sqlite3 -readonly "$PROBE_404_DB" \
  "SELECT 'agents', COUNT(*) FROM agents
   UNION ALL SELECT 'schedules', COUNT(*) FROM probe_schedules
   UNION ALL SELECT 'jobs', COUNT(*) FROM probe_jobs
   UNION ALL SELECT 'results', COUNT(*) FROM probe_results;"
```

Back up the database before maintenance. V0.5 does not include a purge command or supported manual-deletion recipe; retention, archival, and downsampling remain future work.

## Control credential incident response

If the Control token may have leaked, treat the event as administrator credential compromise. Stop the Server, generate a new canonical token into a new `0600` regular file, replace the configured token file, and restart the Server so it loads the new token. The old token remains valid until that restart. Review Server access logs and probe activity without copying Authorization values into tickets or chat. V0.5 does not provide token rotation, multiple concurrent Control tokens, RBAC, or an audit-log subsystem.

## Optional Control API

Prefer the `remote` CLI for read operations because it keeps the token out of command-line arguments and enforces the current transport policy. Direct API clients authenticate with `Authorization: Bearer <control-token>` and must apply equivalent HTTPS and secret-handling controls.

The read plane is:

```text
GET /api/v1/control/agents
GET /api/v1/control/agents/{agent_id}
GET /api/v1/control/schedules
GET /api/v1/control/schedules/{schedule_id}
GET /api/v1/control/jobs
GET /api/v1/control/jobs/{job_id}
```

All Control responses use `Cache-Control: no-store`. Authentication is checked before query validation. Collections use stable `(created_at DESC, id DESC)` keyset pagination, a default limit of 50, a maximum limit of 100, and endpoint/filter-bound cursors. Agent collections accept `status`; Schedule collections accept `agent_id`, `enabled`, and `probe_type`; Job collections accept `agent_id`, `schedule_id`, `probe_type`, `status`, `success`, and the documented created/finished time bounds. Job collections expose only `result_summary`; Job detail exposes the complete typed measurement. Dedicated response DTOs exclude token hashes, lease credentials, Agent epoch/session fencing data, result hashes, and raw storage fields.

The following direct API examples include both reads and trusted-administrator writes. Writes remain intentionally unavailable through the read-only `remote` CLI. Create a private curl header file so the token is not included directly in curl's arguments:

```bash
PROBE_404_CONTROL_BASE=http://127.0.0.1:8080
PROBE_404_CONTROL_JOB_ID="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
PROBE_404_CONTROL_AUTH_HEADER_FILE="$PWD/run/server/control-auth.header"
umask 077
printf 'Authorization: Bearer %s\n' "$(cat "$PROBE_404_CONTROL_TOKEN_FILE")" > "$PROBE_404_CONTROL_AUTH_HEADER_FILE"
chmod 600 "$PROBE_404_CONTROL_AUTH_HEADER_FILE"
trap 'rm -f "$PROBE_404_CONTROL_AUTH_HEADER_FILE"' EXIT

curl --fail-with-body \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/agents?limit=50"

curl --fail-with-body \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/jobs?agent_id=$PROBE_404_AGENT_ID&status=finished&limit=50"

curl --fail-with-body \
  -X PUT "$PROBE_404_CONTROL_BASE/api/v1/control/jobs/$PROBE_404_CONTROL_JOB_ID" \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  -H 'Content-Type: application/json' \
  --data "{\"agent_id\":\"$PROBE_404_AGENT_ID\",\"probe_type\":\"tcp_connect\",\"config\":{\"host\":\"example.com\",\"port\":443},\"timeout_ms\":5000,\"expires_in_seconds\":300}"

curl --fail-with-body \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/jobs/$PROBE_404_CONTROL_JOB_ID"

PROBE_404_CONTROL_SCHEDULE_ID="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"

curl --fail-with-body \
  -X PUT "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID" \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  -H 'Content-Type: application/json' \
  --data "{\"agent_id\":\"$PROBE_404_AGENT_ID\",\"name\":\"homepage\",\"probe_type\":\"http\",\"config\":{\"url\":\"https://example.com/\",\"method\":\"GET\",\"expected_status\":200},\"timeout_ms\":5000,\"interval_seconds\":60,\"enabled\":true}"

curl --fail-with-body \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID"

curl --fail-with-body \
  -X DELETE \
  -H @"$PROBE_404_CONTROL_AUTH_HEADER_FILE" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID"

rm -f "$PROBE_404_CONTROL_AUTH_HEADER_FILE"
trap - EXIT
```

## Revoke an agent

```bash
kill "$PROBE_404_AGENT_PID"
wait "$PROBE_404_AGENT_PID" || true
./404-probe-server agent revoke "$PROBE_404_AGENT_ID" \
  --db "$PROBE_404_DB"
```
