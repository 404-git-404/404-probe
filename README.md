# 404-probe

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

Stop the server and back up the database before opening it with the new binary. V1 and V2 databases are migrated to schema V3 on open; newer schemas are rejected.

```bash
sudo systemctl stop 404-probe-server
sudo cp -a /var/lib/404-probe/404-probe.db /var/lib/404-probe/404-probe.db.pre-v0.2
sudo ./404-probe-server agent list --db /var/lib/404-probe/404-probe.db
```

## Start the server

The control API is optional. Its token must be 32 random bytes encoded as unpadded base64url, stored in a private regular file.

```bash
install -d -m 700 "$PWD/run/server"
export PROBE_404_DB="$PWD/run/server/404-probe.db"
export PROBE_404_CONTROL_TOKEN_FILE="$PWD/run/server/control.token"
umask 077
head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n' > "$PROBE_404_CONTROL_TOKEN_FILE"
chmod 600 "$PROBE_404_CONTROL_TOKEN_FILE"

./404-probe-server serve \
  --listen :8080 \
  --db "$PROBE_404_DB" \
  --offline-timeout 30s \
  --control-token-file "$PROBE_404_CONTROL_TOKEN_FILE"
```

Use HTTPS at the public edge. Plain HTTP is intended only for localhost or isolated development.

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

## Optional control API

```bash
PROBE_404_CONTROL_TOKEN="$(cat "$PROBE_404_CONTROL_TOKEN_FILE")"
PROBE_404_CONTROL_BASE=http://127.0.0.1:8080
PROBE_404_CONTROL_JOB_ID="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"

curl --fail-with-body \
  -X PUT "$PROBE_404_CONTROL_BASE/api/v1/control/jobs/$PROBE_404_CONTROL_JOB_ID" \
  -H "Authorization: Bearer $PROBE_404_CONTROL_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "{\"agent_id\":\"$PROBE_404_AGENT_ID\",\"probe_type\":\"tcp_connect\",\"config\":{\"host\":\"example.com\",\"port\":443},\"timeout_ms\":5000,\"expires_in_seconds\":300}"

curl --fail-with-body \
  -H "Authorization: Bearer $PROBE_404_CONTROL_TOKEN" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/jobs/$PROBE_404_CONTROL_JOB_ID"

PROBE_404_CONTROL_SCHEDULE_ID="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"

curl --fail-with-body \
  -X PUT "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID" \
  -H "Authorization: Bearer $PROBE_404_CONTROL_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "{\"agent_id\":\"$PROBE_404_AGENT_ID\",\"name\":\"homepage\",\"probe_type\":\"http\",\"config\":{\"url\":\"https://example.com/\",\"method\":\"GET\",\"expected_status\":200},\"timeout_ms\":5000,\"interval_seconds\":60,\"enabled\":true}"

curl --fail-with-body \
  -H "Authorization: Bearer $PROBE_404_CONTROL_TOKEN" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID"

curl --fail-with-body \
  -X DELETE \
  -H "Authorization: Bearer $PROBE_404_CONTROL_TOKEN" \
  "$PROBE_404_CONTROL_BASE/api/v1/control/schedules/$PROBE_404_CONTROL_SCHEDULE_ID"
```

## Revoke an agent

```bash
kill "$PROBE_404_AGENT_PID"
wait "$PROBE_404_AGENT_PID" || true
./404-probe-server agent revoke "$PROBE_404_AGENT_ID" \
  --db "$PROBE_404_DB"
```
