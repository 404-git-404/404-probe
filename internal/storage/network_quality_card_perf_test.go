package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

// TestQualityCardPerformanceReport measures the complete read path of the judgement (design 1.6): 31 cards, six
// lanes per family, 24 hours of 1-minute buckets, three check intervals, concurrent browsers and a writer.
// It prints numbers and asserts nothing; it only runs with R25_PERF=1 (go test -run PerformanceReport -v).
func TestQualityCardPerformanceReport(t *testing.T) {
	if os.Getenv("R25_PERF") == "" {
		t.Skip("set R25_PERF=1 to measure")
	}
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const cards = 31
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	type card struct {
		k   *v4Kit
		cfg protocol.QualityV4Config
	}
	var all []card
	for i := 0; i < cards; i++ {
		id, _ := auth.NewID()
		_, hash, _ := auth.NewToken()
		if err := s.AddAgent(ctx, id, fmt.Sprintf("perf-%d", i), hash, now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		k := &v4Kit{t: t, s: s, id: id, now: now.Add(-25 * time.Hour), epoch: 1, session: "perf-session", ctx: ctx}
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		var slots []protocol.QualityV4Slot
		for _, name := range protocol.QualitySlotOrder() {
			slots = append(slots, v4Slot(name, name+".example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		}
		k.save(30000, slots...)
		k.enable(true)
		all = append(all, card{k: k, cfg: k.get()})
	}
	// Seed 24 hours of buckets for every lane of every card: 31 x 12 x 1440 = 535,680 rows.
	seed := func(intervalMS int) {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for ci, c := range all {
			cfgRev, _ := parseRevision(c.cfg.ConfigRevision)
			for _, lane := range c.cfg.Lanes {
				target := *lane.TargetID
				if _, err := tx.Exec(`INSERT OR IGNORE INTO quality_series(agent_id,target_id,semantics) VALUES(?,?,?)`, c.k.id, target, protocol.QualityStagedSemantics); err != nil {
					t.Fatal(err)
				}
				var series int64
				if err := tx.QueryRow(`SELECT id FROM quality_series WHERE target_id=? AND semantics=?`, target, protocol.QualityStagedSemantics).Scan(&series); err != nil {
					t.Fatal(err)
				}
				slotRev := slotRevisionOf(lane)
				per := max(1, 60000/intervalMS)
				for m := int64(0); m < 1440; m++ {
					b := qualityNewBucket()
					b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax, b.IntervalMax = cfgRev, cfgRev, slotRev, slotRev, intervalMS
					b.Stats.Count = per
					if lane.Protocol == "tcp" {
						b.Stats.ConnectAttempts, b.Stats.ConnectSuccesses, b.Stats.ValidRTTs = per, per, per
						b.Histogram[uint16(40+int(m)%20)] = uint32(per)
						b.RTTSum = float64(per) * 12
						if ci%3 == 0 && m >= 1000 && m < 1008 { // an outage: nothing connects
							b.Stats.ConnectSuccesses, b.Stats.ValidRTTs, b.Histogram, b.RTTSum = 0, 0, map[uint16]uint32{}, 0
						}
						if ci%3 == 0 && m >= 1200 && m < 1215 { // 15 slow minutes
							b.Histogram, b.RTTSum = map[uint16]uint32{3200: uint32(per)}, float64(per)*800
						}
					} else {
						b.Stats.Sent, b.Stats.Received = 3*per, 3*per
					}
					wire, err := qualityEncodeBucket(b)
					if err != nil {
						t.Fatal(err)
					}
					start := (now.UnixMilli()/60000 - 1440 + m) * 60000
					if _, err := tx.Exec(`INSERT OR REPLACE INTO quality_buckets(series_id,resolution,bucket_start,data) VALUES(?,60000,?,?)`, series, start, wire); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// Open intervals started a day ago (the kits ran at now-25h), so the whole day is usable.
	stats := func(label string, durations []time.Duration) {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var sum time.Duration
		for _, d := range durations {
			sum += d
		}
		t.Logf("%-46s n=%-4d mean=%-9s p50=%-9s p95=%-9s max=%-9s", label, len(durations), (sum / time.Duration(len(durations))).Round(time.Microsecond),
			durations[len(durations)/2].Round(time.Microsecond), durations[len(durations)*95/100].Round(time.Microsecond), durations[len(durations)-1].Round(time.Microsecond))
	}
	for _, intervalMS := range []int{30000, 60000, 300000} {
		seed(intervalMS)
		var one []time.Duration
		var bytes int
		for _, c := range all {
			begin := time.Now()
			sum, err := s.QualityCardSummary(ctx, c.k.id, "ipv4", now)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(sum) // the encoding is part of the answer
			one = append(one, time.Since(begin))
			bytes = max(bytes, len(wire))
		}
		stats(fmt.Sprintf("card summary, %d s interval, sequential", intervalMS/1000), one)
		t.Logf("  response size %d bytes", bytes)
		var marks []time.Duration
		var marksBytes int
		for _, c := range all[:12] {
			begin := time.Now()
			m, err := s.QualityMarks(ctx, c.k.id, "ipv4", now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli(), now)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(m)
			marks = append(marks, time.Since(begin))
			marksBytes = max(marksBytes, len(wire))
		}
		stats(fmt.Sprintf("marks 24 h, %d s interval, sequential", intervalMS/1000), marks)
		t.Logf("  marks response size %d bytes", marksBytes)
	}

	// 20 browsers, each asks for all 31 cards (the worst case is that they all look at the full grid), with
	// and without a writer that keeps reporting for all devices.
	concurrent := func(label string, withWriter bool) {
		var wg, background sync.WaitGroup
		var mu sync.Mutex
		var durations []time.Duration
		stop := make(chan struct{})
		var writes, accepted, rejected, cleanups int
		var writeWait time.Duration
		if withWriter {
			background.Add(2)
			go func() {
				defer background.Done()
				seq := uint64(100)
				for {
					select {
					case <-stop:
						return
					default:
					}
					for _, c := range all {
						seq++
						r := validReport(c.k.id, 1, "perf-session", "boot", seq, seq*10, seq*20)
						begin := time.Now()
						_, ok, _, err := s.ProcessReport(ctx, c.k.id, r, now.Add(time.Duration(seq)*time.Millisecond))
						mu.Lock()
						writes++
						writeWait += time.Since(begin)
						if err == nil && ok {
							accepted++
						} else {
							rejected++
						}
						mu.Unlock()
					}
				}
			}()
			go func() { // the periodic cleanup of the quality tables runs against the same connection
				defer background.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, _, err := s.CleanupQualityBatch(ctx, now); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					cleanups++
					mu.Unlock()
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
		begin := time.Now()
		for b := 0; b < 20; b++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, c := range all {
					started := time.Now()
					sum, err := s.QualityCardSummary(ctx, c.k.id, "ipv4", now)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = json.Marshal(sum)
					mu.Lock()
					durations = append(durations, time.Since(started))
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		elapsed := time.Since(begin)
		close(stop)
		background.Wait() // the counters below are final
		stats(label, durations)
		t.Logf("  wall %s for %d requests (%.0f requests/s, one connection, 20 goroutines that start together)", elapsed.Round(time.Millisecond), len(durations), float64(len(durations))/elapsed.Seconds())
		if withWriter {
			t.Logf("  writer: %d reports, %d accepted, %d rejected, mean %s each; %d cleanup passes", writes, accepted, rejected, (writeWait / time.Duration(max(writes, 1))).Round(time.Microsecond), cleanups)
		}
	}
	concurrent("20 browsers x 31 cards", false)
	concurrent("20 browsers x 31 cards, with a writer and cleanup", true)
}
