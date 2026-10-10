//go:build linux

package agent

import (
	"404-probe/internal/updater"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalMigrationHealthNeedsAcceptedActualVersionAndWorksWithoutServerV2(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "report-not-accepted", true: "accepted-real-version"}[accepted], func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "updater.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan updater.Request, 2)
			done := make(chan error, 1)
			go func() {
				for i := 0; i < 1+map[bool]int{false: 0, true: 1}[accepted]; i++ {
					connection, err := listener.Accept()
					if err != nil {
						done <- err
						return
					}
					body, err := io.ReadAll(connection)
					if err != nil {
						connection.Close()
						done <- err
						return
					}
					var request updater.Request
					if err := json.Unmarshal(body, &request); err != nil {
						connection.Close()
						done <- err
						return
					}
					requests <- request
					err = json.NewEncoder(connection).Encode(updater.Response{Accepted: true, Capabilities: updater.UpdaterCapabilities{UpgradeV2: true}, State: updater.State{LocalMigration: true, OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v1.0.1", Status: "health_check"}})
					connection.Close()
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			var claims atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { claims.Add(1); w.WriteHeader(204) }))
			defer server.Close()
			runner := &Runner{config: Config{AgentVersion: "v1.0.1"}}
			runner.versionReportAccepted.Store(accepted)
			if !runner.runUpgradeCycle(context.Background(), upgradeHTTPClient{baseURL: server.URL, client: server.Client()}, updater.Client{Socket: socket, Timeout: time.Second}) {
				t.Fatal("local migration ignored")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("IPC did not finish")
			}
			capability := <-requests
			if capability.ProtocolVersion != 2 || capability.Action != updater.ActionCapabilities {
				t.Fatal("capability is not explicit IPC2", capability)
			}
			if accepted {
				healthy := <-requests
				if healthy.Action != updater.ActionHealthy || healthy.ProtocolVersion != 1 || healthy.TargetVersion != "v1.0.1" || healthy.OperationID != "0123456789abcdef0123456789abcdef" {
					t.Fatal("wrong health confirmation", healthy)
				}
			}
			if claims.Load() != 0 {
				t.Fatal("local migration required a new Server protocol")
			}
		})
	}
}
