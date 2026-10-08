package storage

import (
	"context"
	"time"
)

// One pass is bounded to 500 per table; cascades/counters share each statement's tx.
func (s *Store) CleanupQualityBatch(ctx context.Context, now time.Time) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	cutoff := now.Add(-QualityRetention).UnixMilli()
	var total int64
	more := false
	for _, q := range []string{
		`DELETE FROM quality_raw WHERE id IN(SELECT id FROM quality_raw WHERE sample_time<? ORDER BY sample_time,id LIMIT 500)`,
		`DELETE FROM quality_minute WHERE (target_id,bucket) IN(SELECT target_id,bucket FROM quality_minute WHERE bucket<? ORDER BY bucket,target_id LIMIT 500)`,
		`DELETE FROM quality_gaps WHERE id IN(SELECT id FROM quality_gaps WHERE end_at<? ORDER BY end_at,id LIMIT 500)`,
		`DELETE FROM quality_intervals WHERE (agent_id,slot,family,revision) IN(SELECT agent_id,slot,family,revision FROM quality_intervals WHERE end_at<? ORDER BY end_at,agent_id,revision LIMIT 500)`,
		`DELETE FROM quality_targets WHERE id IN(SELECT t.id FROM quality_targets t WHERE t.retired_at<? AND NOT EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_intervals i WHERE i.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_raw r WHERE r.target_id=t.id) AND NOT EXISTS(SELECT 1 FROM quality_minute m WHERE m.target_id=t.id) ORDER BY t.retired_at,t.id LIMIT 500)`,
	} {
		result, err := tx.ExecContext(ctx, q, cutoff)
		if err != nil {
			return 0, false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, false, err
		}
		total += n
		if n == 500 {
			more = true
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return total, more, nil
}
