package storage

import (
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Opt-in materialized capacity fixtures. Historical observations are folded
// with the production contribution/merge/codec, then bulk-written through real
// SQLite tables and indexes. This is an equivalent-data capacity test, not an
// upload throughput claim. Ordinary tests never create these large artifacts.
func TestS1MaterializedCapacityMatrix(t *testing.T) {
	root := os.Getenv("QUALITY_S1_CAPACITY_ROOT")
	if root == "" {
		t.Skip("large equivalent-data fixture is opt-in")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, agents := range []int{1, 3, 10, 30} {
		for _, lanes := range []int{6, 12} {
			t.Run(fmt.Sprintf("%dx%d", agents, lanes), func(t *testing.T) { s1CapacityCase(t, root, agents, lanes) })
		}
	}
}

// Re-read every completed fixture with the final Go budget query. This also
// binds earlier bulk fixtures to later budget-only directed repairs.
func TestS1RecheckMaterializedCapacity(t *testing.T) {
	root := os.Getenv("QUALITY_S1_CAPACITY_RECHECK_ROOT")
	if root == "" {
		t.Skip("capacity reread is opt-in")
	}
	results := []map[string]any{}
	for _, agents := range []int{1, 3, 10, 30} {
		for _, lanes := range []int{6, 12} {
			path := filepath.Join(root, fmt.Sprintf("capacity-%dx%d.db", agents, lanes))
			if agents == 30 && lanes == 12 && os.Getenv("QUALITY_S1_CAPACITY_FINAL_LARGE_DB") != "" {
				path = os.Getenv("QUALITY_S1_CAPACITY_FINAL_LARGE_DB")
			}
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			var used, auto int64
			if err = s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE aggregate=TRUE AND (name LIKE 'quality_%' OR name IN(SELECT name FROM sqlite_master WHERE type='index' AND tbl_name LIKE 'quality_%'))`).Scan(&used); err != nil {
				t.Fatal(err)
			}
			s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE aggregate=TRUE AND name LIKE 'sqlite_autoindex_quality_%'`).Scan(&auto)
			s.qualityDiskObserve = func() (qualityDisk, error) {
				db, _ := sqliteFileSize(path, "database", false)
				wal, _ := sqliteFileSize(path+"-wal", "WAL", true)
				return qualityDisk{DB: db, WAL: wal, Free: 18 << 30, Total: 20 << 30, RootFree: 18 << 30, RootTotal: 20 << 30}, nil
			}
			p := s.qualityBudget(context.Background())
			if p.Scale != 1 || p.Pressure || p.Degraded || p.MigrationBytes != 0 {
				t.Fatalf("final budget %dx%d %+v", agents, lanes, p)
			}
			counts := map[string]int64{}
			for _, name := range []string{"raw", "receipts", "facts", "buckets", "minute", "series", "sessions"} {
				var n, c int64
				s.db.QueryRow(`SELECT COUNT(*) FROM quality_` + name).Scan(&n)
				s.db.QueryRow(`SELECT value FROM quality_totals WHERE kind=?`, name).Scan(&c)
				if n != c {
					t.Fatalf("counter %s %d/%d", name, n, c)
				}
				counts[name] = n
			}
			if counts["series"] != int64(agents*lanes) {
				t.Fatal("series", counts)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			results = append(results, map[string]any{"agents": agents, "lanes_per_agent": lanes, "quality_dbstat_bytes_including_autoindexes": used, "automatic_index_bytes": auto, "scale": p.Scale, "migration_reserved_bytes": p.MigrationBytes, "counts": counts})
		}
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reportPath := os.Getenv("QUALITY_S1_CAPACITY_RECHECK_REPORT")
	if reportPath == "" {
		reportPath = filepath.Join(root, "final-Go-recheck.json")
	}
	if err = os.WriteFile(reportPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}

func s1CapacityCase(t *testing.T, root string, agents, lanes int) {
	ctx := context.Background()
	path := filepath.Join(root, fmt.Sprintf("capacity-%dx%d.db", agents, lanes))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("capacity target already exists: %s", path)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.UnixMilli(1800000000000 / 3600000 * 3600000)
	first := now.Add(-30 * 24 * time.Hour).UnixMilli()
	started := time.Now()
	var peakWAL int64
	var observations int64
	for a := 0; a < agents; a++ {
		id := fmt.Sprintf("%032x", a+1000)
		_, hash, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.AddAgent(ctx, id, "capacity", hash, now); err != nil {
			t.Fatal(err)
		}
		caps := a1Capabilities()
		caps.Semantics = protocol.QualityStagedSemantics
		a1Report(t, s, id, 1, now, caps, "debian 13")
		u := a1LaneConfig("0")
		if lanes == 6 {
			filtered := []protocol.QualityLaneChoice{}
			for _, choice := range u.Lanes {
				if choice.Family == "ipv4" {
					filtered = append(filtered, choice)
				}
			}
			u.Lanes = filtered
			u.IPv6 = false
		}
		cfg, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, time.UnixMilli(first))
		if err != nil || len(cfg.Targets) != lanes {
			t.Fatalf("config %d %v", len(cfg.Targets), err)
		}
		for lane, target := range cfg.Targets {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			must := func(q string, args ...any) {
				if _, err := tx.ExecContext(ctx, q, args...); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
			}
			must(`INSERT OR IGNORE INTO quality_sessions(agent_id,epoch,session_id) VALUES(?,1,'quality_session')`, id)
			must(`INSERT INTO quality_series(agent_id,target_id,semantics) VALUES(?,?,?)`, id, target.ID, protocol.QualityStagedSemantics)
			var session, series int64
			if err = tx.QueryRowContext(ctx, `SELECT id FROM quality_sessions WHERE agent_id=?`, id).Scan(&session); err != nil {
				t.Fatal(err)
			}
			if err = tx.QueryRowContext(ctx, `SELECT id FROM quality_series WHERE target_id=?`, target.ID).Scan(&series); err != nil {
				t.Fatal(err)
			}
			bins := map[int64]map[int64]qualityBucketData{}
			for _, res := range qualityResolutions {
				bins[res] = map[int64]qualityBucketData{}
			}
			baseID := int64((a*lanes + lane) * 86400)
			var prior *qualityFact
			samples := make([]protocol.QualitySample, 17)
			parts := make([]qualityBucketData, 17)
			for k := 0; k < 17; k++ {
				v := 20.13 + float64(k)*.25
				sample := a2TCP(target, cfg.Revision, 1, now, v, "success")
				if target.Protocol == "icmp" {
					sample = a2Echo(target, cfg.Revision, 1, now, []float64{v, v + .17, v + .31})
					if k == 4 {
						sample = a2Echo(target, cfg.Revision, 1, now, []float64{-1, -1, -1})
					}
					if k == 7 {
						sample = a2Echo(target, cfg.Revision, 1, now, []float64{v, -1, v + .31})
					}
				} else {
					if k == 4 {
						sample = a2TCP(target, cfg.Revision, 1, now, 0, "timeout")
					}
					if k == 7 {
						sample.Outcome = "canceled"
						sample.LatencyMS = nil
						sample.Details = (&protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "literal", ErrorStage: "local", ErrorCode: "canceled", IntervalMS: 30000}).Encode()
					}
				}
				if target.Family == "ipv6" {
					sample.ResolvedIP = "::1"
				}
				samples[k] = sample
				parts[k], err = qualityContribution(sample, target.Protocol)
				if err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 86400; i++ {
				if i%17 == 13 {
					continue
				} // absent jobs remain UNKNOWN; no fabricated sample.
				stamp := first + int64(i)*30000
				seq := int64(i*lanes + lane + 1)
				sample := samples[i%17]
				sample.Sequence = strconv.FormatInt(seq, 10)
				sample.ScheduledAt, sample.StartedAt, sample.FinishedAt = stamp, stamp, stamp+2500
				part := parts[i%17]
				cfgRev, _ := protocol.QualityInteger(sample.ConfigRevision)
				slotRev, _ := protocol.QualityInteger(sample.SlotRevision)
				fact := qualityFact{ID: baseID + int64(i) + 1, Target: target.ID, Series: series, Session: session, Seq: seq, Time: stamp, Config: cfgRev, Slot: slotRev, IP: sample.ResolvedIP, Interval: 30000, Eligible: target.Protocol == "tcp" && sample.Outcome == "success" && sample.Attempt() && sample.LatencyMS != nil}
				part.FirstID, part.LastID = fact.ID, fact.ID
				part.FirstTime, part.LastTime = stamp, stamp
				part.FirstSeq, part.LastSeq = seq, seq
				part.ConfigMin, part.ConfigMax, part.SlotMin, part.SlotMax = cfgRev, cfgRev, slotRev, slotRev
				if sample.LatencyMS != nil {
					fact.RTT.Valid = true
					fact.RTT.Float64 = *sample.LatencyMS
				}
				difference, pair := qualityFactPair(prior, &fact)
				if pair {
					part.PreviousID = prior.ID
					part.IncomingPair = true
					part.IncomingJitter = difference
				}
				for j, res := range qualityResolutions {
					bucket := stamp / res * res
					if bucket < now.Add(-qualityBucketRetentions[j]).UnixMilli() {
						continue
					}
					b, ok := bins[res][bucket]
					if !ok {
						b = qualityNewBucket()
					}
					qualityMergeBucket(&b, part)
					bins[res][bucket] = b
				}
				if stamp >= now.Add(-QualityReceiptRetention).UnixMilli() {
					canonical, e := json.Marshal(sample)
					if e != nil {
						t.Fatal(e)
					}
					digest := sha256.Sum256(canonical)
					must(`INSERT INTO quality_receipts VALUES(?,?,?,?,?,?,?,?)`, session, seq, series, stamp, cfgRev, slotRev, digest[:], len(canonical))
					if stamp >= now.Add(-QualityRawRetention).UnixMilli() {
						attempt, success := 0, 0
						if sample.Attempt() {
							attempt = 1
							if sample.Outcome == "success" {
								success = 1
							}
						}
						sent, received := sample.Sent, sample.Received
						must(`INSERT INTO quality_raw(agent_id,target_id,epoch,session_id,seq,slot_revision,config_revision,sample_time,received_at,scheduled_at,finished_at,duration_ms,outcome,resolved_ip,raw_sent,raw_received,content_hash,attempt,success,latency,packet_sent,packet_received,details_json) VALUES(?,?,1,'quality_session',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, target.ID, seq, slotRev, cfgRev, stamp, stamp+3000, sample.ScheduledAt, sample.FinishedAt, sample.DurationMS, sample.Outcome, sample.ResolvedIP, sent, received, digest[:], attempt, success, sample.LatencyMS, sent, received, string(sample.Details))
						var rawID int64
						if e = tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&rawID); e != nil {
							t.Fatal(e)
						}
						must(`INSERT INTO quality_facts VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, fact.ID, rawID, target.ID, series, session, seq, stamp, cfgRev, slotRev, sample.ResolvedIP, fact.Eligible, fact.RTT, 30000)
					}
				}
				if stamp >= now.Add(-24*time.Hour).UnixMilli() {
					sum := 0.0
					if sample.LatencyMS != nil {
						sum = *sample.LatencyMS
					}
					attempt, success := 0, 0
					if sample.Attempt() {
						attempt = 1
						if sample.Outcome == "success" {
							success = 1
						}
					}
					must(`INSERT INTO quality_minute(agent_id,target_id,bucket,count,attempts,successes,latency_sum,sent,received) VALUES(?,?,?,1,?,?,?,?,?) ON CONFLICT(target_id,bucket) DO UPDATE SET count=count+1,attempts=attempts+excluded.attempts,successes=successes+excluded.successes,latency_sum=latency_sum+excluded.latency_sum,sent=sent+excluded.sent,received=received+excluded.received`, id, target.ID, stamp/60000*60000, attempt, success, sum, sample.Sent, sample.Received)
				}
				if stamp == now.Add(-QualityRawRetention).UnixMilli()-30000 {
					must(`INSERT INTO quality_facts VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, fact.ID, 0, target.ID, series, session, seq, stamp, cfgRev, slotRev, sample.ResolvedIP, fact.Eligible, fact.RTT, 30000)
				}
				prior = &fact
				observations++
			}
			for res, byTime := range bins {
				for stamp, b := range byTime {
					wire, e := qualityEncodeBucket(b)
					if e != nil {
						t.Fatal(e)
					}
					must(`INSERT INTO quality_buckets VALUES(?,?,?,?)`, series, res, stamp, wire)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if info, e := os.Stat(path + "-wal"); e == nil {
				peakWAL = max(peakWAL, info.Size())
			}
			if _, err = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
				t.Fatal(err)
			}
		}
	}
	counts := map[string]int64{}
	for _, table := range []string{"raw", "minute", "receipts", "facts", "buckets", "series", "sessions"} {
		var count int64
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM quality_` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	expectedRaw, expectedReceipt := int64(0), int64(0)
	for i := 0; i < 86400; i++ {
		if i%17 == 13 {
			continue
		}
		if i >= 86400-960 {
			expectedRaw++
		}
		if i >= 86400-5760 {
			expectedReceipt++
		}
	}
	if counts["raw"] != int64(agents*lanes)*expectedRaw || counts["receipts"] != int64(agents*lanes)*expectedReceipt {
		t.Fatal("retention occupancy", counts)
	}
	var integrity string
	if err = s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %s %v", integrity, err)
	}
	var bad int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Fatal("foreign keys", bad, err)
	}
	var dbstat int64
	if err = s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE aggregate=TRUE AND (name LIKE 'quality_%' OR name IN(SELECT name FROM sqlite_master WHERE type='index' AND tbl_name LIKE 'quality_%'))`).Scan(&dbstat); err != nil {
		t.Fatal("actual dbstat unavailable", err)
	}
	s.qualityDiskObserve = func() (qualityDisk, error) {
		return qualityDisk{DB: dbstat, Free: 18 << 30, Total: 20 << 30, RootFree: 18 << 30, RootTotal: 20 << 30}, nil
	}
	s.qualityBudgetAt = time.Time{}
	budget := s.qualityBudget(ctx)
	if budget.Scale != 1 || budget.Pressure || budget.Degraded {
		t.Fatal("normal 30s budget", budget)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"agents": agents, "lanes_per_agent": lanes, "equivalent_days": 30, "folded_observations": observations, "counts": counts, "db_bytes": info.Size(), "quality_dbstat_bytes": dbstat, "peak_wal_bytes": peakWAL, "wall_seconds": time.Since(started).Seconds(), "scale": budget.Scale, "method": "bulk equivalent-data; production contribution merge codec; actual SQLite tables indexes and triggers; not online throughput"}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "capacity-"+strconv.Itoa(agents)+"x"+strconv.Itoa(lanes)+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
