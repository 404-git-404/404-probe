package storage

import (
	"context"
	"testing"
	"time"
)

func seedMinuteTotals(t *testing.T, s *Store, id string, bucket int64, rx, tx int64) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,1,0,0,0,0,0,0,0,0,0,?,?)`, id, bucket, rx, tx); err != nil {
		t.Fatal(err)
	}
}

func TestUsage24hFromTheMinuteHistory(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 12, 0, 30, 0, time.UTC)
	minute := func(back time.Duration) int64 { return now.Add(-back).Truncate(time.Minute).UnixMilli() }

	if _, ok, err := s.GetUsage24h(ctx, id, now); err != nil || ok {
		t.Fatalf("no history: ok=%v err=%v", ok, err)
	}
	seedMinuteTotals(t, s, id, minute(time.Minute), 1000, 500)
	if _, ok, err := s.GetUsage24h(ctx, id, now); err != nil || ok {
		t.Fatalf("a single minute is no usage: ok=%v err=%v", ok, err)
	}
	// A history shorter than a day: the usage is what the device did since the first minute, and the
	// coverage says how long that is.
	seedMinuteTotals(t, s, id, minute(10*time.Hour), 400, 100)
	seedMinuteTotals(t, s, id, minute(time.Hour), 900, 450)
	got, ok, err := s.GetUsage24h(ctx, id, now)
	if err != nil || !ok || got.RX != 600 || got.TX != 400 || got.CoverageMS != (10*time.Hour-time.Minute).Milliseconds() {
		t.Fatalf("short history: %+v ok=%v err=%v", got, ok, err)
	}
	// Minutes older than 24 hours never take part: the first row is the first one inside the day.
	seedMinuteTotals(t, s, id, minute(30*time.Hour), 10, 5)
	seedMinuteTotals(t, s, id, minute(25*time.Hour), 20, 8)
	got, ok, _ = s.GetUsage24h(ctx, id, now)
	if !ok || got.RX != 600 || got.TX != 400 {
		t.Fatalf("old rows leaked in: %+v", got)
	}
	// A complete day: the coverage is just under 24 hours.
	seedMinuteTotals(t, s, id, minute(23*time.Hour+59*time.Minute), 100, 50)
	got, ok, _ = s.GetUsage24h(ctx, id, now)
	if !ok || got.RX != 900 || got.TX != 450 || got.CoverageMS != (23*time.Hour+58*time.Minute).Milliseconds() {
		t.Fatalf("complete history: %+v", got)
	}
	// Counters that went backwards are no usage.
	if _, err := s.db.Exec(`UPDATE minute_metrics SET rx_total=5 WHERE agent_id=? AND bucket=?`, id, minute(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetUsage24h(ctx, id, now); ok {
		t.Fatal("a decreasing counter was reported as usage")
	}
	// Another device has its own history.
	if _, ok, _ := s.GetUsage24h(ctx, "another", now); ok {
		t.Fatal("rows of another agent")
	}
}
