package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// m34Preserved are the tables migration 34 must not touch, with the columns that exist from every start.
var m34Preserved = []struct{ table, columns, order string }{
	{"quality_raw", "*", "agent_id,target_id,epoch,session_id,seq"},
	{"quality_minute", "*", "agent_id,target_id,bucket"},
	{"quality_series", "*", "id"},
	{"quality_buckets", "*", "series_id,resolution,bucket_start"},
	{"quality_gaps", "*", "rowid"},
	{"agent_google_status", "agent_id,youtube_status,youtube_region,sent_to_china,youtube_error,search_status,search_error,signin_status,signin_error,gemini_status,gemini_region,gemini_error,checked_at,unknown_streak,next_due_at", "agent_id"},
}

// m34Fingerprint hashes the full content of every preserved table.
func m34Fingerprint(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range m34Preserved {
		rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", p.columns, p.table, p.order))
		if err != nil {
			t.Fatalf("%s: %v", p.table, err)
		}
		columns, _ := rows.Columns()
		h := sha256.New()
		n := 0
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%v\n", values)
			n++
		}
		rows.Close()
		out[p.table] = fmt.Sprintf("%d:%s", n, hex.EncodeToString(h.Sum(nil)))
	}
	return out
}

func m34Rows(fingerprint map[string]string, table string) string {
	return strings.SplitN(fingerprint[table], ":", 2)[0]
}

// m34SeedData fills the tables the migration must leave alone with real content.
func m34SeedData(t *testing.T, s *Store, id string, premium bool) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	var target string
	if err := s.db.QueryRow(`SELECT id FROM quality_targets WHERE agent_id=? ORDER BY id LIMIT 1`, id).Scan(&target); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO quality_series(id,agent_id,target_id,semantics) VALUES(77,?,?,'staged_probe_v2')`, id, target)
	exec(`INSERT INTO quality_buckets(series_id,resolution,bucket_start,data) VALUES(77,300000,1800000000000,x'0102030405')`)
	exec(`INSERT INTO quality_buckets(series_id,resolution,bucket_start,data) VALUES(77,3600000,1800000000000,x'0a0b0c')`)
	exec(`INSERT INTO quality_gaps(agent_id,epoch,session_id,start_at,end_at,reason) VALUES(?,1,'s',3000,4000,'unknown_gap')`, id)
	exec(`INSERT INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized,last_high,last_low,revision) VALUES(?,1000,5000,1,3000,0,3)`, id)
	exec(`INSERT INTO tcp_mini_high(agent_id,minute,peak_ms,source) VALUES(?,2940000,4700,'raw')`, id)
	exec(`INSERT INTO agent_google_status(agent_id,youtube_status,youtube_region,sent_to_china,search_status,signin_status,gemini_status,checked_at,unknown_streak,next_due_at) VALUES(?,'cn','CN',1,'ok','reachable','available',123456,2,654321)`, id)
	if premium {
		exec(`UPDATE agent_google_status SET youtube_premium_notice=1 WHERE agent_id=?`, id)
	}
}

// R23's data and the measurement history survive the migration byte for byte, and survive a failed
// attempt: the failed run leaves the original content, the retry then keeps it (design 3.1, G3).
func TestMigration34KeepsR23DataAndHistory(t *testing.T) {
	for _, from32 := range []bool{false, true} {
		for _, stage := range []string{"none", "structure", "devices", "validation"} {
			t.Run(fmt.Sprintf("from32=%t/fail=%s", from32, stage), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "data.db")
				s := m34Open(t, path, from32)
				spec := m34Specs()[0]
				spec.history = true
				id := m34Seed(t, s, spec, 1)
				m34SeedData(t, s, id, !from32)
				before := m34Fingerprint(t, s.db)
				for _, table := range []string{"quality_raw", "quality_series", "quality_buckets", "agent_google_status"} {
					if m34Rows(before, table) == "0" {
						t.Fatalf("fixture has no rows in %s", table)
					}
				}
				var premiumBefore sql.NullInt64
				if !from32 {
					if err := s.db.QueryRow(`SELECT youtube_premium_notice FROM agent_google_status WHERE agent_id=?`, id).Scan(&premiumBefore); err != nil || premiumBefore.Int64 != 1 {
						t.Fatalf("R23 value: %v %v", premiumBefore, err)
					}
				}
				s.Close()
				migration34Skip = false
				if stage != "none" {
					injected := errors.New("injected")
					migration34FailAt = func(at string) error {
						if at == stage {
							return injected
						}
						return nil
					}
					t.Cleanup(func() { migration34FailAt = nil })
					if _, err := Open(context.Background(), path); !errors.Is(err, injected) {
						t.Fatalf("Open: %v", err)
					}
					db, err := sql.Open("sqlite", path)
					if err != nil {
						t.Fatal(err)
					}
					got := m34Fingerprint(t, db)
					var premium sql.NullInt64
					if !from32 {
						if err := db.QueryRow(`SELECT youtube_premium_notice FROM agent_google_status WHERE agent_id=?`, id).Scan(&premium); err != nil || premium.Int64 != 1 {
							t.Fatalf("R23 value after the failed attempt: %v %v", premium, err)
						}
					}
					db.Close()
					for table, want := range before {
						if got[table] != want {
							t.Fatalf("%s changed by the failed attempt", table)
						}
					}
					migration34FailAt = nil
				}
				s, err := Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				after := m34Fingerprint(t, s.db)
				for table, want := range before {
					if after[table] != want {
						t.Fatalf("%s changed by the migration", table)
					}
				}
				if !from32 {
					var premium sql.NullInt64
					if err := s.db.QueryRow(`SELECT youtube_premium_notice FROM agent_google_status WHERE agent_id=?`, id).Scan(&premium); err != nil || premium.Int64 != 1 {
						t.Fatalf("R23 value after the migration: %v %v", premium, err)
					}
				}
			})
		}
	}
}

// A binary that supports schema 32 (v1.0.4) or 34 (v1.0.6) refuses the current database, so a rollback
// never runs an old binary on a newer schema: the installer must restore the matching backup. The check is
// the one in storage.go of those tags; the test runs the same comparison with their limits and, when a tag
// is available, confirms the tag really has that limit.
func TestMigration34DatabaseIsRefusedBySchema32Binary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refuse.db")
	s, _, _ := testStore(t, path)
	version, err := s.SchemaVersion(context.Background())
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("version %d %v", version, err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, older := range []int{32, 34} {
		if err := checkSchemaVersionAtMost(context.Background(), db, older); !errors.Is(err, ErrUnsupportedSchemaVersion) {
			t.Fatalf("a schema %d binary would open a schema %d database: %v", older, currentSchemaVersion, err)
		}
	}
	if err := checkSchemaVersionAtMost(context.Background(), db, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []struct {
		name  string
		limit int
	}{{"v1.0.4", 32}, {"v1.0.6", 34}} {
		out, err := exec.Command("git", "show", tag.name+":internal/storage/storage.go").Output()
		if err != nil {
			t.Logf("the %s tag is not available here: %v", tag.name, err)
			continue
		}
		text := string(out)
		if !strings.Contains(text, fmt.Sprintf("const currentSchemaVersion = %d", tag.limit)) || !strings.Contains(text, "if version > currentSchemaVersion {\n\t\treturn unsupportedSchemaVersionError(version") {
			t.Fatalf("%s no longer has the schema limit this test assumes", tag.name)
		}
	}
}
