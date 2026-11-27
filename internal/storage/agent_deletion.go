package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

var (
	ErrAgentOnline       = errors.New("agent is online")
	ErrAgentNameMismatch = errors.New("agent name does not match")
	ErrAgentDeleted      = fmt.Errorf("agent deleted: %w", ErrAgentRevoked)
)

func migrateAgentDeletionAndDNS48Tx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		ALTER TABLE agents ADD COLUMN paused_contact_at INTEGER NOT NULL DEFAULT 0;
		CREATE TABLE deleted_agent_credentials (token_hash BLOB PRIMARY KEY NOT NULL) WITHOUT ROWID;
		ALTER TABLE agent_system_check_ignores RENAME TO agent_system_check_ignores_47;
		CREATE TABLE agent_system_check_ignores (
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('service', 'service_restart', 'certificate', 'time', 'dns')),
			item TEXT NOT NULL CHECK(length(CAST(item AS BLOB)) BETWEEN 1 AND 512),
			created_at INTEGER NOT NULL,
			PRIMARY KEY (agent_id, kind, item)
		) WITHOUT ROWID;
		INSERT INTO agent_system_check_ignores SELECT agent_id,kind,item,created_at FROM agent_system_check_ignores_47;
		DROP TABLE agent_system_check_ignores_47;
		INSERT INTO schema_migrations(version, applied_at) VALUES (48, ?);
	`, time.Now().UnixMilli())
	return err
}

// RecordPausedAgentContact keeps the deletion check current without ingesting
// a paused Agent's report or treating the Agent as online for monitoring.
func (s *Store) RecordPausedAgentContact(ctx context.Context, token string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET paused_contact_at=?
		WHERE token_hash=? AND revoked=0 AND disabled_at IS NOT NULL AND paused_contact_at<?`,
		now.UnixMilli(), auth.Hash(token), now.UnixMilli())
	return err
}

func agentContactRecent(lastSeen, lastPaused int64, disabledAt sql.NullInt64, nowMS, offlineMS int64) bool {
	if disabledAt.Valid {
		// A connected Agent backs off to a slower report interval after pause.
		// Start that grace period at pause if the Agent was still reporting then.
		if lastSeen > 0 && lastSeen >= disabledAt.Int64-offlineMS {
			lastPaused = max(lastPaused, disabledAt.Int64)
		}
		return lastPaused > 0 && lastPaused >= nowMS-offlineMS-protocol.AgentPausedReportInterval.Milliseconds()
	}
	return lastSeen > 0 && lastSeen >= nowMS-offlineMS
}

func (s *Store) DeleteOfflineAgent(ctx context.Context, id, confirmedName string, now time.Time, offlineTimeout time.Duration) error {
	nowMS, offlineMS, err := validateAgentSnapshotTime(now, offlineTimeout)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name string
	var tokenHash []byte
	var revoked bool
	var pausedAt sql.NullInt64
	var lastSeen, lastPaused int64
	err = tx.QueryRowContext(ctx, `SELECT a.name,a.token_hash,a.revoked,a.disabled_at,a.paused_contact_at,COALESCE(s.last_seen,0)
		FROM agents a LEFT JOIN agent_state s ON s.agent_id=a.id WHERE a.id=?`, id).Scan(&name, &tokenHash, &revoked, &pausedAt, &lastPaused, &lastSeen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrAgentNotFound
	case err != nil:
		return err
	case revoked:
		return ErrAgentRevoked
	case agentContactRecent(lastSeen, lastPaused, pausedAt, nowMS, offlineMS):
		return ErrAgentOnline
	case name != confirmedName:
		return ErrAgentNameMismatch
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deleted_agent_credentials(token_hash) VALUES (?)`, tokenHash); err != nil {
		return err
	}
	// Spent enrollments predate foreign keys. All other agent_id tables
	// cascade directly or through Agent-owned rows.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE agent_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
