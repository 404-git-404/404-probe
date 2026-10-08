package storage

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"404-probe/internal/auth"
)

var (
	ErrAgentRemovalUnsupported     = errors.New("agent does not support remote removal")
	ErrAgentRemovalPending         = errors.New("agent removal is already pending")
	ErrAgentRemovalUpgradeConflict = errors.New("agent upgrade is active")
	ErrAgentRemovalNotFound        = errors.New("agent removal operation not found")
	ErrAgentRemovalCredential      = errors.New("invalid or expired removal receipt credential")
	ErrAgentRemovalTransition      = errors.New("invalid agent removal state transition")
	ErrAgentRemovalCompleted       = errors.New("agent removal operation is already complete")
)

const AgentRemovalReceiptTTL = 30 * time.Minute

// AgentRemovalReceiptReplayTTL retains only the detached operation ID and
// token hash long enough to make a lost success response safely replayable.
// It is deliberately short and is not a historical audit-retention period.
const AgentRemovalReceiptReplayTTL = 24 * time.Hour

type AgentRemovalStatus string

const (
	AgentRemovalRequested    AgentRemovalStatus = "requested"
	AgentRemovalDelivered    AgentRemovalStatus = "delivered"
	AgentRemovalUninstalling AgentRemovalStatus = "uninstalling"
)

type AgentRemovalOperation struct {
	OperationID   string
	AgentID       string
	Status        AgentRemovalStatus
	CreatedAt     int64
	UpdatedAt     int64
	DeliveryCount int64
}

type AgentRemovalDelivery struct {
	Operation    AgentRemovalOperation
	ReceiptToken string
}

type AgentRemovalReceiptAck struct {
	Completed bool
	Duplicate bool
}

// CreateAgentRemovalOperation is idempotent by operation ID. It pins the
// request to the currently accepted capable session, rejects active upgrades,
// and atomically stops queued work before publishing the pending operation.
func (s *Store) CreateAgentRemovalOperation(ctx context.Context, agentID, operationID string, now time.Time) (AgentRemovalOperation, bool, error) {
	if !validStorageID(agentID, 128) || !validRemovalOperationID(operationID) || now.UnixMilli() <= 0 {
		return AgentRemovalOperation{}, false, errors.New("invalid agent removal request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRemovalOperation{}, false, err
	}
	defer tx.Rollback()

	if existing, exists, err := readAgentRemovalOperationTx(ctx, tx, operationID); err != nil {
		return AgentRemovalOperation{}, false, err
	} else if exists {
		if existing.AgentID != agentID {
			return AgentRemovalOperation{}, false, ErrJobIDConflict
		}
		if err := tx.Commit(); err != nil {
			return AgentRemovalOperation{}, false, err
		}
		return existing, false, nil
	}
	var completed int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_removal_receipts WHERE operation_id=?)`, operationID).Scan(&completed); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if completed != 0 {
		return AgentRemovalOperation{}, false, ErrAgentRemovalCompleted
	}
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return AgentRemovalOperation{}, false, ErrAgentNotFound
	}
	epoch, sessionID, supported, err := currentRemoteRemovalCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if !supported {
		return AgentRemovalOperation{}, false, ErrAgentRemovalUnsupported
	}
	var upgrading int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_upgrade_operations WHERE agent_id=?
		AND status IN ('requested','claimed','downloading','verifying','staging','installing','restarting','health_check'))`, agentID).Scan(&upgrading); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if upgrading != 0 {
		return AgentRemovalOperation{}, false, ErrAgentRemovalUpgradeConflict
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_removal_operations WHERE agent_id=?)`, agentID).Scan(&pending); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if pending != 0 {
		return AgentRemovalOperation{}, false, ErrAgentRemovalPending
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_removal_receipts WHERE replay_expires_at<=?`, now.UnixMilli()); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired',lease_token=NULL,lease_epoch=NULL,
		lease_session_id=NULL,leased_at=NULL,lease_until=NULL WHERE agent_id=? AND status IN ('queued','leased')`, agentID); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_schedules SET enabled=0,updated_at=? WHERE agent_id=? AND enabled=1`, now.UnixMilli(), agentID); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_removal_operations(
		operation_id,agent_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,delivery_count,created_at,updated_at
	) VALUES(?,?,'requested',?,?,?,?,0,?,?)`, operationID, agentID, int64(epoch), sessionID, int64(epoch), sessionID, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return AgentRemovalOperation{}, false, err
	}
	operation := AgentRemovalOperation{OperationID: operationID, AgentID: agentID, Status: AgentRemovalRequested, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	if err := tx.Commit(); err != nil {
		return AgentRemovalOperation{}, false, err
	}
	return operation, true, nil
}

// ClaimAgentRemoval issues a fresh, short-lived credential scoped solely to
// the fixed receipt endpoint. A newly accepted capable session can resume a
// pending operation after restart; rebinding also invalidates the old token.
func (s *Store) ClaimAgentRemoval(ctx context.Context, agentID string, epoch uint64, sessionID string, now time.Time) (*AgentRemovalDelivery, error) {
	if !validStorageID(agentID, 128) || epoch == 0 || epoch > math.MaxInt64 || !validStorageID(sessionID, 128) || now.UnixMilli() <= 0 || now.UnixMilli() > math.MaxInt64-AgentRemovalReceiptTTL.Milliseconds() {
		return nil, ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return nil, err
	}
	currentEpoch, currentSession, supported, err := currentRemoteRemovalCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if !supported || currentEpoch != epoch || currentSession != sessionID {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	operation, exists, err := readAgentRemovalOperationByAgentTx(ctx, tx, agentID)
	if err != nil || !exists {
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if operation.DeliveryCount == math.MaxInt64 {
		return nil, ErrAttemptExhausted
	}
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	stamp := now.UnixMilli()
	expires := stamp + AgentRemovalReceiptTTL.Milliseconds()
	nextStatus := operation.Status
	if nextStatus == AgentRemovalRequested {
		nextStatus = AgentRemovalDelivered
	}
	update, err := tx.ExecContext(ctx, `UPDATE agent_removal_operations SET status=?,active_epoch=?,active_session_id=?,
		receipt_token_hash=?,receipt_expires_at=?,delivery_count=delivery_count+1,updated_at=? WHERE operation_id=?`,
		nextStatus, int64(epoch), sessionID, tokenHash, expires, stamp, operation.OperationID)
	if err != nil {
		return nil, err
	}
	changed, err := update.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return nil, err
		}
		return nil, ErrAgentRemovalNotFound
	}
	operation.Status, operation.UpdatedAt = nextStatus, stamp
	operation.DeliveryCount++
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &AgentRemovalDelivery{Operation: operation, ReceiptToken: token}, nil
}

// MarkAgentRemovalUninstalling advances delivered work idempotently and fences
// the update to the exact session that claimed the operation.
func (s *Store) MarkAgentRemovalUninstalling(ctx context.Context, agentID, operationID string, epoch uint64, sessionID string, now time.Time) (AgentRemovalOperation, error) {
	if !validRemovalOperationID(operationID) || !validStorageID(agentID, 128) || epoch == 0 || epoch > math.MaxInt64 || !validStorageID(sessionID, 128) || now.UnixMilli() <= 0 {
		return AgentRemovalOperation{}, ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRemovalOperation{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return AgentRemovalOperation{}, err
	}
	currentEpoch, currentSession, supported, err := currentRemoteRemovalCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return AgentRemovalOperation{}, err
	}
	if !supported || currentEpoch != epoch || currentSession != sessionID {
		return AgentRemovalOperation{}, ErrUnauthorized
	}
	operation, exists, err := readAgentRemovalOperationTx(ctx, tx, operationID)
	if err != nil {
		return AgentRemovalOperation{}, err
	}
	if !exists || operation.AgentID != agentID {
		return AgentRemovalOperation{}, ErrAgentRemovalNotFound
	}
	var activeEpoch int64
	var activeSession string
	if err := tx.QueryRowContext(ctx, `SELECT active_epoch,active_session_id FROM agent_removal_operations WHERE operation_id=?`, operationID).Scan(&activeEpoch, &activeSession); err != nil {
		return AgentRemovalOperation{}, err
	}
	if activeEpoch != int64(epoch) || activeSession != sessionID {
		return AgentRemovalOperation{}, ErrUnauthorized
	}
	if operation.Status != AgentRemovalDelivered && operation.Status != AgentRemovalUninstalling {
		return AgentRemovalOperation{}, ErrAgentRemovalTransition
	}
	if operation.Status == AgentRemovalDelivered {
		stamp := now.UnixMilli()
		if _, err := tx.ExecContext(ctx, `UPDATE agent_removal_operations SET status='uninstalling',updated_at=? WHERE operation_id=? AND status='delivered'`, stamp, operationID); err != nil {
			return AgentRemovalOperation{}, err
		}
		operation.Status, operation.UpdatedAt = AgentRemovalUninstalling, stamp
	}
	if err := tx.Commit(); err != nil {
		return AgentRemovalOperation{}, err
	}
	return operation, nil
}

// CompleteAgentRemoval accepts only the currently issued receipt credential
// and fixed receipt kind. In one transaction it writes a minimal detached
// receipt and deletes the Agent, cascading all business data and the operation.
func (s *Store) CompleteAgentRemoval(ctx context.Context, operationID, credential, receiptKind string, now time.Time) (AgentRemovalReceiptAck, error) {
	if !validRemovalOperationID(operationID) || len(credential) < 32 || len(credential) > 128 || receiptKind != "agent_uninstalled" || now.UnixMilli() <= 0 {
		return AgentRemovalReceiptAck{}, ErrAgentRemovalCredential
	}
	wantHash := auth.Hash(credential)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRemovalReceiptAck{}, err
	}
	defer tx.Rollback()
	var agentID string
	var status AgentRemovalStatus
	var storedHash []byte
	var expires sql.NullInt64
	var activeEpoch, currentEpoch int64
	var activeSession, currentSession string
	err = tx.QueryRowContext(ctx, `SELECT o.agent_id,o.status,o.receipt_token_hash,o.receipt_expires_at,
		o.active_epoch,o.active_session_id,s.epoch,s.session_id
		FROM agent_removal_operations o JOIN agent_state s ON s.agent_id=o.agent_id
		WHERE o.operation_id=?`, operationID).Scan(&agentID, &status, &storedHash, &expires, &activeEpoch, &activeSession, &currentEpoch, &currentSession)
	if errors.Is(err, sql.ErrNoRows) {
		var auditHash []byte
		var replayExpires int64
		auditErr := tx.QueryRowContext(ctx, `SELECT receipt_token_hash,replay_expires_at FROM agent_removal_receipts WHERE operation_id=?`, operationID).Scan(&auditHash, &replayExpires)
		if errors.Is(auditErr, sql.ErrNoRows) || (auditErr == nil && (replayExpires <= now.UnixMilli() || subtle.ConstantTimeCompare(auditHash, wantHash) != 1)) {
			return AgentRemovalReceiptAck{}, ErrAgentRemovalCredential
		}
		if auditErr != nil {
			return AgentRemovalReceiptAck{}, auditErr
		}
		if err := tx.Commit(); err != nil {
			return AgentRemovalReceiptAck{}, err
		}
		return AgentRemovalReceiptAck{Completed: true, Duplicate: true}, nil
	}
	if err != nil {
		return AgentRemovalReceiptAck{}, err
	}
	if !expires.Valid || expires.Int64 <= now.UnixMilli() || len(storedHash) != len(wantHash) || subtle.ConstantTimeCompare(storedHash, wantHash) != 1 {
		return AgentRemovalReceiptAck{}, ErrAgentRemovalCredential
	}
	if status != AgentRemovalUninstalling {
		return AgentRemovalReceiptAck{}, ErrAgentRemovalTransition
	}
	// A new accepted Agent process fences out an old updater receipt even if
	// the new process has not claimed the pending operation and rotated its
	// bearer credential yet. The transaction ordering makes the decision
	// atomic with report acceptance and Agent deletion.
	if activeEpoch <= 0 || currentEpoch != activeEpoch || activeSession != currentSession {
		return AgentRemovalReceiptAck{}, ErrAgentRemovalCredential
	}
	replayExpires := now.Add(AgentRemovalReceiptReplayTTL).UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_removal_receipts(operation_id,receipt_token_hash,receipt_expires_at,receipt_kind,completed_at,replay_expires_at)
		VALUES(?,?,?,'agent_uninstalled',?,?)`, operationID, storedHash, expires.Int64, now.UnixMilli(), replayExpires); err != nil {
		return AgentRemovalReceiptAck{}, err
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, agentID)
	if err != nil {
		return AgentRemovalReceiptAck{}, err
	}
	changed, err := deleted.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return AgentRemovalReceiptAck{}, err
		}
		return AgentRemovalReceiptAck{}, ErrAgentRemovalNotFound
	}
	if err := tx.Commit(); err != nil {
		return AgentRemovalReceiptAck{}, err
	}
	return AgentRemovalReceiptAck{Completed: true}, nil
}

// PruneExpiredAgentRemovalReceipts removes the detached idempotency receipts
// as soon as their short protocol window expires. These records are not a
// historical audit log; callers run this on startup and periodically.
func (s *Store) PruneExpiredAgentRemovalReceipts(ctx context.Context, now time.Time) (int64, error) {
	if now.UnixMilli() <= 0 {
		return 0, errors.New("invalid Agent removal receipt cleanup time")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_removal_receipts WHERE replay_expires_at<=?`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) GetAgentRemovalOperation(ctx context.Context, agentID string) (AgentRemovalOperation, bool, error) {
	if !validStorageID(agentID, 128) {
		return AgentRemovalOperation{}, false, ErrAgentRemovalNotFound
	}
	operation, exists, err := readAgentRemovalOperationByAgentDB(ctx, s.db, agentID)
	return operation, exists, err
}

func currentRemoteRemovalCapabilityTx(ctx context.Context, tx *sql.Tx, agentID string) (uint64, string, bool, error) {
	var epoch int64
	var session string
	var supported int
	err := tx.QueryRowContext(ctx, `SELECT s.epoch,s.session_id,c.remote_removal
		FROM agent_state s JOIN agent_management_capabilities c
		ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id
		WHERE s.agent_id=?`, agentID).Scan(&epoch, &session, &supported)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if epoch <= 0 || !validStorageID(session, 128) || (supported != 0 && supported != 1) {
		return 0, "", false, ErrCorruptProbeData
	}
	return uint64(epoch), session, supported == 1, nil
}

func requireNoAgentRemovalTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_removal_operations WHERE agent_id=?)`, agentID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return ErrAgentRemovalPending
	}
	return nil
}

func readAgentRemovalOperationTx(ctx context.Context, tx *sql.Tx, operationID string) (AgentRemovalOperation, bool, error) {
	return scanAgentRemovalOperation(tx.QueryRowContext(ctx, `SELECT operation_id,agent_id,status,created_at,updated_at,delivery_count
		FROM agent_removal_operations WHERE operation_id=?`, operationID))
}

func readAgentRemovalOperationByAgentTx(ctx context.Context, tx *sql.Tx, agentID string) (AgentRemovalOperation, bool, error) {
	return scanAgentRemovalOperation(tx.QueryRowContext(ctx, `SELECT operation_id,agent_id,status,created_at,updated_at,delivery_count
		FROM agent_removal_operations WHERE agent_id=?`, agentID))
}

func readAgentRemovalOperationByAgentDB(ctx context.Context, db *sql.DB, agentID string) (AgentRemovalOperation, bool, error) {
	return scanAgentRemovalOperation(db.QueryRowContext(ctx, `SELECT operation_id,agent_id,status,created_at,updated_at,delivery_count
		FROM agent_removal_operations WHERE agent_id=?`, agentID))
}

func scanAgentRemovalOperation(row *sql.Row) (AgentRemovalOperation, bool, error) {
	var operation AgentRemovalOperation
	var status string
	err := row.Scan(&operation.OperationID, &operation.AgentID, &status, &operation.CreatedAt, &operation.UpdatedAt, &operation.DeliveryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRemovalOperation{}, false, nil
	}
	if err != nil {
		return AgentRemovalOperation{}, false, err
	}
	operation.Status = AgentRemovalStatus(status)
	if !validRemovalOperationID(operation.OperationID) || !validStorageID(operation.AgentID, 128) || !validAgentRemovalStatus(operation.Status) || operation.CreatedAt <= 0 || operation.UpdatedAt < operation.CreatedAt || operation.DeliveryCount < 0 {
		return AgentRemovalOperation{}, false, ErrCorruptProbeData
	}
	return operation, true, nil
}

func validAgentRemovalStatus(status AgentRemovalStatus) bool {
	return status == AgentRemovalRequested || status == AgentRemovalDelivered || status == AgentRemovalUninstalling
}

func validRemovalOperationID(value string) bool {
	if len(value) != 32 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
