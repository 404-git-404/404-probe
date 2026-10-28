//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/updater"
)

func TestTwoDigitAgentStartsOnlyStrictlyNewerLocalTarget(t *testing.T) {
	for _, row := range []struct {
		from, to string
		starts   int32
	}{{"v1.0.9", "v1.0.10", 1}, {"v1.0.10", "v1.0.9", 0}, {"v1.0.10", "v1.0.10", 0}} {
		t.Run(row.from+"-"+row.to, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "u.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			var starts atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					connection, err := listener.Accept()
					if err != nil {
						return
					}
					body, _ := io.ReadAll(connection)
					var request updater.Request
					if json.Unmarshal(body, &request) == nil && request.Action == updater.ActionStart {
						starts.Add(1)
					}
					_ = json.NewEncoder(connection).Encode(updater.Response{Accepted: true, State: updater.State{OperationID: strings.Repeat("a", 32), TargetVersion: row.to, Status: "claimed"}})
					_ = connection.Close()
				}
			}()
			defer func() { listener.Close(); <-done }()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(protocol.UpgradeOperation{ProtocolVersion: 1, OperationID: strings.Repeat("a", 32), FromVersion: row.from, TargetVersion: row.to, Status: "claimed"})
			}))
			defer server.Close()
			runner := &Runner{config: Config{AgentVersion: row.from}}
			runner.upgradeAPISupported.Store(true)
			if !runner.runUpgradeCycle(context.Background(), upgradeHTTPClient{baseURL: server.URL, client: server.Client()}, updater.Client{Socket: socket, Timeout: time.Second}) {
				t.Fatal("claim ignored")
			}
			if starts.Load() != row.starts {
				t.Fatal("unexpected Start calls", starts.Load())
			}
			if runner.config.AgentVersion != row.from {
				t.Fatal("identity changed")
			}
		})
	}
}
