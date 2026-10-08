package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func seedResourceMinute(t *testing.T, s *Store, id string, bucket int64, samples int) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,?,24,100,0,40,2,4,6,2000,4000,9007199254740993,42)`, id, bucket, samples)
	if err != nil {
		t.Fatal(err)
	}
}

func TestResourceHistoryOriginalMeansAndReadOnly(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	at := time.Unix(1800000000, 0)
	for i := 0; i < 3; i++ {
		r := validReport(id, 1, "session", "boot", uint64(i+1), uint64(100+i*10000), uint64(200+i*20000))
		r.CPUPercent = float64(10 + i*10)
		if i == 2 {
			r.RXBytes = 1
			r.TXBytes = 2
		} // Existing counter-reset zero remains in the mean.
		if _, ok, reason, err := s.ProcessReport(ctx, id, r, at.Add(time.Duration(i)*10*time.Second)); err != nil || !ok {
			t.Fatalf("%v %v %s", err, ok, reason)
		}
	}
	before, err := s.History(ctx, id, at)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ResourceHistory(ctx, id, at.Add(time.Second), at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got, before) || got[0].CPU != 20 || got[0].RXRate != 1000.0/3 || got[0].TXRate != 2000.0/3 {
		t.Fatalf("means got=%+v legacy=%+v", got, before)
	}
	var samples int
	var sum float64
	if err := s.db.QueryRow("SELECT samples,cpu_sum FROM minute_metrics WHERE agent_id=?", id).Scan(&samples, &sum); err != nil || samples != 3 || sum != 60 {
		t.Fatalf("changed stored means: %d %g %v", samples, sum, err)
	}
	after, err := s.History(ctx, id, at)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("legacy changed", err)
	}
}

func TestResourceHistoryClosedEdgesMissingMinutesAndIsolation(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	base := int64(1800000000000)
	for _, offset := range []int64{180000, 0, 60000, 300000} {
		seedResourceMinute(t, s, id, base+offset, 2)
	}
	got, err := s.ResourceHistory(ctx, id, time.UnixMilli(base+59999), time.UnixMilli(base+180001))
	if err != nil || len(got) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	for i, want := range []int64{base, base + 60000, base + 180000} {
		if got[i].Timestamp != want || got[i].CPU != 12 || got[i].RXRate != 1000 || got[i].RXTotal != 9007199254740993 {
			t.Fatalf("%+v", got[i])
		}
	}
	empty, err := s.ResourceHistory(ctx, "other", time.UnixMilli(base), time.UnixMilli(base+1))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%v %v", empty, err)
	}
	legacy, err := s.History(ctx, "other", time.UnixMilli(base))
	if err != nil || legacy != nil {
		t.Fatal("legacy nil changed")
	}
	single, err := s.ResourceHistory(ctx, id, time.UnixMilli(base), time.UnixMilli(base+1))
	if err != nil || len(single) != 1 {
		t.Fatal(single, err)
	}
}

func TestResourceHistorySQLUpperBoundAndHardLimit(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	base := int64(1800000000000)
	ctx := context.Background()
	// Insert descending, with thousands of clock-rollback future buckets.
	for i := 6440; i >= 0; i-- {
		seedResourceMinute(t, s, id, base+int64(i)*60000, 2)
	}
	got, err := s.ResourceHistory(ctx, id, time.UnixMilli(base+1), time.UnixMilli(base+ResourceHistoryMaxSpanMS+1))
	if err != nil || len(got) != 1441 {
		t.Fatal(len(got), err)
	}
	for i, p := range got {
		if p.Timestamp != base+int64(i)*60000 {
			t.Fatalf("index %d: %+v", i, p)
		}
	}
	legacy, err := s.History(ctx, id, time.UnixMilli(base))
	if err != nil || len(legacy) != 6441 {
		t.Fatal("legacy upper bound changed", len(legacy), err)
	}
	// Corrupt off-grid rows prove the SQL hard LIMIT, independent of minute PK math.
	for i := 1; i <= 1500; i++ {
		seedResourceMinute(t, s, id, base+int64(i), 2)
	}
	got, err = s.ResourceHistory(ctx, id, time.UnixMilli(base), time.UnixMilli(base+60000))
	if err != nil || len(got) != 1441 {
		t.Fatal("hard limit", len(got), err)
	}
}

func TestResourceHistoryErrorsCancellationAndRowsReuse(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	base := int64(1800000000000)
	for _, pair := range [][2]int64{{-1, 1}, {base, base}, {base + 1, base}, {base, base + ResourceHistoryMaxSpanMS + 1}} {
		if _, err := s.ResourceHistory(ctx, id, time.UnixMilli(pair[0]), time.UnixMilli(pair[1])); !errors.Is(err, ErrResourceHistoryRange) {
			t.Fatal(pair, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ResourceHistory(canceled, id, time.UnixMilli(base), time.UnixMilli(base+1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Cancel a real QueryContext while waiting for the Store's sole connection.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	_, waitErr := s.ResourceHistory(waitCtx, id, time.UnixMilli(base), time.UnixMilli(base+1))
	stop()
	conn.Close()
	if !errors.Is(waitErr, context.DeadlineExceeded) || s.db.Stats().InUse != 0 {
		t.Fatal("canceled acquisition leaked connection", waitErr)
	}
	seedResourceMinute(t, s, id, base, 0) // Division by zero yields NULL: real Scan failure.
	if _, err := s.ResourceHistory(ctx, id, time.UnixMilli(base), time.UnixMilli(base+1)); err == nil {
		t.Fatal("missing scan failure")
	}
	if s.db.Stats().InUse != 0 {
		t.Fatal("scan error leaked rows")
	}
	if _, err := s.db.Exec("UPDATE minute_metrics SET samples=2"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if p, err := s.ResourceHistory(ctx, id, time.UnixMilli(base), time.UnixMilli(base+1)); err != nil || len(p) != 1 || s.db.Stats().InUse != 0 {
			t.Fatal("reuse", err)
		}
	}
	if _, err := s.db.Exec("ALTER TABLE minute_metrics RENAME TO unavailable_minutes"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResourceHistory(ctx, id, time.UnixMilli(base), time.UnixMilli(base+1)); err == nil {
		t.Fatal("missing query failure")
	}
	if s.db.Stats().InUse != 0 {
		t.Fatal("query error leaked connection")
	}
}
