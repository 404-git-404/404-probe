package storage

import (
	"context"
	"database/sql"
	"time"
)

func migrateRootInventories50Tx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE agent_security_baselines (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			baseline_json TEXT,
			baseline_digest TEXT NOT NULL DEFAULT '',
			baseline_at INTEGER NOT NULL DEFAULT 0,
			snapshot_json TEXT,
			snapshot_digest TEXT NOT NULL DEFAULT '',
			snapshot_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE agent_singbox_structures (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			collected_at INTEGER NOT NULL,
			digest TEXT NOT NULL DEFAULT '',
			truncated INTEGER NOT NULL DEFAULT 0,
			structure_json TEXT
		);

		-- Keep references on skipped devices valid while removing a target from the catalog.
		CREATE TEMP TABLE network_target_references_50 (
			agent_id TEXT NOT NULL,slot TEXT NOT NULL,library_id TEXT NOT NULL,PRIMARY KEY(agent_id,slot)
		);
		INSERT INTO network_target_references_50
			SELECT agent_id,slot,library_id FROM quality_slot_settings WHERE library_id IS NOT NULL;
		DROP INDEX idx_quality_slot_library;
		ALTER TABLE quality_slot_settings DROP COLUMN library_id;
		ALTER TABLE network_target_library RENAME TO network_target_library_49;
		CREATE TABLE network_target_library (
			id TEXT NOT NULL PRIMARY KEY,
			region TEXT NOT NULL,
			isp TEXT NOT NULL,
			host TEXT NOT NULL COLLATE NOCASE CHECK(host<>''),
			port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
			revision INTEGER NOT NULL CHECK(revision>0),
			retired INTEGER NOT NULL DEFAULT 0 CHECK(retired IN (0,1))
		);
		INSERT INTO network_target_library(id,region,isp,host,port,revision)
			SELECT id,region,isp,host,port,revision FROM network_target_library_49;
		CREATE UNIQUE INDEX idx_network_target_live ON network_target_library(region,isp,host,port) WHERE retired=0;
		DROP TABLE network_target_library_49;
		ALTER TABLE quality_slot_settings ADD COLUMN library_id TEXT REFERENCES network_target_library(id);
		UPDATE quality_slot_settings AS s SET library_id=(
			SELECT library_id FROM network_target_references_50 r WHERE r.agent_id=s.agent_id AND r.slot=s.slot
		) WHERE (agent_id,slot) IN (SELECT agent_id,slot FROM network_target_references_50);
		DROP TABLE network_target_references_50;
		CREATE INDEX idx_quality_slot_library ON quality_slot_settings(library_id,agent_id) WHERE library_id IS NOT NULL;
		INSERT INTO schema_migrations(version, applied_at) VALUES(50,?);
	`, time.Now().UnixMilli())
	return err
}
