package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

var v4QueryIndexDefinitions = map[string]string{
	"idx_agents_created_id":                  "CREATE INDEX idx_agents_created_id ON agents(created_at DESC,id DESC)",
	"idx_agents_revoked_created_id":          "CREATE INDEX idx_agents_revoked_created_id ON agents(revoked,created_at DESC,id DESC)",
	"idx_probe_schedules_created_id":         "CREATE INDEX idx_probe_schedules_created_id ON probe_schedules(created_at DESC,id DESC)",
	"idx_probe_schedules_agent_created_id":   "CREATE INDEX idx_probe_schedules_agent_created_id ON probe_schedules(agent_id,created_at DESC,id DESC)",
	"idx_probe_schedules_enabled_created_id": "CREATE INDEX idx_probe_schedules_enabled_created_id ON probe_schedules(enabled,created_at DESC,id DESC)",
	"idx_probe_schedules_type_created_id":    "CREATE INDEX idx_probe_schedules_type_created_id ON probe_schedules(probe_type,created_at DESC,id DESC)",
	"idx_probe_jobs_created_id":              "CREATE INDEX idx_probe_jobs_created_id ON probe_jobs(created_at DESC,id DESC)",
	"idx_probe_jobs_agent_created_id":        "CREATE INDEX idx_probe_jobs_agent_created_id ON probe_jobs(agent_id,created_at DESC,id DESC)",
	"idx_probe_jobs_schedule_created_id":     "CREATE INDEX idx_probe_jobs_schedule_created_id ON probe_jobs(schedule_id,created_at DESC,id DESC) WHERE schedule_id IS NOT NULL",
	"idx_probe_jobs_type_created_id":         "CREATE INDEX idx_probe_jobs_type_created_id ON probe_jobs(probe_type,created_at DESC,id DESC)",
	"idx_probe_jobs_status_created_id":       "CREATE INDEX idx_probe_jobs_status_created_id ON probe_jobs(status,created_at DESC,id DESC)",
}

func TestMigratesV3ToV4QueryIndexesAndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	agentID := prepareV3QueryFixture(t, path)

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := store.SchemaVersion(context.Background()); err != nil || version != currentSchemaVersion {
		store.Close()
		t.Fatalf("version=%d err=%v", version, err)
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil || len(agents) != 1 || agents[0].ID != agentID {
		store.Close()
		t.Fatalf("agents=%v err=%v", agents, err)
	}
	if schedule, err := store.GetProbeSchedule(context.Background(), "schedule-v3"); err != nil || schedule.AgentID != agentID {
		store.Close()
		t.Fatalf("schedule=%+v err=%v", schedule, err)
	}
	job, result, err := store.GetProbeJobSnapshot(context.Background(), "job-v3", time.Unix(1_001, 0))
	if err != nil || job.AgentID != agentID || job.Status != JobStatusFinished || result == nil || !result.Result.Success {
		store.Close()
		t.Fatalf("job=%+v result=%+v err=%v", job, result, err)
	}
	assertV4QueryIndexes(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertV4QueryIndexes(t, store.db)
	var migrationRows int
	if err := store.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=4`).Scan(&migrationRows); err != nil || migrationRows != 1 {
		t.Fatalf("migration 4 rows=%d err=%v", migrationRows, err)
	}
}

func TestMigrationV4RollsBackAllIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3-conflict.db")
	prepareV3QueryFixture(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_probe_jobs_status_created_id ON agents(created_at)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if store != nil {
		store.Close()
		t.Fatal("migration with conflicting index unexpectedly succeeded")
	}
	if err == nil || !strings.Contains(err.Error(), "migration 4") {
		t.Fatalf("migration error=%v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 3 {
		db.Close()
		t.Fatalf("version after rollback=%d err=%v", version, err)
	}
	var firstIndex int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_agents_created_id'`).Scan(&firstIndex); err != nil || firstIndex != 0 {
		db.Close()
		t.Fatalf("partial V4 index count=%d err=%v", firstIndex, err)
	}
	if _, err := db.Exec(`DROP INDEX idx_probe_jobs_status_created_id`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertV4QueryIndexes(t, store.db)
}

func TestV4CollectionQueryPlansUseKeysetIndexes(t *testing.T) {
	store, _, _ := testStore(t, ":memory:")
	defer store.Close()
	tests := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{"agents", `SELECT id FROM agents ORDER BY created_at DESC,id DESC LIMIT ?`, []any{51}, "idx_agents_created_id"},
		{"agents status", `SELECT id FROM agents WHERE revoked=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{0, 51}, "idx_agents_revoked_created_id"},
		{"schedules", `SELECT id FROM probe_schedules ORDER BY created_at DESC,id DESC LIMIT ?`, []any{51}, "idx_probe_schedules_created_id"},
		{"schedules agent", `SELECT id FROM probe_schedules WHERE agent_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"agent", 51}, "idx_probe_schedules_agent_created_id"},
		{"schedules enabled", `SELECT id FROM probe_schedules WHERE enabled=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{1, 51}, "idx_probe_schedules_enabled_created_id"},
		{"schedules type", `SELECT id FROM probe_schedules WHERE probe_type=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"http", 51}, "idx_probe_schedules_type_created_id"},
		{"jobs", `SELECT id FROM probe_jobs ORDER BY created_at DESC,id DESC LIMIT ?`, []any{51}, "idx_probe_jobs_created_id"},
		{"jobs agent", `SELECT id FROM probe_jobs WHERE agent_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"agent", 51}, "idx_probe_jobs_agent_created_id"},
		{"jobs schedule", `SELECT id FROM probe_jobs WHERE schedule_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"schedule", 51}, "idx_probe_jobs_schedule_created_id"},
		{"jobs type", `SELECT id FROM probe_jobs WHERE probe_type=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"http", 51}, "idx_probe_jobs_type_created_id"},
		{"jobs status", `SELECT id FROM probe_jobs WHERE status=? ORDER BY created_at DESC,id DESC LIMIT ?`, []any{"finished", 51}, "idx_probe_jobs_status_created_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := explainQueryPlan(t, store.db, test.query, test.args...)
			if !strings.Contains(plan, test.index) {
				t.Fatalf("plan=%q does not use %s", plan, test.index)
			}
		})
	}
}

func prepareV3QueryFixture(t *testing.T, path string) string {
	t.Helper()
	store, agentID, _ := testStore(t, path)
	at := time.Unix(1_000, 0)
	if _, _, err := store.PutProbeSchedule(context.Background(), scheduleParams("schedule-v3", agentID, at)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.CreateOneShotJob(context.Background(), oneShot("job-v3", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	job, err := store.ClaimJob(context.Background(), agentID, claimRequest(1, "session-v3", protocol.ProbeTypeTCPConnect), at, time.Minute)
	if err != nil || job == nil {
		store.Close()
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if _, err := store.SubmitJobResult(context.Background(), agentID, job.JobID, resultFor(job, true), at.Add(time.Second)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for name := range v4QueryIndexDefinitions {
		if _, err := db.Exec(`DROP INDEX ` + name); err != nil {
			db.Close()
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	for _, name := range []string{"idx_probe_jobs_one_active_probe_lease", "idx_probe_jobs_one_active_selector_lease"} {
		if _, err := db.Exec(`DROP INDEX ` + name); err != nil {
			db.Close()
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`DROP INDEX idx_probe_jobs_one_pending_google_status`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, table := range []string{"agent_google_status", "agent_google_status_capabilities"} {
		if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	for _, table := range []string{"agent_security_batches", "agent_security_capabilities"} {
		if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_probe_jobs_one_active_lease ON probe_jobs(agent_id) WHERE status='leased'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_agents_disabled_created_id`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE agent_outbound_snapshots`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE agent_upgrade_operations`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE agent_state DROP COLUMN agent_upgrade_capable`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE agent_state DROP COLUMN agent_version`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE agents DROP COLUMN disabled_at`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version>=4`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return agentID
}

func assertV4QueryIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	for name, wantSQL := range v4QueryIndexDefinitions {
		var gotSQL string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&gotSQL); err != nil {
			t.Errorf("index %s: %v", name, err)
			continue
		}
		if gotSQL != wantSQL {
			t.Errorf("index %s SQL=%q want %q", name, gotSQL, wantSQL)
		}
	}
}

func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, fmt.Sprintf("%d/%d %s", id, parent, detail))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "; ")
}
