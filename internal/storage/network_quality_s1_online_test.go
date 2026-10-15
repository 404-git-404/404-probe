package storage

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestS1OnlineCleanupReuse(t *testing.T) {
	root := os.Getenv("QUALITY_S1_ONLINE_ROOT")
	if root == "" {
		t.Skip("materialized online fixture is opt-in")
	}
	input := os.Getenv("QUALITY_S1_ONLINE_INPUT")
	if input == "" {
		t.Fatal("input required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "online.db")
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data = nil
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := fmt.Sprintf("%032x", 1000)
	cfg, err := s.GetQualityConfigV2(ctx, id)
	if err != nil || len(cfg.Targets) != 12 {
		t.Fatal("twelve lanes", len(cfg.Targets), err)
	}
	var seq int64
	if err = s.db.QueryRow(`SELECT MAX(seq) FROM quality_raw`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1800000000000 / 3600000 * 3600000)
	started := time.Now()
	uploads, reports, cleanups := []float64{}, []float64{}, []float64{}
	views := map[string][]float64{}
	snapshots := []map[string]any{}
	var peakWAL int64
	for cycle := 0; cycle < 4; cycle++ {
		for round := 0; round < 120; round++ {
			samples := []protocol.QualitySample{}
			for _, target := range cfg.Targets {
				seq++
				v := 20.13 + float64(round%17)*.25
				sample := a2TCP(target, cfg.Revision, seq, now, v, "success")
				if target.Protocol == "icmp" {
					sample = a2Echo(target, cfg.Revision, seq, now, []float64{v, -1, v + .31})
				}
				if target.Family == "ipv6" {
					sample.ResolvedIP = "::1"
				}
				samples = append(samples, sample)
			}
			batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: samples}
			begin := time.Now()
			ack, err := s.SaveQualityBatch(ctx, id, batch, now.Add(3*time.Second))
			uploads = append(uploads, float64(time.Since(begin).Microseconds())/1000)
			if err != nil || len(ack.Results) != 12 {
				t.Fatal("upload", ack, err)
			}
			for _, r := range ack.Results {
				if r[2] != "committed" {
					t.Fatal("ACK", ack)
				}
			}
			if round == 0 {
				ack, err = s.SaveQualityBatch(ctx, id, batch, now.Add(4*time.Second))
				if err != nil || ack.Results[0][2] != "duplicate" {
					t.Fatal("lost ack", ack, err)
				}
			}
			r := validReport(id, 1, "quality_session", "boot", uint64(cycle*120+round+2), 1, 1)
			r.NetworkQuality = true
			r.NetworkQualityV2 = a1Capabilities()
			r.NetworkQualityV2.Semantics = protocol.QualityStagedSemantics
			r.OS = "debian 13"
			begin = time.Now()
			_, accepted, _, err := s.ProcessReport(ctx, id, r, now.Add(4*time.Second))
			reports = append(reports, float64(time.Since(begin).Microseconds())/1000)
			if err != nil || !accepted {
				t.Fatal("basic report", accepted, err)
			}
			now = now.Add(30 * time.Second)
		}
		begin := time.Now()
		for pass := 0; pass < 100; pass++ {
			_, more, err := s.CleanupQualityBatch(ctx, now)
			if err != nil {
				t.Fatal("cleanup", err)
			}
			if !more {
				break
			}
			if pass == 99 {
				t.Fatal("cleanup failed to settle")
			}
		}
		cleanups = append(cleanups, float64(time.Since(begin).Microseconds())/1000)
		counts := map[string]any{"cycle": cycle + 1}
		for _, name := range []string{"raw", "receipts", "facts", "buckets", "minute", "series", "sessions"} {
			var count int64
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM quality_` + name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			counts[name] = count
		}
		for _, view := range []string{"1day", "1week", "1mo"} {
			begin = time.Now()
			h, err := s.QualityHistoryRollup(ctx, id, cfg.Targets[0].ID, view, now)
			views[view] = append(views[view], float64(time.Since(begin).Microseconds())/1000)
			if err != nil || len(h.Groups) != 1 || h.MigrationPending {
				t.Fatal("view", view, err)
			}
			if h.Groups[0].Window.Mean == nil {
				t.Fatal("mean missing")
			}
		}
		if info, e := os.Stat(path + "-wal"); e == nil {
			peakWAL = max(peakWAL, info.Size())
			counts["wal_bytes"] = info.Size()
		}
		if info, e := os.Stat(path); e == nil {
			counts["db_bytes"] = info.Size()
		}
		snapshots = append(snapshots, counts)
	}
	var bad int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Fatal("FK", bad, err)
	}
	for _, name := range []string{"raw", "minute", "receipts", "facts", "buckets", "series", "sessions"} {
		var actual, counter int64
		s.db.QueryRow(`SELECT COUNT(*) FROM quality_` + name).Scan(&actual)
		s.db.QueryRow(`SELECT value FROM quality_totals WHERE kind=?`, name).Scan(&counter)
		if actual != counter {
			t.Fatalf("counter %s %d/%d", name, actual, counter)
		}
	}
	summarize := func(values []float64) map[string]float64 {
		sort.Float64s(values)
		return map[string]float64{"p50_ms": qualityPercentileSorted(values, .5), "p95_ms": qualityPercentileSorted(values, .95)}
	}
	viewSummary := map[string]any{}
	for name, values := range views {
		viewSummary[name] = summarize(values)
	}
	report := map[string]any{"cycles": 4, "cycle_hours": 1, "online_samples": 5760, "basic_reports": 480, "upload_batch": summarize(uploads), "basic_report": summarize(reports), "cleanup_cycle": summarize(cleanups), "views": viewSummary, "snapshots": snapshots, "peak_wal_bytes": peakWAL, "wall_seconds": time.Since(started).Seconds(), "method": "actual SaveQualityBatch/ProcessReport/CleanupQualityBatch/QualityHistoryRollup on copied thirty-day materialized fixture"}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "online.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}

func TestS1LargeColdBudgetAndBasicPriority(t *testing.T) {
	root := os.Getenv("QUALITY_S1_COLD_ROOT")
	if root == "" {
		t.Skip("large cold fixture is opt-in")
	}
	input := os.Getenv("QUALITY_S1_COLD_INPUT")
	if input == "" {
		t.Fatal("input required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "cold.db")
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data = nil
	// The retained failed fixture can have committed frames in WAL. Preserve
	// those frames in the isolated copy; never checkpoint the original evidence.
	if wal, e := os.ReadFile(input + "-wal"); e == nil {
		if e = os.WriteFile(path+"-wal", wal, 0600); e != nil {
			t.Fatal(e)
		}
	} else if !os.IsNotExist(e) {
		t.Fatal(e)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	metrics := map[string]any{}
	disk := func(free, total int64) qualityDisk {
		db, _ := sqliteFileSize(path, "database", false)
		wal, _ := sqliteFileSize(path+"-wal", "WAL", true)
		return qualityDisk{DB: db, WAL: wal, Free: free, Total: total, RootFree: free, RootTotal: total}
	}
	s.qualityDiskObserve = func() (qualityDisk, error) { return disk(18<<30, 20<<30), nil }
	id := fmt.Sprintf("%032x", 1000)
	start := time.Now()
	coldCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cfg, err := s.GetQualityConfigV2(coldCtx, id)
	cancel()
	elapsed := time.Since(start)
	metrics["cold_config_ms"] = float64(elapsed.Microseconds()) / 1000
	if err != nil || len(cfg.Targets) != 12 || elapsed >= 5*time.Second {
		t.Fatalf("first configuration without preheat %s %v", elapsed, err)
	}
	p := s.qualityBudget(ctx)
	if p.Scale != 1 || p.Pressure {
		t.Fatal("normal budget", p)
	}
	now := time.UnixMilli(1800000000000 / 3600000 * 3600000)
	r := validReport(id, 1, "quality_session", "boot", 2, 1, 1)
	r.NetworkQuality = true
	r.NetworkQualityV2 = a1Capabilities()
	r.NetworkQualityV2.Semantics = protocol.QualityStagedSemantics
	r.OS = "debian 13"
	start = time.Now()
	_, ok, _, err := s.ProcessReport(ctx, id, r, now)
	metrics["basic_report_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var seq int64
	s.db.QueryRow(`SELECT MAX(seq) FROM quality_raw WHERE agent_id=?`, id).Scan(&seq)
	target := cfg.Targets[0]
	sample := a2TCP(target, cfg.Revision, seq+1, now.Add(-time.Second), 20.13, "success")
	if target.Protocol == "icmp" {
		sample = a2Echo(target, cfg.Revision, seq+1, now.Add(-time.Second), []float64{20.13, -1, 21.13})
	}
	if target.Family == "ipv6" {
		sample.ResolvedIP = "::1"
	}
	batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}
	start = time.Now()
	ack, err := s.SaveQualityBatch(ctx, id, batch, now)
	metrics["save_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	start = time.Now()
	h, err := s.QualityHistoryRollup(ctx, id, target.ID, "1mo", now)
	metrics["month_query_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if err != nil || len(h.Groups) != 1 {
		t.Fatal(err)
	}
	// Configure the agent that timed out in the preserved partial fixture.
	newID := fmt.Sprintf("%032x", 1022)
	start = time.Now()
	newCfg, err := s.UpdateQualityConfigV2(ctx, newID, a1LaneConfig("0"), qualityTestResolver, now)
	metrics["new_agent_config_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if err != nil || len(newCfg.Targets) != 12 {
		t.Fatal("cold new configuration", err)
	}
	s.qualityDiskObserve = func() (qualityDisk, error) { return disk(80<<20, 1<<30), nil }
	s.qualityBudgetAt = time.Time{}
	r.Sequence = 3
	start = time.Now()
	_, ok, _, err = s.ProcessReport(ctx, id, r, now.Add(time.Second))
	metrics["pressure_basic_report_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if err != nil || !ok {
		t.Fatal("pressure basic", ok, err)
	}
	if !s.qualityBudget(ctx).Pressure {
		t.Fatal("pressure observation ignored")
	}
	s.qualityDiskObserve = func() (qualityDisk, error) { return disk(18<<30, 20<<30), nil }
	s.qualityBudgetAt = time.Time{}
	start = time.Now()
	p = s.qualityBudget(ctx)
	metrics["pressure_recovery_budget_ms"] = float64(time.Since(start).Microseconds()) / 1000
	if p.Pressure || p.Scale != 1 {
		t.Fatal("recovery", p)
	}
	metrics["request_dbstat_calibration"] = "removed; no scan or warm-up"
	metrics["physical_db_wal_charged_bytes"] = p.Disk.DB + p.Disk.WAL
	metrics["method"] = "copied preserved partial DB, real Store operations; injected 20GiB/pressure free-space observation, real DB/WAL lengths"
	encoded, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "cold.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
