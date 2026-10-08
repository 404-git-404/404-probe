package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// This meters real loopback HTTP, not mirrored scheduling formulas. The existing
// instance timing seams scale outbound/report/job intervals by 1/1000. Control
// fixture holds empty requests 27ms vs the real Server's 27s; gate 250ms and
// HTTP deadlines remain unscaled and are explicitly not daily extrapolations.
func TestP3ActualHTTPRequestsByLane(t *testing.T) {
	for _, mode := range []string{"disabled", "legacy_secret", "stable", "changes", "unreachable_recovery", "publish_failure"} {
		t.Run(mode, func(t *testing.T) {
			type record struct {
				Path     string                     `json:"path"`
				Millis   int64                      `json:"ms"`
				Status   int                        `json:"status"`
				Snapshot *protocol.OutboundSnapshot `json:"snapshot,omitempty"`
			}
			var mu sync.Mutex
			counts := map[string]int{}
			records := []record{}
			start := time.Now()
			orderPath := filepath.Join(t.TempDir(), "order.json")
			if err := os.WriteFile(orderPath, []byte(`{"selectors":["proxy","other"]}`), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				counts[req.URL.Path]++
				n := counts[req.URL.Path]
				mu.Unlock()
				status := http.StatusNoContent
				var body any
				var snapshot *protocol.OutboundSnapshot
				switch req.URL.Path {
				case "/proxies":
					status = http.StatusOK
					if mode == "unreachable_recovery" && n <= 4 {
						status = http.StatusServiceUnavailable
					} else {
						current := "hk"
						if mode == "changes" && n >= 3 {
							current = "jp"
						}
						proxies := map[string]any{"proxy": map[string]any{"type": "Selector", "name": "proxy", "now": current, "all": []string{"hk", "jp"}}}
						if mode == "changes" {
							proxies["other"] = map[string]any{"type": "Selector", "name": "other", "now": "hk", "all": []string{"hk", "jp"}}
							if n == 5 {
								if err := os.WriteFile(orderPath, []byte(`{"selectors":["other","proxy"]}`), 0600); err != nil {
									t.Error(err)
								}
							}
						}
						body = map[string]any{"proxies": proxies}
					}
				case "/api/v1/agent/outbounds":
					snapshot = &protocol.OutboundSnapshot{}
					if err := json.NewDecoder(req.Body).Decode(snapshot); err != nil {
						t.Error(err)
					}
					status = http.StatusOK
					body = map[string]bool{"accepted": true}
					if mode == "publish_failure" && n <= 3 {
						status = http.StatusServiceUnavailable
						body = nil
					}
				case "/api/v1/report":
					status = http.StatusOK
					body = protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{InteractiveControl: true, ManagementReport: true, AgentUpgrade: true}}
				case "/api/v1/agent/control/claim":
					select {
					case <-req.Context().Done():
						return
					case <-time.After(27 * time.Millisecond):
					}
				}
				mu.Lock()
				records = append(records, record{req.URL.Path, time.Since(start).Milliseconds(), status, snapshot})
				mu.Unlock()
				w.WriteHeader(status)
				if body != nil {
					_ = json.NewEncoder(w).Encode(body)
				}
			}))
			defer server.Close()
			endpoint := server.URL
			if mode == "disabled" {
				endpoint = ""
			}
			runner, err := NewWithReportCollector(Config{ServerURL: server.URL, AgentID: "p3-local", Token: "synthetic", Interval: 10 * time.Millisecond,
				JobInterval: 10 * time.Millisecond, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"),
				ClashAPIURL: endpoint, LegacyClashSecretConfigured: mode == "legacy_secret", OutboundInterval: 60 * time.Millisecond},
				slog.New(slog.NewTextHandler(io.Discard, nil)), reportCollectorFunc(staticReportCollector))
			if err != nil {
				t.Fatal(err)
			}
			runner.outboundHeartbeatInterval = 300 * time.Millisecond
			if mode == "changes" {
				runner.config.SelectorOrderPath = orderPath
			}
			runner.outboundRetrySteps = []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 60 * time.Millisecond, 300 * time.Millisecond}
			runner.outboundJitter = func(delay time.Duration) time.Duration { return delay }
			ctx, cancel := context.WithTimeout(context.Background(), 650*time.Millisecond)
			defer cancel()
			// Same production workers; empty claims never contact the updater socket.
			var workers sync.WaitGroup
			workers.Add(2)
			go func() { defer workers.Done(); runner.runUpgradeWorker(ctx) }()
			go func() { defer workers.Done(); runner.runRemovalWorker(ctx) }()
			if err := runner.Run(ctx); err != nil {
				t.Fatal(err)
			}
			workers.Wait()
			mu.Lock()
			defer mu.Unlock()
			encoded, _ := json.Marshal(map[string]any{"mode": mode, "duration_ms": 650, "time_scale": 1000, "counts": counts, "requests": records})
			t.Logf("P3_HTTP_METER %s", encoded)
			if counts["/api/v1/report"] < 40 {
				t.Fatalf("telemetry unexpectedly gated: %v", counts)
			}
			if counts["/api/v1/agent/jobs/claim"] < 5 || counts["/api/v1/agent/country-code-lookups/claim"] < 5 || counts["/api/v1/agent/upgrades/claim"] < 5 {
				t.Fatalf("ordinary lanes not exercised: %v", counts)
			}
			if counts["/api/v1/agent/removals/claim"] != 0 {
				t.Fatal("removal opted out but claimed")
			}
			if mode == "disabled" || mode == "legacy_secret" {
				if counts["/proxies"] != 0 || counts["/api/v1/agent/outbounds"] != 0 || counts["/api/v1/agent/control/claim"] != 0 {
					t.Fatalf("disabled integration probed: %v", counts)
				}
			} else if mode == "stable" {
				if counts["/proxies"] < 7 || counts["/api/v1/agent/outbounds"] > 4 || counts["/api/v1/agent/outbounds"] < 2 {
					t.Fatalf("stable heartbeat/dedup mismatch: %v", counts)
				}
			} else if mode == "changes" || mode == "unreachable_recovery" {
				changed := false
				orderChanged := false
				firstPublished := int64(-1)
				choiceBeforeHeartbeat, orderBeforeHeartbeat := false, false
				for _, r := range records {
					if r.Snapshot != nil && r.Snapshot.Available {
						if firstPublished < 0 {
							firstPublished = r.Millis
						}
						if mode != "changes" {
							changed = true
						}
						for _, selector := range r.Snapshot.Selectors {
							if selector.Name == "proxy" && selector.Current == "jp" {
								changed = true
								if r.Millis-firstPublished < 300 {
									choiceBeforeHeartbeat = true
								}
							}
						}
						if len(r.Snapshot.Selectors) == 2 && r.Snapshot.Selectors[0].Name == "other" && r.Snapshot.OrderSource == "config" {
							orderChanged = true
							if r.Millis-firstPublished < 300 {
								orderBeforeHeartbeat = true
							}
						}
					}
				}
				if !changed {
					t.Fatal("recovery/choice update not published")
				}
				if mode == "changes" && !orderChanged {
					t.Fatal("selector order change not published")
				}
				if mode == "changes" && (!choiceBeforeHeartbeat || !orderBeforeHeartbeat) {
					t.Fatal("content changes only appeared after heartbeat deadline")
				}
			} else if counts["/api/v1/agent/outbounds"] < 4 {
				t.Fatal("failed publication not retried")
			}
		})
	}
}
