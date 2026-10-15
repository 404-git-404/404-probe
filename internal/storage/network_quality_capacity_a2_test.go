package storage

import (
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestA2ControlledCapacityActualEnabledAgentsAndSQLite(t *testing.T) {
	if os.Getenv("PROBE_A2_CAPACITY") != "1" {
		t.Skip("opt-in controlled SQLite load and virtual 48h clock")
	}
	for _, n := range []int{10, 30, 50, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, first, old, base := qualityFixture(t, filepath.Join(t.TempDir(), "capacity.db"))
			ctx := context.Background()
			ids := []string{}
			configs := []protocol.QualityConfig{}
			caps := a1Capabilities()
			caps.Semantics = protocol.QualityStagedSemantics
			for i := 0; i < n; i++ {
				id := first
				rev := old.Revision
				if i > 0 {
					id, _ = auth.NewID()
					_, hash, _ := auth.NewToken()
					if err := s.AddAgent(ctx, id, "active", hash, base); err != nil {
						t.Fatal(err)
					}
					rev = "0"
				}
				a1Report(t, s, id, 2, base, caps, "debian 13")
				c, err := s.UpdateQualityConfigV2(ctx, id, a1LaneConfig(rev), qualityTestResolver, base)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				configs = append(configs, c)
			}
			for i := 0; i < 50; i++ {
				id, _ := auth.NewID()
				_, hash, _ := auth.NewToken()
				if err := s.AddAgent(ctx, id, "registered-only", hash, base); err != nil {
					t.Fatal(err)
				}
			}
			size := int64(256 << 30)
			nearFull := false
			s.qualityDiskObserve = func() (qualityDisk, error) {
				db, err := sqliteFileSize(s.databasePath, "database", false)
				if err != nil {
					return qualityDisk{}, err
				}
				wal, err := sqliteFileSize(s.databasePath+"-wal", "WAL", true)
				free := size*9/10 - db - wal
				rootFree := free
				if nearFull {
					rootFree = 8 << 20
				}
				return qualityDisk{DB: db, WAL: wal, Free: free, Total: size, RootFree: rootFree, RootTotal: size}, err
			}
			s.qualityBudgetAt = time.Time{}
			p := s.qualityBudget(ctx)
			if p.Agents != n || p.Lanes != n*12 || p.Scale != 1 || p.Pressure {
				t.Fatal("registered count mistaken for enabled load", p)
			}
			start := time.Now()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			seqs := make([]int64, n)
			committed := make([]int, n)
			acks := map[string]int{}
			for _, hour := range []int{0, 24, 47, 49} {
				phase := base.Add(time.Duration(hour) * time.Hour)
				for i, id := range ids {
					batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session"}
					flush := func() {
						if len(batch.Samples) == 0 {
							return
						}
						ack, err := s.SaveQualityBatch(ctx, id, batch, phase.Add(4*time.Minute))
						if err != nil {
							t.Fatal(err)
						}
						for _, a := range ack.Results {
							acks[a[2]]++
							if a[2] == "committed" {
								committed[i]++
							} else {
								t.Fatal("normal volume rejected", a, p)
							}
						}
						batch.Samples = nil
					}
					for round := 0; round < 8; round++ {
						at := phase.Add(time.Duration(round) * 30 * time.Second)
						for _, target := range configs[i].Targets {
							seqs[i]++
							sample := a2TCP(target, configs[i].Revision, seqs[i], at, 10+float64(round), "success")
							if target.Protocol == "icmp" {
								sample = a2Echo(target, configs[i].Revision, seqs[i], at, []float64{10, 20, 40})
							}
							batch.Samples = append(batch.Samples, sample)
							if len(batch.Samples) == 48 {
								flush()
							}
						}
					}
					flush()
					r := validReport(id, 1, "quality_session", "boot", uint64(hour+10), 1, 1)
					r.NetworkQuality = true
					r.NetworkQualityV2 = caps
					r.OS = "debian 13"
					if _, accepted, _, err := s.ProcessReport(ctx, id, r, phase.Add(5*time.Minute)); err != nil || !accepted {
						t.Fatal("basic report affected", accepted, err)
					}
				}
				if hour == 49 {
					for pass := 0; pass < 32; pass++ {
						_, more, err := s.CleanupQualityBatch(ctx, phase)
						if err != nil {
							t.Fatal(err)
						}
						if !more {
							break
						}
					}
				}
			}
			for _, v := range committed {
				if v != 384 {
					t.Fatal("normal admission unfair", committed)
				}
			}
			health, err := s.CheckpointWAL(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var used, rawPages, minutePages, rawCount, minuteCount int64
			queries := []struct {
				q string
				p *int64
			}{{`SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name LIKE 'quality_%'`, &used}, {`SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name='quality_raw' OR name LIKE 'quality_raw_%' OR name LIKE 'sqlite_autoindex_quality_raw_%'`, &rawPages}, {`SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name='quality_minute' OR name LIKE 'quality_minute_%' OR name LIKE 'sqlite_autoindex_quality_minute_%'`, &minutePages}, {`SELECT COUNT(*) FROM quality_raw`, &rawCount}, {`SELECT COUNT(*) FROM quality_minute`, &minuteCount}}
			for _, q := range queries {
				if err := s.db.QueryRow(q.q).Scan(q.p); err != nil {
					t.Fatal(err)
				}
			}
			queryStart := time.Now()
			h, err := s.QualityHistoryV2(ctx, ids[0], configs[0].Targets[0].ID, 24, base.Add(49*time.Hour+5*time.Minute))
			queryTime := time.Since(queryStart)
			if err != nil || len(h.Groups) != 1 {
				t.Fatal(h, err)
			}
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			s.qualityBudgetAt = time.Time{}
			p = s.qualityBudget(ctx)
			rawPer := float64(rawPages) / float64(rawCount)
			minutePer := float64(minutePages) / float64(minuteCount)
			if p.RawBytes < int64(rawPer) || p.MinuteBytes < int64(minutePer) {
				t.Fatal("capacity estimate below actual allocation", p, rawPer, minutePer)
			}
			nearFull = true
			s.qualityBudgetAt = time.Time{}
			paused, err := s.GetQualitySessionConfig(ctx, ids[0], 1, "quality_session", 2)
			if err != nil || paused.Enabled || paused.State != "storage_pressure" {
				t.Fatal(paused, err)
			}
			managed, err := s.GetQualityConfig(ctx, ids[0])
			if err != nil || !managed.Enabled {
				t.Fatal("management configuration overwritten by admission projection", managed, err)
			}
			nearFull = false
			size = 2 << 30
			s.qualityBudgetAt = time.Time{}
			small := s.qualityBudget(ctx)
			throttle, err := s.GetQualitySessionConfig(ctx, ids[0], 1, "quality_session", 2)
			if err != nil || throttle.IntervalMS != 30000*small.Scale {
				t.Fatal(throttle, small, err)
			}
			size = 256 << 30
			s.qualityBudgetAt = time.Time{}
			recovered, err := s.GetQualitySessionConfig(ctx, ids[0], 1, "quality_session", 2)
			if err != nil || !recovered.Enabled || recovered.IntervalMS != 30000 {
				t.Fatal("bounded recovery", recovered, err)
			}
			record := map[string]any{"agents_enabled": n, "lanes_enabled": n * 12, "registered_agents": n + 50, "virtual_hours": 49, "rows_committed": acks["committed"], "raw_surviving": rawCount, "minute_surviving": minuteCount, "quality_pages_bytes": used, "raw_allocation_per_row": rawPer, "minute_allocation_per_row": minutePer, "db_bytes": health.DatabaseBytes, "wal_bytes": health.WALBytes, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc, "total_allocation_delta": after.TotalAlloc - before.TotalAlloc, "wall_ms": time.Since(start).Milliseconds(), "history_query_us": queryTime.Microseconds(), "small_volume_scale": small.Scale, "small_volume_degraded": small.Degraded}
			b, _ := json.Marshal(record)
			t.Log(string(b))
		})
	}
}
