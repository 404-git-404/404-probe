package storage

import (
	"context"
	"database/sql"
	"time"

	"404-probe/internal/auth"
)

func migrateStartupDiskFailure40Tx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_startup_disk_failures (
		agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
		received_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(40,unixepoch())`)
	return err
}

// RecordStartupDiskFailure creates at most one alert until an accepted report
// clears it. Recheck the credential and lifecycle at the write boundary.
func (s *Store) RecordStartupDiskFailure(ctx context.Context, agentID, token string, received time.Time) (bool, error) {
	at := received.UnixMilli()
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_startup_disk_failures(agent_id,received_at)
		SELECT a.id,? FROM agents a
		WHERE a.id=? AND a.token_hash=? AND a.revoked=0 AND a.disabled_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM agent_state s WHERE s.agent_id=a.id AND s.last_seen>?)
		ON CONFLICT(agent_id) DO NOTHING`, at, agentID, auth.Hash(token), at)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
