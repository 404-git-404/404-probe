package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"404-probe/internal/webdomain"
)

const (
	WebDomainModeExact  = "exact"
	WebDomainModeSuffix = "suffix"
)

type WebDomainPolicy struct {
	Mode     string            `json:"mode"`
	Revision int64             `json:"revision"`
	Suffixes []WebDomainSuffix `json:"suffixes"`
}

type WebDomainSuffix struct {
	Suffix    string `json:"suffix"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) GetWebDomainPolicy(ctx context.Context) (WebDomainPolicy, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WebDomainPolicy{}, fmt.Errorf("begin Web domain policy read: %w", err)
	}
	defer tx.Rollback()
	policy, err := getWebDomainPolicy(ctx, tx)
	if err != nil {
		return WebDomainPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return WebDomainPolicy{}, fmt.Errorf("commit Web domain policy read: %w", err)
	}
	return policy, nil
}

type webDomainPolicyQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getWebDomainPolicy(ctx context.Context, query webDomainPolicyQuerier) (WebDomainPolicy, error) {
	var policy WebDomainPolicy
	if err := query.QueryRowContext(ctx, `SELECT mode,revision FROM web_domain_policy WHERE id=1`).Scan(&policy.Mode, &policy.Revision); err != nil {
		return WebDomainPolicy{}, fmt.Errorf("read Web domain policy: %w", err)
	}
	rows, err := query.QueryContext(ctx, `SELECT suffix,created_at FROM web_domain_suffixes ORDER BY suffix`)
	if err != nil {
		return WebDomainPolicy{}, fmt.Errorf("list Web domain suffixes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item WebDomainSuffix
		if err := rows.Scan(&item.Suffix, &item.CreatedAt); err != nil {
			return WebDomainPolicy{}, err
		}
		canonical, err := webdomain.ParseSuffix(item.Suffix)
		if err != nil || canonical != item.Suffix {
			return WebDomainPolicy{}, errors.New("stored Web domain suffix is invalid")
		}
		policy.Suffixes = append(policy.Suffixes, item)
	}
	if err := rows.Err(); err != nil {
		return WebDomainPolicy{}, err
	}
	if policy.Mode != WebDomainModeExact && policy.Mode != WebDomainModeSuffix {
		return WebDomainPolicy{}, errors.New("stored Web domain mode is invalid")
	}
	if policy.Revision < 0 {
		return WebDomainPolicy{}, errors.New("stored Web domain policy revision is invalid")
	}
	if policy.Mode == WebDomainModeExact && len(policy.Suffixes) != 0 {
		return WebDomainPolicy{}, errors.New("exact Web domain mode contains suffixes")
	}
	if policy.Mode == WebDomainModeSuffix && len(policy.Suffixes) == 0 {
		return WebDomainPolicy{}, errors.New("suffix Web domain mode has no suffixes")
	}
	return policy, nil
}

func (s *Store) AddWebDomainSuffix(ctx context.Context, value string, now time.Time) (bool, WebDomainPolicy, error) {
	suffix, err := webdomain.ParseSuffix(value)
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO web_domain_suffixes(suffix,created_at) VALUES(?,?)`, suffix, now.UnixMilli())
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	if changed == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE web_domain_policy SET mode='suffix',revision=revision+1,updated_at=? WHERE id=1`, now.UnixMilli()); err != nil {
			return false, WebDomainPolicy{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, WebDomainPolicy{}, err
	}
	policy, err := s.GetWebDomainPolicy(ctx)
	return changed == 1, policy, err
}

func (s *Store) RemoveWebDomainSuffix(ctx context.Context, value string, now time.Time) (bool, WebDomainPolicy, error) {
	suffix, err := webdomain.ParseSuffix(value)
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	defer tx.Rollback()
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode FROM web_domain_policy WHERE id=1`).Scan(&mode); err != nil {
		return false, WebDomainPolicy{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM web_domain_suffixes`).Scan(&count); err != nil {
		return false, WebDomainPolicy{}, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM web_domain_suffixes WHERE suffix=?`, suffix).Scan(&exists); err != nil {
		return false, WebDomainPolicy{}, err
	}
	if exists == 0 {
		if err := tx.Commit(); err != nil {
			return false, WebDomainPolicy{}, err
		}
		policy, err := s.GetWebDomainPolicy(ctx)
		return false, policy, err
	}
	if mode != WebDomainModeSuffix || count <= 1 {
		return false, WebDomainPolicy{}, ErrLastWebDomainSuffix
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_domain_suffixes WHERE suffix=?`, suffix); err != nil {
		return false, WebDomainPolicy{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE web_domain_policy SET revision=revision+1,updated_at=? WHERE id=1`, now.UnixMilli()); err != nil {
		return false, WebDomainPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return false, WebDomainPolicy{}, err
	}
	policy, err := s.GetWebDomainPolicy(ctx)
	return true, policy, err
}

func (s *Store) DisableWebDomainSuffixes(ctx context.Context, now time.Time) (bool, WebDomainPolicy, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, WebDomainPolicy{}, err
	}
	defer tx.Rollback()
	var mode string
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT mode,(SELECT count(*) FROM web_domain_suffixes) FROM web_domain_policy WHERE id=1`).Scan(&mode, &count); err != nil {
		return false, WebDomainPolicy{}, err
	}
	changed := mode != WebDomainModeExact || count != 0
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_domain_suffixes`); err != nil {
		return false, WebDomainPolicy{}, err
	}
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE web_domain_policy SET mode='exact',revision=revision+1,updated_at=? WHERE id=1`, now.UnixMilli()); err != nil {
			return false, WebDomainPolicy{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, WebDomainPolicy{}, err
	}
	policy, err := s.GetWebDomainPolicy(ctx)
	return changed, policy, err
}
