package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// AddServerLocalAgent registers an ordinary credential and its Server identity
// in one transaction. Only the offline Server administration command calls it.
func (s *Store) AddServerLocalAgent(ctx context.Context, id, name string, hash []byte, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, id, name, hash, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_local_agent(singleton,agent_id) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET agent_id=excluded.agent_id`, id); err != nil {
		return err
	}
	return tx.Commit()
}
