package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// asManualLines rewrites a fixture the way a v1.0.3 Server stored a hand-entered line: the choice and
// its targets say "manual" and carry the typed host and port.
func asManualLines(t *testing.T, s *Store, id string, slots ...string) {
	t.Helper()
	ctx := context.Background()
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT choices FROM quality_config WHERE agent_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var choices []protocol.QualityChoice
	if err := json.Unmarshal([]byte(raw), &choices); err != nil {
		t.Fatal(err)
	}
	manual := map[string]bool{}
	for _, slot := range slots {
		manual[slot] = true
	}
	for i := range choices {
		if manual[choices[i].Slot] {
			choices[i].Source, choices[i].Region, choices[i].Host, choices[i].Port = "manual", "", "127.0.0.1", 80
		}
	}
	encoded, _ := json.Marshal(choices)
	if _, err := s.db.ExecContext(ctx, `UPDATE quality_config SET choices=? WHERE agent_id=?`, string(encoded), id); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.payload FROM quality_targets t WHERE t.agent_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, payload string }
	var targets []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, r)
	}
	rows.Close()
	for _, r := range targets {
		var target protocol.QualityTarget
		if err := json.Unmarshal([]byte(r.payload), &target); err != nil {
			t.Fatal(err)
		}
		if manual[target.Slot] {
			target.Source = "manual"
			payload, _ := json.Marshal(target)
			if _, err := s.db.ExecContext(ctx, `UPDATE quality_targets SET payload=? WHERE id=?`, string(payload), r.id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func rerunMigration31(t *testing.T, s *Store, path string) *Store {
	t.Helper()
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version>=31`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	return reopened
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigration31TurnsManualLinesIntoUnsetAndKeepsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manual.db")
	s, id, cfg, now := qualityFixture(t, path)
	ctx := context.Background()
	target := qualityTarget(t, cfg, "telecom")
	qualitySave(t, s, id, now, qualitySample(target, cfg.Revision, 1, now.Add(-time.Minute), 12.5, "success"))
	rawBefore := countRows(t, s, `SELECT COUNT(*) FROM quality_raw WHERE agent_id=?`, id)
	targetsBefore := countRows(t, s, `SELECT COUNT(*) FROM quality_targets WHERE agent_id=?`, id)
	if rawBefore == 0 {
		t.Fatal("fixture has no history")
	}
	asManualLines(t, s, id, "telecom")
	var revisionBefore int64
	if err := s.db.QueryRow(`SELECT revision FROM quality_config WHERE agent_id=?`, id).Scan(&revisionBefore); err != nil {
		t.Fatal(err)
	}
	openBefore := countRows(t, s, `SELECT COUNT(*) FROM quality_intervals WHERE agent_id=? AND slot='telecom' AND end_at IS NULL`, id)
	otherBefore := countRows(t, s, `SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND slot<>'telecom' AND target_id IS NOT NULL`, id)
	if openBefore == 0 || otherBefore == 0 {
		t.Fatalf("fixture lanes: open=%d others=%d", openBefore, otherBefore)
	}

	s = rerunMigration31(t, s, path)

	// The line is "not set": catalog, no region, nothing typed in, and nothing is sent to the Agent.
	config, err := s.GetQualityConfig(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if config.Revision == strconv.FormatInt(revisionBefore, 10) {
		t.Fatalf("revision was not bumped: %s", config.Revision)
	}
	for _, t1 := range config.Targets {
		if t1.Slot == "telecom" && (t1.Source != "catalog" || t1.Region != "" || t1.Host != "" || t1.Port != 0 || t1.ID != "" || t1.Status == "active") {
			t.Fatalf("telecom target after migration: %+v", t1)
		}
		if t1.Slot != "telecom" && (t1.Host != "127.0.0.1" || t1.ID == "") {
			t.Fatalf("an untouched line changed: %+v", t1)
		}
	}
	var raw string
	if err := s.db.QueryRow(`SELECT choices FROM quality_config WHERE agent_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var choices []protocol.QualityChoice
	if err := json.Unmarshal([]byte(raw), &choices); err != nil {
		t.Fatal(err)
	}
	for _, c := range choices {
		if c.Source == "manual" || c.Slot == "telecom" && (c.Host != "" || c.Port != 0 || c.Region != "" || c.Protocol != "tcp") {
			t.Fatalf("choice after migration: %+v", c)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_intervals WHERE agent_id=? AND slot='telecom' AND end_at IS NULL`, id); n != 0 {
		t.Fatalf("%d measurement intervals of the manual line are still open", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND slot='telecom' AND target_id IS NOT NULL`, id); n != 0 {
		t.Fatalf("%d slots of the manual line still point at a target", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND slot<>'telecom' AND target_id IS NOT NULL`, id); n != otherBefore {
		t.Fatalf("other lines were touched: %d != %d", n, otherBefore)
	}
	// History stays; the target is retired, not deleted.
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_raw WHERE agent_id=?`, id); n != rawBefore {
		t.Fatalf("samples %d -> %d", rawBefore, n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_targets WHERE agent_id=?`, id); n != targetsBefore {
		t.Fatalf("targets %d -> %d", targetsBefore, n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_targets WHERE agent_id=? AND retired_at IS NOT NULL AND payload LIKE '%"manual"%'`, id); n == 0 {
		t.Fatal("the manual target was not retired")
	}

	// Running it again changes nothing: same revision, same rows.
	revision := config.Revision
	s = rerunMigration31(t, s, path)
	again, err := s.GetQualityConfig(ctx, id)
	if err != nil || again.Revision != revision {
		t.Fatalf("second run: revision %s -> %s err=%v", revision, again.Revision, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_raw WHERE agent_id=?`, id); n != rawBefore {
		t.Fatalf("second run changed samples: %d", n)
	}
	var manualLeft int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_targets t JOIN quality_slots s ON s.target_id=t.id WHERE t.payload LIKE '%"source":"manual"%'`).Scan(&manualLeft); err != nil || manualLeft != 0 {
		t.Fatalf("manual targets still scheduled: %d err=%v", manualLeft, err)
	}
	if version, err := s.SchemaVersion(ctx); err != nil || version != 31 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
}

// A database with no manual lines migrates without touching any quality row.
func TestMigration31LeavesCatalogOnlyDatabasesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, id, _, _ := qualityFixture(t, path)
	var revision int64
	if err := s.db.QueryRow(`SELECT revision FROM quality_config WHERE agent_id=?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	slotsBefore := countRows(t, s, `SELECT COUNT(*) FROM quality_slots WHERE target_id IS NOT NULL`)
	s = rerunMigration31(t, s, path)
	var after int64
	if err := s.db.QueryRow(`SELECT revision FROM quality_config WHERE agent_id=?`, id).Scan(&after); err != nil || after != revision {
		t.Fatalf("revision %d -> %d err=%v", revision, after, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM quality_slots WHERE target_id IS NOT NULL`); n != slotsBefore {
		t.Fatalf("slots %d -> %d", slotsBefore, n)
	}
}

// The migration is one transaction: a failure leaves the database as it was, which is what the
// installer's rehearsal on a copy and its recovery rely on.
func TestMigration31IsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atomic.db")
	s, id, _, _ := qualityFixture(t, path)
	asManualLines(t, s, id, "telecom", "unicom")
	var before string
	if err := s.db.QueryRow(`SELECT choices FROM quality_config WHERE agent_id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version>=31; CREATE TRIGGER block_manual_migration BEFORE UPDATE ON quality_targets BEGIN SELECT RAISE(ABORT,'blocked'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if bad, err := Open(context.Background(), path); err == nil {
		bad.Close()
		t.Fatal("migration 31 succeeded although its last step was blocked")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var after string
	if err := db.QueryRow(`SELECT choices FROM quality_config`).Scan(&after); err != nil || after != before {
		t.Fatalf("a failed migration left changes behind: %s err=%v", after, err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 30 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func TestQualityConfigUpdateRefusesManualSource(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	for _, source := range []string{"manual", "custom", ""} {
		_, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true,
			Choices: []protocol.QualityChoice{{Slot: "unicom", Source: source, Protocol: "tcp", Host: "203.0.113.9", Port: 443}}}, qualityTestResolver, now)
		if err != ErrQualityInvalid {
			t.Errorf("source %q: err=%v", source, err)
		}
	}
}
