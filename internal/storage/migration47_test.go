package storage

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestMigration47BackfillsColorsAndPreservesTagMembership(t *testing.T) {
	s := organizationStore(t)
	ctx := t.Context()
	rewindSystemCheckIgnoresFixture(t, s.db)
	if version, err := s.SchemaVersion(ctx); err != nil || version != 46 {
		t.Fatal(version, err)
	}
	id := strings.Repeat("a", 32)
	organizationAgent(t, s, id)
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "café", "标签"}
	colors := []string{"indigo", "purple", "cyan", "teal", "blue", "sky", "pink", "gray", "sky", "pink"}
	want := make(map[string]string)
	for i, name := range names {
		tag := strings.Repeat(string(rune('0'+i)), 32)
		if _, err := s.db.Exec(`INSERT INTO tags(id,name,name_key,revision) VALUES(?,?,?,1)`, tag, name, name); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO agent_tags VALUES(?,?)`, id, tag); err != nil {
			t.Fatal(err)
		}
		want[tag] = colors[i]
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := organizationRead(t, s)
	for _, tag := range catalog.Tags {
		if tag.Color != want[tag.ID] {
			t.Errorf("tag %+v has color %q, want %q", tag, tag.Color, want[tag.ID])
		}
	}
	if len(catalog.Tags) != len(want) || len(catalog.Assignments[id].TagIDs) != len(want) {
		t.Fatal(catalog)
	}
	if version, err := s.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatal(version, err)
	}
	for _, color := range []any{nil, "", "red", "amber", "#fff"} {
		if _, err := s.db.Exec(`UPDATE tags SET color=?`, color); err == nil {
			t.Fatalf("database accepted color %v", color)
		}
	}
	first := m35Fingerprint(t, s.db)
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, m35Fingerprint(t, s.db)) {
		t.Fatal("reapplying migrations changed data or schema")
	}
}

func TestMigration47RollsBackFromSchema46(t *testing.T) {
	for _, tc := range []struct{ stage, event string }{
		{"backfill", "BEFORE UPDATE ON tags"},
		{"version", "BEFORE INSERT ON schema_migrations WHEN NEW.version=47"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			s := organizationStore(t)
			ctx := t.Context()
			rewindSystemCheckIgnoresFixture(t, s.db)
			if _, err := s.db.Exec(`INSERT INTO tags VALUES(?, 'edge', 'edge', 1)`, strings.Repeat("a", 32)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`CREATE TRIGGER stop_migration ` + tc.event + ` BEGIN SELECT RAISE(ABORT, 'migration stopped'); END`); err != nil {
				t.Fatal(err)
			}
			before := m35Fingerprint(t, s.db)
			if err := s.migrate(ctx); err == nil || !strings.Contains(err.Error(), "migration stopped") {
				t.Fatalf("expected injected failure, got %v", err)
			}
			if version, err := s.SchemaVersion(ctx); err != nil || version != 46 {
				t.Fatal(version, err)
			}
			if !reflect.DeepEqual(before, m35Fingerprint(t, s.db)) {
				t.Fatal("failed migration changed data or schema")
			}
			if _, err := s.db.Exec(`DROP TRIGGER stop_migration`); err != nil {
				t.Fatal(err)
			}
			if err := s.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if catalog := organizationRead(t, s); len(catalog.Tags) != 1 || catalog.Tags[0].Color != "indigo" {
				t.Fatal(catalog)
			}
		})
	}
}

func TestSystemCheckIgnoresCascadeOnAgentDelete(t *testing.T) {
	s := organizationStore(t)
	id, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, id := range []string{id, other} {
		organizationAgent(t, s, id)
		changed, err := s.ChangeSystemCheckIgnore(t.Context(), id, protocol.SystemCheckKey{Kind: "time", Item: "time"}, true, time.Now())
		if err != nil || !changed {
			t.Fatal(changed, err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, `SELECT count(*) FROM agent_system_check_ignores`); got != 1 {
		t.Fatal(got)
	}
	if got := countRows(t, s, `SELECT count(*) FROM agent_system_check_ignores WHERE agent_id=?`, other); got != 1 {
		t.Fatal(got)
	}
}
