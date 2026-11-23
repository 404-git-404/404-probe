package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
)

func migrateBackup45(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS offsite_backup (
		id INTEGER PRIMARY KEY CHECK(id=1), state TEXT NOT NULL
	)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(45,?)`, time.Now().UnixMilli())
	return err
}

func (s *Store) ReadBackupState(ctx context.Context) ([]byte, error) {
	return readBackupState(ctx, s.db)
}

func ReadBackupStateFile(ctx context.Context, path string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(abs))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return readBackupState(ctx, db)
}

func readBackupState(ctx context.Context, db *sql.DB) ([]byte, error) {
	var state []byte
	err := db.QueryRowContext(ctx, `SELECT state FROM offsite_backup WHERE id=1`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return state, err
}

func (s *Store) WriteBackupState(ctx context.Context, state []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO offsite_backup(id,state) VALUES(1,?)
		ON CONFLICT(id) DO UPDATE SET state=excluded.state`, string(state))
	return err
}

// PauseBackupForRestore updates a verified offline copy before installation.
// A replacement Server must be explicitly enabled before managing R2 objects.
func PauseBackupForRestore(ctx context.Context, path string, version int) error {
	if version < 45 {
		return nil
	}
	db, err := sql.Open("sqlite", sqliteOpenDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE offsite_backup SET state=json_set(state,
		'$.enabled',json('false'),'$.enabled_since',0,'$.scheduled',0,'$.failed',json('false'),'$.object_count',NULL) WHERE id=1`); err != nil {
		return err
	}
	// Keep staging self-contained even if an external age archive carries a
	// SQLite file with the WAL journal mode set in its database header.
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("could not finalize the offline restore database")
	}
	return nil
}

// CheckBackupSpace keeps working space in reserve for the live database and OS.
func CheckBackupSpace(dir string, needed int64) error {
	free, _, err := qualityFilesystem(dir)
	if err != nil {
		return errors.New("could not check backup working space")
	}
	const reserve = 128 << 20
	if free < reserve || needed > free-reserve {
		return errors.New("insufficient backup working space (128 MiB reserve)")
	}
	return nil
}

// BackupSnapshot pins a WAL read transaction on its own connection, leaving
// the Store's single write connection available to Agent reports throughout.
// dst must be a new path inside a private working directory.
func (s *Store) BackupSnapshot(ctx context.Context, dst string) (int, error) {
	if s.databasePath == "" {
		return 0, errors.New("backup requires an on-disk database")
	}
	abs, err := filepath.Abs(s.databasePath)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(abs)+"&_pragma=busy_timeout%281000%29")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var version int
	var pageSize, pageCount int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT MAX(version) FROM schema_migrations),
		(SELECT page_size FROM pragma_page_size), (SELECT page_count FROM pragma_page_count)`).Scan(&version, &pageSize, &pageCount); err != nil {
		return 0, err
	}
	dir := filepath.Dir(dst)
	if err := CheckBackupSpace(dir, pageSize*pageCount); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	return version, conn.Raw(func(driverConn any) (err error) {
		online, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite online backup is unavailable")
		}
		backup, err := online.NewBackup(dst)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		const pages = 128
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := CheckBackupSpace(dir, pages*pageSize); err != nil {
				return err
			}
			more, err := backup.Step(pages)
			if err != nil {
				return fmt.Errorf("copy SQLite snapshot: %w", err)
			}
			if !more {
				return nil
			}
		}
	})
}
