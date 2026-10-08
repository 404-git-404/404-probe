package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	sqliteWALAutoCheckpointPages = 1000
	sqliteJournalSizeLimitBytes  = 64 << 20
)

// WALHealth is one sampled PASSIVE checkpoint tuple and the on-disk sizes of
// the database and its WAL sidecars. journal_size_limit is not a hard ceiling
// while a WAL is active; SQLite applies it after a checkpoint can complete.
type WALHealth struct {
	CheckpointBusy     int
	LogFrames          int
	CheckpointedFrames int
	RemainingFrames    int
	DatabaseBytes      int64
	WALBytes           int64
	SHMBytes           int64
}

// CheckpointWAL samples the SQLite checkpoint status using PASSIVE mode and
// records the database, WAL, and SHM file sizes. It never truncates or removes
// database files.
func (s *Store) CheckpointWAL(ctx context.Context) (WALHealth, error) {
	var health WALHealth
	checkpointErr := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(
		&health.CheckpointBusy,
		&health.LogFrames,
		&health.CheckpointedFrames,
	)
	if checkpointErr != nil {
		checkpointErr = fmt.Errorf("PASSIVE WAL checkpoint: %w", checkpointErr)
	}
	if health.LogFrames > health.CheckpointedFrames {
		health.RemainingFrames = health.LogFrames - health.CheckpointedFrames
	}
	if s.databasePath == "" {
		return health, checkpointErr
	}
	for _, item := range []struct {
		path          string
		kind          string
		dst           *int64
		missingIsZero bool
	}{
		{path: s.databasePath, kind: "database", dst: &health.DatabaseBytes},
		{path: s.databasePath + "-wal", kind: "WAL sidecar", dst: &health.WALBytes, missingIsZero: true},
		{path: s.databasePath + "-shm", kind: "SHM sidecar", dst: &health.SHMBytes, missingIsZero: true},
	} {
		size, err := sqliteFileSize(item.path, item.kind, item.missingIsZero)
		if err != nil {
			return health, errors.Join(checkpointErr, err)
		}
		*item.dst = size
	}
	return health, checkpointErr
}

func sqliteFileSize(path, fileKind string, missingIsZero bool) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if missingIsZero {
			return 0, nil
		}
		return 0, fmt.Errorf("stat SQLite %s: %w", fileKind, err)
	}
	if err != nil {
		return 0, fmt.Errorf("stat SQLite %s: %w", fileKind, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("SQLite %s is not a regular file", fileKind)
	}
	return info.Size(), nil
}

func sqliteDatabasePath(path string) string {
	if path == "" || sqliteIsMemoryDatabase(path) {
		return ""
	}
	raw := path
	if strings.HasPrefix(path, "file:") {
		parsed, err := url.Parse(path)
		if err != nil {
			return ""
		}
		raw = parsed.Path
		if parsed.Opaque != "" {
			raw, err = url.PathUnescape(parsed.Opaque)
			if err != nil {
				return ""
			}
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			raw = filepath.Join("//"+parsed.Host, raw)
		}
	} else if query := strings.IndexByte(raw, '?'); query >= 0 {
		raw = raw[:query]
	}
	if len(raw) >= 3 && raw[0] == '/' && raw[2] == ':' {
		raw = raw[1:]
	}
	if raw == "" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(raw))
}

func sqliteIsMemoryDatabase(path string) bool {
	if path == ":memory:" {
		return true
	}
	if !strings.HasPrefix(path, "file:") {
		return false
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return false
	}
	if parsed.Opaque == ":memory:" {
		return true
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	return err == nil && query.Get("mode") == "memory"
}
