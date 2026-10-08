package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

var sqliteRegressionScalarSequence atomic.Uint64

func registerBlockingSQLiteScalar(t *testing.T, queryContexts <-chan context.Context, entered chan<- struct{}) string {
	t.Helper()
	name := fmt.Sprintf("storage_regression_wait_%d", sqliteRegressionScalarSequence.Add(1))
	err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		queryCtx := <-queryContexts
		entered <- struct{}{}
		<-queryCtx.Done()
		return int64(42), nil
	})
	if err != nil {
		t.Fatalf("register scalar function %q: %v", name, err)
	}
	return name
}

func isSQLiteCancellation(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "interrupted (9)")
}

func TestSQLiteRuntimeVersionPinned(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "runtime-version.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version string
	if err := store.db.QueryRowContext(context.Background(), "SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("SQLite runtime version: %s", version)
	if version != "3.51.3" {
		t.Fatalf("SQLite runtime version = %q, want 3.51.3", version)
	}
}

func TestCanceledSQLiteQueryReleasesRowsAndAllowsCheckpoint(t *testing.T) {
	const maxCancellationAttempts = 20
	queryContexts := make(chan context.Context, maxCancellationAttempts)
	entered := make(chan struct{}, maxCancellationAttempts)
	scalarName := registerBlockingSQLiteScalar(t, queryContexts, entered)

	path := filepath.Join(t.TempDir(), "cancelled-query.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = store.Close()
		}
	})
	if _, err := store.db.ExecContext(context.Background(), `CREATE TABLE cancellation_rows (id INTEGER PRIMARY KEY, payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO cancellation_rows (id, payload) VALUES (1, zeroblob(8192))`); err != nil {
		t.Fatal(err)
	}
	var pageSize, autoCheckpointPages int
	if err := store.db.QueryRowContext(context.Background(), `PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(context.Background(), `PRAGMA wal_autocheckpoint`).Scan(&autoCheckpointPages); err != nil {
		t.Fatal(err)
	}
	if pageSize <= 0 || autoCheckpointPages <= 0 {
		t.Fatalf("invalid WAL sizing settings: page_size=%d wal_autocheckpoint=%d", pageSize, autoCheckpointPages)
	}
	var initialBusy, initialFrames, initialCheckpointed int
	if err := store.db.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&initialBusy, &initialFrames, &initialCheckpointed); err != nil {
		t.Fatalf("reset test WAL before workload: %v", err)
	}
	if initialBusy != 0 {
		t.Fatalf("initial WAL checkpoint reported busy=%d", initialBusy)
	}
	if initialCheckpointed != initialFrames {
		t.Fatalf("initial WAL checkpoint left frames behind: checkpointed=%d frames=%d", initialCheckpointed, initialFrames)
	}

	type queryResult struct {
		rows *sql.Rows
		err  error
	}
	cancellationHits, completedRaces := 0, 0
	for attempt := 1; attempt <= maxCancellationAttempts; attempt++ {
		queryCtx, cancelQuery := context.WithCancel(context.Background())
		queryContexts <- queryCtx
		queryDone := make(chan queryResult, 1)
		go func() {
			rows, queryErr := store.db.QueryContext(queryCtx, "SELECT "+scalarName+"() FROM cancellation_rows")
			queryDone <- queryResult{rows: rows, err: queryErr}
		}()

		select {
		case <-entered:
			// The scalar remains blocked on queryCtx.Done(), proving cancellation
			// is requested while SQLite is inside the query rather than after it.
		case <-time.After(5 * time.Second):
			cancelQuery()
			select {
			case result := <-queryDone:
				if result.rows != nil {
					_ = result.rows.Close()
				}
				t.Fatalf("blocking SQLite scalar was not entered on cancellation attempt %d; query returned rows=%t error=%v", attempt, result.rows != nil, result.err)
			case <-time.After(5 * time.Second):
				t.Fatalf("blocking SQLite scalar was not entered and query did not return on cancellation attempt %d", attempt)
			}
		}
		cancelQuery()

		var result queryResult
		select {
		case result = <-queryDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("SQLite query did not return after cancellation attempt %d", attempt)
		}
		if result.rows != nil {
			if err := result.rows.Close(); err != nil {
				t.Fatalf("close Rows returned during cancellation attempt %d: %v", attempt, err)
			}
			if result.err != nil {
				t.Fatalf("cancellation attempt %d returned both Rows and error: %v", attempt, result.err)
			}
			completedRaces++
			t.Logf("cancellation attempt %d completed before the asynchronous interrupt won; Rows explicitly closed (completion races=%d)", attempt, completedRaces)
			cancelQuery()
			continue
		}
		if !isSQLiteCancellation(result.err) {
			t.Fatalf("cancellation attempt %d error = %v, want context cancellation or SQLite interrupt", attempt, result.err)
		}
		cancellationHits++
		cancelQuery()
		t.Logf("cancellation attempt %d observed cancellation/interrupt (actual hits=%d, completion races=%d)", attempt, cancellationHits, completedRaces)
		break
	}
	if cancellationHits == 0 {
		t.Fatalf("cancellation was requested while the scalar was executing, but no cancellation/interrupt result was observed in %d attempts (completed races=%d); cancellation behavior remains uncovered", maxCancellationAttempts, completedRaces)
	}

	writeCtx, cancelWrites := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelWrites()
	for i := 0; i < 1500; i++ {
		if _, err := store.db.ExecContext(writeCtx, `UPDATE cancellation_rows SET payload=randomblob(8192) WHERE id=1`); err != nil {
			t.Fatalf("write %d after canceled query: %v", i+1, err)
		}
	}
	walInfo, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("stat WAL after writes: %v", err)
	}
	// Allow the configured autocheckpoint threshold, one 8 KiB update, and
	// bounded SQLite bookkeeping slack, then compare against actual WAL size.
	writeTransactionFrames := (8192+pageSize-1)/pageSize + 8
	walFrameBytes := int64(pageSize + 24)
	walSizeBudget := int64(32) + int64(autoCheckpointPages+writeTransactionFrames+32)*walFrameBytes
	t.Logf("WAL automatic-checkpoint settings: page_size=%d wal_autocheckpoint=%d initial_frames=%d/%d, physical_size=%d bytes budget=%d bytes", pageSize, autoCheckpointPages, initialCheckpointed, initialFrames, walInfo.Size(), walSizeBudget)
	if walInfo.Size() > walSizeBudget {
		t.Fatalf("WAL physical size exceeded autocheckpoint-derived budget: size=%d budget=%d", walInfo.Size(), walSizeBudget)
	}
	var busy, frames, checkpointed int
	if err := store.db.QueryRowContext(writeCtx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		t.Fatalf("checkpoint after canceled query: %v", err)
	}
	t.Logf("after 1500 writes WAL=%d bytes, checkpoint busy=%d frames=%d checkpointed=%d", walInfo.Size(), busy, frames, checkpointed)
	if busy != 0 {
		t.Fatalf("checkpoint reported busy=%d after canceled query", busy)
	}
	if frames <= 0 {
		t.Fatalf("checkpoint did not observe any WAL frames after writes: frames=%d", frames)
	}
	if checkpointed != frames {
		t.Fatalf("checkpoint left WAL frames behind after canceled query: checkpointed=%d frames=%d", checkpointed, frames)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	storeClosed = true
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WAL after closing store: stat error=%v, want file absent", err)
	}
}

func TestSQLiteBusyCommitLeavesConnectionReusable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "busy-commit.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var journalMode string
	if err := setup.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&journalMode); err != nil {
		_ = setup.Close()
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		_ = setup.Close()
		t.Fatalf("test database journal mode = %q, want DELETE", journalMode)
	}
	if _, err := setup.ExecContext(ctx, `CREATE TABLE commit_recovery (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`); err != nil {
		_ = setup.Close()
		t.Fatal(err)
	}
	if _, err := setup.ExecContext(ctx, `INSERT INTO commit_recovery (id, value) VALUES (1, 0)`); err != nil {
		_ = setup.Close()
		t.Fatal(err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	readerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer readerDB.Close()
	writerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writerDB.Close()
	readerDB.SetMaxOpenConns(1)
	writerDB.SetMaxOpenConns(1)
	readerConn, err := readerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer readerConn.Close()
	writerConn, err := writerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	if _, err := readerConn.ExecContext(ctx, `PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}
	if _, err := writerConn.ExecContext(ctx, `PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}

	readerTx, err := readerConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readerTx.Rollback() }()
	var originalValue int
	if err := readerTx.QueryRowContext(ctx, `SELECT value FROM commit_recovery WHERE id=1`).Scan(&originalValue); err != nil {
		_ = readerTx.Rollback()
		t.Fatal(err)
	}
	if originalValue != 0 {
		_ = readerTx.Rollback()
		t.Fatalf("initial value = %d, want 0", originalValue)
	}

	failedTx, err := writerConn.BeginTx(ctx, nil)
	if err != nil {
		_ = readerTx.Rollback()
		t.Fatal(err)
	}
	defer func() { _ = failedTx.Rollback() }()
	if _, err := failedTx.ExecContext(ctx, `UPDATE commit_recovery SET value=value+1 WHERE id=1`); err != nil {
		_ = readerTx.Rollback()
		_ = failedTx.Rollback()
		t.Fatalf("write before intentionally busy commit: %v", err)
	}
	commitErr := failedTx.Commit()
	if commitErr == nil {
		_ = readerTx.Rollback()
		t.Fatal("Commit unexpectedly succeeded while another transaction held a read lock")
	}
	if !strings.Contains(strings.ToLower(commitErr.Error()), "busy") && !strings.Contains(strings.ToLower(commitErr.Error()), "locked") {
		_ = readerTx.Rollback()
		t.Fatalf("Commit error = %v, want SQLite busy/locked error", commitErr)
	}
	t.Logf("intentional Commit failure: %v", commitErr)
	if err := readerTx.Rollback(); err != nil {
		t.Fatalf("release blocking reader transaction: %v", err)
	}

	recoveryTx, err := writerConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction on same sql.Conn after failed Commit: %v", err)
	}
	defer func() { _ = recoveryTx.Rollback() }()
	if _, err := recoveryTx.ExecContext(ctx, `UPDATE commit_recovery SET value=value+1 WHERE id=1`); err != nil {
		_ = recoveryTx.Rollback()
		t.Fatalf("write on same sql.Conn after failed Commit: %v", err)
	}
	if err := recoveryTx.Commit(); err != nil {
		t.Fatalf("commit recovery transaction on same sql.Conn: %v", err)
	}

	var finalValue int
	if err := readerConn.QueryRowContext(ctx, `SELECT value FROM commit_recovery WHERE id=1`).Scan(&finalValue); err != nil {
		t.Fatal(err)
	}
	if finalValue != 1 {
		t.Fatalf("value after recovery commit = %d, want 1 (failed transaction must not persist)", finalValue)
	}
}
