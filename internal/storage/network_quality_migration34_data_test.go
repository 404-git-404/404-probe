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
	"time"
)

// m34Preserved are the tables migration 34 must not touch, with the columns that exist from every start.
var m34Preserved = []struct{ table, columns, order string }{
	{"quality_raw", "*", "agent_id,target_id,epoch,session_id,seq"},
	{"quality_minute", "*", "agent_id,target_id,bucket"},
	{"quality_series", "*", "id"},
	{"quality_buckets", "*", "series_id,resolution,bucket_start"},
	{"quality_gaps", "*", "rowid"},
	{"tcp_mini_state", "*", "agent_id"},
	{"tcp_mini_high", "*", "agent_id,minute"},
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
				for _, table := range []string{"quality_raw", "quality_series", "quality_buckets", "tcp_mini_high", "agent_google_status"} {
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

// A binary that supports schema 32 (v1.0.4) refuses a schema 34 database. Its check is the one in
// storage.go of the v1.0.4 tag; the test runs the same comparison with that limit and, when the tag
// is available, confirms the tag really has that limit and that comparison.
func TestMigration34DatabaseIsRefusedBySchema32Binary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refuse.db")
	s, _, _ := testStore(t, path)
	version, err := s.SchemaVersion(context.Background())
	if err != nil || version != 34 {
		t.Fatalf("version %d %v", version, err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := checkSchemaVersionAtMost(context.Background(), db, 32); !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("a schema 32 binary would open a schema 34 database: %v", err)
	}
	if err := checkSchemaVersionAtMost(context.Background(), db, 34); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "show", "v1.0.4:internal/storage/storage.go").Output()
	if err != nil {
		t.Skip("the v1.0.4 tag is not available here: ", err)
	}
	text := string(out)
	if !strings.Contains(text, "const currentSchemaVersion = 32") || !strings.Contains(text, "if version > currentSchemaVersion {\n\t\treturn unsupportedSchemaVersionError(version)") {
		t.Fatal("v1.0.4 no longer has the schema limit this test assumes")
	}
}

// Upgrading a device that had a TCP Mini alert: the alert stays what it was, only current_tcp turns
// false because migration 34 closes the lanes. The 72-hour behaviour is unchanged as well.
func TestMigration34TCPMiniAlertContractAcrossMigration(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		lastHigh time.Time
		peak     float64
		lastLow  time.Time
	}{
		{"fresh alert", now.Add(-26 * time.Hour), 4700, time.Time{}},
		{"evidence older than 72 hours, no normal minute", now.Add(-80 * time.Hour), 4700, time.Time{}},
		{"cleared by a later normal minute", now.Add(-50 * time.Hour), 3000, now.Add(-2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "alert.db")
			s := m34Open(t, path, false)
			_, intervals, targets := m34Catalog([]string{"telecom", "unicom", "mobile"}, 4)
			id := m34Seed(t, s, m34Spec{name: "alerting", revision: 4, enabled: true, targets: targets, intervals: intervals,
				slots: []m34Slot{{"telecom", "ipv4", "tcp", "telecomipv4", 4}}}, 1)
			low := int64(0)
			if !tc.lastLow.IsZero() {
				low = tc.lastLow.UnixMilli()
			}
			evidenceStart := now.Add(-120 * time.Hour).UnixMilli()
			for _, q := range []string{
				fmt.Sprintf(`INSERT INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized,last_high,last_low,revision) VALUES('%s',%d,%d,1,%d,%d,4)`, id, evidenceStart, now.Add(-time.Hour).UnixMilli(), tc.lastHigh.UnixMilli(), low),
				fmt.Sprintf(`INSERT INTO tcp_mini_high(agent_id,minute,peak_ms,source) VALUES('%s',%d,%v,'raw')`, id, tc.lastHigh.Truncate(time.Minute).UnixMilli(), tc.peak),
			} {
				if _, err := s.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			read := func(s *Store) TCPMiniStatus {
				status, err := s.TCPMini(context.Background(), id, "UTC", now)
				if err != nil {
					t.Fatal(err)
				}
				return status
			}
			before := read(s)
			if !before.CurrentTCP {
				t.Fatal("fixture: the v1.0.4 device has an open IPv4 TCP interval")
			}
			s.Close()
			migration34Skip = false
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			after := read(s)
			if after.CurrentTCP {
				t.Fatal("current_tcp must be false after the migration closed the lanes")
			}
			if after.State != before.State || after.Coverage != before.Coverage || after.Level != before.Level || after.Peak != before.Peak ||
				after.SourceDay != before.SourceDay || after.Today != before.Today || after.LastHigh != before.LastHigh || after.LastLow != before.LastLow {
				t.Fatalf("the alert contract moved:\nbefore %+v\nafter  %+v", before, after)
			}
			t.Logf("%s: state=%s coverage=%s level=%v source_day=%s", tc.name, after.State, after.Coverage, after.Level, after.SourceDay)
		})
	}
}
