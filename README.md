# 404-probe

> V0.9.3 refreshes the dashboard with compact frosted status cards, continuous
> scrolling, stable live refresh state, plan and traffic context, local
> country/region flags, selector config ordering, and expanded Linux CPU/disk
> telemetry. A fresh Agent installation performs one bounded request to
> `https://ipwho.is/?fields=success,country_code` and persists only the validated
> country code. Failure remains unknown and does not block installation;
> restarts and upgrades never repeat the lookup.

## Quick Start

On a fresh Linux VPS with systemd, run the unified installer and choose Server or Agent:

```bash
curl -fsSL https://raw.githubusercontent.com/404-git-404/404-probe/main/install.sh | sudo bash
```

The same command detects an existing supported Server and offers one local, explicit upgrade confirmation. It resolves the latest canonical stable release from the fixed official repository, verifies the release installer, metadata, checksum manifest, candidate build identity, database, and available space before stopping the service, then preserves the existing configuration, credentials, database, listen/domain settings, and systemd state. A root-only persistent transaction and consistent pre-migration backup allow an interrupted or failed upgrade to restore the old binary and database before the old service is restarted. Existing Agents remain on the Server Web upgrade path.

V0.9.2 adds an explicit, persistent login-domain policy. Existing installations
remain in exact-origin mode after migration and do not automatically trust a
parent domain. A local administrator can enable suffix mode and manage multiple
registrable root domains without editing configuration or restarting the Server:

```bash
sudo 404-probe-install domains
sudo 404-probe-install domains list
sudo 404-probe-install domains add navolyn.com
sudo 404-probe-install domains remove navolyn.com
sudo 404-probe-install domains disable
```

Registering `navolyn.com` permits that root and any dot-delimited subdomain, but
not `evilnavolyn.com` or `navolyn.com.evil.example`. The request Origin must
still exactly match the current request's scheme, host, and effective port.
Cookies remain host-only, so a new subdomain requires a new login. Suffix changes
take effect immediately and persist in the Server database. Any actual add,
remove, or disable change invalidates all existing Web sessions and login tokens,
so administrators and users must log in again; a duplicate add is a no-op and
does not invalidate credentials. The last suffix cannot be removed; use
`domains disable` with local confirmation to return to the configured exact
origin.

To repeatably target one official release instead of `latest`, pass the canonical version to the privileged shell:

```bash
curl -fsSL https://raw.githubusercontent.com/404-git-404/404-probe/main/install.sh | sudo PROBE_404_VERSION=v0.9.1 bash
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

### Linux CPU steal and disk I/O telemetry

New Agents report optional CPU steal and disk I/O fields. CPU steal is derived
from consecutive aggregate `cpu` lines in `/proc/stat`; the total includes only
user through steal, because `guest` and `guest_nice` are already accounted in
user and nice. Disk rates use the kernel-defined 512-byte sector counters from
`/proc/diskstats`. The collector selects non-pseudo entries from `/sys/block`
at the highest non-overlapping logical layer. A whole disk is selected when none
of its partitions backs a mapper; when only some partitions back LVM, dm-crypt,
or MD, the parent disk is excluded and unmapped sibling partitions are counted
beside the highest holder device. Nested holders likewise contribute only their
topmost device. Read/write rates are summed across that selected set; busy
percentage is the maximum `io_ticks`
fraction of any selected device and therefore describes the most-busy disk, not
physical saturation or remaining performance.

The first sample, a counter rollback, restart, unreadable kernel files, or any
change to the selected device set rebuilds the affected baseline and reports
that metric as unknown. CPU and disk baselines are independent, so an unavailable
disk topology does not suppress CPU steal (and vice versa). Older Agents omit the
fields and remain compatible. Opening an
existing Server database migrates it to schema 14; back it up first and do not
downgrade it afterward.

## Upgrade to V0.9

V0.9 adds local sing-box REALITY security observability. A root-only, fixed-purpose daily systemd oneshot reads only `sing-box.service` journal JSON, stores its cursor and private outbox under `/var/lib/404-probe-security/private`, and exports bounded aggregate batches under `/var/lib/404-probe-security/export`. The normal locked `404-probe` Agent can read those aggregates but cannot read the private cursor/outbox, write the export, invoke arbitrary journal queries, or perform firewall/ban actions. It uploads only canonical source IP, counts, time bounds, and typed classifications; raw journal messages and ports never leave the host.

First upgrade an existing V0.8 Agent through the Web UI. Because remote upgrade deliberately replaces only the verified Agent binary and cannot install a new root helper, enable the V0.9 collector once on each Agent host with the version-pinned installer:

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v0.9.0/install.sh | sudo bash -s -- setup-security
```

`setup-security` is local-only, idempotent, and fails closed unless the existing binary, unit, private environment, service account, and exact verified V0.9.0 build pass validation. It preserves Agent identity, credential, Server URL, and epoch state; adds only the fixed Security paths and units; checks the generated aggregate is readable but not writable by the Agent; and restores changed files if setup fails. Fresh V0.9 Agent installs include this setup automatically. A dashboard status of `Setup required` means this local step is still needed.

Stop the Server and back up its database before first opening it with V0.9. The migration advances schema V9 to V10 by adding negotiated Security capability and immutable, idempotent batch history. Security batches are retained for 30 days; existing telemetry retention behavior is unchanged. Do not downgrade a migrated database.

```bash
sudo systemctl stop 404-probe-server
sudo cp -a /var/lib/404-probe/404-probe.db /var/lib/404-probe/404-probe.db.pre-v0.9
sudo ./404-probe-server agent list --db /var/lib/404-probe/404-probe.db
```

Collection is cursor-based and crash-safe. Each run is bounded to 100,000 lines, 64 MiB, 90 seconds, 4,096 tracked IPs, and 100 uploaded sources. Partial input, truncation, invalid cursors, malformed trusted fields, and delivery coverage gaps are surfaced explicitly instead of being presented as complete data. See `RELEASE_NOTES_v0.9.md` for thresholds, trust boundaries, and limitations.

## Upgrade to V0.8

V0.8 introduces version reporting and a narrowly scoped Agent updater. A V0.7 Agent cannot receive the first upgrade remotely because it has neither the protocol nor the privileged updater. Bootstrap each existing Agent locally with the V0.8 installer; it preserves the Agent ID, credential, Server URL, epoch, and optional local Clash configuration:

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v0.8.0/install.sh | sudo bash -s -- agent --server 'https://probe.example.com'
```

The installer recognizes a complete supported V0.7 installation, stages and verifies V0.8 before stopping it, installs the updater service, and rolls back to the previous Agent binary if authentication does not recover. Partial or unrecognized installations fail closed. After this local bootstrap, later eligible releases can be requested from the authenticated Agent card in the Web UI.

Stop the Server and back up the database before opening it with the V0.8 binary. V0.8 migrates schema V6 to V7 by adding Agent version/capability fields and a dedicated upgrade-operation table; schemas newer than the binary supports are rejected.

```bash
sudo systemctl stop 404-probe-server
sudo cp -a /var/lib/404-probe/404-probe.db /var/lib/404-probe/404-probe.db.pre-v0.8
sudo ./404-probe-server agent list --db /var/lib/404-probe/404-probe.db
```

The first V0.8 report remains V0.7-compatible. The Agent sends version and updater capability only after the Server advertises support, so older Servers reject no new fields. Paused, offline, revoked, bootstrap-required, development, dirty, same-version, and downgrade cases cannot start an upgrade.

The updater is a separate root service on `/run/404-probe/agent-updater.sock`; the normal Agent remains the locked `404-probe` user. It accepts only typed start/status/healthy requests for a fixed operation and target version. It downloads only official GitHub Release assets over HTTPS. A strict `RELEASE-METADATA.json` binds the canonical version and release commit to one fixed platform asset and digest; `SHA256SUMS` and the actual candidate must agree with that digest. The updater reads Go build information statically and never executes a candidate during verification. It then atomically swaps the fixed Agent binary, waits for a healthy authenticated version report, and rolls back on failure. V0.8 does not accept custom URLs, paths, commands, batch operations, reboot requests, automatic updates, or downgrades. See `RELEASE_NOTES_v0.8.md` for the trust boundary and operational details.

Revoking an Agent preserves its telemetry, schedules, jobs, results, and upgrade history. Back up the database first and do not downgrade a database after it has migrated.

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
GET /api/v1/web/version
GET /api/v1/web/schedules
GET /api/v1/web/schedules/{schedule_id}
GET /api/v1/web/jobs
GET /api/v1/web/jobs/{job_id}
POST /api/v1/web/agents
POST /api/v1/web/agents/{agent_id}/disable
POST /api/v1/web/agents/{agent_id}/enable
POST /api/v1/web/agents/{agent_id}/revoke
POST /api/v1/web/agents/{agent_id}/outbounds/switch
GET /api/v1/web/agents/{agent_id}/upgrade
POST /api/v1/web/agents/{agent_id}/upgrade
GET /api/v1/web/agents/{agent_id}/plan
PUT /api/v1/web/agents/{agent_id}/plan
```

The default Agent collection contains non-revoked Agents, including paused Agents. It accepts `status=online|offline|disabled|revoked` for an explicit state filter. The Schedule collection accepts `agent_id`, `enabled=true|false`, and `probe_type=http|tcp_connect|icmp_ping`. The Job collection accepts `agent_id`, `schedule_id`, `probe_type`, `status`, `success`, and the documented created/finished time bounds. Every collection accepts `limit=1..100` and an opaque endpoint/filter-bound `cursor`. History accepts `hours=1..720`.

These responses are `no-store` and use explicit browser-safe DTOs: agent tokens, the Control token, lease credentials, epochs, session IDs, report sequences, boot IDs, raw network counters, result hashes, and storage-only fields are not exposed. The SSE feed uses the same whitelist. Job collections expose only a bounded `result_summary`; Job detail exposes the target config, error text, and the complete typed probe or selector result. There is no separate Web Result resource.

Schedule targets, Job errors, and measurements are sensitive operational data visible only after Web authentication. The Web Schedule and Job surfaces are strictly read-only; legacy V0.1 browser data routes are unavailable, and Web JavaScript never calls `/api/v1/control/*`.

### V0.8 Web Agent lifecycle

`Add Agent` creates the same Agent ID and credential used by the existing CLI and Agent protocol. SQLite stores only the credential hash. The no-store creation response is the only retrieval path for the enrollment value: closing the result dialog clears it from the DOM, and list, detail, history, and SSE responses never expose it. There is no credential recovery API. Losing it requires revoking that Agent and creating a replacement.

The generated Linux install command contains only the configured Server origin and a version-pinned installer URL. It does not place the enrollment value in a URL, shell argument, or command history. The installer reads the value without echo from `/dev/tty`, validates the existing `404p1_` format, and writes the resulting credential only to the private Agent environment file.

`Remove Agent` means credential revocation and removal from the default active dashboard. It is not a hard delete, remote uninstall, or remote command. Historical telemetry, schedules, Probe Jobs, and Results remain queryable; the operation does not delete or cancel those records. It also does not remove software from the Agent host.

`Pause` temporarily rejects reports, discovery, and new work while retaining the credential and history. Queued selector switches expire instead of executing while paused. `Resume` re-enables that same Agent and credential. `Remove Agent` remains permanent: a revoked Agent cannot be resumed, and the Agent exits cleanly after the Server returns the revoked signal. The installed systemd unit uses `Restart=on-failure`, so crashes restart but this clean revoked exit does not.

To explicitly remove the software and private Agent state from its Linux host, run:

```bash
sudo 404-probe-install uninstall agent
```

Uninstall is intentionally destructive in v1.0: it shows the exact role and requires typing `DELETE 404-probe agent` (or `server`). For automation, pass the explicit `--confirm-delete-data` flag. Agent uninstall stops and disables the Agent, updater, and security collector, then removes their units, binary, credentials, selector metadata, epoch/security state, runtime files, managed candidates, and managed state directories. Server uninstall removes its service, binary, configuration, credentials, SQLite database (including WAL/SHM), and managed upgrade/rollback state. Both preserve systemd journal history and any unrecognized files, and neither removes sing-box or its configuration. The dedicated service account is removed only after its locked, non-interactive identity is verified and no recognized role or preserved directory remains.

The installed helper removes itself last. To retry after an interrupted or completed uninstall, use the official version-pinned entry point; the command is idempotent:

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v0.9.3/install.sh | sudo PROBE_404_VERSION=v0.9.3 bash -s -- uninstall agent --confirm-delete-data
```

Replace `agent` with `server` for the Server role. Local uninstall does not contact the Server or remove the Agent's Server-side telemetry; Web “permanent delete” is a separate tracked v1.0 operation.

All Web mutations require an authenticated Web session, the full canonical Origin admitted for the current request (with exact scheme, host, and effective port), `Sec-Fetch-Site: same-origin`, and the session CSRF token. Requests use bounded strict JSON bodies and no-store responses. The browser never receives the Control token.

Agent cards prioritize identity, availability, CPU/core count, RAM and disk usage, live network rates, and optional plan facts. Long Agent names use an ellipsis in the compact card and expose the complete name through the accessible label and tooltip. Swap, load, versions, report timestamps, Google status, Security status, and maintenance controls remain available through detail/history or the compact management menu instead of competing with the primary overview.

Each Agent can store optional traffic, bandwidth, price, lifecycle-date, and IANA-timezone facts through the authenticated plan drawer. Traffic quantities retain the explicitly entered decimal or binary unit. Calendar cycles preserve the original anchor day (including month-end anchors) and use the plan timezone. Billing usage is derived only from accepted permanent network-counter deltas: duplicate or stale reports do not count twice, same-boot reconnects can recover their full monotonic delta, and reboots/resets or a crossed cycle boundary are marked partial rather than inventing history. A manual calibration sets an accounting boundary: if the next report delta spans that boundary, the ambiguous delta is conservatively skipped and coverage becomes partial; subsequent accepted deltas continue from the calibrated value. Plans without traffic accounting, including one-time purchases, retain their own date timezone.

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

## Zero-configuration sing-box selector discovery and switching

The Agent automatically probes the local Clash API at `http://127.0.0.1:9090` and publishes only the latest selector name, current choice, choices, discovery state, and timestamps. A manual loopback-only origin override is available through `PROBE_404_SING_BOX_CLASH_API`; credentials, paths, queries, fragments, and non-loopback hosts are rejected. Fresh installation asks for neither an API URL nor a secret.

Selector cards follow the top-level sing-box `outbounds` order when the root installer can safely extract it into `/etc/404-probe/selector-order.json`. The containing directory remains `root:404-probe` mode `0750`, so the low-privilege Agent can read the strict, secret-free tag list but cannot replace its directory entry or access the full sing-box configuration. On an unusual config layout, stale metadata, or missing/invalid metadata, discovery falls back to deterministic name order and the UI states that fallback.

For an existing Agent, first upgrade its binary through the Web UI, then run the matching version-pinned `install.sh selector-order /absolute/path/to/config.json` command on that Agent host. This verifies the installed build, preserves identity and service state, atomically installs the reviewed local helper, and creates the protected metadata. Afterward, refresh changed outbound order locally with `sudo 404-probe-install selector-order /absolute/path/to/config.json`; the next discovery interval picks it up without restarting the Agent.

The Clash API itself is not an exact substitute for this metadata. In the [official sing-box Clash API implementation](https://github.com/SagerNet/sing-box/blob/testing/experimental/clashapi/proxies.go), each group's `all` array comes from that group's own member list, while synthetic `GLOBAL.all` is built from all visible outbounds and then moves the default outbound ahead of its original position for dashboard compatibility. `/proxies` is serialized as an object. Consequently, neither surface reliably preserves the exact top-level selector order required here; the root-side narrow extraction remains the authoritative path.

404-probe does not support Clash API secrets. If the legacy `PROBE_404_SING_BOX_CLASH_SECRET` variable is present, only the sing-box integration fails closed; normal telemetry continues, and the Agent logs a fixed remediation message without reading or printing the value. Configure sing-box's Clash API without authentication and keep it bound to loopback.

After discovery, the authenticated Agent detail page shows the current selector and its allowlisted choices. v0.8.1 Server and Agent negotiate a dedicated outbound-only long-poll control lane which can carry only the fixed `singbox_selector_switch` operation. A v0.8.1 Agent talking to a v0.8.0 Server falls back to the existing 10-second Job claim lane; a v0.8.0 Agent continues to work with a v0.8.1 Server through that old lane.

The Server validates against the latest snapshot, then the Agent performs `GET /proxies`, validates the live selector and choice, sends the selector `PUT`, immediately reads back `GET /proxies`, and publishes an immediate verified snapshot. Re-selecting the current value is a successful no-op. SSE updates the detail page after the snapshot arrives. Not detected, authentication required, unavailable, stale, offline, paused, and revoked states disable control. This is not a generic shell, HTTP, configuration-editing, restart, scheduling, bulk-switch, or automatic-failover facility.

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

The read-only remote CLI accepts `online`, `offline`, or `revoked` for `--status`. Paused Agents remain visible through the authenticated Web UI.

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

Agent hostnames, internal addresses, probe targets, errors, and measurements are sensitive operational data even when they are not credentials. Do not expose remote output publicly or place the Control token in a browser; V0.7 Web authentication uses a separate password and server-side session boundary.

## Capacity runbook

V0.7 continues the existing behavior of keeping Probe Jobs and Results without automatic retention or purge. Fixed-interval schedules therefore grow the SQLite database continuously. Check capacity regularly on the Server host:

```bash
du -h "$PROBE_404_DB"

sqlite3 -readonly "$PROBE_404_DB" \
  "SELECT 'agents', COUNT(*) FROM agents
   UNION ALL SELECT 'schedules', COUNT(*) FROM probe_schedules
   UNION ALL SELECT 'jobs', COUNT(*) FROM probe_jobs
   UNION ALL SELECT 'results', COUNT(*) FROM probe_results;"
```

Back up the database before maintenance. V0.7 does not include a purge command or supported manual-deletion recipe; retention, archival, and downsampling remain future work.

## Control credential incident response

If the Control token may have leaked, treat the event as administrator credential compromise. Stop the Server, generate a new canonical token into a new `0600` regular file, replace the configured token file, and restart the Server so it loads the new token. The old token remains valid until that restart. Review Server access logs and probe activity without copying Authorization values into tickets or chat. V0.7 does not provide token rotation, multiple concurrent Control tokens, RBAC, or an audit-log subsystem.

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
