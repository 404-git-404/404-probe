package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

var (
	ErrAgentCountryLookupUnsupported = errors.New("Agent does not support country-code lookup")
	ErrAgentCountryLookupPending     = errors.New("Agent country-code lookup is already pending")
	ErrAgentCountryLookupNotFound    = errors.New("Agent country-code lookup operation not found")
	ErrAgentCountryLookupTransition  = errors.New("invalid Agent country-code lookup transition")
)

const AgentCountryCodeLookupPendingTTL = 10 * time.Minute

type AgentCountryCodeLookupStatus string

const (
	AgentCountryCodeLookupRequested AgentCountryCodeLookupStatus = "requested"
	AgentCountryCodeLookupDelivered AgentCountryCodeLookupStatus = "delivered"
	AgentCountryCodeLookupSucceeded AgentCountryCodeLookupStatus = "succeeded"
	AgentCountryCodeLookupFailed    AgentCountryCodeLookupStatus = "failed"
)

type AgentCountryCodeLookup struct {
	AgentID          string
	OperationID      string
	Status           AgentCountryCodeLookupStatus
	ResultCode       string
	ErrorCode        string
	LastCode         string
	CreatedAt        int64
	UpdatedAt        int64
	RequestedEpoch   uint64
	RequestedSession string
	ActiveEpoch      uint64
	ActiveSession    string
}

type AgentCountryCodeLookupAck struct {
	Accepted  bool
	Duplicate bool
	Status    AgentCountryCodeLookupStatus
}

// CreateAgentCountryCodeLookup replaces the latest terminal one-shot request
// while retaining the last successful code. Active requests are not repeated;
// a stale pending request may be explicitly superseded after its wait window.
func (s *Store) CreateAgentCountryCodeLookup(ctx context.Context, agentID string, now time.Time) (AgentCountryCodeLookup, error) {
	if !validStorageID(agentID, 128) || now.UnixMilli() <= 0 {
		return AgentCountryCodeLookup{}, ErrAgentNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCountryCodeLookup{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return AgentCountryCodeLookup{}, ErrAgentNotFound
	}
	epoch, sessionID, supported, err := countryCodeLookupCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return AgentCountryCodeLookup{}, err
	}
	if !supported {
		return AgentCountryCodeLookup{}, ErrAgentCountryLookupUnsupported
	}
	lastCode := ""
	if existing, exists, err := readAgentCountryCodeLookupByAgentTx(ctx, tx, agentID); err != nil {
		return AgentCountryCodeLookup{}, err
	} else if exists {
		lastCode = existing.LastCode
		if isPendingCountryCodeLookup(existing.Status) && now.UnixMilli()-existing.UpdatedAt < AgentCountryCodeLookupPendingTTL.Milliseconds() {
			return AgentCountryCodeLookup{}, ErrAgentCountryLookupPending
		}
	}
	operationID, err := auth.NewID()
	if err != nil {
		return AgentCountryCodeLookup{}, err
	}
	stamp := now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_country_code_lookups(
		agent_id,operation_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,
		result_code,error_code,last_country_code,created_at,updated_at
	) VALUES(?,?,'requested',?,?,?,?,NULL,NULL,NULL,?,?)
	ON CONFLICT(agent_id) DO UPDATE SET operation_id=excluded.operation_id,status='requested',
		requested_epoch=excluded.requested_epoch,requested_session_id=excluded.requested_session_id,
		active_epoch=excluded.active_epoch,active_session_id=excluded.active_session_id,
		result_code=NULL,error_code=NULL,created_at=excluded.created_at,updated_at=excluded.updated_at`,
		agentID, operationID, int64(epoch), sessionID, int64(epoch), sessionID, stamp, stamp)
	if err != nil {
		return AgentCountryCodeLookup{}, err
	}
	operation := AgentCountryCodeLookup{
		AgentID: agentID, OperationID: operationID, Status: AgentCountryCodeLookupRequested,
		CreatedAt: stamp, UpdatedAt: stamp, RequestedEpoch: epoch, RequestedSession: sessionID,
		ActiveEpoch: epoch, ActiveSession: sessionID, LastCode: lastCode,
	}
	if err := tx.Commit(); err != nil {
		return AgentCountryCodeLookup{}, err
	}
	return operation, nil
}

// ClaimAgentCountryCodeLookup delivers a queued trigger once to the current
// capable session. A lost claim response is never redelivered automatically.
func (s *Store) ClaimAgentCountryCodeLookup(ctx context.Context, agentID string, epoch uint64, sessionID string, now time.Time) (*AgentCountryCodeLookup, error) {
	if !validStorageID(agentID, 128) || epoch == 0 || epoch > math.MaxInt64 || !validStorageID(sessionID, 128) || now.UnixMilli() <= 0 {
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
	currentEpoch, currentSession, supported, err := countryCodeLookupCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if !supported || currentEpoch != epoch || currentSession != sessionID {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	operation, exists, err := readAgentCountryCodeLookupByAgentTx(ctx, tx, agentID)
	if err != nil || !exists || operation.Status != AgentCountryCodeLookupRequested {
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	stamp := now.UnixMilli()
	update, err := tx.ExecContext(ctx, `UPDATE agent_country_code_lookups SET status='delivered',active_epoch=?,active_session_id=?,updated_at=?
		WHERE operation_id=? AND status='requested'`, int64(epoch), sessionID, stamp, operation.OperationID)
	if err != nil {
		return nil, err
	}
	changed, err := update.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return nil, err
		}
		return nil, ErrAgentCountryLookupTransition
	}
	operation.Status, operation.UpdatedAt, operation.ActiveEpoch, operation.ActiveSession = AgentCountryCodeLookupDelivered, stamp, epoch, sessionID
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &operation, nil
}

// CompleteAgentCountryCodeLookup is idempotent for an identical result from
// the same still-current Agent session. Failure never replaces LastCode.
func (s *Store) CompleteAgentCountryCodeLookup(ctx context.Context, agentID, operationID string, epoch uint64, sessionID, countryCode, errorCode string, now time.Time) (AgentCountryCodeLookupAck, error) {
	if !validStorageID(agentID, 128) || !validRemovalOperationID(operationID) || epoch == 0 || epoch > math.MaxInt64 || !validStorageID(sessionID, 128) || now.UnixMilli() <= 0 {
		return AgentCountryCodeLookupAck{}, ErrUnauthorized
	}
	request := protocol.AgentCountryCodeLookupResultRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion,
		AgentEpoch: epoch, SessionID: sessionID, CountryCode: countryCode, ErrorCode: errorCode}
	if request.Validate() != nil {
		return AgentCountryCodeLookupAck{}, ErrAgentCountryLookupTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	currentEpoch, currentSession, supported, err := countryCodeLookupCapabilityTx(ctx, tx, agentID)
	if err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	if !supported || currentEpoch != epoch || currentSession != sessionID {
		return AgentCountryCodeLookupAck{}, ErrUnauthorized
	}
	operation, exists, err := readAgentCountryCodeLookupByOperationTx(ctx, tx, operationID)
	if err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	if !exists || operation.AgentID != agentID {
		return AgentCountryCodeLookupAck{}, ErrAgentCountryLookupNotFound
	}
	if operation.ActiveEpoch != epoch || operation.ActiveSession != sessionID {
		return AgentCountryCodeLookupAck{}, ErrUnauthorized
	}
	if operation.Status == AgentCountryCodeLookupSucceeded || operation.Status == AgentCountryCodeLookupFailed {
		if operation.ResultCode != countryCode || operation.ErrorCode != errorCode {
			return AgentCountryCodeLookupAck{}, ErrAgentCountryLookupTransition
		}
		if err := tx.Commit(); err != nil {
			return AgentCountryCodeLookupAck{}, err
		}
		return AgentCountryCodeLookupAck{Accepted: true, Duplicate: true, Status: operation.Status}, nil
	}
	if operation.Status != AgentCountryCodeLookupDelivered {
		return AgentCountryCodeLookupAck{}, ErrAgentCountryLookupTransition
	}
	status, resultCode := AgentCountryCodeLookupFailed, sql.NullString{}
	if countryCode != "" {
		status, resultCode = AgentCountryCodeLookupSucceeded, sql.NullString{String: countryCode, Valid: true}
	}
	var failure sql.NullString
	if errorCode != "" {
		failure = sql.NullString{String: errorCode, Valid: true}
	}
	stamp := now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_country_code_lookups SET status=?,result_code=?,error_code=?,
		last_country_code=CASE WHEN ?<>'' THEN ? ELSE last_country_code END,updated_at=? WHERE operation_id=? AND status='delivered'`,
		status, resultCode, failure, countryCode, countryCode, stamp, operationID); err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	operation.Status, operation.ResultCode, operation.ErrorCode, operation.UpdatedAt = status, countryCode, errorCode, stamp
	if countryCode != "" {
		operation.LastCode = countryCode
	}
	if err := tx.Commit(); err != nil {
		return AgentCountryCodeLookupAck{}, err
	}
	return AgentCountryCodeLookupAck{Accepted: true, Status: status}, nil
}

func (s *Store) GetAgentCountryCodeLookup(ctx context.Context, agentID string) (AgentCountryCodeLookup, bool, error) {
	if !validStorageID(agentID, 128) {
		return AgentCountryCodeLookup{}, false, ErrAgentCountryLookupNotFound
	}
	return readAgentCountryCodeLookupByAgentDB(ctx, s.db, agentID)
}

func countryCodeLookupCapabilityTx(ctx context.Context, tx *sql.Tx, agentID string) (uint64, string, bool, error) {
	var epoch int64
	var session string
	var supported int
	err := tx.QueryRowContext(ctx, `SELECT s.epoch,s.session_id,c.country_code_lookup FROM agent_state s
		JOIN agent_management_capabilities c ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id
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

func readAgentCountryCodeLookupByAgentTx(ctx context.Context, tx *sql.Tx, agentID string) (AgentCountryCodeLookup, bool, error) {
	return scanAgentCountryCodeLookup(tx.QueryRowContext(ctx, `SELECT agent_id,operation_id,status,result_code,error_code,last_country_code,
		created_at,updated_at,requested_epoch,requested_session_id,active_epoch,active_session_id FROM agent_country_code_lookups WHERE agent_id=?`, agentID))
}

func readAgentCountryCodeLookupByOperationTx(ctx context.Context, tx *sql.Tx, operationID string) (AgentCountryCodeLookup, bool, error) {
	return scanAgentCountryCodeLookup(tx.QueryRowContext(ctx, `SELECT agent_id,operation_id,status,result_code,error_code,last_country_code,
		created_at,updated_at,requested_epoch,requested_session_id,active_epoch,active_session_id FROM agent_country_code_lookups WHERE operation_id=?`, operationID))
}

func readAgentCountryCodeLookupByAgentDB(ctx context.Context, db *sql.DB, agentID string) (AgentCountryCodeLookup, bool, error) {
	return scanAgentCountryCodeLookup(db.QueryRowContext(ctx, `SELECT agent_id,operation_id,status,result_code,error_code,last_country_code,
		created_at,updated_at,requested_epoch,requested_session_id,active_epoch,active_session_id FROM agent_country_code_lookups WHERE agent_id=?`, agentID))
}

func scanAgentCountryCodeLookup(row *sql.Row) (AgentCountryCodeLookup, bool, error) {
	var operation AgentCountryCodeLookup
	var status string
	var resultCode, errorCode, lastCode sql.NullString
	var requestedEpoch, activeEpoch int64
	err := row.Scan(&operation.AgentID, &operation.OperationID, &status, &resultCode, &errorCode, &lastCode,
		&operation.CreatedAt, &operation.UpdatedAt, &requestedEpoch, &operation.RequestedSession, &activeEpoch, &operation.ActiveSession)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentCountryCodeLookup{}, false, nil
	}
	if err != nil {
		return AgentCountryCodeLookup{}, false, err
	}
	operation.Status = AgentCountryCodeLookupStatus(status)
	operation.ResultCode, operation.ErrorCode, operation.LastCode = resultCode.String, errorCode.String, lastCode.String
	if requestedEpoch <= 0 || activeEpoch <= 0 || !validRemovalOperationID(operation.OperationID) || !validStorageID(operation.AgentID, 128) ||
		!validAgentCountryCodeLookupStatus(operation.Status) || operation.CreatedAt <= 0 || operation.UpdatedAt < operation.CreatedAt ||
		(operation.ResultCode != "" && !protocol.ValidCountryCode(operation.ResultCode)) || (operation.LastCode != "" && !protocol.ValidCountryCode(operation.LastCode)) {
		return AgentCountryCodeLookup{}, false, ErrCorruptProbeData
	}
	operation.RequestedEpoch, operation.ActiveEpoch = uint64(requestedEpoch), uint64(activeEpoch)
	return operation, true, nil
}

func isPendingCountryCodeLookup(status AgentCountryCodeLookupStatus) bool {
	return status == AgentCountryCodeLookupRequested || status == AgentCountryCodeLookupDelivered
}

func validAgentCountryCodeLookupStatus(status AgentCountryCodeLookupStatus) bool {
	return status == AgentCountryCodeLookupRequested || status == AgentCountryCodeLookupDelivered || status == AgentCountryCodeLookupSucceeded || status == AgentCountryCodeLookupFailed
}
