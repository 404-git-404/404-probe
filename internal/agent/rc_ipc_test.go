package agent

import (
	"404-probe/internal/updater"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestRCHelperNegotiationDoesNotTrustOldV2(t *testing.T) {
	for _, tc := range []struct {
		version     string
		aware, want bool
		calls       int
	}{{"v1.0.3-rc.1", false, false, 2}, {"v1.0.3-rc.1", true, true, 2}, {"v1.0.2", false, true, 1}} {
		t.Run(tc.version+map[bool]string{true: "-new", false: "-old"}[tc.aware], func(t *testing.T) {
			f, e := os.CreateTemp("", "404-rc-ipc-")
			if e != nil {
				t.Fatal(e)
			}
			socket := f.Name()
			f.Close()
			os.Remove(socket)
			l, e := net.Listen("unix", socket)
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			defer os.Remove(socket)
			done := make(chan error, 1)
			go func() {
				for i := 0; i < tc.calls; i++ {
					c, e := l.Accept()
					if e != nil {
						done <- e
						return
					}
					b, e := io.ReadAll(c)
					if e != nil {
						c.Close()
						done <- e
						return
					}
					var r updater.Request
					e = json.Unmarshal(b, &r)
					if e != nil {
						c.Close()
						done <- e
						return
					}
					accepted := r.Action == updater.ActionCapabilities || tc.aware && r.Action == updater.ActionRCCapabilities
					e = json.NewEncoder(c).Encode(updater.Response{Accepted: accepted, Capabilities: updater.UpdaterCapabilities{UpgradeV2: accepted}})
					c.Close()
					if e != nil {
						done <- e
						return
					}
				}
				done <- nil
			}()
			r := &Runner{config: Config{AgentVersion: tc.version}}
			r.upgradeV2ServerSupported.Store(true)
			r.runUpgradeCycle(context.Background(), upgradeHTTPClient{}, updater.Client{Socket: socket, Timeout: time.Second})
			if got := r.upgradeV2UpdaterSupported.Load(); got != tc.want {
				t.Fatal(got, tc.want)
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing RC probe")
			}
		})
	}
}
