package server

import (
	"404-probe/internal/auth"
	"404-probe/internal/storage"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Explicit opt-in: real SQLite, report authentication and loopback HTTP. Target
// literals are configuration fixtures; no DNS or outgoing probes run here.
func TestDashboardPerformanceFixture(t *testing.T) {
	if os.Getenv("V110_DASHBOARD_PERF") != "1" {
		t.Skip("opt-in interface measurement")
	}
	for _, n := range []int{50, 100, 200} {
		if only := os.Getenv("V110_PERF_COUNT"); only != "" && only != fmt.Sprint(n) {
			continue
		}
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "performance.db")
			s, err := storage.Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Shutdown()
			now := time.Now()
			a.now = func() time.Time { return now }
			ids := []string{}
			tokens := map[string]string{}
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("%032x", i+1)
				ids = append(ids, id)
				token, hash, err := auth.NewToken()
				if err != nil {
					t.Fatal(err)
				}
				tokens[id] = token
				if err := s.AddAgent(t.Context(), id, fmt.Sprintf("synthetic-%03d", i), hash, now); err != nil {
					t.Fatal(err)
				}
				report := userReport(id, 1)
				report.CollectedAt = now.UnixMilli()
				if w := postWithHeaders(t, a, token, "/api/v1/report", report, userHeaders); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				configureUserTargets(t, s, id, now.Add(-2*time.Hour), [3]string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}, 443)
				if _, err := s.PutAgentPlan(t.Context(), storage.AgentPlan{AgentID: id, Currency: "USD", RenewalPrice: "12", RenewalPricePeriod: "quarterly", RenewalDate: "2026-11-01", Timezone: "UTC", TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2026-10-01", QuotaValue: "100", QuotaUnit: "GB"}, nil, now); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			stmt, err := tx.Prepare(`INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,1,50,50,0,50,1,1,1,100,100,?,?)`)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				for m := 0; m < 1440; m++ {
					if _, err := stmt.Exec(id, now.Truncate(time.Minute).Add(time.Duration(m-1440)*time.Minute).UnixMilli(), m*1000, m*2000); err != nil {
						t.Fatal(err)
					}
				}
			}
			stmt.Close()
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			cfgs := seedDashboardSoak(t, a, s, ids, tokens, now)
			for i, id := range ids {
				if i%7 == 0 {
					if _, err := db.Exec(`UPDATE agent_plans SET quota_bytes=100,usage_bytes=85 WHERE agent_id=?`, id); err != nil {
						t.Fatal(err)
					}
				}
				if i%17 == 0 {
					if _, err := db.Exec(`UPDATE agent_plans SET quota_bytes=0 WHERE agent_id=?`, id); err != nil {
						t.Fatal(err)
					}
				}
				if i%13 == 0 {
					if _, err := db.Exec(`UPDATE agent_plans SET cycle_end=? WHERE agent_id=?`, now.Add(-time.Hour).UnixMilli(), id); err != nil {
						t.Fatal(err)
					}
				}
			}
			if os.Getenv("V110_SOAK_ROOT") != "" {
				prepareDashboardSoakClock(t, a, ids, tokens, cfgs, now)
			}
			// The populated fixture now enters the same startup warmup as a
			// production CleanupLoop, without waiting for its completion.
			a.startDashboardNetworkWarmup()
			server := httptest.NewServer(dashboardSoakHandler(t, a, n))
			defer server.Close()
			client := server.Client()
			samples := []float64{}
			size := 0
			var decoded dashboardResponse
			for i := 0; i < 11; i++ {
				r, _ := http.NewRequest("GET", server.URL+"/api/v1/web/dashboard", nil)
				addTestWebSession(t, a, r)
				start := dashboardPerfMilliseconds()
				res, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 {
					t.Fatal(res.StatusCode, err, string(body))
				}
				samples = append(samples, dashboardPerfMilliseconds()-start)
				size = len(body)
				if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Items) != n {
					t.Fatal(err, len(decoded.Items))
				}
			}
			alerts := 0
			for _, item := range decoded.Items {
				if item.Network.Active != nil && *item.Network.Active {
					alerts++
				}
				if !item.Network.Monitored || !item.Network.Known {
					t.Fatal("valid network targets absent", item.AgentID, item.Network)
				}
			}
			if alerts == 0 {
				t.Fatal("fixture has no actual red quality alerts")
			}
			t.Logf("V110_QUALITY red_devices=%d missing_devices=%d lanes=3 window_minutes=90", alerts, (n+4)/5)
			warm := append([]float64(nil), samples[1:]...)
			sort.Float64s(warm)
			var rows int
			if err := db.QueryRow(`SELECT count(*) FROM minute_metrics`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			result := map[string]any{"devices": n, "cold_ms": samples[0], "warm_median_ms": (warm[4] + warm[5]) / 2, "warm_p95_ms": warm[9], "response_bytes": size, "requests": 1, "minute_rows": rows, "ipv4_tcp_targets_per_device": 3, "samples_ms": samples}
			encoded, _ := json.Marshal(result)
			t.Log("V110_PERF " + string(encoded))
			dashboardSoak(t, a, server, ids, tokens, cfgs)
			dashboardBrowserPerformance(t, a, server, ids, tokens, now, n, rows)
		})
	}
}
