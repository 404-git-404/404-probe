package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunScheduledProbeJobCleanupStopsAtConvergence(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	wantCutoff := now.Add(-30 * 24 * time.Hour)
	calls := 0
	deleted, err := runScheduledProbeJobCleanup(context.Background(), wantCutoff, func(_ context.Context, before time.Time) (int64, bool, error) {
		calls++
		if before != wantCutoff {
			t.Fatalf("cutoff = %s, want %s", before, wantCutoff)
		}
		if calls == 1 {
			return 1000, true, nil
		}
		return 200, false, nil
	})
	if err != nil || deleted != 1200 || calls != 2 {
		t.Fatalf("deleted=%d calls=%d err=%v, want deleted=1200 calls=2", deleted, calls, err)
	}
}

func TestRunScheduledProbeJobCleanupCapsBatchesAndStopsOnError(t *testing.T) {
	t.Run("32 batch cap", func(t *testing.T) {
		calls := 0
		deleted, err := runScheduledProbeJobCleanup(context.Background(), time.Time{}, func(context.Context, time.Time) (int64, bool, error) {
			calls++
			return 1000, true, nil
		})
		if err != nil || calls != scheduledProbeJobCleanupMaxBatches || deleted != int64(calls*1000) {
			t.Fatalf("deleted=%d calls=%d err=%v", deleted, calls, err)
		}
	})

	t.Run("first error stops the round", func(t *testing.T) {
		wantErr := errors.New("retention batch failed")
		calls := 0
		deleted, err := runScheduledProbeJobCleanup(context.Background(), time.Time{}, func(context.Context, time.Time) (int64, bool, error) {
			calls++
			return 0, false, wantErr
		})
		if !errors.Is(err, wantErr) || calls != 1 || deleted != 0 {
			t.Fatalf("deleted=%d calls=%d err=%v", deleted, calls, err)
		}
	})
}
