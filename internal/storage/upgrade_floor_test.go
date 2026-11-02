package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeSchemaFloor(t *testing.T) {
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
			if version == 34 {
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
