package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"404-probe/internal/protocol"
)

const MaxScheduleCandidatesPerTick = 64

var ErrScheduleTimeRange = errors.New("schedule time is outside the supported range")

type MaterializeResult string

const (
	MaterializeNoop                MaterializeResult = "noop"
	MaterializeCreated             MaterializeResult = "materialized"
	MaterializeAlreadyMaterialized MaterializeResult = "already_materialized"
	MaterializeBackpressured       MaterializeResult = "backpressured"
	MaterializeAgentRevoked        MaterializeResult = "agent_revoked"
	MaterializeCorruptDisabled     MaterializeResult = "corrupt_disabled"
)

type MaterializeOutcome struct {
	Result MaterializeResult
	JobID  string
	Slot   int64
}

func (s *Store) DueScheduleIDs(ctx context.Context, now time.Time, limit int) ([]string, error) {
	nowMillis := now.UnixMilli()
	if nowMillis <= 0 {
		return nil, ErrScheduleTimeRange
	}
	if limit <= 0 || limit > MaxScheduleCandidatesPerTick {
		return nil, errors.New("invalid schedule candidate limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM probe_schedules
		WHERE enabled=1 AND next_run_at<=? ORDER BY next_run_at,id LIMIT ?`, nowMillis, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MaterializeSchedule atomically handles one due schedule candidate. The
// candidate is re-read and fenced inside the transaction before any job or
// schedule state is changed.
func (s *Store) MaterializeSchedule(ctx context.Context, scheduleID string, now time.Time, minimumLifetime time.Duration) (MaterializeOutcome, error) {
	nowMillis := now.UnixMilli()
	minimumLifetimeMillis := minimumLifetime.Milliseconds()
	if nowMillis <= 0 || minimumLifetimeMillis <= 0 {
		return MaterializeOutcome{}, ErrScheduleTimeRange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterializeOutcome{}, err
	}
	defer tx.Rollback()
	schedule, exists, err := readScheduleTx(ctx, tx, scheduleID)
	if err != nil {
		if !errors.Is(err, ErrCorruptProbeData) {
			return MaterializeOutcome{}, err
		}
		result, disableErr := tx.ExecContext(ctx, `UPDATE probe_schedules SET enabled=0,updated_at=? WHERE id=? AND enabled=1`, nowMillis, scheduleID)
		if disableErr != nil {
			return MaterializeOutcome{}, disableErr
		}
		changed, disableErr := result.RowsAffected()
		if disableErr != nil {
			return MaterializeOutcome{}, disableErr
		}
		if changed == 0 {
			return MaterializeOutcome{Result: MaterializeNoop}, nil
		}
		if err := tx.Commit(); err != nil {
			return MaterializeOutcome{}, err
		}
		return MaterializeOutcome{Result: MaterializeCorruptDisabled}, fmt.Errorf("%w: %v", ErrCorruptProbeData, err)
	}
	if !exists || !schedule.Enabled || schedule.NextRunAt > nowMillis {
		return MaterializeOutcome{Result: MaterializeNoop}, nil
	}
	intervalMillis := int64(schedule.IntervalSeconds) * int64(time.Second/time.Millisecond)
	slot, nextRunAt, err := scheduledSlot(schedule.NextRunAt, nowMillis, intervalMillis)
	if err != nil {
		return MaterializeOutcome{}, err
	}
	lifetimeMillis := intervalMillis
	if minimumLifetimeMillis > lifetimeMillis {
		lifetimeMillis = minimumLifetimeMillis
	}
	if nowMillis > math.MaxInt64-lifetimeMillis {
		return MaterializeOutcome{}, ErrScheduleTimeRange
	}
	expiresAt := nowMillis + lifetimeMillis
	jobID := ScheduledJobID(schedule.ID, slot)

	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, schedule.AgentID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return MaterializeOutcome{Result: MaterializeNoop}, nil
	}
	if err != nil {
		return MaterializeOutcome{}, err
	}
	if revoked != 0 {
		changed, err := advanceOrDisableScheduleTx(ctx, tx, schedule, 0, nowMillis, true)
		if err != nil {
			return MaterializeOutcome{}, err
		}
		if !changed {
			return MaterializeOutcome{Result: MaterializeNoop}, nil
		}
		if err := tx.Commit(); err != nil {
			return MaterializeOutcome{}, err
		}
		return MaterializeOutcome{Result: MaterializeAgentRevoked, Slot: slot}, nil
	}

	var existingJobID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM probe_jobs WHERE schedule_id=? AND scheduled_for=?`, schedule.ID, slot).Scan(&existingJobID)
	if err == nil {
		changed, err := advanceOrDisableScheduleTx(ctx, tx, schedule, nextRunAt, 0, false)
		if err != nil {
			return MaterializeOutcome{}, err
		}
		if !changed {
			return MaterializeOutcome{Result: MaterializeNoop}, nil
		}
		if err := tx.Commit(); err != nil {
			return MaterializeOutcome{}, err
		}
		return MaterializeOutcome{Result: MaterializeAlreadyMaterialized, JobID: existingJobID, Slot: slot}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MaterializeOutcome{}, err
	}
	var conflictingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM probe_jobs WHERE id=?`, jobID).Scan(&conflictingID)
	if err == nil {
		return MaterializeOutcome{}, fmt.Errorf("%w: deterministic scheduled job ID is already in use", ErrCorruptProbeData)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MaterializeOutcome{}, err
	}

	if err := cleanupExpiredJobsTx(ctx, tx, schedule.AgentID, nowMillis); err != nil {
		return MaterializeOutcome{}, err
	}
	var outstanding int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE agent_id=? AND status IN ('queued','leased')`, schedule.AgentID).Scan(&outstanding); err != nil {
		return MaterializeOutcome{}, err
	}
	if outstanding >= MaxOutstandingJobsPerAgent {
		changed, err := advanceOrDisableScheduleTx(ctx, tx, schedule, nextRunAt, 0, false)
		if err != nil {
			return MaterializeOutcome{}, err
		}
		if !changed {
			return MaterializeOutcome{Result: MaterializeNoop}, nil
		}
		if err := tx.Commit(); err != nil {
			return MaterializeOutcome{}, err
		}
		return MaterializeOutcome{Result: MaterializeBackpressured, Slot: slot}, nil
	}

	configJSON, err := protocol.MarshalProbeConfig(schedule.ProbeType, schedule.Config)
	if err != nil {
		return MaterializeOutcome{}, fmt.Errorf("%w: stored schedule config is invalid", ErrCorruptProbeData)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_jobs(
		id,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt
	) VALUES(?,?,?,?,?,?,?,?,?,?,'queued',0)`, jobID, schedule.ID, schedule.AgentID, string(schedule.ProbeType), string(configJSON),
		schedule.TimeoutMS, nowMillis, slot, nowMillis, expiresAt); err != nil {
		return MaterializeOutcome{}, err
	}
	changed, err := advanceOrDisableScheduleTx(ctx, tx, schedule, nextRunAt, 0, false)
	if err != nil {
		return MaterializeOutcome{}, err
	}
	if !changed {
		return MaterializeOutcome{Result: MaterializeNoop}, nil
	}
	if err := tx.Commit(); err != nil {
		return MaterializeOutcome{}, err
	}
	return MaterializeOutcome{Result: MaterializeCreated, JobID: jobID, Slot: slot}, nil
}

func scheduledSlot(oldNextRunAt, nowMillis, intervalMillis int64) (int64, int64, error) {
	if oldNextRunAt <= 0 || nowMillis < oldNextRunAt || intervalMillis <= 0 {
		return 0, 0, ErrScheduleTimeRange
	}
	delta := nowMillis - oldNextRunAt
	slot := oldNextRunAt + (delta/intervalMillis)*intervalMillis
	if slot > math.MaxInt64-intervalMillis {
		return 0, 0, ErrScheduleTimeRange
	}
	return slot, slot + intervalMillis, nil
}

func advanceOrDisableScheduleTx(ctx context.Context, tx *sql.Tx, schedule ProbeScheduleRecord, nextRunAt, updatedAt int64, disable bool) (bool, error) {
	var result sql.Result
	var err error
	if disable {
		result, err = tx.ExecContext(ctx, `UPDATE probe_schedules SET enabled=0,updated_at=?
			WHERE id=? AND enabled=1 AND next_run_at=? AND updated_at=?`, updatedAt, schedule.ID, schedule.NextRunAt, schedule.UpdatedAt)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE probe_schedules SET next_run_at=?
			WHERE id=? AND enabled=1 AND next_run_at=? AND updated_at=?`, nextRunAt, schedule.ID, schedule.NextRunAt, schedule.UpdatedAt)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func ScheduledJobID(scheduleID string, slot int64) string {
	digest := sha256.Sum256([]byte("404-probe:schedule-job:v1\x00" + scheduleID + "\x00" + strconv.FormatInt(slot, 10)))
	return hex.EncodeToString(digest[:])
}
