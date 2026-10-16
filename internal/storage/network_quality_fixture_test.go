package storage

import (
	"database/sql"
	"testing"
)

// Existing old-schema fixtures start from Open(latest). Remove migrations24/23
// before their original explicit downgrade, so they genuinely remain old DBs.
func dropQualityFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropUpgradeV2FixtureSchema(t, db)
	for _, table := range []string{"agent_plan_renewal_requests", "agent_plan_config"} {
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=24`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"quality_raw", "quality_minute", "quality_gaps", "quality_intervals", "quality_slots", "quality_config", "quality_capability", "quality_targets", "quality_usage", "quality_totals"} {
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=23`); err != nil {
		t.Fatal(err)
	}
}

// Return Open(latest) fixtures to the actual schema24 before older migrations.
func dropUpgradeV2FixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropQualityV2FixtureSchema(t, db)
	for _, statement := range []string{
		`ALTER TABLE agent_state DROP COLUMN agent_upgrade_v2`,
		`ALTER TABLE agent_upgrade_operations DROP COLUMN beta_confirmed`,
		`ALTER TABLE agent_upgrade_operations DROP COLUMN server_version`,
		`ALTER TABLE agent_upgrade_operations DROP COLUMN target_commit`,
		`ALTER TABLE agent_upgrade_operations DROP COLUMN required_protocol`,
		`ALTER TABLE agent_upgrade_operations DROP COLUMN channel`,
		`DELETE FROM schema_migrations WHERE version=25`,
	} {
		if len(statement) > 12 && statement[:12] == "ALTER TABLE " {
			var exists int
			name := "agent_upgrade_operations"
			if statement[12:23] == "agent_state" {
				name = "agent_state"
			}
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists == 0 {
				continue
			}
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

// Test fixtures must remove schema26 before explicitly testing older schemas.
// Only legal single-protocol legacy fixtures can be downgraded by this helper.
func dropQualityV2FixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropQualityRollupFixtureSchema(t, db)
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=26`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists == 0 {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE quality_raw DROP COLUMN details_json`,
		`ALTER TABLE quality_capability DROP COLUMN v2_details`,
		`ALTER TABLE quality_capability DROP COLUMN excluded_reason`,
		`ALTER TABLE quality_config DROP COLUMN region_id`,
		`ALTER TABLE quality_config DROP COLUMN v2_lanes`,
		`CREATE TABLE quality_slots_old(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,revision INTEGER NOT NULL,target_id TEXT REFERENCES quality_targets(id),PRIMARY KEY(agent_id,slot,family))`,
		`INSERT INTO quality_slots_old SELECT agent_id,slot,family,revision,target_id FROM quality_slots`,
		`CREATE TABLE quality_intervals_old(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,revision INTEGER NOT NULL,target_id TEXT NOT NULL REFERENCES quality_targets(id),start_at INTEGER NOT NULL,end_at INTEGER,config_from INTEGER NOT NULL,config_until INTEGER,PRIMARY KEY(agent_id,slot,family,revision))`,
		`INSERT INTO quality_intervals_old SELECT agent_id,slot,family,revision,target_id,start_at,end_at,config_from,config_until FROM quality_intervals`,
		`DROP TABLE quality_slots`, `ALTER TABLE quality_slots_old RENAME TO quality_slots`,
		`DROP TABLE quality_intervals`, `ALTER TABLE quality_intervals_old RENAME TO quality_intervals`,
		`CREATE INDEX quality_slot_target ON quality_slots(target_id)`,
		`CREATE INDEX quality_interval_retention ON quality_intervals(end_at,agent_id,revision)`,
		`CREATE INDEX quality_interval_target ON quality_intervals(agent_id,target_id,revision,start_at)`,
		`CREATE INDEX quality_interval_target_id ON quality_intervals(target_id)`,
		`CREATE TRIGGER quality_intervals_insert AFTER INSERT ON quality_intervals BEGIN INSERT OR IGNORE INTO quality_usage(agent_id) VALUES(NEW.agent_id); UPDATE quality_usage SET intervals=intervals+1 WHERE agent_id=NEW.agent_id; UPDATE quality_totals SET value=value+1 WHERE kind='intervals'; END`,
		`CREATE TRIGGER quality_intervals_delete AFTER DELETE ON quality_intervals BEGIN UPDATE quality_usage SET intervals=intervals-1 WHERE agent_id=OLD.agent_id; UPDATE quality_totals SET value=value-1 WHERE kind='intervals'; END`,
		`DELETE FROM schema_migrations WHERE version=26`,
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Remove schema28/27 before constructing genuine prior-schema fixtures.
func dropQualityRollupFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropPublicEgressFixtureSchema(t, db)
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=27`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists == 0 {
		return
	}
	for _, table := range []string{"quality_receipts", "quality_facts", "quality_buckets", "quality_retired", "quality_series", "quality_sessions", "quality_backfill"} {
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM quality_totals WHERE kind IN ('receipts','facts','buckets','series','sessions')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=27`); err != nil {
		t.Fatal(err)
	}
}

// Open(latest) fixtures must remove M10 before reconstructing older schemas.
// Keep this in the shared downgrade chain, including fixtures already below 27.
func dropPublicEgressFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropTCPMiniFixtureSchema(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DROP TABLE IF EXISTS agent_public_egress`,
		`DELETE FROM schema_migrations WHERE version=28`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
