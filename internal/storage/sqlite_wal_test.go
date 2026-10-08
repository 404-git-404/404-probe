package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointWALReportsStatusAndFileSizes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-health.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE wal_health (id INTEGER PRIMARY KEY, payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO wal_health (payload) VALUES (randomblob(2048))`); err != nil {
			t.Fatal(err)
		}
	}

	health, err := store.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.DatabaseBytes <= 0 {
		t.Fatalf("database size = %d, want a non-empty file", health.DatabaseBytes)
	}
	wantRemaining := 0
	if health.LogFrames > health.CheckpointedFrames {
		wantRemaining = health.LogFrames - health.CheckpointedFrames
	}
	if health.RemainingFrames != wantRemaining {
		t.Fatalf("remaining frames = %d, want max(log-checkpointed, 0) = %d (log=%d checkpointed=%d)", health.RemainingFrames, wantRemaining, health.LogFrames, health.CheckpointedFrames)
	}
	for _, item := range []struct {
		path string
		got  int64
	}{
		{path: path, got: health.DatabaseBytes},
		{path: path + "-wal", got: health.WALBytes},
		{path: path + "-shm", got: health.SHMBytes},
	} {
		info, err := os.Stat(item.path)
		want := int64(0)
		if err == nil {
			want = info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %q: %v", item.path, err)
		}
		if item.got != want {
			t.Errorf("reported size for %q = %d, want %d", item.path, item.got, want)
		}
	}
}

func TestSQLiteFileSizeTreatsMissingSidecarAsZero(t *testing.T) {
	dir := t.TempDir()
	size, err := sqliteFileSize(filepath.Join(dir, "missing.db-wal"), "WAL sidecar", true)
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Fatalf("missing sidecar size = %d, want 0", size)
	}
	if _, err := sqliteFileSize(filepath.Join(dir, "missing.db"), "database", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database error = %v, want os.ErrNotExist", err)
	}
}

func TestSQLiteDatabasePathKeepsMemorySubstringInOrdinaryFilename(t *testing.T) {
	const path = `probe:memory:.db`
	if got := sqliteDatabasePath(path); got != filepath.Clean(path) {
		t.Fatalf("sqliteDatabasePath(%q) = %q, want ordinary file path", path, got)
	}
	for _, memoryPath := range []string{":memory:", "file::memory:?cache=shared", "file:shared?mode=memory&cache=shared"} {
		if got := sqliteDatabasePath(memoryPath); got != "" {
			t.Errorf("sqliteDatabasePath(%q) = %q, want empty memory-database path", memoryPath, got)
		}
	}
}
