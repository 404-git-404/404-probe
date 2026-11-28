package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"404-probe/internal/updater"
)

func TestRCHelperNegotiationDoesNotTrustOldV2(t *testing.T) {
	for _, tc := range []struct {
		version     string
		aware, want bool
		calls       int
	}{{"v1.0.3-rc.1", false, false, 2}, {"v1.0.3-rc.1", true, true, 2}, {"v1.0.2", false, true, 1}, {"v1.0.3", false, false, 2}, {"v1.0.3", true, true, 2}} {
		t.Run(tc.version+map[bool]string{true: "-new", false: "-old"}[tc.aware], func(t *testing.T) {
			f, err := os.CreateTemp("", "404-rc-ipc-")
			if err != nil {
				t.Fatal(err)
			}
			socket := f.Name()
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(socket); err != nil {
				t.Fatal(err)
			}
			l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan error, 1)
			var actions []updater.Action
			answer := func(conn *net.UnixConn) error {
				defer conn.Close()
				if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
					return err
				}
				// The request already has a JSON boundary. The fixture can answer
				// as soon as it is decoded, independent of the peer's EOF timing.
				var request updater.Request
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					return err
				}
				actions = append(actions, request.Action)
				accepted := request.Action == updater.ActionCapabilities || tc.aware && request.Action == updater.ActionRCCapabilities
				if err := json.NewEncoder(conn).Encode(updater.Response{Accepted: accepted, Capabilities: updater.UpdaterCapabilities{UpgradeV2: accepted}}); err != nil {
					return err
				}
				return conn.CloseWrite()
			}
			go func() {
				for {
					conn, err := l.AcceptUnix()
					if errors.Is(err, net.ErrClosed) {
						done <- nil
						return
					}
					if err == nil {
						err = answer(conn)
					}
					if err != nil {
						done <- err
						return
					}
				}
			}()
			r := &Runner{config: Config{AgentVersion: tc.version}}
			r.upgradeV2ServerSupported.Store(true)
			r.runUpgradeCycle(t.Context(), upgradeHTTPClient{}, updater.Client{Socket: socket, Timeout: time.Second})
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			wantActions := []updater.Action{updater.ActionCapabilities, updater.ActionRCCapabilities}[:tc.calls]
			if !slices.Equal(actions, wantActions) {
				t.Fatalf("probes = %v, want %v", actions, wantActions)
			}
			if got := r.upgradeV2UpdaterSupported.Load(); got != tc.want {
				t.Fatal(got, tc.want)
			}
			// Claim protocol 2 needs only the plain v2 answer, never the RC proof.
			if got := r.upgradeV2ClaimSupported.Load(); !got {
				t.Fatal("claim protocol 2 unavailable with a v2 Updater")
			}
		})
	}
}
