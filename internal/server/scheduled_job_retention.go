package server

import (
	"context"
	"time"

	"404-probe/internal/storage"
)

const (
	scheduledProbeJobCleanupTimeout    = 10 * time.Second
	scheduledProbeJobCleanupMaxBatches = 32
)

type scheduledProbeJobRetentionBatch func(context.Context, time.Time) (deleted int64, more bool, err error)

func (a *App) cleanupScheduledProbeJobs() {
	ctx, cancel := context.WithTimeout(a.shutdown, scheduledProbeJobCleanupTimeout)
	defer cancel()
	before := a.now().Add(-storage.ScheduledProbeJobRetention)
	if _, err := runScheduledProbeJobCleanup(ctx, before, a.store.CleanupScheduledProbeJobsBatch); err != nil && a.shutdown.Err() == nil {
		a.logger.Error("clean scheduled probe jobs", "error", err)
	}
}

func runScheduledProbeJobCleanup(ctx context.Context, before time.Time, cleanup scheduledProbeJobRetentionBatch) (int64, error) {
	var totalDeleted int64
	for batch := 0; batch < scheduledProbeJobCleanupMaxBatches; batch++ {
		deleted, more, err := cleanup(ctx, before)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
		if !more {
			return totalDeleted, nil
		}
	}
	return totalDeleted, nil
}
