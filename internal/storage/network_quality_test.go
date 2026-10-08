package storage

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func qualityTestResolver(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
	host := c.Host
	if family == "ipv6" {
		host = "::1"
	}
	return protocol.QualityTarget{Host: host, Port: c.Port, EndpointVersion: host + family}, nil
}
func qualityFixture(t *testing.T, path string) (*Store, string, protocol.QualityConfig, time.Time) {
	t.Helper()
	s, id, _ := testStore(t, path)
	t.Cleanup(func() { s.Close() })
	now := time.Unix(200000, 300000000)
	r := validReport(id, 1, "quality_session", "boot", 1, 1, 1)
	r.NetworkQuality = true
	if _, ok, _, err := s.ProcessReport(context.Background(), id, r, now); err != nil || !ok {
		t.Fatalf("register %v %v", ok, err)
	}
	choices := []protocol.QualityChoice{}
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "manual", Protocol: "tcp", Host: "127.0.0.1", Port: 80})
	}
	cfg, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: "0", Enabled: true, Choices: choices}, qualityTestResolver, now.Add(-4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return s, id, cfg, now
}
func qualityTarget(t *testing.T, c protocol.QualityConfig, slot string) protocol.QualityTarget {
	t.Helper()
	for _, target := range c.Targets {
		if target.Slot == slot && target.Family == "ipv4" {
			return target
		}
	}
	t.Fatal("target missing")
	return protocol.QualityTarget{}
}
func qualitySample(target protocol.QualityTarget, cfg string, seq int64, at time.Time, latency float64, outcome string) protocol.QualitySample {
	s := protocol.QualitySample{Sequence: strconv.FormatInt(seq, 10), TargetID: target.ID, ConfigRevision: cfg, SlotRevision: target.SlotRevision, ScheduledAt: at.UnixMilli(), StartedAt: at.UnixMilli(), FinishedAt: at.UnixMilli() + 20, DurationMS: 20, Outcome: outcome, ResolvedIP: "127.0.0.1"}
	if outcome == "success" {
		s.LatencyMS = &latency
		if latency > s.DurationMS {
			s.DurationMS = latency
			s.FinishedAt = s.StartedAt + int64(latency)
		}
	}
	return s
}
func qualitySave(t *testing.T, s *Store, id string, now time.Time, samples ...protocol.QualitySample) protocol.QualityBatchResponse {
	t.Helper()
	ack, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: samples}, now)
	if err != nil {
		t.Fatal(err)
	}
	return ack
}
func qualityCounts(t *testing.T, s *Store, id string, expectedRaw int) {
	t.Helper()
	var raw, usage, total int
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_raw WHERE agent_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT raw FROM quality_usage WHERE agent_id=?`, id).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT value FROM quality_totals WHERE kind='raw'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if raw != expectedRaw || usage != raw || total != raw {
		t.Fatalf("raw=%d usage=%d total=%d expected=%d", raw, usage, total, expectedRaw)
	}
}

func TestN2aQualityCASIntervalsPartialDedupAndReturn(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	ct := qualityTarget(t, cfg, "telecom")
	cu := qualityTarget(t, cfg, "unicom")
	valid := qualitySample(ct, cfg.Revision, 9007199254740993, now.Add(-time.Second), 10, "success")
	invalid := valid
	invalid.Sequence = "2"
	invalid.TargetID = "foreign"
	ack := qualitySave(t, s, id, now, valid, valid, invalid)
	if ack.Results[0][2] != "committed" || ack.Results[1][2] != "duplicate" || ack.Results[2][2] != "unauthorized" {
		t.Fatal(ack)
	}
	qualityCounts(t, s, id, 1)
	conflict := valid
	conflict.DurationMS = 21
	if qualitySave(t, s, id, now, conflict).Results[0][2] != "conflict" {
		t.Fatal("conflict accepted")
	}
	oldCU := qualitySample(cu, cfg.Revision, 3, now.Add(-time.Second), 15, "success")
	updated, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: "127.0.0.2", Port: 80}}}, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	if qualityTarget(t, updated, "telecom").SlotRevision != ct.SlotRevision {
		t.Fatal("untouched slot invalidated")
	}
	valid.Sequence = "4"
	valid.ConfigRevision = updated.Revision
	if qualitySave(t, s, id, now, valid, oldCU).Results[1][2] != "committed" {
		t.Fatal("legitimate late task rejected")
	}
	old, err := s.QualityHistory(ctx, id, cu.ID, 1, now.Add(time.Second))
	if err != nil || !old.Stale || old.Latest == nil {
		t.Fatalf("old history %+v %v", old, err)
	}
	back, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: updated.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: "127.0.0.1", Port: 80}}}, qualityTestResolver, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if qualityTarget(t, back, "unicom").ID != cu.ID {
		t.Fatal("identity not reused")
	}
	old, err = s.QualityHistory(ctx, id, cu.ID, 1, now.Add(2*time.Second))
	if err != nil || !old.Stale || len(old.Gaps) == 0 || old.Gaps[0].Reason != "known_pause" {
		t.Fatalf("return history %+v %v", old, err)
	}
	if _, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: "1", Enabled: true}, qualityTestResolver, now); !errors.Is(err, ErrQualityConflict) {
		t.Fatal(err)
	}
	noChange, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: back.Revision, Enabled: true}, qualityTestResolver, now)
	if err != nil || noChange.Revision != back.Revision {
		t.Fatal("duplicate config changed revision")
	}
}

func TestN2aQualityRawWindowsFutureAndExactQuantiles(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := qualityTarget(t, cfg, "telecom")
	inputs := []protocol.QualitySample{qualitySample(target, "1", 1, now.Add(-301*time.Second), 100, "success"), qualitySample(target, "1", 2, now.Add(-299*time.Second), 1, "success"), qualitySample(target, "1", 3, now.Add(-2*time.Second), 9, "success"), qualitySample(target, "1", 4, now.Add(-time.Second), 0, "timeout"), qualitySample(target, "1", 5, now.Add(-time.Second), 0, "canceled"), qualitySample(target, "1", 6, now.Add(time.Second), 99, "success")}
	ack := qualitySave(t, s, id, now, inputs...)
	for _, item := range ack.Results {
		if item[2] != "committed" {
			t.Fatal(ack)
		}
	}
	h, err := s.QualityHistory(context.Background(), id, target.ID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Recent.Attempts != 3 || h.Recent.Failures != 1 || h.Recent.NotExecuted != 1 || h.Recent.PacketLossPercent != nil {
		t.Fatalf("wrong denominator %+v", h.Recent)
	}
	if h.Window.Successes != 3 || h.Window.P50 == nil || *h.Window.P50 != 9 || *h.Window.P95 != 90.89999999999999 && *h.Window.P95 != 90.9 {
		t.Fatalf("wrong raw quantile %+v", h.Window)
	}
	if len(h.RecentBlocks) != 20 || !h.RecentBlocks[19].InProgress || h.RecentBlocks[19].End != now.UnixMilli() {
		t.Fatal("current minute mismatch")
	}
	h24, err := s.QualityHistory(context.Background(), id, target.ID, 24, now)
	if err != nil || len(h24.Blocks) != 288 {
		t.Fatal(err)
	}
	empty, err := s.QualityHistory(context.Background(), id, qualityTarget(t, cfg, "mobile").ID, 1, now)
	if err != nil || empty.Latest != nil || empty.Recent.FailurePercent != nil || empty.Window.P95 != nil {
		t.Fatal("empty invented zero")
	}
}

func TestN2aQualityCapabilitiesFencesAndCommitFailure(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	sample := qualitySample(qualityTarget(t, cfg, "telecom"), "1", 1, now.Add(-time.Second), 5, "success")
	for _, q := range []string{`CREATE TABLE quality_commit_failure(agent_id TEXT REFERENCES agents(id) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER quality_test_commit_failure AFTER INSERT ON quality_raw BEGIN INSERT INTO quality_commit_failure VALUES('missing-agent'); END`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ack, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now)
	if err == nil || len(ack.Results) != 0 {
		t.Fatalf("commit error falsely ACK %+v %v", ack, err)
	}
	qualityCounts(t, s, id, 0)
	if _, err := s.db.Exec(`DROP TRIGGER quality_test_commit_failure`); err != nil {
		t.Fatal(err)
	}
	r := validReport(id, 2, "next_session", "boot", 1, 2, 2)
	if _, ok, _, err := s.ProcessReport(ctx, id, r, now); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := s.GetQualitySessionConfig(ctx, id, 1, "quality_session"); !errors.Is(err, ErrQualityUnsupported) {
		t.Fatal("stale capability accepted", err)
	}
	if _, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now); !errors.Is(err, ErrQualityUnsupported) {
		t.Fatal(err)
	}
	r.NetworkQuality = true
	r.Sequence = 2
	if _, ok, _, err := s.ProcessReport(ctx, id, r, now); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := s.DisableAgent(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 1, Epoch: "2", SessionID: "next_session", Samples: []protocol.QualitySample{sample}}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("pause bypass", err)
	}
}

func TestN2aQualityRetentionCountersAndCascade(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	cu := qualityTarget(t, cfg, "unicom")
	qualitySave(t, s, id, now, qualitySample(cu, "1", 1, now.Add(-time.Second), 3, "success"))
	_, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: "1", Enabled: false, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: "127.0.0.2", Port: 80}}}, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CleanupQualityBatch(ctx, now.Add(49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	qualityCounts(t, s, id, 0)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_targets WHERE agent_id=?`, id).Scan(&count); err != nil || count != 3 {
		t.Fatalf("current disabled targets lost: %d %v", count, err)
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"config", "targets", "slots", "intervals", "capability", "raw", "minute", "gaps", "usage"} {
		if err := s.db.QueryRow(`SELECT count(*) FROM quality_` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade %s count%d err%v", table, count, err)
		}
	}
	if err := s.db.QueryRow(`SELECT SUM(value) FROM quality_totals`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("totals after cascade %d %v", count, err)
	}
}

func TestN2aQuality22MigrationPreservesResourceIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema22.db")
	s, id, token := testStore(t, path)
	ctx := context.Background()
	r := validReport(id, 1, "old_session", "boot", 1, 100, 200)
	if _, ok, _, err := s.ProcessReport(ctx, id, r, time.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := s.PutAgentPlan(ctx, AgentPlan{AgentID: id, CountryCodeOverride: "DE"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetAgentSnapshot(ctx, id, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	beforeState, _ := json.Marshal(before.State)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	dropQualityFixtureSchema(t, db)
	var objects int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE 'quality_%'`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("not true schema22: %d %v", objects, err)
	}
	rows, err := db.Query(`SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	actualObjects := [][4]string{}
	for rows.Next() {
		var item [4]string
		if err := rows.Scan(&item[0], &item[1], &item[2], &item[3]); err != nil {
			t.Fatal(err)
		}
		actualObjects = append(actualObjects, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	actualJSON, _ := json.MarshalIndent(actualObjects, "", "  ")
	expectedJSON, err := os.ReadFile("network_quality_schema22_objects_test.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualJSON, bytes.TrimSpace(expectedJSON)) {
		t.Fatal("fixture sqlite_master differs from frozen baseline schema22")
	}
	t.Logf("schema22 fixture matches %d actual baseline objects", len(actualObjects))
	db.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Authenticate(ctx, token); err != nil || got != id {
		t.Fatal("token lost", err)
	}
	state, err := s.GetAgentSnapshot(ctx, id, time.Now(), time.Minute)
	if err != nil || state.State == nil || state.State.RXBytes != 100 || state.State.CPUPercent != 12 {
		t.Fatalf("resource lost %+v %v", state, err)
	}
	afterState, _ := json.Marshal(state.State)
	if !bytes.Equal(beforeState, afterState) {
		t.Fatalf("before/after resource changed: %s / %s", beforeState, afterState)
	}
	var minutes int
	if err := s.db.QueryRow(`SELECT count(*) FROM minute_metrics WHERE agent_id=?`, id).Scan(&minutes); err != nil || minutes != 1 {
		t.Fatal("minute lost", err)
	}
	if plan, exists, err := s.GetAgentPlan(ctx, id); err != nil || !exists || plan.CountryCodeOverride != "DE" {
		t.Fatalf("plan lost %+v %v", plan, err)
	}
}

func TestN2aQualityFilePageCost10000(t *testing.T) {
	if os.Getenv("PROBE_N2A_PAGE_COST") != "1" {
		t.Skip("one bounded raw measurement recorded in focused-v2; opt in for independent replay")
	}
	path := filepath.Join(t.TempDir(), "quality.db")
	s, id, cfg, now := qualityFixture(t, path)
	target := qualityTarget(t, cfg, "telecom")
	ctx := context.Background()
	started := time.Now()
	initial, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for first := 1; first <= 10000; first += 64 {
		batch := []protocol.QualitySample{}
		for seq := first; seq < first+64 && seq <= 10000; seq++ {
			batch = append(batch, qualitySample(target, "1", int64(seq), now.Add(-time.Hour).Add(time.Duration(seq)*time.Millisecond), 12, "success"))
		}
		ack := qualitySave(t, s, id, now, batch...)
		for _, item := range ack.Results {
			if item[2] != "committed" {
				t.Fatal(ack)
			}
		}
	}
	qualityCounts(t, s, id, 10000)
	before, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	after, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pages, pageSize int64
	s.db.QueryRow(`PRAGMA page_count`).Scan(&pages)
	s.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	t.Logf("N2A_PAGE_COST %s", mustQualityJSON(t, map[string]any{"rows": 10000, "initial_db_bytes": initial.DatabaseBytes, "before_checkpoint": before, "after_checkpoint": after, "page_count": pages, "page_size": pageSize, "elapsed_ms": time.Since(started).Milliseconds(), "schema": "real schema23+all indexes/counters, synthetic timestamps, not Agent cadence"}))
	if _, _, err := s.CleanupQualityBatch(ctx, now.Add(49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var remaining int
	s.db.QueryRow(`SELECT count(*) FROM quality_raw`).Scan(&remaining)
	if remaining != 9500 {
		t.Fatalf("not bounded: %d", remaining)
	}
	for pass := 0; pass < 32; pass++ {
		_, more, err := s.CleanupQualityBatch(ctx, now.Add(49*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	qualityCounts(t, s, id, 0)
	reuse, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("N2A_AFTER_CLEANUP %s", mustQualityJSON(t, reuse))
	if reuse.DatabaseBytes < after.DatabaseBytes {
		t.Fatal("unexpected immediate shrink, inspect evidence")
	}
	var details string
	var planID, parent, unused int
	if err := s.db.QueryRow(`EXPLAIN QUERY PLAN SELECT `+qualitySampleColumns+` FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? ORDER BY sample_time,seq LIMIT 45000`, id, target.ID, 0, now.UnixMilli()).Scan(&planID, &parent, &unused, &details); err != nil {
		t.Fatal(err)
	}
	t.Log("QUERY_PLAN", details)
	// High-water observation boundary: sparse synthetic file, not an ENOSPC experiment.
	dummy := filepath.Join(t.TempDir(), "pressure.db")
	file, err := os.Create(dummy)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(2 << 30); err != nil {
		t.Fatal(err)
	}
	file.Close()
	realPath := s.databasePath
	s.databasePath = dummy
	sample := qualitySample(target, "1", 11000, now.Add(-time.Second), 4, "success")
	ack := qualitySave(t, s, id, now, sample)
	s.databasePath = realPath
	if ack.Results[0][2] != "storage_pressure" {
		t.Fatal("pressure falsely ACK", ack)
	}
}
func mustQualityJSON(t *testing.T, v any) string {
	t.Helper()
	wire, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(string(wire))
}
