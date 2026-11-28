package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestDeviceScoreSyntheticTimings(t *testing.T) {
	if os.Getenv("S3C_SYNTHETIC") != "1" {
		t.Skip("set S3C_SYNTHETIC=1 for the SQLite score timing fixture")
	}
	for _, devices := range []int{50, 200} {
		t.Run(fmt.Sprintf("devices_%d", devices), func(t *testing.T) {
			testDeviceScoreSyntheticFleet(t, devices)
		})
	}
}

func testDeviceScoreSyntheticFleet(t *testing.T, devices int) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 11, 23, 13, 45, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "scores.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()
	app.now = func() time.Time { return now }
	id, token, err := seedNetworkStatusTemplate(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	config := qualityServerConfig(t, app, store, id, token)
	var samples []protocol.QualitySample
	for i := range 12 {
		at := now.Add(time.Duration(30*i+2) * time.Second)
		sample := userTCPSample(t, config, "telecom", int64(i+1), at, 42+float64(i)/2, "success")
		sample.DurationMS += 10
		sample.FinishedAt += 10
		details, err := protocol.DecodeQualityDetails(sample.Details)
		if err != nil {
			t.Fatal(err)
		}
		details.ConnectTimeoutMS = protocol.QualityTCPDefaultTimeoutMS
		sample.Details = details.Encode()
		samples = append(samples, sample)
	}
	app.now = func() time.Time { return now.Add(6 * time.Minute) }
	result := postAgentUpgrade(t, app, token, "/api/v1/agent/network-quality",
		protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: samples})
	var ack protocol.QualityBatchResponse
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &ack) != nil || len(ack.Results) != len(samples) {
		t.Fatalf("quality sample: %d %s", result.Code, result.Body)
	}
	for _, item := range ack.Results {
		if item[2] != "committed" {
			t.Fatalf("seed receipt: %v", item)
		}
	}
	app.now = func() time.Time { return now }
	seedDeviceScoreFleet(t, path, id, now, devices)
	if _, err := store.RefreshNetworkFleet(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("devices=%d score refresh started", devices)
	start := time.Now()
	snapshot, err := app.calculateDeviceScores(ctx, now)
	elapsed := time.Since(start)
	t.Logf("devices=%d score refresh=%s", devices, elapsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != devices {
		t.Fatalf("found %d devices, want %d", len(snapshot.Items), devices)
	}
	for device, result := range snapshot.Items {
		if len(result.Network.Lines) != 3 || result.Network.Score == nil || result.Overall.Score == nil {
			t.Fatalf("device %s missing synthetic score: %+v", device, result)
		}
		for _, line := range result.Network.Lines {
			if line.Score == nil {
				t.Fatalf("device %s missing score for synthetic line: %+v", device, line)
			}
		}
	}
	app.deviceScores.Store(snapshot)
	response := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/scores")
	if response.Code != 200 {
		t.Fatalf("cached score response = %d", response.Code)
	}
}

func seedDeviceScoreFleet(t *testing.T, database, templateID string, now time.Time, devices int) {
	t.Helper()
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	query := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	var wire []byte
	var payload string
	err = tx.QueryRow(`SELECT b.data,t.payload FROM quality_buckets b
		JOIN quality_series s ON s.id=b.series_id JOIN quality_targets t ON s.target_id=t.id
		WHERE b.resolution=3600000 AND s.agent_id=? AND s.semantics=? LIMIT 1`,
		templateID, protocol.QualityTCPLossSemantics).Scan(&wire, &payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < devices; i++ {
		id := fmt.Sprintf("%032x", i)
		query(`INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`,
			id, id, id, now.Add(-31*24*time.Hour).UnixMilli(), now.UnixMilli())
	}
	query(`INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,objects_configured)
		SELECT id,1,1,0,'[]',created_at,1 FROM agents WHERE true
		ON CONFLICT(agent_id) DO UPDATE SET objects_configured=1`)
	for index, slot := range []string{"telecom", "unicom", "mobile"} {
		query(`INSERT INTO quality_slot_settings(agent_id,slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,ipv6_tcp,ipv6_icmp,updated_revision,position,show_on_card)
			SELECT id,?1,'custom','中国',?1,'synthetic.example',443,1,1,0,0,0,1,?2,1 FROM agents WHERE true
			ON CONFLICT(agent_id,slot) DO UPDATE SET show_on_card=1,position=excluded.position`, slot, index)
		query(`INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at)
			SELECT id||?1,id,'score-case-'||?1,json_set(?2,'$.slot',?1),created_at FROM agents`, slot, payload)
		query(`INSERT OR IGNORE INTO quality_intervals(agent_id,slot,family,protocol,revision,target_id,start_at,config_from,v1_authorized)
			SELECT agent_id,?1,'ipv4','tcp',9,id,created_at,1,0 FROM quality_targets
			WHERE fingerprint='score-case-'||?1 AND (agent_id<>?2 OR ?1<>'telecom')`, slot, templateID)
	}
	query(`INSERT OR IGNORE INTO quality_series(agent_id,target_id,semantics)
		SELECT agent_id,id,? FROM quality_targets WHERE id IN (SELECT target_id FROM quality_intervals WHERE end_at IS NULL)`, protocol.QualityTCPLossSemantics)
	for _, resolution := range []time.Duration{5 * time.Minute, time.Hour} {
		query(`WITH RECURSIVE score_buckets(at) AS (VALUES(?1) UNION ALL SELECT at+?2 FROM score_buckets WHERE at+?2<?3)
			INSERT OR IGNORE INTO quality_buckets SELECT s.id,?2,score_buckets.at,?4
			FROM quality_series s CROSS JOIN score_buckets`, now.Add(-storage.DeviceScoreWindow).Truncate(resolution).UnixMilli(),
			resolution.Milliseconds(), now.Truncate(resolution).UnixMilli(), wire)
	}
	query(`WITH RECURSIVE minutes(at) AS (VALUES(?1) UNION ALL SELECT at+60000 FROM minutes WHERE at+60000<?2)
		INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total)
		SELECT id,at,3,30,75,0,90,0,0,0,0,0,0,0 FROM agents CROSS JOIN minutes`,
		now.Add(-storage.DeviceScoreWindow).UnixMilli(), now.UnixMilli())
	query(`INSERT INTO agent_presence_events(agent_id,at,kind) SELECT id,?,'online' FROM agents`, now.Add(-30*24*time.Hour).UnixMilli())
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
