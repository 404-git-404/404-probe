package storage

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestN2aQualityQuotasReuseAndCounterBoundary(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	for i := 0; i < 509; i++ {
		var err error
		cfg, err = s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: fmt.Sprintf("fixture-%d.invalid", i), Port: 80}}}, qualityTestResolver, now.Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			t.Fatalf("target churn %d %v", i, err)
		}
	}
	var targetCount int
	s.db.QueryRow(`SELECT targets FROM quality_usage WHERE agent_id=?`, id).Scan(&targetCount)
	if targetCount != 512 {
		t.Fatal(targetCount)
	}
	_, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: "one-more.invalid", Port: 80}}}, qualityTestResolver, now.Add(time.Second))
	if !errors.Is(err, ErrQualityQuota) {
		t.Fatal("513 target accepted", err)
	}
	cfg, err = s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "tcp", Host: "127.0.0.1", Port: 80}}}, qualityTestResolver, now.Add(time.Second))
	if err != nil {
		t.Fatal("reusable identity blocked", err)
	}
	target := qualityTarget(t, cfg, "telecom")
	sample := qualitySample(target, cfg.Revision, 1, now.Add(-time.Second), 4, "success")
	qualitySave(t, s, id, now, sample)
	// Counter boundary fault injection, not millions of measured rows. Restore after each.
	for _, field := range []string{"raw", "minute"} {
		limit := qualityRawGlobal
		if field == "minute" {
			limit = qualityMinuteGlobal
		}
		s.db.Exec(`UPDATE quality_totals SET value=? WHERE kind=?`, limit, field)
		next := sample
		next.Sequence = "2"
		if field == "minute" {
			next.StartedAt -= 60000
			next.ScheduledAt = next.StartedAt
			next.FinishedAt = next.StartedAt + 20
		}
		ack := qualitySave(t, s, id, now, sample, next)
		if ack.Results[0][2] != "duplicate" || ack.Results[1][2] != "quota" {
			t.Fatal(field, ack)
		}
		s.db.Exec(`UPDATE quality_totals SET value=1 WHERE kind=?`, field)
	}
	qualityCounts(t, s, id, 1)
}

func TestN2aQualityICMPOriginalPacketsGapsAndRemoval(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	cfg, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: "1", Enabled: true, Choices: []protocol.QualityChoice{{Slot: "telecom", Source: "manual", Protocol: "icmp", Host: "127.0.0.1", Port: 0}}}, qualityTestResolver, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	target := qualityTarget(t, cfg, "telecom")
	canceled := qualitySample(target, cfg.Revision, 1, now.Add(-time.Second), 0, "canceled")
	canceled.Sent = 1
	received := qualitySample(target, cfg.Revision, 2, now.Add(-time.Second), 8, "success")
	received.Sent = 1
	received.Received = 1
	ack := qualitySave(t, s, id, now, canceled, received)
	if ack.Results[0][2] != "committed" || ack.Results[1][2] != "committed" {
		t.Fatal(ack)
	}
	var rawSent, denominator int
	if err := s.db.QueryRow(`SELECT raw_sent,packet_sent FROM quality_raw WHERE agent_id=? AND seq=1`, id).Scan(&rawSent, &denominator); err != nil || rawSent != 1 || denominator != 0 {
		t.Fatalf("lost ICMP original packets %d/%d %v", rawSent, denominator, err)
	}
	dropped := 3
	gap := protocol.QualityGap{Start: now.Add(-20 * time.Second).UnixMilli(), End: now.Add(-10 * time.Second).UnixMilli(), Reason: "queue_drop", Dropped: &dropped}
	batch := protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Gaps: []protocol.QualityGap{gap, gap}}
	gapACK, err := s.SaveQualityBatch(ctx, id, batch, now)
	if err != nil || gapACK.Gaps[0][2] != "committed" || gapACK.Gaps[1][2] != "duplicate" {
		t.Fatal(gapACK, err)
	}
	history, err := s.QualityHistory(ctx, id, target.ID, 1, now)
	if err != nil || history.Recent.Attempts != 1 || history.Recent.Sent != 1 || history.Recent.Received != 1 || len(history.Gaps) != 1 || history.Gaps[0].Dropped == nil || *history.Gaps[0].Dropped != 3 {
		t.Fatalf("packet/gap %+v %v", history, err)
	}
	r := validReport(id, 1, "quality_session", "boot", 2, 2, 2)
	r.NetworkQuality = true
	r.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	if _, ok, _, err := s.ProcessReport(ctx, id, r, now); err != nil || !ok {
		t.Fatal(err)
	}
	op := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := s.CreateAgentRemovalOperation(ctx, id, op, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQualityBatch(ctx, id, batch, now); !errors.Is(err, ErrAgentRemovalPending) {
		t.Fatal("pending removal bypass", err)
	}
	delivery, err := s.ClaimAgentRemoval(ctx, id, 1, "quality_session", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkAgentRemovalUninstalling(ctx, id, op, 1, "quality_session", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteAgentRemoval(ctx, op, delivery.ReceiptToken, "agent_uninstalled", now); err != nil {
		t.Fatal(err)
	}
	var rows, total int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_removal_receipts`).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("detached receipt changed")
	}
	s.db.QueryRow(`SELECT sum(value) FROM quality_totals`).Scan(&total)
	if total != 0 {
		t.Fatal("quality totals after actual removal", total)
	}
}

func TestN2aQualityPressureDuplicateResourceAndRecoverySemantics(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := qualityTarget(t, cfg, "telecom")
	sample := qualitySample(target, "1", 1, now.Add(-time.Second), 5, "success")
	qualitySave(t, s, id, now, sample)
	dummy := filepath.Join(t.TempDir(), "pressure.db")
	file, err := os.Create(dummy)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(2 << 30); err != nil {
		t.Fatal(err)
	}
	file.Close()
	original := s.databasePath
	s.databasePath = dummy
	defer func() { s.databasePath = original }()
	next := sample
	next.Sequence = "2"
	ack := qualitySave(t, s, id, now, sample, next)
	if ack.Results[0][2] != "duplicate" || ack.Results[1][2] != "storage_pressure" {
		t.Fatal(ack)
	}
	r := validReport(id, 1, "quality_session", "boot", 2, 2, 2)
	r.NetworkQuality = true
	if _, ok, _, err := s.ProcessReport(context.Background(), id, r, now); err != nil || !ok {
		t.Fatal("resource stopped under quality pressure", err)
	}
	if qualityPressureHealth(WALHealth{WALBytes: 256 << 20, RemainingFrames: 0, CheckpointBusy: 0}, nil) {
		t.Fatal("healthy reusable big WAL stopped")
	}
	if !qualityPressureHealth(WALHealth{RemainingFrames: 1}, nil) || !qualityPressureHealth(WALHealth{}, errors.New("checkpoint fail")) {
		t.Fatal("failed checkpoint accepted")
	}
	qualityCounts(t, s, id, 1)
}

func TestN2aQualityMinutePageCost10000(t *testing.T) {
	if os.Getenv("PROBE_N2A_MINUTE_COST") != "1" {
		t.Skip("bounded isolated minute measurement recorded in all-v3; opt in for independent replay")
	}
	s, id, cfg, now := qualityFixture(t, filepath.Join(t.TempDir(), "minute.db"))
	ctx := context.Background()
	cfg, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: "1", Enabled: true, IPv6: true}, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO quality_minute(agent_id,target_id,bucket,count,attempts,successes,latency_sum,sent,received) VALUES(?,?,?,2,2,2,24,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		target := cfg.Targets[i%6]
		bucket := now.UnixMilli()/60000*60000 - int64(i/6)*60000
		if _, err := stmt.Exec(id, target.ID, bucket); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := s.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("N2A_MINUTE_COST %s", mustQualityJSON(t, map[string]any{"rows": 10000, "initial_db_bytes": initial.DatabaseBytes, "after": after, "delta_db_bytes": after.DatabaseBytes - initial.DatabaseBytes, "bytes_per_row": float64(after.DatabaseBytes-initial.DatabaseBytes) / 10000, "elapsed_ms": time.Since(started).Milliseconds(), "method": "isolated real minute schema/index/counter inserts, no raw ingestion/cadence assertion"}))
	var count int
	if err := s.db.QueryRow(`SELECT minute FROM quality_usage WHERE agent_id=?`, id).Scan(&count); err != nil || count != 10000 {
		t.Fatal("counter", count, err)
	}
	// No extra raw benchmark. This measures the previously unmeasured minute component.
}

func TestN2aQualitySQLiteFullRollsBackWithoutACK(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, filepath.Join(t.TempDir(), "full.db"))
	target := qualityTarget(t, cfg, "telecom")
	var pages int64
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	// Test-only page quota generates real SQLITE_FULL without filling the user's disk.
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA max_page_count=%d`, pages)); err != nil {
		t.Fatal(err)
	}
	batch := protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session"}
	for i := int64(1); i <= 64; i++ {
		batch.Samples = append(batch.Samples, qualitySample(target, "1", i, now.Add(-time.Second), 6, "success"))
	}
	ack, err := s.SaveQualityBatch(context.Background(), id, batch, now)
	if err == nil || len(ack.Results) != 0 {
		t.Fatalf("SQLITE_FULL false ACK %+v %v", ack, err)
	}
	t.Logf("actual SQLite write failure=%v (test page quota, not physical OS ENOSPC)", err)
	qualityCounts(t, s, id, 0)
	if _, err := s.db.Exec(`PRAGMA max_page_count=2147483646`); err != nil {
		t.Fatal(err)
	}
	r := validReport(id, 1, "quality_session", "boot", 2, 3, 3)
	r.NetworkQuality = true
	if _, ok, _, err := s.ProcessReport(context.Background(), id, r, now); err != nil || !ok {
		t.Fatal("resource after rollback", err)
	}
}

func TestN2aQualityTypedCanonicalEveryFieldAndRevocation(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := qualityTarget(t, cfg, "telecom")
	sample := qualitySample(target, "1", 1, now.Add(-time.Second), 0, "success")
	qualitySave(t, s, id, now, sample)
	negativeZero := sample
	zero := 0.0
	negativeZero.DurationMS = -zero
	if qualitySave(t, s, id, now, negativeZero).Results[0][2] != "conflict" {
		t.Fatal("different duration not conflict")
	}
	zeroSample := sample
	zeroSample.Sequence = "2"
	zeroSample.DurationMS = 0
	zeroSample.FinishedAt = zeroSample.StartedAt
	qualitySave(t, s, id, now, zeroSample)
	zeroSample.DurationMS = math.Copysign(0, -1)
	negativeLatency := math.Copysign(0, -1)
	zeroSample.LatencyMS = &negativeLatency
	if qualitySave(t, s, id, now, zeroSample).Results[0][2] != "duplicate" {
		t.Fatal("negative zero not canonical")
	}
	for _, mutate := range []func(*protocol.QualitySample){func(p *protocol.QualitySample) { p.TargetID = "different" }, func(p *protocol.QualitySample) { p.ConfigRevision = "2" }, func(p *protocol.QualitySample) { p.SlotRevision = "2" }, func(p *protocol.QualitySample) { p.ScheduledAt-- }, func(p *protocol.QualitySample) { p.StartedAt-- }, func(p *protocol.QualitySample) { p.FinishedAt++ }, func(p *protocol.QualitySample) { p.DurationMS++ }, func(p *protocol.QualitySample) { p.Outcome = "timeout"; p.LatencyMS = nil }, func(p *protocol.QualitySample) { p.ResolvedIP = "127.0.0.2" }, func(p *protocol.QualitySample) { v := 1.0; p.LatencyMS = &v }, func(p *protocol.QualitySample) { p.Sent = 1 }, func(p *protocol.QualitySample) { p.Received = 1 }} {
		changed := sample
		mutate(&changed)
		if qualitySave(t, s, id, now, changed).Results[0][2] != "conflict" {
			t.Fatal("field omitted from content identity", changed)
		}
	}
	// Even a matching stored digest cannot bypass typed canonical re-comparison.
	if _, err := s.db.Exec(`UPDATE quality_raw SET scheduled_at=scheduled_at-1 WHERE agent_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if qualitySave(t, s, id, now, sample).Results[0][2] != "conflict" {
		t.Fatal("digest-only comparison")
	}
	if _, err := s.RevokeAgent(context.Background(), id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revocation bypass", err)
	}
}
