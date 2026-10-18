package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// One-click Agent installation uses a registration token that is separate from the Agent's own
// credential: it is single-use, expires, is bound to the Agent name typed in the panel, and only
// its SHA-256 is stored. Redeeming it creates the Agent and hands back that Agent's credential.
const (
	EnrollmentTokenTTL          = 30 * time.Minute
	maxPendingEnrollmentTokens  = 32
	enrollmentTokenRetention    = 7 * 24 * time.Hour
	maxEnrollmentTokenNameBytes = 128
)

var (
	ErrEnrollmentInvalid      = errors.New("enrollment token is not valid")
	ErrEnrollmentUsed         = errors.New("enrollment token was already used")
	ErrEnrollmentExpired      = errors.New("enrollment token has expired")
	ErrEnrollmentRevoked      = errors.New("enrollment token was revoked")
	ErrEnrollmentNameMismatch = errors.New("enrollment token belongs to a different Agent name")
	ErrEnrollmentLimit        = errors.New("too many pending enrollment tokens")
)

func migrateAgentEnrollmentTokensTx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE agent_enrollment_tokens(
			id TEXT PRIMARY KEY,
			token_hash BLOB NOT NULL UNIQUE,
			name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			revoked_at INTEGER,
			agent_id TEXT
		)`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(32,unixepoch())`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// CreateAgentEnrollmentToken stores a new pending token. id is the public handle used to revoke it;
// tokenHash is the hash of the secret that is shown once.
func (s *Store) CreateAgentEnrollmentToken(ctx context.Context, id, name string, tokenHash []byte, now time.Time) (expiresAt time.Time, err error) {
	if !validStorageID(id, 128) || len(tokenHash) != 32 || strings.TrimSpace(name) == "" || len(name) > maxEnrollmentTokenNameBytes {
		return time.Time{}, errors.New("invalid enrollment token")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE expires_at<?`, now.Add(-enrollmentTokenRetention).UnixMilli()); err != nil {
		return time.Time{}, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_enrollment_tokens WHERE used_at IS NULL AND revoked_at IS NULL AND expires_at>?`, now.UnixMilli()).Scan(&pending); err != nil {
		return time.Time{}, err
	}
	if pending >= maxPendingEnrollmentTokens {
		return time.Time{}, ErrEnrollmentLimit
	}
	expiresAt = now.Add(EnrollmentTokenTTL)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(id,token_hash,name,created_at,expires_at) VALUES(?,?,?,?,?)`,
		id, tokenHash, name, now.UnixMilli(), expiresAt.UnixMilli()); err != nil {
		return time.Time{}, err
	}
	return expiresAt, tx.Commit()
}

// RevokeAgentEnrollmentToken voids a token that has not been used. Revoking twice, or revoking an
// expired token, succeeds; revoking a token that already registered an Agent reports ErrEnrollmentUsed.
func (s *Store) RevokeAgentEnrollmentToken(ctx context.Context, id string, now time.Time) error {
	if !validStorageID(id, 128) {
		return ErrEnrollmentInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT used_at FROM agent_enrollment_tokens WHERE id=?`, id).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnrollmentInvalid
	}
	if err != nil {
		return err
	}
	if used.Valid {
		return ErrEnrollmentUsed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now.UnixMilli(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// RedeemAgentEnrollmentToken atomically consumes the token and creates the Agent. A name that does
// not match leaves the token untouched.
func (s *Store) RedeemAgentEnrollmentToken(ctx context.Context, tokenHash []byte, name, agentID string, agentTokenHash []byte, now time.Time) error {
	if len(tokenHash) != 32 || strings.TrimSpace(name) == "" || !validStorageID(agentID, 128) || len(agentTokenHash) == 0 {
		return ErrEnrollmentInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, boundName string
	var expiresAt int64
	var used, revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,name,expires_at,used_at,revoked_at FROM agent_enrollment_tokens WHERE token_hash=?`, tokenHash).Scan(&id, &boundName, &expiresAt, &used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnrollmentInvalid
	}
	if err != nil {
		return err
	}
	switch {
	case used.Valid:
		return ErrEnrollmentUsed
	case revoked.Valid:
		return ErrEnrollmentRevoked
	case !now.Before(time.UnixMilli(expiresAt)):
		return ErrEnrollmentExpired
	case boundName != name:
		return ErrEnrollmentNameMismatch
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET used_at=?,agent_id=? WHERE id=? AND used_at IS NULL AND revoked_at IS NULL`, now.UnixMilli(), agentID, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ErrEnrollmentUsed
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, agentID, name, agentTokenHash, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
