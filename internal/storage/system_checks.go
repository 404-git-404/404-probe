package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"404-probe/internal/protocol"
)

func migrateSystemChecks41Tx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE agent_system_check_settings (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			settings_json TEXT NOT NULL
		);
		CREATE TABLE server_local_agent (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE
		);
		INSERT INTO schema_migrations(version, applied_at) VALUES(41, ?)
	`, time.Now().UnixMilli())
	return err
}

func (s *Store) SystemCheckSettings(ctx context.Context, id string) (protocol.SystemCheckSettings, error) {
	settings := protocol.SystemCheckSettings{}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT settings_json FROM agent_system_check_settings WHERE agent_id=?`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &settings)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return settings, err
	}
	return settings, settings.Normalize()
}

func (s *Store) SaveSystemCheckSettings(ctx context.Context, id string, settings protocol.SystemCheckSettings) error {
	if err := settings.Normalize(); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_system_check_settings (agent_id,settings_json)
		SELECT id,? FROM agents WHERE id=? AND revoked=0
		ON CONFLICT(agent_id) DO UPDATE SET settings_json=excluded.settings_json`, string(raw), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

func (s *Store) ServerLocalAgent(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM server_local_agent WHERE singleton=1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// RegisterServerLocalAgent rotates the local credential while retaining its
// Agent identity, reports, and history. The returned epoch is the last one
// the Server accepted, for reinstalls that have lost their local epoch file.
func (s *Store) RegisterServerLocalAgent(ctx context.Context, newID, name string, hash []byte, now time.Time) (string, uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE agents SET token_hash=?,updated_at=?
		WHERE id=(SELECT agent_id FROM server_local_agent WHERE singleton=1) AND revoked=0 RETURNING id`, hash, now.UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_local_agent)`).Scan(&exists); err != nil {
			return "", 0, err
		}
		if exists {
			return "", 0, ErrAgentRevoked
		}
		id = newID
		if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, id, name, hash, now.UnixMilli(), now.UnixMilli()); err != nil {
			return "", 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO server_local_agent(singleton,agent_id) VALUES(1,?)`, id); err != nil {
			return "", 0, err
		}
	} else if err != nil {
		return "", 0, err
	}
	var epoch uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT epoch FROM agent_state WHERE agent_id=?),0)`, id).Scan(&epoch); err != nil {
		return "", 0, err
	}
	if epoch >= math.MaxInt64 {
		return "", 0, errors.New("Server local Agent epoch exhausted")
	}
	if err := tx.Commit(); err != nil {
		return "", 0, err
	}
	return id, epoch, nil
}
