package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrWebCredentialConflict = errors.New("Web credential revision conflict")

type WebCredential struct {
	PasswordHash string
	FileSHA256   string
	Revision     int64
}

// WebCredential returns a zero revision when the password file is authoritative.
func (s *Store) WebCredential(ctx context.Context) (WebCredential, error) {
	var credential WebCredential
	err := s.db.QueryRowContext(ctx, `SELECT password_hash,file_sha256,revision FROM web_admin_credential WHERE id=1`).
		Scan(&credential.PasswordHash, &credential.FileSHA256, &credential.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return WebCredential{}, nil
	}
	return credential, err
}

// ReplaceWebCredential writes only if the caller verified the current revision.
func (s *Store) ReplaceWebCredential(ctx context.Context, expected int64, hash, fileSHA256 string, now time.Time) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO web_admin_credential(id,password_hash,file_sha256,revision,updated_at)
		SELECT 1,?,?,?+1,? WHERE ?=0 OR EXISTS(SELECT 1 FROM web_admin_credential WHERE id=1 AND revision=?)
		ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash,file_sha256=excluded.file_sha256,
			revision=excluded.revision,updated_at=excluded.updated_at WHERE web_admin_credential.revision=?
		RETURNING revision`, hash, fileSHA256, expected, now.UnixMilli(), expected, expected, expected).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrWebCredentialConflict
	}
	return revision, err
}

// AdoptWebCredential invalidates every existing session even if the hash is unchanged.
func (s *Store) AdoptWebCredential(ctx context.Context, hash, fileSHA256 string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_admin_credential(id,password_hash,file_sha256,revision,updated_at)
		VALUES(1,?,?,1,?) ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash,
		file_sha256=excluded.file_sha256,revision=web_admin_credential.revision+1,updated_at=excluded.updated_at`, hash, fileSHA256, now.UnixMilli())
	return err
}

// WebAuthenticationPolicy reads both revisions from a single SQLite snapshot.
func (s *Store) WebAuthenticationPolicy(ctx context.Context) (WebDomainPolicy, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WebDomainPolicy{}, 0, err
	}
	defer tx.Rollback()
	policy, err := getWebDomainPolicy(ctx, tx)
	if err != nil {
		return WebDomainPolicy{}, 0, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM web_admin_credential WHERE id=1),0)`).Scan(&revision); err != nil {
		return WebDomainPolicy{}, 0, err
	}
	return policy, revision, tx.Commit()
}
