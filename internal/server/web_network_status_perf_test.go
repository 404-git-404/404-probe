package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestNetworkStatusSyntheticTimings(t *testing.T) {
	if os.Getenv("S3A_SYNTHETIC") != "1" {
		t.Skip("set S3A_SYNTHETIC=1 for the 200-device SQLite timing fixture")
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 13, 47, 15, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "synthetic.db")
	s, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := NewApp(s, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	a.now = func() time.Time { return now }
	id, token, err := seedNetworkStatusTemplate(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := qualityServerConfig(t, a, s, id, token)
	var samples []protocol.QualitySample
	for i := range 12 {
		at := now.Add(time.Duration(30*i+2) * time.Second)
		sample := userTCPSample(t, cfg, "telecom", int64(1+i), at, float64(40+i)*1.25, "success")
		sample.DurationMS += 10
		sample.FinishedAt += 10
		samples = append(samples, sample)
	}
	a.now = func() time.Time { return now.Add(6 * time.Minute) }
	batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: samples}
	w := postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", batch)
	var ack protocol.QualityBatchResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &ack) != nil || len(ack.Results) != len(samples) {
		t.Fatalf("seed batch: %d %s", w.Code, w.Body)
	}
	for _, item := range ack.Results {
		if item[2] != "committed" {
			t.Fatalf("seed receipt: %v", item)
		}
	}
	a.now = func() time.Time { return now }
	seedNetworkStatusFleet(t, path, id, now)
	warmStart := time.Now()
	refreshed, err := s.RefreshNetworkFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initial rollups: %d series-days prepared in %s", refreshed, time.Since(warmStart))
	rangeQuery := func(d time.Duration) string {
		return fmt.Sprintf("start=%d&end=%d", now.Add(-d).UnixMilli(), now.UnixMilli())
	}
	historyPath := webAgentPathPrefix + id + "/network-quality/range?slot=telecom&family=ipv4&protocol=tcp&"
	for _, trial := range []struct {
		name, url string
		fleet     bool
		limit     time.Duration
	}{
		{"range-7d", historyPath + rangeQuery(7*24*time.Hour), false, 0},
		{"range-30d", historyPath + rangeQuery(30*24*time.Hour), false, 0},
		{"fleet-200-24h", networkFleetPath + "?" + rangeQuery(24*time.Hour), true, time.Second},
		{"fleet-200-30d", networkFleetPath + "?" + rangeQuery(30*24*time.Hour), true, 2 * time.Second},
	} {
		t.Run(trial.name, func(t *testing.T) {
			var elapsed []time.Duration
			var size int
			for range 3 {
				start := time.Now()
				resp := webAgentResponse(t, a, http.MethodGet, trial.url)
				elapsed = append(elapsed, time.Since(start))
				if resp.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", trial.name, resp.Code, resp.Body)
				}
				size = resp.Body.Len()
				if trial.fleet {
					var out storage.NetworkFleet
					if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || len(out.Machines) != 200 || len(out.Machines[0].Lines) != 12 {
						t.Fatalf("machines=%d err=%v", len(out.Machines), err)
					}
				} else {
					var out storage.NetworkHistory
					if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || out.Points[0].Samples == 0 {
						t.Fatalf("history samples=%d err=%v", out.Stats.Samples, err)
					}
				}
			}
			sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
			t.Logf("min=%s median=%s max=%s body=%d bytes", elapsed[0], elapsed[1], elapsed[2], size)
			if trial.limit > 0 && elapsed[len(elapsed)-1] > trial.limit {
				t.Errorf("fleet request exceeded %s", trial.limit)
			}
		})
	}
}

func seedNetworkStatusTemplate(ctx context.Context, s *storage.Store, now time.Time) (id, token string, err error) {
	id, err = auth.NewID()
	if err != nil {
		return
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		return id, token, err
	}
	err = s.AddAgent(ctx, id, "Network timing template", hash, now.Add(-31*24*time.Hour))
	return id, token, err
}

func seedNetworkStatusFleet(t *testing.T, path, firstAgent string, now time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var wire []byte
	if err := tx.QueryRow(`SELECT b.data FROM quality_buckets b JOIN quality_series s ON s.id=b.series_id
		WHERE s.agent_id=? AND b.resolution=3600000 LIMIT 1`, firstAgent).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 200; i++ {
		id := fmt.Sprintf("%032x", i)
		if _, err := tx.Exec(`INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`,
			id, fmt.Sprintf("Network timing %03d", i), id, now.Add(-31*24*time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	for _, slot := range protocol.QualitySlotOrder() {
		for _, family := range []string{"ipv4", "ipv6"} {
			for _, proto := range []string{"tcp", "icmp"} {
				lane := slot + "/" + family + "/" + proto
				// Keep the real template target for the range endpoint. Each
				// synthetic line uses the same encoded histogram as the template.
				_, err := tx.Exec(`INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at)
					SELECT id||?,id,?,json_object('slot',?,'family',?,'protocol',?,'host','synthetic.example','source','user'),created_at
					FROM agents`, lane, lane, slot, family, proto)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO quality_series(agent_id,target_id,semantics)
		SELECT agent_id,id,? FROM quality_targets`, protocol.QualityStagedSemantics); err != nil {
		t.Fatal(err)
	}
	for _, series := range []struct{ resolution, n int64 }{{60000, 1440}, {300000, 2016}, {3600000, 720}} {
		res := series.resolution
		edge := now.UnixMilli() / res * res
		// All devices have source data at the non-aligned 24-hour window
		// edges, where the query must use 1-minute and 5-minute buckets.
		upper := edge + res
		if res == 3600000 {
			upper = edge
		}
		_, err := tx.Exec(`WITH RECURSIVE stamp(at) AS (
				VALUES(?1) UNION ALL SELECT at+?2 FROM stamp WHERE at+?2<?3
			) INSERT OR IGNORE INTO quality_buckets SELECT s.id,?2,stamp.at,?4
			FROM quality_series s CROSS JOIN stamp
			WHERE ?2=3600000 OR s.agent_id=?5 OR
				ABS(stamp.at-?6)<=3600000 OR ABS(stamp.at-(?6-86400000))<=3600000`,
			edge-series.n*res, res, upper, wire, firstAgent, now.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
