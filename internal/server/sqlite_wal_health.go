package server

import (
	"context"
	"time"

	"404-probe/internal/storage"
)

const (
	sqliteWALHealthInterval        = 5 * time.Minute
	sqliteWALHealthTimeout         = 10 * time.Second
	sqliteWALWarnBytes       int64 = 128 << 20
	sqliteWALWarnFrames            = 4096
	sqliteWALWarningInterval       = time.Hour
)

type sqliteWALAlertEvent uint8

const (
	sqliteWALAlertNone sqliteWALAlertEvent = iota
	sqliteWALAlertWarn
	sqliteWALAlertRecovered
)

// sqliteWALAlertState keeps repeated warnings bounded and emits one recovery
// event when a previously unhealthy checkpoint sample returns to normal.
type sqliteWALAlertState struct {
	active        bool
	lastWarningAt time.Time
}

func (s *sqliteWALAlertState) observe(now time.Time, warning bool) sqliteWALAlertEvent {
	if warning {
		if !s.active || now.Before(s.lastWarningAt) || now.Sub(s.lastWarningAt) >= sqliteWALWarningInterval {
			s.active = true
			s.lastWarningAt = now
			return sqliteWALAlertWarn
		}
		return sqliteWALAlertNone
	}
	if s.active {
		s.active = false
		s.lastWarningAt = time.Time{}
		return sqliteWALAlertRecovered
	}
	return sqliteWALAlertNone
}

func sqliteWALHealthNeedsWarning(health storage.WALHealth, err error) bool {
	return err != nil || health.CheckpointBusy != 0 || health.RemainingFrames >= sqliteWALWarnFrames || health.WALBytes >= sqliteWALWarnBytes
}

func (a *App) checkWALHealth() {
	ctx, cancel := context.WithTimeout(a.shutdown, sqliteWALHealthTimeout)
	started := time.Now()
	health, err := a.store.CheckpointWAL(ctx)
	elapsed := time.Since(started)
	cancel()
	if a.shutdown.Err() != nil {
		return
	}
	warning := sqliteWALHealthNeedsWarning(health, err)
	a.walHealthAlertMu.Lock()
	event := a.walHealthAlert.observe(a.now(), warning)
	a.walHealthAlertMu.Unlock()
	if event == sqliteWALAlertNone {
		return
	}
	fields := []any{
		"db_bytes", health.DatabaseBytes,
		"wal_bytes", health.WALBytes,
		"shm_bytes", health.SHMBytes,
		"busy", health.CheckpointBusy,
		"log_frames", health.LogFrames,
		"checkpointed_frames", health.CheckpointedFrames,
		"remaining_frames", health.RemainingFrames,
		"elapsed_ms", elapsed.Milliseconds(),
	}
	switch event {
	case sqliteWALAlertWarn:
		if err != nil {
			fields = append(fields, "error", err)
		}
		a.logger.Warn("SQLite WAL checkpoint health warning", fields...)
	case sqliteWALAlertRecovered:
		a.logger.Info("SQLite WAL checkpoint health recovered", fields...)
	}
}
