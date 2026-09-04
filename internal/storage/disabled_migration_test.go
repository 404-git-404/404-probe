package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigratesV4DatabaseWithAgentsEnabledByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	store, agentID, token := testStore(t, path)
	ctx := context.Background()
	if _, accepted, reason, err := store.ProcessReport(ctx, agentID,
		validReport(agentID, 1, "session", "boot", 1, 10, 20), time.Unix(200, 0)); err != nil || !accepted {
		store.Close()
		t.Fatalf("prepare report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP INDEX idx_probe_jobs_one_active_selector_lease`,
		`DROP INDEX idx_probe_jobs_one_active_probe_lease`,
		`DELETE FROM schema_migrations WHERE version=8`,
		`DROP TABLE agent_upgrade_operations`,
		`ALTER TABLE agent_state DROP COLUMN agent_upgrade_capable`,
		`ALTER TABLE agent_state DROP COLUMN agent_version`,
		`DELETE FROM schema_migrations WHERE version=7`,
		`DROP TABLE agent_outbound_snapshots`,
		`DELETE FROM schema_migrations WHERE version=6`,
		`DROP INDEX idx_agents_disabled_created_id`,
		`ALTER TABLE agents DROP COLUMN disabled_at`,
		`DELETE FROM schema_migrations WHERE version=5`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare V4 fixture: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].ID != agentID || agents[0].DisabledAt != nil || agents[0].Revoked {
		t.Fatalf("migrated agents=%+v err=%v", agents, err)
	}
	if authenticatedID, err := store.Authenticate(ctx, token); err != nil || authenticatedID != agentID {
		t.Fatalf("migrated credential id=%q err=%v", authenticatedID, err)
	}
	snapshot, err := store.GetAgentSnapshot(ctx, agentID, time.Unix(201, 0), 30*time.Second)
	if err != nil || snapshot.State == nil || snapshot.State.Sequence != 1 {
		t.Fatalf("migrated snapshot=%+v err=%v", snapshot, err)
	}
}
