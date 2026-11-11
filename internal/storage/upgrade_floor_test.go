package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeSchemaFloor(t *testing.T) {
	if !strings.Contains(ErrSchemaTooOld.Error(), "if that installer also reports that your version is too old, follow its instructions to upgrade step by step") {
		t.Fatal("schema floor rejection is missing step-by-step upgrade guidance")
	}
	ctx := context.Background()
	for _, version := range []int{34, 35, 36, 37} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "database.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if version == 35 {
				for _, table := range organizationTables {
					if _, err := store.db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version>?`, version); err != nil {
				t.Fatal(err)
			}
			if version == 37 {
				if _, err := store.db.Exec(`INSERT INTO schema_migrations VALUES(37,1)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wantErr := error(nil)
			if version == 34 || version == 35 {
				wantErr = ErrSchemaTooOld
			}
			if version == 37 {
				wantErr = ErrUnsupportedSchemaVersion
			}
			verification, err := VerifyReadOnly(ctx, path)
			if !errors.Is(err, wantErr) {
				t.Fatalf("verify error=%v, want %v", err, wantErr)
			}
			if wantErr == nil && verification.SchemaVersion != version {
				t.Fatalf("verify=%+v", verification)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only verification changed database", err)
			}
			store, err = Open(ctx, path)
			if !errors.Is(err, wantErr) {
				if store != nil {
					store.Close()
				}
				t.Fatalf("open error=%v, want %v", err, wantErr)
			}
			if store != nil {
				got, err := store.SchemaVersion(ctx)
				if err != nil || got != 36 {
					t.Fatalf("schema=%d err=%v", got, err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			after, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if (wantErr != nil || version == 36) && !bytes.Equal(before, after) {
				t.Fatal("non-migrating open changed database")
			}
		})
	}
}

func TestUpgradeSchemaFloorDistinguishesFreshFromUnversionedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unversioned.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE existing_data(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path)
	if store != nil {
		store.Close()
		t.Fatal("opened unversioned data")
	}
	if !errors.Is(err, ErrSchemaTooOld) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unversioned database changed", err)
	}
}

func TestUpgradeSchemaFloorBoundaryObjects(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		fresh     bool
	}{
		{"empty file", "", true},
		{"only user_version", "PRAGMA user_version=123", true},
		{"empty migration table", "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)", false},
		{"business table", "CREATE TABLE business_data(value TEXT)", false},
		{"only view", "CREATE VIEW business_view AS SELECT 1", false},
		{"sqlite prefix table", "CREATE TABLE sqlitexdata(value TEXT)", false},
		{"sqlite prefix view", "CREATE VIEW sqlitexview AS SELECT 1", false},
		{"text version", "CREATE TABLE schema_migrations(version TEXT); INSERT INTO schema_migrations VALUES('abc')", false},
		{"negative version", "CREATE TABLE schema_migrations(version INTEGER); INSERT INTO schema_migrations VALUES(-5)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Include a URI-sensitive character to verify that ro and rw inspect
			// the same file, including callers using an explicit file URI.
			path := filepath.Join(t.TempDir(), "boundary #data.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err := Open(context.Background(), path)
			if tc.fresh {
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if got, err := store.SchemaVersion(context.Background()); err != nil || got != 36 {
					t.Fatalf("schema=%d err=%v", got, err)
				}
				return
			}
			if store != nil {
				store.Close()
				t.Fatal("opened nonempty or invalid database")
			}
			if err == nil {
				t.Fatal("missing rejection")
			}
			if _, err := VerifyReadOnly(context.Background(), path); err == nil {
				t.Fatal("readonly verification accepted invalid schema")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected database changed", err)
			}
		})
	}
}

func TestUpgradeSchemaFloorRefusesUncheckpointedWALWithoutChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old-wal.db")
	child := exec.Command(os.Args[0], "-test.run=^TestUpgradeSchemaFloorWALFixtureProcess$", "-test.count=1")
	child.Env = append(os.Environ(), "V11_UPGRADE_WAL_FIXTURE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("WAL fixture: %v\n%s", err, output)
	}
	before := map[string][]byte{}
	for _, name := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(name)
		if err != nil || len(data) == 0 {
			t.Fatal("missing WAL fixture", name, err)
		}
		before[name] = data
	}
	for _, candidate := range []string{path, "file:" + filepath.ToSlash(path)} {
		store, err := Open(ctx, candidate)
		if store != nil {
			store.Close()
			t.Fatal("old WAL database opened")
		}
		if !errors.Is(err, ErrSchemaTooOld) {
			t.Fatal(err)
		}
		if _, err := VerifyReadOnly(ctx, path); !errors.Is(err, ErrSchemaTooOld) {
			t.Fatal(err)
		}
		for name, data := range before {
			after, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("rejection changed database/WAL", name, err)
			}
		}
	}
}

// Exit without closing SQLite so the parent sees an orphan, uncheckpointed WAL.
func TestUpgradeSchemaFloorWALFixtureProcess(t *testing.T) {
	path := os.Getenv("V11_UPGRADE_WAL_FIXTURE")
	if path == "" {
		return
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)",
		"INSERT INTO schema_migrations VALUES(34)",
		"CREATE TABLE old_data(value TEXT)", "INSERT INTO old_data VALUES('uncheckpointed')",
	} {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}
