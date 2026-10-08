package storage

import (
	"database/sql"
	"testing"
)

// Existing old-schema fixtures start from Open(latest). Remove migrations24/23
// before their original explicit downgrade, so they genuinely remain old DBs.
func dropQualityFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
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
