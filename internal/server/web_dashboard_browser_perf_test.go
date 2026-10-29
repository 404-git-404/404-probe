package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in bridge exists only in the test binary. It uses the same real
// SQLite/report fixture as the interface benchmark; there are no public routes,
// outgoing probes, production database paths or persisted agent credentials.
func dashboardBrowserPerformance(t *testing.T, a *App, server *httptest.Server, ids []string, tokens map[string]string, now time.Time, count, rows int) {
	root := os.Getenv("V110_BROWSER_PERF_ROOT")
	if root == "" {
		return
	}
	if !filepath.IsAbs(root) {
		t.Fatal("browser performance root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	a.webAuth.publicOrigin, _ = url.Parse(server.URL)
	a.webAuth.secureCookie = false
	a.now = time.Now
	write := func(name string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(root, name+".tmp")
		if err := os.WriteFile(tmp, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprint(count)
	write("ready-"+prefix+".json", map[string]any{"base": server.URL, "devices": count, "minute_rows": rows, "targets_per_device": 3, "id": ids[0]})
	defer os.Remove(filepath.Join(root, "ready-"+prefix+".json"))
	last := uint64(1)
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser performance driver did not finish")
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-tick.C:
			raw, err := os.ReadFile(filepath.Join(root, "command-"+prefix+".json"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			var cmd struct {
				Command  string  `json:"command"`
				Sequence uint64  `json:"sequence"`
				CPU      float64 `json:"cpu"`
			}
			if json.Unmarshal(raw, &cmd) != nil {
				continue
			}
			if cmd.Command == "done" {
				return
			}
			if cmd.Sequence <= last {
				continue
			}
			report := userReport(ids[0], cmd.Sequence)
			report.CollectedAt = time.Now().UnixMilli()
			report.CPUPercent = cmd.CPU
			posted := time.Now().UnixMilli()
			response := postWithHeaders(t, a, tokens[ids[0]], "/api/v1/report", report, userHeaders)
			if response.Code != 200 {
				t.Fatal("synthetic authenticated report failed", response.Code, response.Body.String())
			}
			last = cmd.Sequence
			write("ack-"+prefix+".json", map[string]any{"sequence": last, "posted_at": posted, "status": response.Code})
		}
	}
}
