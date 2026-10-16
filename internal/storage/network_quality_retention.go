package storage

import (
	"context"
	"database/sql"
	"time"
)

// One pass is bounded to 500 per table; cascades/counters share each statement's tx.
func (s *Store) CleanupQualityBatch(ctx context.Context, now time.Time) (int64, bool, error) {
	if err := s.MaintainTCPMini(ctx, now); err != nil {
		return 0, false, err
	}
	if pending, err := s.tcpMiniPending(ctx, now); err != nil || pending {
		return 0, pending, err
	}
	n, pending, err := s.BackfillQualityBatch(ctx, now)
	if err != nil || pending {
		return int64(n), pending, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var total int64
	more := false
	exec := func(q string, args ...any) error {
		r, e := tx.ExecContext(ctx, q, args...)
		if e != nil {
			return e
		}
		count, e := r.RowsAffected()
		total += count
		more = more || count == 500
		return e
	}
	receiptCutoff := now.Add(-QualityReceiptRetention).UnixMilli()
	rows, err := tx.QueryContext(ctx, `SELECT session_key,seq,sample_time FROM quality_receipts WHERE sample_time<? ORDER BY sample_time,session_key,seq LIMIT 500`, receiptCutoff)
	if err != nil {
		return 0, false, err
	}
	type retired struct{ sid, seq, stamp int64 }
	old := []retired{}
	for rows.Next() {
		var r retired
		if err = rows.Scan(&r.sid, &r.seq, &r.stamp); err != nil {
			rows.Close()
			return 0, false, err
		}
		old = append(old, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, false, err
	}
	for _, r := range old {
		if err = qualityReceiptFenceTx(ctx, tx, r.sid, r.seq, r.stamp); err != nil {
			return 0, false, err
		}
		if err = exec(`DELETE FROM quality_receipts WHERE session_key=? AND seq=?`, r.sid, r.seq); err != nil {
			return 0, false, err
		}
	}
	more = more || len(old) == 500
	rawCutoff := now.Add(-QualityRawRetention).UnixMilli()
	if err = exec(`DELETE FROM quality_raw WHERE id IN(SELECT r.id FROM quality_raw r WHERE r.sample_time<? AND EXISTS(SELECT 1 FROM quality_facts f JOIN quality_sessions s ON s.id=f.session_key WHERE f.raw_id=r.id AND f.seq=r.seq AND s.agent_id=r.agent_id AND s.epoch=r.epoch AND s.session_id=r.session_id) ORDER BY r.sample_time,r.id LIMIT 500)`, rawCutoff); err != nil {
		return 0, false, err
	}
	if err = exec(`DELETE FROM quality_facts WHERE id IN(SELECT f.id FROM quality_facts f WHERE f.sample_time<? AND (f.sample_time<? OR EXISTS(SELECT 1 FROM quality_facts n WHERE n.target_id=f.target_id AND n.sample_time<? AND (n.sample_time,n.seq,n.id)>(f.sample_time,f.seq,f.id))) ORDER BY f.sample_time,f.id LIMIT 500)`, rawCutoff, now.Add(-QualityHistoryRetention).UnixMilli(), rawCutoff); err != nil {
		return 0, false, err
	}
	for i, res := range qualityResolutions {
		if err = exec(`DELETE FROM quality_buckets WHERE (series_id,resolution,bucket_start) IN(SELECT series_id,resolution,bucket_start FROM quality_buckets WHERE resolution=? AND bucket_start<? ORDER BY bucket_start,series_id LIMIT 500)`, res, now.Add(-qualityBucketRetentions[i]).UnixMilli()/res*res); err != nil {
			return 0, false, err
		}
	}
	for _, item := range []struct {
		q      string
		cutoff int64
	}{
		{`DELETE FROM quality_minute WHERE (target_id,bucket) IN(SELECT target_id,bucket FROM quality_minute WHERE bucket<? ORDER BY bucket,target_id LIMIT 500)`, now.Add(-24 * time.Hour).UnixMilli()},
		{`DELETE FROM quality_gaps WHERE id IN(SELECT id FROM quality_gaps WHERE end_at<? ORDER BY end_at,id LIMIT 500)`, now.Add(-QualityHistoryRetention).UnixMilli()},
		{`DELETE FROM quality_intervals WHERE (agent_id,slot,family,protocol,revision) IN(SELECT agent_id,slot,family,protocol,revision FROM quality_intervals WHERE end_at<? ORDER BY end_at,agent_id,revision LIMIT 500)`, receiptCutoff},
	} {
		if err = exec(item.q, item.cutoff); err != nil {
			return 0, false, err
		}
	}
	for _, q := range []string{
		`DELETE FROM quality_series WHERE id IN(SELECT s.id FROM quality_series s WHERE NOT EXISTS(SELECT 1 FROM quality_buckets b WHERE b.series_id=s.id) AND NOT EXISTS(SELECT 1 FROM quality_receipts r WHERE r.series_id=s.id) AND NOT EXISTS(SELECT 1 FROM quality_facts f WHERE f.series_id=s.id) AND NOT EXISTS(SELECT 1 FROM quality_intervals i WHERE i.target_id=s.target_id AND i.end_at IS NULL) LIMIT 500)`,
		`DELETE FROM quality_sessions WHERE id IN(SELECT s.id FROM quality_sessions s WHERE NOT EXISTS(SELECT 1 FROM quality_receipts r WHERE r.session_key=s.id) AND NOT EXISTS(SELECT 1 FROM quality_facts f WHERE f.session_key=s.id) AND NOT EXISTS(SELECT 1 FROM agent_state a WHERE a.agent_id=s.agent_id AND a.epoch=s.epoch AND a.session_id=s.session_id) LIMIT 500)`,
		`DELETE FROM quality_targets WHERE id IN(SELECT t.id FROM quality_targets t WHERE t.retired_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_intervals i WHERE i.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_raw r WHERE r.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_minute m WHERE m.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_series x WHERE x.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_facts f WHERE f.target_id=t.id) LIMIT 500)`,
	} {
		if err = exec(q); err != nil {
			return 0, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return total, more, nil
}

func qualityReceiptFenceTx(ctx context.Context, tx *sql.Tx, session, seq, stamp int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO quality_retired VALUES(?,?,?) ON CONFLICT(session_key) DO UPDATE SET seq=MAX(seq,excluded.seq),sample_time=MAX(sample_time,excluded.sample_time)`, session, seq, stamp)
	return err
}
