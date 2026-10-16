package storage

import (
	"404-probe/internal/protocol"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestA2RotationAtomicRollbackAndRestartRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.db")
	s, id, old, now := qualityFixture(t, path)
	ctx := context.Background()
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	cfg, err := s.UpdateQualityConfigV2(ctx, id, a1LaneConfig(old.Revision), qualityTestResolver, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	target := a1Target(t, cfg, "telecom", "ipv4", "tcp")
	samples := []protocol.QualitySample{}
	for i := 0; i < 24; i++ {
		samples = append(samples, a2TCP(target, cfg.Revision, int64(i+1), now.Add(-time.Minute).Add(time.Duration(i)*time.Second), 10, "success"))
	}
	b := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: samples}
	ack, err := s.SaveQualityBatch(ctx, id, b, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	s.qualityBudgetCache = qualityBudget{Scale: 1, RawLimit: 4, MinuteLimit: 96, Agents: 1, Lanes: 12}
	s.qualityBudgetAt = time.Now()
	if _, err = s.db.Exec(`CREATE TRIGGER quality_a2_abort BEFORE INSERT ON quality_raw BEGIN SELECT RAISE(ABORT,'construction injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	fresh := a2TCP(target, cfg.Revision, 25, now.Add(-time.Second), 10, "success")
	b.Samples = []protocol.QualitySample{fresh}
	ack, err = s.SaveQualityBatch(ctx, id, b, now)
	if err == nil || len(ack.Results) != 0 {
		t.Fatal("write failure falsely ACKed", ack, err)
	}
	qualityCounts(t, s, id, 24)
	var marks int
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_gaps WHERE reason LIKE 'storage_pruned:%'`).Scan(&marks); err != nil || marks != 0 {
		t.Fatal("watermark survived rolled-back retirement", marks, err)
	}
	t.Log("injected INSERT failure rolled back pruning, watermark, usage and ACK:", err)
	if _, err = s.db.Exec(`DROP TRIGGER quality_a2_abort`); err != nil {
		t.Fatal(err)
	}
	ack, err = s.SaveQualityBatch(ctx, id, b, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	qualityCounts(t, s, id, 1)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b.Samples = samples[:1]
	ack, err = reopened.SaveQualityBatch(ctx, id, b, now)
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal("restart lost retained receipt", ack, err)
	}
	qualityCounts(t, reopened, id, 1)
	version, versionErr := schemaVersion(ctx, reopened.db)
	if versionErr != nil || version != currentSchemaVersion {
		t.Fatal("schema changed", version, versionErr)
	}
	h, err := reopened.QualityHistoryV2(ctx, id, target.ID, 1, now)
	if err != nil || len(h.Gaps) != 1 || h.Gaps[0].Reason != "storage_pruned" || h.Gaps[0].Dropped == nil || *h.Gaps[0].Dropped != 24 {
		t.Fatal("retirement truth lost", h, err)
	}
}
func TestA2RealSQLiteFullRollbackBasicAndRecovery(t *testing.T) {
	s, id, old, now := qualityFixture(t, filepath.Join(t.TempDir(), "full.db"))
	ctx := context.Background()
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	cfg, err := s.UpdateQualityConfigV2(ctx, id, a1LaneConfig(old.Revision), qualityTestResolver, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	target := a1Target(t, cfg, "telecom", "ipv4", "icmp")
	if _, err = s.CheckpointWAL(ctx); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err = s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(fmt.Sprintf(`PRAGMA max_page_count=%d`, pages)); err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	committed := 0
	var failed protocol.QualityBatch
	for attempt := 0; attempt < 32; attempt++ {
		b := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session"}
		for j := 0; j < 48; j++ {
			seq++
			b.Samples = append(b.Samples, a2Echo(target, cfg.Revision, seq, now.Add(-time.Minute), []float64{10, 20, 40}))
		}
		ack, err := s.SaveQualityBatch(ctx, id, b, now)
		if err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "full") {
				t.Fatal("unexpected failure", err)
			}
			if len(ack.Results) != 0 {
				t.Fatal("SQLITE_FULL falsely ACKed", ack)
			}
			failed = b
			t.Log("actual SQLITE_FULL:", err)
			break
		}
		for _, a := range ack.Results {
			if a[2] != "committed" {
				t.Fatal(a)
			}
		}
		committed += 48
	}
	if len(failed.Samples) == 0 {
		t.Fatal("actual full condition not reached")
	}
	qualityCounts(t, s, id, committed)
	// Updating the existing basic-report bucket still works at the imposed cap.
	r := validReport(id, 1, "quality_session", "boot", 3, 1, 1)
	r.NetworkQuality = true
	r.NetworkQualityV2 = caps
	r.OS = "debian 13"
	if _, accepted, _, err := s.ProcessReport(ctx, id, r, now); err != nil || !accepted {
		t.Fatal("basic report failed at quality write cap", accepted, err)
	}
	if _, err = s.db.Exec(`PRAGMA max_page_count=1073741823`); err != nil {
		t.Fatal(err)
	}
	s.qualityBudgetAt = time.Time{}
	ack, err := s.SaveQualityBatch(ctx, id, failed, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal("full recovery lost retry", ack, err)
	}
	qualityCounts(t, s, id, committed+48)
	ack, err = s.SaveQualityBatch(ctx, id, failed, now)
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal("recovered retry duplicated statistics", ack, err)
	}
}
