package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

var ErrSecuritySnapshotChanged = errors.New("security baseline snapshot changed")

type SecurityBaseline struct {
	Status, Reason, SnapshotDigest string
	BaselineAt, SnapshotAt         int64
	Baseline, Current              *protocol.SecuritySnapshot
}

func saveRootInventoriesTx(ctx context.Context, tx *sql.Tx, r protocol.Report, received time.Time) error {
	checks := r.SystemChecks
	if checks == nil {
		return nil
	}
	baseline := checks.SecurityBaseline
	switch {
	case !checks.Supported:
		baseline = &protocol.SecurityBaselineReport{Status: protocol.InventoryUnavailable, Reason: "unsupported"}
	case baseline == nil && checks.RootStatus == protocol.CheckDisabled:
		baseline = &protocol.SecurityBaselineReport{Status: protocol.InventoryUnavailable, Reason: "root_collection_disabled"}
	case baseline == nil && (checks.RootStatus == protocol.CheckOK || checks.RootStatus == protocol.CheckError || checks.RootCollectedAt != 0):
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_security_baselines(agent_id,status) VALUES(?,'pending')
			ON CONFLICT(agent_id) DO UPDATE SET status='pending',reason=''
			WHERE snapshot_json IS NULL AND status<>'pending'`, r.AgentID); err != nil {
			return err
		}
	}
	if baseline != nil {
		if err := saveSecurityBaselineTx(ctx, tx, r.AgentID, *baseline, received); err != nil {
			return err
		}
	}
	structure := checks.SingboxStructure
	if !checks.Supported {
		structure = &protocol.SingboxStructureReport{Status: protocol.InventoryUnavailable, Reason: "unsupported"}
	}
	if structure != nil {
		return saveSingboxStructureTx(ctx, tx, r.AgentID, *structure)
	}
	return nil
}

func saveSecurityBaselineTx(ctx context.Context, tx *sql.Tx, id string, r protocol.SecurityBaselineReport, received time.Time) error {
	if r.Status != protocol.CheckOK {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_security_baselines(agent_id,status,reason) VALUES(?,'unavailable',?)
			ON CONFLICT(agent_id) DO UPDATE SET status=excluded.status,reason=excluded.reason
			WHERE status<>excluded.status OR reason<>excluded.reason`, id, r.Reason)
		return err
	}
	if r.Snapshot == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_security_baselines(agent_id,status) VALUES(?,'pending')
			ON CONFLICT(agent_id) DO NOTHING`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE agent_security_baselines SET status='ok',reason='',snapshot_at=?
			WHERE agent_id=? AND snapshot_digest=? AND snapshot_json IS NOT NULL AND (status<>'ok' OR snapshot_at<>?)`, r.CollectedAt, id, r.Digest, r.CollectedAt)
		return err
	}
	body, err := json.Marshal(r.Snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_security_baselines
		(agent_id,status,baseline_json,baseline_digest,baseline_at,snapshot_json,snapshot_digest,snapshot_at)
		VALUES(?,'ok',?,?,?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET status='ok',reason='',
			baseline_json=COALESCE(baseline_json,excluded.baseline_json),
			baseline_digest=CASE WHEN baseline_json IS NULL THEN excluded.baseline_digest ELSE baseline_digest END,
			baseline_at=CASE WHEN baseline_json IS NULL THEN excluded.baseline_at ELSE baseline_at END,
			snapshot_json=excluded.snapshot_json,snapshot_digest=excluded.snapshot_digest,snapshot_at=excluded.snapshot_at`,
		id, string(body), r.Digest, received.UnixMilli(), string(body), r.Digest, r.CollectedAt)
	return err
}

func saveSingboxStructureTx(ctx context.Context, tx *sql.Tx, id string, r protocol.SingboxStructureReport) error {
	if r.Status != protocol.CheckOK {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_singbox_structures(agent_id,status,reason,collected_at) VALUES(?,?,?,?)
			ON CONFLICT(agent_id) DO UPDATE SET status=excluded.status,reason=excluded.reason,collected_at=excluded.collected_at
			WHERE status<>excluded.status OR reason<>excluded.reason OR collected_at<>excluded.collected_at`, id, r.Status, r.Reason, r.CollectedAt)
		return err
	}
	if r.Structure == nil {
		_, err := tx.ExecContext(ctx, `UPDATE agent_singbox_structures SET status='ok',reason='',collected_at=?,truncated=?
			WHERE agent_id=? AND digest=? AND structure_json IS NOT NULL AND (status<>'ok' OR collected_at<>? OR truncated<>?)`,
			r.CollectedAt, r.Truncated, id, r.Digest, r.CollectedAt, r.Truncated)
		return err
	}
	body, err := json.Marshal(r.Structure)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_singbox_structures(agent_id,status,collected_at,digest,truncated,structure_json)
		VALUES(?,'ok',?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET status='ok',reason='',collected_at=excluded.collected_at,
			digest=excluded.digest,truncated=excluded.truncated,structure_json=excluded.structure_json`,
		id, r.CollectedAt, r.Digest, r.Truncated, string(body))
	return err
}

const securityBaselineColumns = `status,reason,baseline_at,snapshot_at,snapshot_digest,baseline_json,snapshot_json`

func readSecurityBaselines(ctx context.Context, txn interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, ids []string) (map[string]SecurityBaseline, error) {
	query := `SELECT agent_id,` + securityBaselineColumns + ` FROM agent_security_baselines`
	var args []any
	if ids != nil {
		if len(ids) == 0 {
			return map[string]SecurityBaseline{}, nil
		}
		query += ` WHERE agent_id IN (` + strings.Repeat("?,", len(ids)-1) + `?)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := txn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SecurityBaseline)
	for rows.Next() {
		var id string
		baseline, err := scanSecurityBaseline(rows, &id)
		if err != nil {
			return nil, err
		}
		out[id] = baseline
	}
	return out, rows.Err()
}

func (s *Store) SecurityBaselinesForAgents(ctx context.Context, ids []string) (map[string]SecurityBaseline, error) {
	if len(ids) == 0 {
		return map[string]SecurityBaseline{}, nil
	}
	return readSecurityBaselines(ctx, s.db, ids)
}

func scanSecurityBaseline(row dashboardScanner, prefix ...any) (SecurityBaseline, error) {
	var b SecurityBaseline
	var baseline, snapshot sql.NullString
	cols := append(prefix, &b.Status, &b.Reason, &b.BaselineAt, &b.SnapshotAt, &b.SnapshotDigest, &baseline, &snapshot)
	if err := row.Scan(cols...); err != nil {
		return b, err
	}
	if baseline.Valid {
		if err := json.Unmarshal([]byte(baseline.String), &b.Baseline); err != nil {
			return b, err
		}
	}
	if snapshot.Valid {
		if err := json.Unmarshal([]byte(snapshot.String), &b.Current); err != nil {
			return b, err
		}
	}
	return b, nil
}

func (s *Store) SecurityBaseline(ctx context.Context, id string) (SecurityBaseline, error) {
	b, err := scanSecurityBaseline(s.db.QueryRowContext(ctx, `SELECT `+securityBaselineColumns+` FROM agent_security_baselines WHERE agent_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SecurityBaseline{}, s.checkRootInventoryAgent(ctx, id)
	}
	return b, err
}

func (s *Store) AcknowledgeSecurityBaseline(ctx context.Context, id, digest string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_security_baselines
		SET baseline_json=snapshot_json,baseline_digest=snapshot_digest,baseline_at=?
		WHERE agent_id=? AND snapshot_digest=? AND snapshot_json IS NOT NULL`, now.UnixMilli(), id, digest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 0 {
		return err
	}
	if err := s.checkRootInventoryAgent(ctx, id); err != nil {
		return err
	}
	return ErrSecuritySnapshotChanged
}

func (s *Store) SingboxStructure(ctx context.Context, id string) (protocol.SingboxStructureReport, error) {
	var r protocol.SingboxStructureReport
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status,reason,collected_at,digest,truncated,structure_json
		FROM agent_singbox_structures WHERE agent_id=?`, id).Scan(&r.Status, &r.Reason, &r.CollectedAt, &r.Digest, &r.Truncated, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.SingboxStructureReport{Status: protocol.InventoryUnavailable, Reason: "not_collected"}, s.checkRootInventoryAgent(ctx, id)
	}
	if err != nil {
		return r, err
	}
	if r.Status == protocol.CheckOK && raw.Valid {
		err = json.Unmarshal([]byte(raw.String), &r.Structure)
	} else {
		r.Digest, r.Truncated = "", false
	}
	return r, err
}

func (s *Store) checkRootInventoryAgent(ctx context.Context, id string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAgentNotFound
	}
	return nil
}
