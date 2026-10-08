package server

import (
	"context"
	"time"
)

const planRenewalCleanupTimeout = 5 * time.Second
const planRenewalCleanupMaxBatches = 4

type planRenewalRetentionBatch func(context.Context, time.Time) (int64, bool, error)

func (a *App) cleanupPlanRenewalRequests() {
	ctx, cancel := context.WithTimeout(a.shutdown, planRenewalCleanupTimeout)
	defer cancel()
	if _, err := runPlanRenewalCleanup(ctx, a.now(), a.store.CleanupPlanRenewalRequestsBatch); err != nil && a.shutdown.Err() == nil {
		a.logger.Error("clean plan renewal receipts", "error", err)
	}
}
func runPlanRenewalCleanup(ctx context.Context, now time.Time, cleanup planRenewalRetentionBatch) (int64, error) {
	var total int64
	for batch := 0; batch < planRenewalCleanupMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, more, err := cleanup(ctx, now)
		if err != nil {
			return total, err
		}
		total += deleted
		if !more {
			return total, nil
		}
	}
	return total, nil
}
