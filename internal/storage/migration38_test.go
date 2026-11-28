package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMigration38RollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"renewal_url", "regions_table", "regions_seed", "credential_table", "schema_version"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := testStore(t, filepath.Join(t.TempDir(), "schema37.db"))
			defer s.Close()
			rewindWebSettingsFixture(t, s.db)
			before := m35Fingerprint(t, s.db)
			injected := errors.New("injected migration 38 failure")
			migration38FailAt = func(stage string) error {
				if stage == phase {
					return injected
				}
				return nil
			}
			t.Cleanup(func() { migration38FailAt = nil })
			if err := s.migrate(ctx); !errors.Is(err, injected) {
				t.Fatalf("migrate: %v", err)
			}
			if version, err := s.SchemaVersion(ctx); err != nil || version != 37 {
				t.Fatalf("schema after rollback=%d, err=%v", version, err)
			}
			if !reflect.DeepEqual(before, m35Fingerprint(t, s.db)) {
				t.Fatal("failed migration changed schema or rows")
			}
			migration38FailAt = nil
			if err := s.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			assertMigration38Defaults(t, s)
		})
	}
}

func TestMigration38PreservesLegacyRowsAndDoesNotReseed(t *testing.T) {
	ctx := context.Background()
	s, id, _ := testStore(t, filepath.Join(t.TempDir(), "legacy.db"))
	defer s.Close()
	assertMigration38Defaults(t, s)
	group := organizationWrite(t, s, OrganizationChange{Kind: "create_group", Name: "Archive"})
	organizationWrite(t, s, OrganizationChange{Kind: "assign", AgentID: id, GroupID: &group.ID, NewTags: []string{"Legacy"}})
	rewindWebSettingsFixture(t, s.db)
	if _, err := s.db.Exec(`INSERT INTO agent_plans(agent_id,renewal_price,renewal_date,updated_at) VALUES(?,'12.34','2027-02-03',101)`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetYouTubeManualRegion(ctx, id, "JP", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	var columns string
	if err := s.db.QueryRow(`SELECT group_concat(name, ',') FROM pragma_table_info('agent_plans')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	planRow := func() string {
		t.Helper()
		var row string
		if err := s.db.QueryRow(`SELECT json_array(`+columns+`) FROM agent_plans WHERE agent_id=?`, id).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	skip := []string{"schema_migrations", "agent_plans", "web_youtube_ad_free_regions", "web_admin_credential"}
	legacy, legacyPlan := m35Fingerprint(t, s.db, skip...), planRow()
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertMigration38Defaults(t, s)
	rewindAgentControlsFixture(t, s.db)
	if planRow() != legacyPlan || !reflect.DeepEqual(legacy, m35Fingerprint(t, s.db, skip...)) {
		t.Fatal("migration changed legacy rows")
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE agent_plans SET renewal_url=NULL`,
		`UPDATE agent_plans SET renewal_url='https://example.invalid/renew'`,
		`DELETE FROM web_youtube_ad_free_regions`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE agent_plans SET renewal_url=?`, strings.Repeat("a", 2049)); err == nil {
		t.Fatal("accepted an oversized renewal URL")
	}
	before := m35Fingerprint(t, s.db)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := migrateWebSettings38Tx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, m35Fingerprint(t, s.db)) {
		t.Fatal("repeating migration changed user data")
	}
}

func assertMigration38Defaults(t *testing.T, s *Store) {
	t.Helper()
	if version, err := s.SchemaVersion(context.Background()); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema=%d, err=%v", version, err)
	}
	var regions string
	const want = "AD AL CI CN ET KH LA MD MG MM MN MO MV RU SM"
	if err := s.db.QueryRow(`SELECT group_concat(region,' ') FROM (SELECT region FROM web_youtube_ad_free_regions ORDER BY region)`).Scan(&regions); err != nil || regions != want {
		t.Fatalf("default regions=%q, err=%v", regions, err)
	}
	var credentials int
	if err := s.db.QueryRow(`SELECT count(*) FROM web_admin_credential`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("credential rows=%d, err=%v", credentials, err)
	}
}
