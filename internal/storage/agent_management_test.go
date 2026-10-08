package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestAgentManagementCapabilitiesDefaultUnsupportedAndFollowAcceptedSession(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()

	if version, err := store.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	at := time.Now()
	legacy := validReport(agentID, 1, "session-1", "boot", 1, 10, 20)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, legacy, at); err != nil || !accepted {
		t.Fatalf("legacy report accepted=%t err=%v", accepted, err)
	}
	capabilities, err := store.GetAgentManagementCapabilities(ctx, agentID)
	if err != nil || capabilities.RemoteRemoval || capabilities.CountryCodeLookup {
		t.Fatalf("legacy Agent must fail closed: %+v err=%v", capabilities, err)
	}

	current := validReport(agentID, 1, "session-1", "boot", 2, 11, 21)
	current.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true, CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, current, at.Add(time.Second)); err != nil || !accepted {
		t.Fatalf("capable report accepted=%t err=%v", accepted, err)
	}
	capabilities, err = store.GetAgentManagementCapabilities(ctx, agentID)
	if err != nil || !capabilities.RemoteRemoval || !capabilities.CountryCodeLookup {
		t.Fatalf("current capability report was not stored: %+v err=%v", capabilities, err)
	}

	legacyAgain := validReport(agentID, 1, "session-1", "boot", 3, 12, 22)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, legacyAgain, at.Add(2*time.Second)); err != nil || !accepted {
		t.Fatalf("capability omission accepted=%t err=%v", accepted, err)
	}
	capabilities, err = store.GetAgentManagementCapabilities(ctx, agentID)
	if err != nil || capabilities.RemoteRemoval || capabilities.CountryCodeLookup {
		t.Fatalf("omitted capabilities must fail closed: %+v err=%v", capabilities, err)
	}

	nextSession := validReport(agentID, 2, "session-2", "boot", 1, 13, 23)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, nextSession, at.Add(3*time.Second)); err != nil || !accepted {
		t.Fatalf("new session report accepted=%t err=%v", accepted, err)
	}
	capabilities, err = store.GetAgentManagementCapabilities(ctx, agentID)
	if err != nil || capabilities.RemoteRemoval || capabilities.CountryCodeLookup {
		t.Fatalf("capabilities must not survive a new session without renegotiation: %+v err=%v", capabilities, err)
	}
}

func TestMigratesSchema16To22WithoutLosingAgents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema16.db")
	store, agentID, _ := testStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	dropQualityFixtureSchema(t, db)
	for _, statement := range []string{
		`ALTER TABLE agent_state DROP COLUMN network_counters_json`,
		`ALTER TABLE agent_state DROP COLUMN network_counters_version`,
		`DELETE FROM schema_migrations WHERE version=22`,
		`DROP INDEX idx_probe_jobs_scheduled_finished_retention`,
		`DROP INDEX idx_probe_jobs_scheduled_expired_retention`,
		`ALTER TABLE probe_jobs DROP COLUMN origin`,
		`DELETE FROM schema_migrations WHERE version=21`,
		`DROP TABLE agent_country_code_lookups`,
		`DELETE FROM schema_migrations WHERE version=20`,
		`DROP TABLE agent_removal_receipts`,
		`DROP TABLE agent_removal_operations`,
		`DELETE FROM schema_migrations WHERE version=19`,
		`DROP TABLE agent_management_capabilities`,
		`DELETE FROM schema_migrations WHERE version=18`,
		`DELETE FROM schema_migrations WHERE version=17`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare schema 16 fixture: %v", err)
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
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	if _, err := store.GetAgentSnapshot(ctx, agentID, time.Now(), time.Minute); err != nil {
		t.Fatalf("agent did not survive schema migration: %v", err)
	}
	capabilities, err := store.GetAgentManagementCapabilities(ctx, agentID)
	if err != nil || capabilities.RemoteRemoval || capabilities.CountryCodeLookup {
		t.Fatalf("unreported legacy capability must be unsupported: %+v err=%v", capabilities, err)
	}
}
