package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTrafficBaselineIsIdempotentAndUsesPermanentTotals(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(1000, 0)
	first := validReport(agentID, 1, "session", "boot", 1, 1000, 2000)
	first.CollectedAt = base.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, first, base); err != nil || !accepted {
		t.Fatalf("first report accepted=%t err=%v", accepted, err)
	}
	second := validReport(agentID, 1, "session", "boot", 2, 1400, 2600)
	second.CollectedAt = base.Add(time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, second, base.Add(time.Second)); err != nil || !accepted {
		t.Fatalf("second report accepted=%t err=%v", accepted, err)
	}
	before, err := store.TrafficTotals(ctx, agentID)
	if err != nil || before.RXTotal != 400 || before.TXTotal != 600 || before.StartedAt != nil {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	requestID := "0123456789abcdef0123456789abcdef"
	reset, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(2*time.Second), time.Minute)
	if err != nil || reset.RXTotal != 0 || reset.TXTotal != 0 || reset.StartedAt == nil {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	third := validReport(agentID, 1, "session", "boot", 3, 1500, 2800)
	third.CollectedAt = base.Add(3 * time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, third, base.Add(3*time.Second)); err != nil || !accepted {
		t.Fatalf("third report accepted=%t err=%v", accepted, err)
	}
	replayed, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(4*time.Second), time.Minute)
	if err != nil || replayed.RXTotal != 0 || replayed.TXTotal != 0 || replayed.StartedAt == nil || *replayed.StartedAt != base.Add(2*time.Second).UnixMilli() {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	current, err := store.TrafficTotals(ctx, agentID)
	if err != nil || current.RXTotal != 100 || current.TXTotal != 200 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestTrafficResetConcurrentDuplicateRequestIsAppliedOnce(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(1000, 0)
	report := validReport(agentID, 1, "session", "boot", 1, 1000, 2000)
	report.CollectedAt = base.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, base); err != nil || !accepted {
		t.Fatal(err)
	}
	const workers = 8
	requestID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	results := make(chan TrafficTotals, workers)
	errorsSeen := make(chan error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(time.Second), time.Minute)
			results <- result
			errorsSeen <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent reset error=%v", err)
		}
	}
	for result := range results {
		if result.StartedAt == nil || *result.StartedAt != base.Add(time.Second).UnixMilli() || result.RXTotal != 0 || result.TXTotal != 0 {
			t.Fatalf("result=%+v", result)
		}
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_traffic_reset_requests WHERE request_id=?`, requestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("request rows=%d err=%v", count, err)
	}
}

func TestTrafficResetMigrationPreservesLatestV15IdempotencyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v15.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	base := time.Unix(1000, 0)
	report := validReport(agentID, 1, "session", "boot", 1, 1000, 2000)
	report.CollectedAt = base.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, base); err != nil || !accepted {
		t.Fatal(err)
	}
	requestID := "dddddddddddddddddddddddddddddddd"
	if _, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE agent_traffic_reset_requests`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=16`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(2*time.Minute), time.Minute)
	if err != nil || replayed.StartedAt == nil || *replayed.StartedAt != base.Add(time.Second).UnixMilli() {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

func TestTrafficBaselineRequiresSample(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	_, err := store.ResetTrafficTotals(context.Background(), agentID, "0123456789abcdef0123456789abcdef", time.Now(), time.Minute)
	if !errors.Is(err, ErrTrafficSampleUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestTrafficResetRequestRemainsIdempotentAcrossLaterReset(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(1000, 0)
	report := validReport(agentID, 1, "session", "boot", 1, 1000, 2000)
	report.CollectedAt = base.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, base); err != nil || !accepted {
		t.Fatalf("report accepted=%t err=%v", accepted, err)
	}
	requestA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requestB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	firstA, err := store.ResetTrafficTotals(ctx, agentID, requestA, base.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report.Sequence = 2
	report.RXBytes, report.TXBytes = 1100, 2200
	report.CollectedAt = base.Add(2 * time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, base.Add(2*time.Second)); err != nil || !accepted {
		t.Fatalf("second report accepted=%t err=%v", accepted, err)
	}
	if _, err := store.ResetTrafficTotals(ctx, agentID, requestB, base.Add(3*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	replayedA, err := store.ResetTrafficTotals(ctx, agentID, requestA, base.Add(4*time.Second), time.Minute)
	if err != nil || replayedA.StartedAt == nil || firstA.StartedAt == nil || *replayedA.StartedAt != *firstA.StartedAt {
		t.Fatalf("first=%+v replayed=%+v err=%v", firstA, replayedA, err)
	}
	current, err := store.TrafficTotals(ctx, agentID)
	if err != nil || current.RXTotal != 0 || current.TXTotal != 0 || current.StartedAt == nil || *current.StartedAt != base.Add(3*time.Second).UnixMilli() {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestTrafficResetRequiresFreshSampleInsideTransaction(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	base := time.Unix(1000, 0)
	report := validReport(agentID, 1, "session", "boot", 1, 1000, 2000)
	report.CollectedAt = base.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, base); err != nil || !accepted {
		t.Fatal(err)
	}
	_, err := store.ResetTrafficTotals(context.Background(), agentID, "cccccccccccccccccccccccccccccccc", base.Add(2*time.Minute), time.Minute)
	if !errors.Is(err, ErrTrafficSampleUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
