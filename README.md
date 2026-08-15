```bash
go test ./...
go build -o 404-probe-agent ./cmd/agent
go build -o 404-probe-server ./cmd/server

mkdir -p build/linux-amd64 build/linux-arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-server ./cmd/server

./404-probe-server serve \
  --listen :8080 \
  --db /var/lib/404-probe/404-probe.db \
  --offline-timeout 30s

PROBE_404_AGENT_OUTPUT="$(./404-probe-server agent add singapore-01 --db /var/lib/404-probe/404-probe.db)"
printf '%s\n' "$PROBE_404_AGENT_OUTPUT"
PROBE_404_AGENT_ID="$(printf '%s\n' "$PROBE_404_AGENT_OUTPUT" | sed -n 's/^Agent ID: //p')"
PROBE_404_TOKEN="$(printf '%s\n' "$PROBE_404_AGENT_OUTPUT" | sed -n 's/^Token: //p')"
./404-probe-server agent list --db /var/lib/404-probe/404-probe.db

sudo install -m 600 /dev/null /etc/404-probe-agent.env
sudo sh -c "printf '%s\n' 'PROBE_404_SERVER=http://127.0.0.1:8080' 'PROBE_404_AGENT_ID=$PROBE_404_AGENT_ID' 'PROBE_404_TOKEN=$PROBE_404_TOKEN' 'PROBE_404_STATE=/var/lib/404-probe-agent/epoch' > /etc/404-probe-agent.env"
set -a
. /etc/404-probe-agent.env
set +a
./404-probe-agent --interval 10s --timeout 8s --allow-insecure-http

./404-probe-server agent revoke "$PROBE_404_AGENT_ID" --db /var/lib/404-probe/404-probe.db
```
