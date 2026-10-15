package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

const qualityPrunedPrefix = "storage_pruned:"

type qualityPrunedWatermark struct{ id, start, end, sequence, dropped int64 }

func qualityPrunedTx(ctx context.Context, tx *sql.Tx, id string, epoch int64, session string) (qualityPrunedWatermark, error) {
	var m qualityPrunedWatermark
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT id,start_at,end_at,reason,COALESCE(dropped,0) FROM quality_gaps WHERE agent_id=? AND epoch=? AND session_id=? AND reason LIKE 'storage_pruned:%' ORDER BY id DESC LIMIT 1`, id, epoch, session).Scan(&m.id, &m.start, &m.end, &reason, &m.dropped)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	m.sequence, err = strconv.ParseInt(strings.TrimPrefix(reason, qualityPrunedPrefix), 10, 64)
	if err != nil || m.sequence < 1 {
		return m, ErrQualityInvalid
	}
	return m, nil
}

// Retire bounded history and its durable sequence/time watermark in one tx.
// A removed observation is explicitly retired, never falsely called duplicate.
func qualityRotateTx(ctx context.Context, tx *sql.Tx, id string, global bool, before int64) (int, int, error) {
	ready, err := qualityRollupReadyTx(ctx, tx)
	if err != nil || !ready {
		return 0, 0, err
	}
	victim := id
	if global {
		err := tx.QueryRowContext(ctx, `SELECT u.agent_id FROM quality_usage u WHERE u.raw>0 ORDER BY CAST(u.raw AS REAL)/MAX(1,(SELECT COUNT(*) FROM quality_intervals i WHERE i.agent_id=u.agent_id AND i.end_at IS NULL)) DESC,u.agent_id LIMIT 1`).Scan(&victim)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil
		}
		if err != nil {
			return 0, 0, err
		}
	}
	type oldRow struct {
		id, epoch, sequence, started int64
		session                      string
	}
	old := []oldRow{}
	rows, err := tx.QueryContext(ctx, `SELECT id,epoch,session_id,seq,sample_time FROM quality_raw WHERE agent_id=? AND sample_time<? ORDER BY sample_time,id LIMIT 500`, victim, before)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var r oldRow
		if err = rows.Scan(&r.id, &r.epoch, &r.session, &r.sequence, &r.started); err != nil {
			rows.Close()
			return 0, 0, err
		}
		old = append(old, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, err
	}
	if len(old) == 0 {
		minutes, err := qualityOrphanMinutesTx(ctx, tx, victim, before, 500)
		return 0, minutes, err
	}
	type key struct {
		epoch   int64
		session string
	}
	marks := map[key]qualityPrunedWatermark{}
	for _, r := range old {
		k := key{r.epoch, r.session}
		m, ok := marks[k]
		if !ok {
			m, err = qualityPrunedTx(ctx, tx, victim, r.epoch, r.session)
			if err != nil {
				return 0, 0, err
			}
			if m.id == 0 {
				m.start = r.started
				m.end = r.started
			}
		}
		m.start = min(m.start, r.started)
		m.end = max(m.end, r.started)
		m.sequence = max(m.sequence, r.sequence)
		m.dropped++
		marks[k] = m
	}
	for k, m := range marks {
		reason := qualityPrunedPrefix + strconv.FormatInt(m.sequence, 10)
		if m.id == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO quality_gaps(agent_id,epoch,session_id,start_at,end_at,reason,dropped) VALUES(?,?,?,?,?,?,?)`, victim, k.epoch, k.session, m.start, m.end, reason, m.dropped)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE quality_gaps SET start_at=?,end_at=?,reason=?,dropped=? WHERE id=?`, m.start, m.end, reason, m.dropped, m.id)
		}
		if err != nil {
			return 0, 0, err
		}
	}
	for _, r := range old {
		if _, err = tx.ExecContext(ctx, `DELETE FROM quality_raw WHERE id=?`, r.id); err != nil {
			return 0, 0, err
		}
	}
	cutoff := old[len(old)-1].started / 60000 * 60000
	result, err := tx.ExecContext(ctx, `DELETE FROM quality_minute WHERE (target_id,bucket) IN(SELECT target_id,bucket FROM quality_minute WHERE agent_id=? AND bucket<? ORDER BY bucket,target_id LIMIT 500)`, victim, cutoff)
	if err != nil {
		return 0, 0, err
	}
	minutes, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	orphan, err := qualityOrphanMinutesTx(ctx, tx, victim, before, 500-int(minutes))
	return len(old), int(minutes) + orphan, err
}

func qualityOrphanMinutesTx(ctx context.Context, tx *sql.Tx, id string, before int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM quality_minute WHERE (target_id,bucket) IN(SELECT m.target_id,m.bucket FROM quality_minute m WHERE m.agent_id=? AND m.bucket<? AND NOT EXISTS(SELECT 1 FROM quality_raw r WHERE r.agent_id=m.agent_id AND r.target_id=m.target_id AND r.sample_time>=m.bucket AND r.sample_time<m.bucket+60000) ORDER BY m.bucket,m.target_id LIMIT ?)`, id, before/60000*60000, limit)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}
