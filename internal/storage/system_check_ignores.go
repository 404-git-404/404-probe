package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const maxSystemCheckIgnores = 128

var ErrSystemCheckIgnoreLimit = errors.New("system check ignore limit reached")

func (s *Store) ChangeSystemCheckIgnore(ctx context.Context, id string, key protocol.SystemCheckKey, ignore bool, now time.Time) (bool, error) {
	if !key.Valid() {
		return false, ErrOrganizationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var result sql.Result
	if ignore {
		result, err = tx.ExecContext(ctx, `INSERT INTO agent_system_check_ignores(agent_id,kind,item,created_at)
			SELECT id,?,?,? FROM agents
			WHERE id=? AND revoked=0 AND (SELECT count(*) FROM agent_system_check_ignores WHERE agent_id=agents.id) < ?
			ON CONFLICT(agent_id,kind,item) DO NOTHING`, key.Kind, key.Item, now.UnixMilli(), id, maxSystemCheckIgnores)
	} else {
		result, err = tx.ExecContext(ctx, `DELETE FROM agent_system_check_ignores WHERE agent_id=? AND kind=? AND item=?
			AND EXISTS(SELECT 1 FROM agents WHERE id=? AND revoked=0)`, id, key.Kind, key.Item, id)
	}
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		var exists, saved bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND revoked=0),
			EXISTS(SELECT 1 FROM agent_system_check_ignores WHERE agent_id=? AND kind=? AND item=?)`,
			id, id, key.Kind, key.Item).Scan(&exists, &saved)
		switch {
		case err != nil:
			return false, err
		case !exists:
			return false, ErrAgentNotFound
		case ignore && !saved:
			return false, ErrSystemCheckIgnoreLimit
		}
	}
	return count != 0, tx.Commit()
}

type SystemCheckIgnores map[protocol.SystemCheckKey]bool

func (s *Store) SystemCheckIgnores(ctx context.Context, id string) (SystemCheckIgnores, error) {
	all, err := s.SystemCheckIgnoresForAgents(ctx, []string{id})
	return all[id], err
}

func (s *Store) SystemCheckIgnoresForAgents(ctx context.Context, ids []string) (map[string]SystemCheckIgnores, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return readSystemCheckIgnores(ctx, s.db, ids)
}

func readSystemCheckIgnores(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, ids []string) (map[string]SystemCheckIgnores, error) {
	query := `SELECT agent_id,kind,item FROM agent_system_check_ignores`
	args := make([]any, 0, len(ids))
	if len(ids) > 0 {
		query += ` WHERE agent_id IN (?` + strings.Repeat(",?", len(ids)-1) + ")"
	}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SystemCheckIgnores)
	for rows.Next() {
		var id string
		var key protocol.SystemCheckKey
		if err := rows.Scan(&id, &key.Kind, &key.Item); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = make(SystemCheckIgnores)
		}
		out[id][key] = true
	}
	return out, rows.Err()
}
