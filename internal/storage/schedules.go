package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"404-probe/internal/protocol"
)

var (
	ErrScheduleNotFound = errors.New("probe schedule not found")
	ErrScheduleConflict = errors.New("probe schedule conflicts with existing schedule")
	ErrScheduleLimit    = errors.New("probe schedule limit reached")
)

const MaxSchedulesPerAgent = 64

type PutScheduleParams struct {
	ID              string
	AgentID         string
	Name            string
	ProbeType       protocol.ProbeType
	Config          protocol.ProbeConfig
	TimeoutMS       int
	IntervalSeconds int
	Enabled         bool
	Now             int64
}

type ProbeScheduleRecord struct {
	ID              string
	AgentID         string
	Name            string
	ProbeType       protocol.ProbeType
	Config          protocol.ProbeConfig
	TimeoutMS       int
	IntervalSeconds int
	Enabled         bool
	NextRunAt       int64
	CreatedAt       int64
	UpdatedAt       int64
}

// PutProbeSchedule creates or fully replaces a schedule. Reading an existing
// ID, comparing a replay, checking capacity, and writing are one transaction.
func (s *Store) PutProbeSchedule(ctx context.Context, params PutScheduleParams) (ProbeScheduleRecord, bool, error) {
	configJSON, err := validatePutScheduleParams(params)
	if err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	defer tx.Rollback()

	existing, exists, err := readScheduleTx(ctx, tx, params.ID)
	if err != nil {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: %v", ErrCorruptProbeData, err)
	}
	if exists {
		if existing.AgentID != params.AgentID {
			return ProbeScheduleRecord{}, false, ErrScheduleConflict
		}
		identical, err := sameScheduleRequest(existing, params, configJSON)
		if err != nil {
			return ProbeScheduleRecord{}, false, err
		}
		if identical {
			if err := tx.Commit(); err != nil {
				return ProbeScheduleRecord{}, false, err
			}
			return existing, false, nil
		}
		if params.Now < existing.CreatedAt {
			return ProbeScheduleRecord{}, false, errors.New("schedule update time precedes creation")
		}
		if params.Enabled {
			if err := scheduleActiveAgentTx(ctx, tx, params.AgentID); err != nil {
				return ProbeScheduleRecord{}, false, err
			}
		}
		nextRunAt := existing.NextRunAt
		operationalChange := existing.ProbeType != params.ProbeType ||
			existing.TimeoutMS != params.TimeoutMS || existing.IntervalSeconds != params.IntervalSeconds
		storedConfig, err := protocol.MarshalProbeConfig(existing.ProbeType, existing.Config)
		if err != nil {
			return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule config is invalid", ErrCorruptProbeData)
		}
		operationalChange = operationalChange || !bytes.Equal(storedConfig, configJSON)
		if params.Enabled && (!existing.Enabled || operationalChange) {
			nextRunAt = params.Now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE probe_schedules SET
			name=?,probe_type=?,config_json=?,timeout_ms=?,interval_seconds=?,enabled=?,next_run_at=?,updated_at=?
			WHERE id=?`, params.Name, string(params.ProbeType), string(configJSON), params.TimeoutMS,
			params.IntervalSeconds, boolInt(params.Enabled), nextRunAt, params.Now, params.ID); err != nil {
			return ProbeScheduleRecord{}, false, err
		}
		record, ok, err := readScheduleTx(ctx, tx, params.ID)
		if err != nil {
			return ProbeScheduleRecord{}, false, err
		}
		if !ok {
			return ProbeScheduleRecord{}, false, ErrScheduleNotFound
		}
		if err := tx.Commit(); err != nil {
			return ProbeScheduleRecord{}, false, err
		}
		return record, false, nil
	}

	if err := scheduleActiveAgentTx(ctx, tx, params.AgentID); err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_schedules WHERE agent_id=?`, params.AgentID).Scan(&count); err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	if count >= MaxSchedulesPerAgent {
		return ProbeScheduleRecord{}, false, ErrScheduleLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_schedules(
		id,agent_id,name,probe_type,config_json,timeout_ms,interval_seconds,enabled,next_run_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, params.ID, params.AgentID, params.Name, string(params.ProbeType), string(configJSON),
		params.TimeoutMS, params.IntervalSeconds, boolInt(params.Enabled), params.Now, params.Now, params.Now); err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	record, ok, err := readScheduleTx(ctx, tx, params.ID)
	if err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	if !ok {
		return ProbeScheduleRecord{}, false, ErrScheduleNotFound
	}
	if err := tx.Commit(); err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) GetProbeSchedule(ctx context.Context, scheduleID string) (ProbeScheduleRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProbeScheduleRecord{}, err
	}
	defer tx.Rollback()
	record, exists, err := readScheduleTx(ctx, tx, scheduleID)
	if err != nil {
		return ProbeScheduleRecord{}, fmt.Errorf("%w: %v", ErrCorruptProbeData, err)
	}
	if !exists {
		return ProbeScheduleRecord{}, ErrScheduleNotFound
	}
	if err := tx.Commit(); err != nil {
		return ProbeScheduleRecord{}, err
	}
	return record, nil
}

// ListProbeSchedules returns one Agent's schedules in stable name/ID order.
// The transaction ensures every returned record is read from one database
// snapshot, and stored corruption is never silently skipped.
func (s *Store) ListProbeSchedules(ctx context.Context, agentID string) ([]ProbeScheduleRecord, error) {
	if !validStorageID(agentID, 128) {
		return nil, errors.New("agent ID is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM probe_schedules WHERE agent_id=? ORDER BY name,id`, agentID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records := make([]ProbeScheduleRecord, 0, len(ids))
	for _, id := range ids {
		record, exists, err := readScheduleTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrScheduleNotFound
		}
		records = append(records, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) DeleteProbeSchedule(ctx context.Context, scheduleID string) error {
	if !validStorageID(scheduleID, 128) {
		return ErrScheduleNotFound
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM probe_schedules WHERE id=?`, scheduleID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

func validatePutScheduleParams(params PutScheduleParams) ([]byte, error) {
	if !validStorageID(params.ID, 128) || !validStorageID(params.AgentID, 128) {
		return nil, errors.New("schedule ID and agent ID are invalid")
	}
	if !validScheduleName(params.Name) {
		return nil, errors.New("schedule name is invalid")
	}
	if err := params.ProbeType.Validate(); err != nil {
		return nil, err
	}
	configJSON, err := protocol.MarshalProbeConfig(params.ProbeType, params.Config)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if params.TimeoutMS < protocol.MinProbeTimeoutMS || params.TimeoutMS > protocol.MaxProbeTimeoutMS {
		return nil, errors.New("schedule timeout is invalid")
	}
	if params.IntervalSeconds < 30 || params.IntervalSeconds > 604800 {
		return nil, errors.New("schedule interval is invalid")
	}
	if params.Now <= 0 {
		return nil, errors.New("schedule time is invalid")
	}
	return configJSON, nil
}

func validScheduleName(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func sameScheduleRequest(existing ProbeScheduleRecord, params PutScheduleParams, configJSON []byte) (bool, error) {
	storedConfig, err := protocol.MarshalProbeConfig(existing.ProbeType, existing.Config)
	if err != nil {
		return false, fmt.Errorf("%w: stored schedule config is invalid", ErrCorruptProbeData)
	}
	return existing.AgentID == params.AgentID && existing.Name == params.Name && existing.ProbeType == params.ProbeType &&
		bytes.Equal(storedConfig, configJSON) && existing.TimeoutMS == params.TimeoutMS &&
		existing.IntervalSeconds == params.IntervalSeconds && existing.Enabled == params.Enabled, nil
}

func scheduleActiveAgentTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return ErrAgentNotFound
		}
		return err
	}
	return nil
}

func readScheduleTx(ctx context.Context, tx *sql.Tx, scheduleID string) (ProbeScheduleRecord, bool, error) {
	var record ProbeScheduleRecord
	var probeType, configJSON string
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT id,agent_id,name,probe_type,config_json,timeout_ms,
		interval_seconds,enabled,next_run_at,created_at,updated_at FROM probe_schedules WHERE id=?`, scheduleID).Scan(
		&record.ID, &record.AgentID, &record.Name, &probeType, &configJSON, &record.TimeoutMS,
		&record.IntervalSeconds, &enabled, &record.NextRunAt, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeScheduleRecord{}, false, nil
	}
	if err != nil {
		return ProbeScheduleRecord{}, false, err
	}
	if !validStorageID(record.ID, 128) || !validStorageID(record.AgentID, 128) || !validScheduleName(record.Name) {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule identity is invalid", ErrCorruptProbeData)
	}
	record.ProbeType = protocol.ProbeType(probeType)
	if err := record.ProbeType.Validate(); err != nil {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule probe type is invalid", ErrCorruptProbeData)
	}
	config, err := protocol.DecodeProbeConfig(record.ProbeType, []byte(configJSON))
	if err != nil {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: decode stored schedule config: %v", ErrCorruptProbeData, err)
	}
	record.Config = config
	if record.TimeoutMS < protocol.MinProbeTimeoutMS || record.TimeoutMS > protocol.MaxProbeTimeoutMS ||
		record.IntervalSeconds < 30 || record.IntervalSeconds > 604800 {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule bounds are invalid", ErrCorruptProbeData)
	}
	if enabled != 0 && enabled != 1 {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule enabled value is invalid", ErrCorruptProbeData)
	}
	record.Enabled = enabled == 1
	if record.CreatedAt <= 0 || record.UpdatedAt < record.CreatedAt || record.NextRunAt < record.CreatedAt {
		return ProbeScheduleRecord{}, false, fmt.Errorf("%w: stored schedule timestamps are invalid", ErrCorruptProbeData)
	}
	return record, true, nil
}
