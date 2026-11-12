package storage

import (
	"context"
	"database/sql"
)

var migration37FailAt func(string) error // Test-only fault injection.

func migrateHostHealth37Tx(ctx context.Context, tx *sql.Tx) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('agent_state') WHERE name='host_health_json')`).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_state ADD COLUMN host_health_json TEXT CHECK(host_health_json IS NULL OR json_valid(host_health_json))`); err != nil {
			return err
		}
	}
	if migration37FailAt != nil {
		if err := migration37FailAt("host_health_column"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_youtube_region_manual (
		agent_id TEXT NOT NULL PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
		region TEXT NOT NULL CHECK(region GLOB '[A-Z][A-Z]'),
		updated_at INTEGER NOT NULL CHECK(typeof(updated_at)='integer' AND updated_at>=0),
		revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>0)
	)`); err != nil {
		return err
	}
	if migration37FailAt != nil {
		if err := migration37FailAt("youtube_region_table"); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(37,unixepoch())`)
	return err
}
