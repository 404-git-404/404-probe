package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type UpgradeStatus string

const (
	UpgradeRequested   UpgradeStatus = "requested"
	UpgradeClaimed     UpgradeStatus = "claimed"
	UpgradeDownloading UpgradeStatus = "downloading"
	UpgradeVerifying   UpgradeStatus = "verifying"
	UpgradeStaging     UpgradeStatus = "staging"
	UpgradeInstalling  UpgradeStatus = "installing"
	UpgradeRestarting  UpgradeStatus = "restarting"
	UpgradeHealthCheck UpgradeStatus = "health_check"
	UpgradeSucceeded   UpgradeStatus = "succeeded"
	UpgradeFailed      UpgradeStatus = "failed"
	UpgradeRolledBack  UpgradeStatus = "rolled_back"
)

type UpgradeOperation struct {
	OperationID    string
	AgentID        string
	FromVersion    string
	TargetVersion  string
	Status         UpgradeStatus
	FailureCode    string
	FailureMessage string
	CreatedAt      int64
	StartedAt      *int64
	FinishedAt     *int64
	UpdatedAt      int64
}

func (s *Store) CreateUpgrade(ctx context.Context, operation UpgradeOperation) (UpgradeOperation, error) {
	if !validStorageID(operation.OperationID, 64) || !validStorageID(operation.AgentID, 128) || operation.FromVersion == "" || operation.TargetVersion == "" {
		return UpgradeOperation{}, errors.New("invalid upgrade operation")
	}
	now := operation.CreatedAt
	if now <= 0 {
		return UpgradeOperation{}, errors.New("invalid upgrade creation time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UpgradeOperation{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, operation.AgentID); err != nil {
		return UpgradeOperation{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, operation.AgentID); err != nil {
		return UpgradeOperation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_upgrade_operations(operation_id,agent_id,from_version,target_version,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		operation.OperationID, operation.AgentID, operation.FromVersion, operation.TargetVersion, UpgradeRequested, now, now)
	if err != nil {
		_ = tx.Rollback()
		if existing, readErr := s.GetActiveUpgrade(ctx, operation.AgentID); readErr == nil && existing != nil {
			return *existing, ErrUpgradeConflict
		}
		return UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return UpgradeOperation{}, err
	}
	operation.Status = UpgradeRequested
	operation.UpdatedAt = now
	return operation, nil
}

func (s *Store) GetActiveUpgrade(ctx context.Context, agentID string) (*UpgradeOperation, error) {
	return readUpgrade(s.db.QueryRowContext(ctx, `SELECT operation_id,agent_id,from_version,target_version,status,failure_code,failure_message,created_at,started_at,finished_at,updated_at FROM agent_upgrade_operations WHERE agent_id=? AND status IN ('requested','claimed','downloading','verifying','staging','installing','restarting','health_check') ORDER BY created_at DESC LIMIT 1`, agentID))
}

func (s *Store) GetLatestUpgrade(ctx context.Context, agentID string) (*UpgradeOperation, error) {
	return readUpgrade(s.db.QueryRowContext(ctx, `SELECT operation_id,agent_id,from_version,target_version,status,failure_code,failure_message,created_at,started_at,finished_at,updated_at FROM agent_upgrade_operations WHERE agent_id=? ORDER BY created_at DESC LIMIT 1`, agentID))
}

func (s *Store) GetUpgrade(ctx context.Context, operationID string) (UpgradeOperation, error) {
	operation, err := readUpgrade(s.db.QueryRowContext(ctx, `SELECT operation_id,agent_id,from_version,target_version,status,failure_code,failure_message,created_at,started_at,finished_at,updated_at FROM agent_upgrade_operations WHERE operation_id=?`, operationID))
	if err != nil {
		return UpgradeOperation{}, err
	}
	if operation == nil {
		return UpgradeOperation{}, ErrUpgradeNotFound
	}
	return *operation, nil
}

func (s *Store) ClaimUpgrade(ctx context.Context, agentID string, now time.Time) (*UpgradeOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireNoAgentRemovalTx(ctx, tx, agentID); err != nil {
		if errors.Is(err, ErrAgentRemovalPending) {
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, commitErr
			}
			return nil, nil
		}
		return nil, err
	}
	operation, err := readUpgrade(tx.QueryRowContext(ctx, `SELECT operation_id,agent_id,from_version,target_version,status,failure_code,failure_message,created_at,started_at,finished_at,updated_at FROM agent_upgrade_operations WHERE agent_id=? AND status IN ('requested','claimed','downloading','verifying','staging','installing','restarting','health_check') ORDER BY created_at LIMIT 1`, agentID))
	if err != nil || operation == nil {
		return operation, err
	}
	if operation.Status == UpgradeRequested {
		stamp := now.UnixMilli()
		result, err := tx.ExecContext(ctx, `UPDATE agent_upgrade_operations SET status='claimed',started_at=?,updated_at=? WHERE operation_id=? AND status='requested'`, stamp, stamp, operation.OperationID)
		if err != nil {
			return nil, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return nil, ErrUpgradeConflict
		}
		operation.Status = UpgradeClaimed
		operation.StartedAt = &stamp
		operation.UpdatedAt = stamp
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return operation, nil
}

func (s *Store) UpdateUpgradeStatus(ctx context.Context, agentID, operationID string, status UpgradeStatus, failureCode, failureMessage string, now time.Time) (UpgradeOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UpgradeOperation{}, err
	}
	defer tx.Rollback()
	operation, err := readUpgrade(tx.QueryRowContext(ctx, `SELECT operation_id,agent_id,from_version,target_version,status,failure_code,failure_message,created_at,started_at,finished_at,updated_at FROM agent_upgrade_operations WHERE operation_id=?`, operationID))
	if err != nil {
		return UpgradeOperation{}, err
	}
	if operation == nil || operation.AgentID != agentID {
		return UpgradeOperation{}, ErrUpgradeNotFound
	}
	if !validUpgradeTransition(operation.Status, status) {
		return UpgradeOperation{}, ErrUpgradeTransition
	}
	stamp := now.UnixMilli()
	var finished any
	if status == UpgradeSucceeded || status == UpgradeFailed || status == UpgradeRolledBack {
		finished = stamp
	}
	if status != UpgradeFailed && status != UpgradeRolledBack {
		failureCode, failureMessage = "", ""
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_upgrade_operations SET status=?,failure_code=?,failure_message=?,finished_at=COALESCE(?,finished_at),updated_at=? WHERE operation_id=?`,
		status, nullableText(failureCode), nullableText(failureMessage), finished, stamp, operationID)
	if err != nil {
		return UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return UpgradeOperation{}, err
	}
	operation.Status, operation.FailureCode, operation.FailureMessage, operation.UpdatedAt = status, failureCode, failureMessage, stamp
	if finished != nil {
		value := stamp
		operation.FinishedAt = &value
	}
	return *operation, nil
}

func validUpgradeTransition(from, to UpgradeStatus) bool {
	allowed := map[UpgradeStatus][]UpgradeStatus{
		UpgradeClaimed:     {UpgradeDownloading, UpgradeFailed},
		UpgradeDownloading: {UpgradeVerifying, UpgradeFailed},
		UpgradeVerifying:   {UpgradeStaging, UpgradeFailed},
		UpgradeStaging:     {UpgradeInstalling, UpgradeFailed},
		UpgradeInstalling:  {UpgradeRestarting, UpgradeFailed, UpgradeRolledBack},
		UpgradeRestarting:  {UpgradeHealthCheck, UpgradeFailed, UpgradeRolledBack},
		UpgradeHealthCheck: {UpgradeSucceeded, UpgradeFailed, UpgradeRolledBack},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

type rowScanner interface{ Scan(...any) error }

func readUpgrade(row rowScanner) (*UpgradeOperation, error) {
	var operation UpgradeOperation
	var failureCode, failureMessage sql.NullString
	var startedAt, finishedAt sql.NullInt64
	if err := row.Scan(&operation.OperationID, &operation.AgentID, &operation.FromVersion, &operation.TargetVersion, &operation.Status,
		&failureCode, &failureMessage, &operation.CreatedAt, &startedAt, &finishedAt, &operation.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read upgrade operation: %w", err)
	}
	operation.FailureCode, operation.FailureMessage = failureCode.String, failureMessage.String
	if startedAt.Valid {
		value := startedAt.Int64
		operation.StartedAt = &value
	}
	if finishedAt.Valid {
		value := finishedAt.Int64
		operation.FinishedAt = &value
	}
	return &operation, nil
}
