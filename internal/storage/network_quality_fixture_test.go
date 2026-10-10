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
