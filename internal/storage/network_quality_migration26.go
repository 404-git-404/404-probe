package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Rebuild only small tables. Raw IDs, hashes and payload bytes stay in place.
func migrateQualityV2Tx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE quality_slots_v2(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,protocol TEXT NOT NULL CHECK(protocol IN ('tcp','icmp')),revision INTEGER NOT NULL,target_id TEXT REFERENCES quality_targets(id),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),PRIMARY KEY(agent_id,slot,family,protocol))`,
		`INSERT INTO quality_slots_v2 SELECT q.agent_id,q.slot,q.family,CASE WHEN q.target_id IS NOT NULL THEN json_extract(t.payload,'$.protocol') ELSE (SELECT json_extract(c.value,'$.protocol') FROM quality_config f,json_each(f.choices) c WHERE f.agent_id=q.agent_id AND json_extract(c.value,'$.slot')=q.slot) END,q.revision,q.target_id,1 FROM quality_slots q LEFT JOIN quality_targets t ON t.id=q.target_id`,
		`CREATE TABLE quality_intervals_v2(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,protocol TEXT NOT NULL CHECK(protocol IN ('tcp','icmp')),revision INTEGER NOT NULL,target_id TEXT NOT NULL REFERENCES quality_targets(id),start_at INTEGER NOT NULL,end_at INTEGER,config_from INTEGER NOT NULL,config_until INTEGER,v1_authorized INTEGER NOT NULL CHECK(v1_authorized IN (0,1)),PRIMARY KEY(agent_id,slot,family,protocol,revision))`,
		`INSERT INTO quality_intervals_v2 SELECT q.agent_id,q.slot,q.family,json_extract(t.payload,'$.protocol'),q.revision,q.target_id,q.start_at,q.end_at,q.config_from,q.config_until,1 FROM quality_intervals q LEFT JOIN quality_targets t ON t.id=q.target_id`,
		`DROP TABLE quality_slots`, `ALTER TABLE quality_slots_v2 RENAME TO quality_slots`,
		`DROP TABLE quality_intervals`, `ALTER TABLE quality_intervals_v2 RENAME TO quality_intervals`,
		`CREATE INDEX quality_slot_target ON quality_slots(target_id)`,
		`CREATE INDEX quality_interval_retention ON quality_intervals(end_at,agent_id,revision)`,
		`CREATE INDEX quality_interval_target ON quality_intervals(agent_id,target_id,protocol,revision,start_at)`,
		`CREATE INDEX quality_interval_target_id ON quality_intervals(target_id)`,
		`CREATE TRIGGER quality_intervals_insert AFTER INSERT ON quality_intervals BEGIN INSERT OR IGNORE INTO quality_usage(agent_id) VALUES(NEW.agent_id); UPDATE quality_usage SET intervals=intervals+1 WHERE agent_id=NEW.agent_id; UPDATE quality_totals SET value=value+1 WHERE kind='intervals'; END`,
		`CREATE TRIGGER quality_intervals_delete AFTER DELETE ON quality_intervals BEGIN UPDATE quality_usage SET intervals=intervals-1 WHERE agent_id=OLD.agent_id; UPDATE quality_totals SET value=value-1 WHERE kind='intervals'; END`,
		`ALTER TABLE quality_raw ADD COLUMN details_json TEXT CHECK(details_json IS NULL OR (length(CAST(details_json AS BLOB))<=2048 AND json_valid(details_json)))`,
		`ALTER TABLE quality_capability ADD COLUMN v2_details TEXT CHECK(v2_details IS NULL OR (length(CAST(v2_details AS BLOB))<=2048 AND json_valid(v2_details)))`,
		`ALTER TABLE quality_capability ADD COLUMN excluded_reason TEXT NOT NULL DEFAULT '' CHECK(length(excluded_reason)<=64)`,
		`UPDATE quality_capability SET excluded_reason='platform_excluded:alpine' WHERE agent_id IN (SELECT agent_id FROM agent_state WHERE lower(trim(os))='alpine' OR lower(trim(os)) LIKE 'alpine %')`,
		`ALTER TABLE quality_config ADD COLUMN region_id TEXT NOT NULL DEFAULT '' CHECK(length(region_id)<=120)`,
		`ALTER TABLE quality_config ADD COLUMN v2_lanes TEXT CHECK(v2_lanes IS NULL OR (length(CAST(v2_lanes AS BLOB))<=8192 AND json_valid(v2_lanes)))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var bad int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_usage u WHERE u.intervals<>(SELECT COUNT(*) FROM quality_intervals i WHERE i.agent_id=u.agent_id)`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("quality interval counts inconsistent")
	}
	if err := tx.QueryRowContext(ctx, `SELECT value<>(SELECT COUNT(*) FROM quality_intervals) FROM quality_totals WHERE kind='intervals'`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("quality total interval count inconsistent")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("quality migration foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(26,unixepoch())`)
	return err
}
