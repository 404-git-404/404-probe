package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/storage"
)

const mebibyte = int64(1 << 20)

func TestDatabaseTimeoutForSize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int64
		want  time.Duration
	}{
		{"empty", 0, 10 * time.Minute},
		{"negative size is treated as empty", -5, 10 * time.Minute},
		{"small database", 80 * mebibyte, 10 * time.Minute},
		{"500 MB is still under the floor", 500 * mebibyte, 10 * time.Minute},
		{"exactly 600 MB is the floor", 600 * mebibyte, 10 * time.Minute},
		{"601 MB", 601 * mebibyte, 601 * time.Second},
		{"1 GiB is 1024 s", 1024 * mebibyte, 1024 * time.Second},
		{"a partial MiB counts as a whole one", 700*mebibyte + 1, 701 * time.Second},
		{"1 GiB plus 1 byte", 1024*mebibyte + 1, 1025 * time.Second},
		{"just under the ceiling", 7199 * mebibyte, 7199 * time.Second},
		{"exactly the ceiling", 7200 * mebibyte, 2 * time.Hour},
		{"a large database is capped at 2 hours", 20 * 1024 * mebibyte, 2 * time.Hour},
		{"an enormous size does not overflow", 1<<62 + 12345, 2 * time.Hour},
	} {
		if got := databaseTimeoutForSize(tc.bytes); got != tc.want {
			t.Errorf("%s: %d bytes -> %s, want %s", tc.name, tc.bytes, got, tc.want)
		}
	}
}

func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if size > 0 {
		if _, err := file.WriteAt([]byte{0}, size-1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDatabaseTimeoutCountsTheWALAndHonoursTheFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "404-probe.db")

	// No file at all: the floor. The command itself reports the missing file later.
	if got, err := databaseTimeout(path, 0); err != nil || got != 10*time.Minute {
		t.Fatalf("missing database: %s, %v", got, err)
	}
	writeSized(t, path, 3*mebibyte)
	if got, _ := databaseTimeout(path, 0); got != 10*time.Minute {
		t.Fatalf("small database: %s", got)
	}
	// The WAL is part of what the check reads; -shm is not.
	writeSized(t, path+"-wal", 2*mebibyte)
	writeSized(t, path+"-shm", 500*mebibyte)
	size, err := databaseFilesSize(path)
	if err != nil || size != 5*mebibyte {
		t.Fatalf("database plus WAL: %d, %v", size, err)
	}

	// --timeout wins over the computed value, up or down, and is not capped.
	for _, explicit := range []time.Duration{time.Second, 45 * time.Second, 3 * time.Hour} {
		if got, err := databaseTimeout(path, explicit); err != nil || got != explicit {
			t.Fatalf("--timeout %s gave %s, %v", explicit, got, err)
		}
	}
	if _, err := databaseTimeout(path, -time.Second); err == nil {
		t.Fatal("a negative --timeout must be rejected")
	}
}

func TestDatabaseCommandUsesTheTimeoutFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "404-probe.db")
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"verify", "migrate-copy", "migrate-protected"} {
		// Without the flag the size based default is far more than a small database needs.
		if err := run([]string{"database", mode, "--db", path}); err != nil {
			t.Fatalf("database %s with the default timeout: %v", mode, err)
		}
		if err := run([]string{"database", mode, "--db", path, "--timeout", "10m"}); err != nil {
			t.Fatalf("database %s with --timeout 10m: %v", mode, err)
		}
		// A limit that has already passed makes the command fail with the deadline, which shows the flag is applied.
		err := run([]string{"database", mode, "--db", path, "--timeout", "1ns"})
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("database %s with --timeout 1ns: %v", mode, err)
		}
		if err := run([]string{"database", mode, "--db", path, "--timeout", "-1s"}); err == nil {
			t.Fatalf("database %s accepted a negative --timeout", mode)
		}
		if err := run([]string{"database", mode, "--db", path, "--timeout", "soon"}); err == nil {
			t.Fatalf("database %s accepted a --timeout that is not a duration", mode)
		}
	}
}
