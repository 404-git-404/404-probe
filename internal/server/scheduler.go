package server

import (
	"errors"
	"time"

	"404-probe/internal/storage"
)

const schedulePollInterval = time.Second

// SchedulerLoop materializes fixed-interval schedules until the application
// is shut down. It performs an immediate pass before waiting for the first
// polling interval.
func (a *App) SchedulerLoop() {
	a.schedulerLoop(schedulePollInterval)
}

func (a *App) schedulerLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		a.runSchedulerTick()
		select {
		case <-a.shutdown.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runSchedulerTick() {
	now := a.now()
	ids, err := a.store.DueScheduleIDs(a.shutdown, now, storage.MaxScheduleCandidatesPerTick)
	if err != nil {
		if a.shutdown.Err() == nil {
			a.logger.Error("discover due schedules", "error", err)
		}
		return
	}
	backpressured := 0
	for _, scheduleID := range ids {
		outcome, err := a.store.MaterializeSchedule(a.shutdown, scheduleID, now, jobLeaseDuration)
		switch outcome.Result {
		case storage.MaterializeCreated:
			a.logger.Debug("materialized scheduled job", "schedule_id", scheduleID, "job_id", outcome.JobID, "scheduled_for", outcome.Slot)
		case storage.MaterializeBackpressured:
			backpressured++
		case storage.MaterializeAgentRevoked:
			a.logger.Warn("disabled schedule for revoked agent", "schedule_id", scheduleID)
		case storage.MaterializeCorruptDisabled:
			a.logger.Warn("disabled corrupt schedule", "schedule_id", scheduleID)
		}
		if err != nil && !errors.Is(err, storage.ErrCorruptProbeData) && a.shutdown.Err() == nil {
			a.logger.Error("materialize schedule", "schedule_id", scheduleID, "error", err)
		}
		if a.shutdown.Err() != nil {
			return
		}
	}
	if backpressured > 0 {
		a.logger.Warn("scheduled jobs backpressured", "count", backpressured)
	}
}
