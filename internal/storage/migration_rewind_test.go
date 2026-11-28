package storage

import (
	"context"
	"database/sql"
	"testing"
)

// withoutMigration34 makes the test see the schema as migrations up to 33 leave it.
func withoutMigration34(t *testing.T) {
	t.Helper()
	migration34Skip = true
	t.Cleanup(func() { migration34Skip = false })
}

// dropColumnIfPresent rewinds an added column; a table that a fixture already dropped is skipped.
func dropColumnIfPresent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var present int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name=?`, column).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present > 0 {
		if _, err := db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
}

func rewindNetworkStatusFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindSystemCheckIgnoresFixture(t, db)
	for _, q := range []string{
		`DROP TABLE IF EXISTS offsite_backup`,
		`DROP TRIGGER IF EXISTS quality_fleet_hour_insert`, `DROP TRIGGER IF EXISTS quality_fleet_hour_update`, `DROP TRIGGER IF EXISTS quality_fleet_hour_delete`,
		`DROP TABLE IF EXISTS quality_fleet_rollups`, `DROP TABLE IF EXISTS quality_fleet_dirty`,
		`DELETE FROM schema_migrations WHERE version>=43`,
		`DROP TRIGGER IF EXISTS quality_target_history_close`, `DROP TRIGGER IF EXISTS quality_target_history_insert`,
		`DROP TRIGGER IF EXISTS agent_presence_pause`, `DROP TRIGGER IF EXISTS agent_presence_resume`, `DROP TRIGGER IF EXISTS agent_presence_register`,
		`DROP TABLE IF EXISTS quality_target_history`, `DROP TABLE IF EXISTS quality_alert_history`,
		`DROP TABLE IF EXISTS quality_alert_cursor`, `DROP TABLE IF EXISTS quality_clock_estimates`,
		`DROP TABLE IF EXISTS agent_presence_events`, `DROP TABLE IF EXISTS agent_presence`,
		`DROP TABLE IF EXISTS agent_presence_anchor`, `DELETE FROM schema_migrations WHERE version=42`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dropColumnIfPresent(t, db, "minute_metrics", "rate_samples")
	dropColumnIfPresent(t, db, "agent_state", "observed_rx_rate")
	dropColumnIfPresent(t, db, "agent_state", "observed_tx_rate")
}

func rewindNetworkTargetsAndSkinsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_quality_slot_library;
		DROP TABLE IF EXISTS skin_settings;
		DROP TABLE IF EXISTS skins;
		DELETE FROM schema_migrations WHERE version=49`); err != nil {
		t.Fatal(err)
	}
	dropColumnIfPresent(t, db, "quality_slot_settings", "library_id")
	dropColumnIfPresent(t, db, "quality_slot_settings", "legacy_label")
	if _, err := db.Exec(`DROP TABLE IF EXISTS network_target_library`); err != nil {
		t.Fatal(err)
	}
}

func rewindSystemCheckIgnoresFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindNetworkTargetsAndSkinsFixture(t, db)
	dropColumnIfPresent(t, db, "tags", "color")
	dropColumnIfPresent(t, db, "agents", "paused_contact_at")
	if _, err := db.Exec(`DROP TABLE IF EXISTS deleted_agent_credentials;
		DROP TABLE IF EXISTS agent_system_check_ignores;
		DELETE FROM schema_migrations WHERE version>=47`); err != nil {
		t.Fatal(err)
	}
}

func rewindSystemChecksFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindNetworkStatusFixture(t, db)
	for _, q := range []string{`DROP TABLE IF EXISTS agent_system_check_settings`, `DROP TABLE IF EXISTS server_local_agent`, `DELETE FROM schema_migrations WHERE version=41`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func rewindStartupDiskFailureFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindSystemChecksFixture(t, db)
	for _, q := range []string{`DROP TABLE IF EXISTS agent_startup_disk_failures`, `DELETE FROM schema_migrations WHERE version=40`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func rewindAgentControlsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindStartupDiskFailureFixture(t, db)
	for _, column := range [][2]string{
		{"agents", "country_lookup_enabled"}, {"agents", "youtube_check_enabled"}, {"agents", "disk_fault_acknowledged_at"},
		{"probe_jobs", "cancelled"}, {"agent_country_code_lookups", "cancelled"},
	} {
		dropColumnIfPresent(t, db, column[0], column[1])
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=39`); err != nil {
		t.Fatal(err)
	}
}

func rewindWebSettingsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindAgentControlsFixture(t, db)
	dropColumnIfPresent(t, db, "agent_plans", "renewal_url")
	for _, q := range []string{
		`DROP TABLE IF EXISTS web_youtube_ad_free_regions`,
		`DROP TABLE IF EXISTS web_admin_credential`,
		`DELETE FROM schema_migrations WHERE version=38`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func rewindHostHealthFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindWebSettingsFixture(t, db)
	dropColumnIfPresent(t, db, "agent_state", "host_health_json")
	for _, q := range []string{`DROP TABLE IF EXISTS agent_youtube_region_manual`, `DELETE FROM schema_migrations WHERE version=37`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// rewindV107Fixture returns a latest-schema database to schema 34 (before v1.0.7): the schema 35 columns
// are dropped and the TCP Mini tables of schema 29 are back, one state row per agent as migration 29 made them.
func rewindV107Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, column := range [][2]string{{"agent_plans", "cycle_rx_bytes"}, {"agent_plans", "cycle_tx_bytes"}, {"agent_plans", "cycle_dir_basis"}, {"agent_plans", "cycle_dir_since"}, {"quality_config", "slow_threshold_ms"}} {
		dropColumnIfPresent(t, db, column[0], column[1])
	}
	rewindV107Version(t, db)
}

// migrateV107Forward runs migration 35 once more on a fixture a test rewound (its version row goes first).
func migrateV107Forward(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version=35`); err != nil {
		t.Fatal(err)
	}
	if err := migrateV107Tx(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// rewindV107Version lets migration 35 run again: its version row goes and the TCP Mini tables return.
func rewindV107Version(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindHostHealthFixture(t, db)
	// Schema36 is additive, but its version and FK tables must also be rewound.
	for _, table := range organizationTables {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=36`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=35`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS tcp_mini_state(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,evidence_start INTEGER NOT NULL,scanned_at INTEGER NOT NULL DEFAULT 0,initialized INTEGER NOT NULL DEFAULT 0,last_high INTEGER NOT NULL DEFAULT 0,last_low INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 0,backfill_minute INTEGER NOT NULL DEFAULT 0,backfill_series INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS tcp_mini_high(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,minute INTEGER NOT NULL,peak_ms REAL NOT NULL CHECK(peak_ms>2000 AND peak_ms<=6000),source TEXT NOT NULL,PRIMARY KEY(agent_id,minute)) WITHOUT ROWID`,
		`INSERT OR IGNORE INTO tcp_mini_state(agent_id,evidence_start) SELECT id,unixepoch()*1000 FROM agents`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// rewindQualityUserTargetsFixture returns a latest-schema database to schema 33 (before R07).
func rewindQualityUserTargetsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindV107Fixture(t, db)
	for _, q := range []string{`DELETE FROM schema_migrations WHERE version=34`, `DROP TABLE IF EXISTS quality_slot_settings`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range [][2]string{{"quality_config", "settings_revision"}, {"quality_config", "interval_ms"}, {"quality_config", "targets_granted"}, {"quality_config", "capability_snapshot"},
		{"quality_capability", "user_targets"}, {"quality_capability", "ipv6_address"}, {"quality_targets", "identity"}, {"quality_targets", "created_revision"}, {"quality_targets", "first_active_at"}} {
		dropColumnIfPresent(t, db, column[0], column[1])
	}
}

func dropPublicEgressControlFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	rewindQualityUserTargetsFixture(t, db)
	// Migration 33 added the premium notice column; rewind it when the table is still there.
	var hasPremiumNotice int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_google_status') WHERE name='youtube_premium_notice'`).Scan(&hasPremiumNotice); err != nil {
		t.Fatal(err)
	}
	if hasPremiumNotice > 0 {
		if _, err := db.Exec(`ALTER TABLE agent_google_status DROP COLUMN youtube_premium_notice`); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DELETE FROM schema_migrations WHERE version=33`, `DELETE FROM schema_migrations WHERE version=32`, `DROP TABLE IF EXISTS agent_enrollment_tokens`, `DELETE FROM schema_migrations WHERE version=31`, `DROP INDEX IF EXISTS idx_probe_jobs_one_pending_public_egress`, `DROP TABLE IF EXISTS agent_public_egress_control`, `DELETE FROM schema_migrations WHERE version=30`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
func dropTCPMiniFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropPublicEgressControlFixtureSchema(t, db)
	for _, q := range []string{`DROP TABLE IF EXISTS tcp_mini_high`, `DROP TABLE IF EXISTS tcp_mini_state`, `DELETE FROM schema_migrations WHERE version=29`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
