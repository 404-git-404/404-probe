package storage

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func s1Bucket(t *testing.T, s *Store, target string, res, stamp int64) qualityBucketData {
	t.Helper()
	var wire []byte
	if err := s.db.QueryRow(`SELECT b.data FROM quality_buckets b JOIN quality_series x ON x.id=b.series_id WHERE x.target_id=? AND b.resolution=? AND b.bucket_start=?`, target, res, stamp/res*res).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	b, err := qualityDecodeBucket(wire)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestS1RawIDReuseReceiptAndBoundary(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	a := qualitySample(target, cfg.Revision, 1, now.Add(-time.Minute), 10.13, "success")
	if ack := qualitySave(t, s, id, now, a); ack.Results[0][2] != "committed" {
		t.Fatal(ack)
	}
	if _, _, err := s.CleanupQualityBatch(context.Background(), now.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var raw int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_raw`).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("raw %d %v", raw, err)
	}
	if ack := qualitySave(t, s, id, now.Add(9*time.Hour), a); ack.Results[0][2] != "duplicate" {
		t.Fatal(ack)
	}
	a.DurationMS++
	if ack := qualitySave(t, s, id, now.Add(9*time.Hour), a); ack.Results[0][2] != "conflict" {
		t.Fatal(ack)
	}
	next := qualitySample(target, cfg.Revision, 2, now.Add(9*time.Hour), 20.01, "success")
	if ack := qualitySave(t, s, id, now.Add(9*time.Hour), next); ack.Results[0][2] != "committed" {
		t.Fatal(ack)
	}
	var facts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_facts`).Scan(&facts); err != nil || facts != 2 {
		t.Fatalf("facts %d %v", facts, err)
	}
	b := s1Bucket(t, s, target.ID, 3600000, next.StartedAt)
	stats := qualityBucketStats(b)
	if math.Abs(*stats.P50-20.01) > .125 || *stats.Mean != 20.01 {
		t.Fatal(stats)
	}
}
func TestS1LateJitterAndGap(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	start := now.Add(-time.Minute).UnixMilli() / 300000 * 300000
	samples := []protocol.QualitySample{}
	for i, v := range []float64{10.13, 20.09, 40.01} {
		samples = append(samples, qualitySample(target, cfg.Revision, int64(i+1), time.UnixMilli(start+30000+int64(i)*30000), v, "success"))
	}
	for _, i := range []int{0, 2, 1} {
		qualitySave(t, s, id, time.UnixMilli(start+180000), samples[i])
	}
	b := s1Bucket(t, s, target.ID, 300000, start)
	if b.Stats.JitterPairs != 2 || math.Abs(b.JitterSum-29.88) > 1e-8 {
		t.Fatal(b)
	}
	gap := protocol.QualityGap{Start: start + 60000, End: start + 60000, Reason: "restart"}
	ack, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Gaps: []protocol.QualityGap{gap}}, time.UnixMilli(start+180000))
	if err != nil || ack.Gaps[0][2] != "committed" {
		t.Fatalf("%v %v", ack, err)
	}
	b = s1Bucket(t, s, target.ID, 300000, start)
	if b.Stats.JitterPairs != 0 || b.Gaps != 1 {
		t.Fatal(b)
	}
	// cross-minute incoming edge was removed too, so a merged window stays zero.
	first := s1Bucket(t, s, target.ID, 60000, start)
	second := s1Bucket(t, s, target.ID, 60000, start+60000)
	qualityMergeBucket(&first, second)
	if first.Stats.JitterPairs != 0 {
		t.Fatal(first)
	}
}
func TestS1HistogramCodecBound(t *testing.T) {
	b := qualityNewBucket()
	sample := protocol.QualitySample{Outcome: "success", StartedAt: 1, LatencyMS: ptrS1(5999.999)}
	b, err := qualityContribution(sample, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := qualityEncodeBucket(b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := qualityDecodeBucket(wire)
	if err != nil {
		t.Fatal(err)
	}
	stats := qualityBucketStats(decoded)
	if math.Abs(*stats.P50-5999.999) > .125 || *stats.Mean != 5999.999 {
		t.Fatal(stats)
	}
	sample.LatencyMS = ptrS1(6000.01)
	if _, err := qualityContribution(sample, "tcp"); err == nil {
		t.Fatal("outlier silently accepted")
	}
}
func ptrS1(v float64) *float64 { return &v }

func TestS1BudgetMigrationPeakAndLowSpaceDefersBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peak.db")
	s, id, cfg, now := qualityFixture(t, path)
	for i := 0; i < 101; i++ {
		qualitySave(t, s, id, now, qualitySample(cfg.Targets[0], cfg.Revision, int64(i+1), now.Add(-time.Hour).Add(time.Duration(i)*30*time.Second), 10.13, "success"))
	}
	dropQualityRollupFixtureSchema(t, s.db)
	s.Close()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.qualityDiskObserve = func() (qualityDisk, error) {
		db, _ := sqliteFileSize(path, "database", false)
		wal, _ := sqliteFileSize(path+"-wal", "WAL", true)
		return qualityDisk{DB: db, WAL: wal, Free: 18 << 30, Total: 20 << 30, RootFree: 18 << 30, RootTotal: 20 << 30}, nil
	}
	p := s.qualityBudget(context.Background())
	want := int64(101) * (p.ReceiptBytes + p.FactBytes + 3*p.BucketBytes)
	if p.MigrationBytes != want || p.Pressure {
		t.Fatalf("peak reservation %+v want %d", p, want)
	}
	var auto int64
	if err = s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE aggregate=TRUE AND name LIKE 'sqlite_autoindex_quality_%'`).Scan(&auto); err != nil || auto == 0 {
		t.Fatal("no real autoindexes", auto, err)
	}
	var total int64
	if err = s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE aggregate=TRUE AND (name LIKE 'quality_%' OR name LIKE 'sqlite_autoindex_quality_%')`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	headroom := int64(18<<30) - int64(20<<30)/20
	if p.Bytes != (p.Disk.DB+p.Disk.WAL+headroom)/4 || p.Disk.DB+p.Disk.WAL < total {
		t.Fatalf("autoindex budget omitted %d bytes", auto)
	}
	s.qualityDiskObserve = func() (qualityDisk, error) {
		return qualityDisk{Free: 80 << 20, Total: 1 << 30, RootFree: 80 << 20, RootTotal: 1 << 30}, nil
	}
	s.qualityBudgetAt = time.Time{}
	n, more, err := s.BackfillQualityBatch(context.Background(), now.Add(12*time.Hour))
	if err != nil || n != 0 || !more {
		t.Fatalf("lowspace %d %v %v", n, more, err)
	}
	var cursor int
	s.db.QueryRow(`SELECT last_raw_id FROM quality_backfill`).Scan(&cursor)
	if cursor != 0 {
		t.Fatal("lowspace advanced cursor", cursor)
	}
	qualityCounts(t, s, id, 101)
}

func TestS1GapUTCSeamAndPointOwnership(t *testing.T) {
	for _, res := range qualityResolutions {
		for _, duration := range []int64{0, 10000} {
			t.Run(strconv.FormatInt(res, 10)+"/"+strconv.FormatInt(duration, 10), func(t *testing.T) {
				s, id, cfg, now := qualityFixture(t, ":memory:")
				target := cfg.Targets[0]
				start := now.Add(-time.Hour).UnixMilli() / res * res
				end := start + res
				now = time.UnixMilli(end + 90000)
				for i, stamp := range []int64{end - 30000, end - 15000, end + 15000} {
					sample := qualitySample(target, cfg.Revision, int64(i+1), time.UnixMilli(stamp), 10+float64(i), "success")
					qualitySave(t, s, id, now, sample)
				}
				gap := protocol.QualityGap{Start: end, End: end + duration, Reason: "restart"}
				ack, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Gaps: []protocol.QualityGap{gap}}, now)
				if err != nil || ack.Gaps[0][2] != "committed" {
					t.Fatal(ack, err)
				}
				prior := s1Bucket(t, s, target.ID, res, start)
				next := s1Bucket(t, s, target.ID, res, end)
				if prior.Gaps != 0 || prior.Stats.JitterPairs != 1 || next.Gaps != 1 {
					t.Fatalf("seam prior %+v next %+v", prior, next)
				}
				stats := qualityBucketStats(prior)
				if stats.Completeness != "complete" || stats.ExecutionCoverage != "unverified" {
					t.Fatal("previous bucket polluted", stats)
				}
				merged := qualityNewBucket()
				qualityMergeBucket(&merged, prior)
				qualityMergeBucket(&merged, next)
				if merged.Stats.JitterPairs != 1 {
					t.Fatal("real seam gap stopped blocking pair", merged)
				}
				view := "1day"
				if res == 300000 {
					view = "1week"
				}
				if res == 3600000 {
					view = "1mo"
				}
				h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, view, now)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range h.Groups[0].Points {
					if p.Start == start && (p.Completeness != "complete" || p.ExecutionCoverage != "unverified") {
						t.Fatal("API seam polluted", p)
					}
				}
			})
		}
	}
	if qualityGapOverlaps(100, 200, protocol.QualityGap{Start: 0, End: 100}) || qualityGapOverlaps(100, 200, protocol.QualityGap{Start: 200, End: 210}) {
		t.Fatal("half-open end overlap")
	}
}

func TestS1Schema26BackfillAtomicResumeAndLiveUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema26.db")
	s, id, cfg, now := qualityFixture(t, path)
	target := cfg.Targets[0]
	for i := 0; i < 201; i++ {
		qualitySave(t, s, id, now, qualitySample(target, cfg.Revision, int64(i+1), now.Add(-2*time.Hour).Add(time.Duration(i)*30*time.Second), 10.13, "success"))
	}
	dropQualityRollupFixtureSchema(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now = now.Add(12 * time.Hour)
	if _, err = s.db.Exec(`CREATE TRIGGER s1_backfill_abort BEFORE INSERT ON quality_receipts BEGIN SELECT RAISE(ABORT,'backfill failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BackfillQualityBatch(context.Background(), now); err == nil {
		t.Fatal("injected failure ignored")
	}
	var cursor, raw, receipts int
	s.db.QueryRow(`SELECT last_raw_id FROM quality_backfill`).Scan(&cursor)
	s.db.QueryRow(`SELECT COUNT(*) FROM quality_raw`).Scan(&raw)
	s.db.QueryRow(`SELECT COUNT(*) FROM quality_receipts`).Scan(&receipts)
	if cursor != 0 || raw != 201 || receipts != 0 {
		t.Fatalf("rollback cursor=%d raw=%d receipt=%d", cursor, raw, receipts)
	}
	s.db.Exec(`DROP TRIGGER s1_backfill_abort`)
	n, more, err := s.BackfillQualityBatch(context.Background(), now)
	if err != nil || n != 100 || !more {
		t.Fatalf("batch %d %v %v", n, more, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fresh := qualitySample(target, cfg.Revision, 202, now.Add(-time.Second), 20.09, "success")
	if ack := qualitySave(t, s, id, now, fresh); ack.Results[0][2] != "committed" {
		t.Fatal(ack)
	}
	for i := 0; i < 5; i++ {
		_, more, err = s.BackfillQualityBatch(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM quality_receipts`).Scan(&receipts)
	if receipts != 202 {
		t.Fatalf("receipts %d", receipts)
	}
	h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1day", now)
	if err != nil || len(h.Groups) != 1 || h.Groups[0].Window.Count != 202 || h.MigrationPending {
		t.Fatalf("history %+v %v", h, err)
	}
	var fk int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("foreign keys %d %v", fk, err)
	}
}

func TestS1RevisionJSONPreservesLargeInteger(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	sample := qualitySample(target, cfg.Revision, 1, now.Add(-time.Minute), 10.13, "success")
	qualitySave(t, s, id, now, sample)
	b := s1Bucket(t, s, target.ID, 3600000, sample.StartedAt)
	large := int64(9007199254740993)
	b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax = large, large, large, large
	wire, err := qualityEncodeBucket(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE quality_buckets SET data=? WHERE resolution=3600000`, wire); err != nil {
		t.Fatal(err)
	}
	h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1mo", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range h.Groups[0].Points {
		if p.Count == 0 {
			continue
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		json.Unmarshal(data, &decoded)
		for _, key := range []string{"config_revision_min", "config_revision_max", "slot_revision_min", "slot_revision_max"} {
			v, ok := decoded[key].(string)
			if !ok || v != strconv.FormatInt(large, 10) {
				t.Fatalf("identity %s %#v", key, decoded[key])
			}
		}
	}
}

func TestS1HistorySurvivesRawExpiry(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	start := now.Add(-time.Minute).UnixMilli() / 60000 * 60000
	for i, v := range []float64{10.13, 20.09, 40.01} {
		sample := qualitySample(target, cfg.Revision, int64(i+1), time.UnixMilli(start+30000*int64(i)), v, "success")
		qualitySave(t, s, id, time.UnixMilli(start+90000), sample)
	}
	queryNow := time.UnixMilli(start + 90000).Add(9 * time.Hour)
	for i := 0; i < 10; i++ {
		_, more, err := s.CleanupQualityBatch(context.Background(), queryNow)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for _, view := range []string{"1day", "1week", "1mo"} {
		h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, view, queryNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Groups) != 1 {
			t.Fatalf("%s groups %#v", view, h)
		}
		stats := h.Groups[0].Window
		if stats.Count != 3 || stats.JitterPairs != 2 || stats.Mean == nil || math.Abs(*stats.Mean-70.23/3) > 1e-8 || stats.Jitter == nil || math.Abs(*stats.Jitter-29.88/2) > 1e-8 {
			t.Fatalf("%s stats %+v", view, stats)
		}
		if len(h.Current) > 0 && !h.Current[0].Stale {
			t.Fatal("expired probe marked fresh")
		}
	}
	h, err := s.QualityHistoryV2(context.Background(), id, target.ID, 24, queryNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Groups) != 1 || h.Groups[0].Window.Count != 3 || h.Groups[0].Window.JitterPairs != 2 {
		t.Fatalf("v2 %+v", h)
	}
	legacy, err := s.QualityHistory(context.Background(), id, target.ID, 24, queryNow)
	if err != nil || legacy.Window.Count != 3 {
		t.Fatalf("legacy %+v %v", legacy, err)
	}
}

func TestS1OpenHistoryExcludesFutureAndMeanExact(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	now = time.UnixMilli(now.UnixMilli()/3600000*3600000 + 600000)
	a := qualitySample(target, cfg.Revision, 1, now.Add(-time.Second), 10.13, "success")
	b := qualitySample(target, cfg.Revision, 2, now.Add(time.Second), 40.01, "success")
	qualitySave(t, s, id, now, a, b)
	h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1mo", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Groups) != 1 || h.Groups[0].Window.Count != 1 || h.Groups[0].Window.Mean == nil || *h.Groups[0].Window.Mean != 10.13 {
		t.Fatalf("future included %+v", h)
	}
}
