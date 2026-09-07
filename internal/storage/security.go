package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"404-probe/internal/protocol"
)

const SecurityRetention = 30 * 24 * time.Hour

type SecurityCapability struct {
	Supported          bool
	Status             protocol.SecurityStatus
	Reason             string
	Epoch              uint64
	SessionID          string
	CurrentBatchID     string
	CurrentCollectedAt int64
	UpdatedAt          int64
}

type SecurityRecord struct {
	Batch               protocol.SecurityBatch
	ReceivedAt          int64
	DeliveryGap         bool
	PreviousCollectedAt int64
}

func (s *Store) SaveSecuritySubmission(ctx context.Context, agentID string, submission protocol.SecuritySubmission, now time.Time) (bool, error) {
	if err := submission.ValidateAt(now); err != nil || submission.AgentEpoch > math.MaxInt64 || now.UnixMilli() <= 0 {
		if err != nil {
			return false, err
		}
		return false, ErrSecurityFence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var revoked int
	var disabled sql.NullInt64
	var epoch int64
	var session string
	if err := tx.QueryRowContext(ctx, `SELECT a.revoked,a.disabled_at,s.epoch,s.session_id FROM agents a JOIN agent_state s ON s.agent_id=a.id WHERE a.id=?`, agentID).Scan(&revoked, &disabled, &epoch, &session); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrUnauthorized
		}
		return false, err
	}
	if revoked != 0 {
		return false, ErrAgentRevoked
	}
	if disabled.Valid {
		return false, ErrAgentDisabled
	}
	if epoch != int64(submission.AgentEpoch) || session != submission.SessionID {
		return false, ErrSecurityFence
	}
	changed := false
	var oldStatus, oldReason, oldSession string
	var oldEpoch, oldCollected int64
	var oldBatch sql.NullString
	capErr := tx.QueryRowContext(ctx, `SELECT status,COALESCE(reason,''),epoch,session_id,current_batch_id,current_collected_at FROM agent_security_capabilities WHERE agent_id=?`, agentID).Scan(&oldStatus, &oldReason, &oldEpoch, &oldSession, &oldBatch, &oldCollected)
	if capErr != nil && !errors.Is(capErr, sql.ErrNoRows) {
		return false, capErr
	}
	currentBatchID := ""
	currentCollected := int64(0)
	if submission.Batch != nil {
		payload, err := json.Marshal(submission.Batch)
		if err != nil {
			return false, err
		}
		hash := sha256.Sum256(payload)
		var existing []byte
		err = tx.QueryRowContext(ctx, `SELECT content_hash FROM agent_security_batches WHERE agent_id=? AND batch_id=?`, agentID, submission.Batch.BatchID).Scan(&existing)
		switch {
		case err == nil:
			if !equalBytes(existing, hash[:]) {
				return false, ErrSecurityConflict
			}
		case errors.Is(err, sql.ErrNoRows):
			batch := submission.Batch
			deliveryGap, previousCollectedAt := securityDeliveryValues(submission.Delivery)
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_security_batches(agent_id,batch_id,content_hash,epoch,session_id,window_start,window_end,collected_at,status,reason,total_events,source_count,payload_json,received_at,delivery_gap,previous_collected_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentID, batch.BatchID, hash[:], epoch, session, batch.WindowStart, batch.WindowEnd, batch.CollectedAt,
				batch.Status, nullableText(batch.Reason), batch.TotalEvents, batch.TrackedSources, string(payload), now.UnixMilli(), deliveryGap, previousCollectedAt); err != nil {
				return false, err
			}
			changed = true
		default:
			return false, err
		}
		if submission.Delivery != nil {
			result, err := tx.ExecContext(ctx, `UPDATE agent_security_batches SET delivery_gap=1,previous_collected_at=? WHERE agent_id=? AND batch_id=? AND (delivery_gap=0 OR previous_collected_at<>?)`,
				submission.Delivery.PreviousCollectedAt, agentID, submission.Batch.BatchID, submission.Delivery.PreviousCollectedAt)
			if err != nil {
				return false, err
			}
			if applied, _ := result.RowsAffected(); applied != 0 {
				changed = true
			}
		}
		currentBatchID = submission.Batch.BatchID
		currentCollected = submission.Batch.CollectedAt
	}
	newSession := errors.Is(capErr, sql.ErrNoRows) || oldEpoch != epoch || oldSession != session
	shouldUpdateCapability := submission.Batch == nil || newSession || currentCollected >= oldCollected
	if shouldUpdateCapability {
		if errors.Is(capErr, sql.ErrNoRows) || oldStatus != string(submission.Status) || oldReason != submission.Reason || oldEpoch != epoch || oldSession != session || oldBatch.String != currentBatchID || oldCollected != currentCollected {
			changed = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_security_capabilities(agent_id,supported,status,reason,epoch,session_id,current_batch_id,current_collected_at,updated_at)
			VALUES(?,1,?,?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET supported=1,status=excluded.status,reason=excluded.reason,epoch=excluded.epoch,session_id=excluded.session_id,current_batch_id=excluded.current_batch_id,current_collected_at=excluded.current_collected_at,updated_at=excluded.updated_at`,
			agentID, submission.Status, nullableText(submission.Reason), epoch, session, nullableText(currentBatchID), currentCollected, now.UnixMilli()); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

func (s *Store) SecurityCapability(ctx context.Context, agentID string) (SecurityCapability, bool, error) {
	var result SecurityCapability
	var supported int
	var epoch int64
	var reason sql.NullString
	var currentBatch sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT supported,status,reason,epoch,session_id,current_batch_id,current_collected_at,updated_at FROM agent_security_capabilities WHERE agent_id=?`, agentID).
		Scan(&supported, &result.Status, &reason, &epoch, &result.SessionID, &currentBatch, &result.CurrentCollectedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SecurityCapability{}, false, nil
	}
	if err != nil {
		return SecurityCapability{}, false, err
	}
	result.Supported, result.Reason, result.Epoch, result.CurrentBatchID = supported != 0, reason.String, uint64(epoch), currentBatch.String
	return result, true, nil
}

func (s *Store) SecurityHistory(ctx context.Context, agentID string, since time.Time, limit int, now time.Time) ([]SecurityRecord, error) {
	if limit < 1 || limit > 100 {
		limit = 31
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json,received_at,delivery_gap,previous_collected_at FROM agent_security_batches WHERE agent_id=? AND collected_at>=? ORDER BY collected_at DESC,batch_id DESC LIMIT ?`, agentID, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SecurityRecord, 0)
	for rows.Next() {
		var payload string
		var record SecurityRecord
		if err := rows.Scan(&payload, &record.ReceivedAt, &record.DeliveryGap, &record.PreviousCollectedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &record.Batch); err != nil {
			return nil, ErrCorruptProbeData
		}
		if err := record.Batch.ValidateAt(now); err != nil {
			return nil, ErrCorruptProbeData
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) CurrentSecurity(ctx context.Context, agentID string, capability SecurityCapability, now time.Time) (*SecurityRecord, error) {
	if capability.CurrentBatchID == "" {
		return nil, nil
	}
	var payload string
	var record SecurityRecord
	err := s.db.QueryRowContext(ctx, `SELECT payload_json,received_at,delivery_gap,previous_collected_at FROM agent_security_batches WHERE agent_id=? AND batch_id=?`,
		agentID, capability.CurrentBatchID).Scan(&payload, &record.ReceivedAt, &record.DeliveryGap, &record.PreviousCollectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batch protocol.SecurityBatch
	if json.Unmarshal([]byte(payload), &batch) != nil {
		return nil, ErrCorruptProbeData
	}
	if err := batch.ValidateAt(now); err != nil || batch.CollectedAt != capability.CurrentCollectedAt {
		return nil, ErrCorruptProbeData
	}
	record.Batch = batch
	return &record, nil
}

func (s *Store) CleanupSecurityHistory(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_security_batches WHERE received_at < ?`, before.UnixMilli())
	return err
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func securityDeliveryValues(delivery *protocol.SecurityDelivery) (int, int64) {
	if delivery == nil {
		return 0, 0
	}
	return 1, delivery.PreviousCollectedAt
}
