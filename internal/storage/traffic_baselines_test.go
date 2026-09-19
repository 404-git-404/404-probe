package storage

import (
	"context"
	"errors"
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
	reset, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(2*time.Second))
	if err != nil || reset.RXTotal != 0 || reset.TXTotal != 0 || reset.StartedAt == nil {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	third := validReport(agentID, 1, "session", "boot", 3, 1500, 2800)
	third.CollectedAt = base.Add(3 * time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, third, base.Add(3*time.Second)); err != nil || !accepted {
		t.Fatalf("third report accepted=%t err=%v", accepted, err)
	}
	replayed, err := store.ResetTrafficTotals(ctx, agentID, requestID, base.Add(4*time.Second))
	if err != nil || replayed.RXTotal != 100 || replayed.TXTotal != 200 || replayed.StartedAt == nil || *replayed.StartedAt != base.Add(2*time.Second).UnixMilli() {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	current, err := store.TrafficTotals(ctx, agentID)
	if err != nil || current.RXTotal != 100 || current.TXTotal != 200 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestTrafficBaselineRequiresSample(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	_, err := store.ResetTrafficTotals(context.Background(), agentID, "0123456789abcdef0123456789abcdef", time.Now())
	if !errors.Is(err, ErrTrafficSampleUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
