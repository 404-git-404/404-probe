package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
)

// Called before inserting the new gap, so overlapping old gaps cannot subtract
// an edge twice. All corrections and the gap ACK share the upload transaction.
func qualityGapRollupTx(ctx context.Context, tx *sql.Tx, id string, gap protocol.QualityGap) error {
	rows, err := tx.QueryContext(ctx, `SELECT f.id,f.raw_id,f.target_id,f.series_id,f.session_key,f.seq,f.sample_time,f.config_revision,f.slot_revision,f.resolved_ip,f.eligible,f.rtt,f.interval_ms FROM quality_facts f JOIN quality_sessions s ON s.id=f.session_key WHERE s.agent_id=? AND f.sample_time>=? AND f.sample_time<=? ORDER BY f.target_id,f.sample_time,f.seq,f.id`, id, gap.Start-450000, gap.End+450000)
	if err != nil {
		return err
	}
	facts := []*qualityFact{}
	for rows.Next() {
		f, e := qualityReadFact(rows)
		if e != nil {
			rows.Close()
			return e
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	oldRows, err := tx.QueryContext(ctx, `SELECT start_at,end_at FROM quality_gaps WHERE agent_id=? AND start_at<=? AND end_at>=? AND reason NOT LIKE 'storage_pruned:%'`, id, gap.End+450000, gap.Start-450000)
	if err != nil {
		return err
	}
	old := [][2]int64{}
	for oldRows.Next() {
		var g [2]int64
		if err = oldRows.Scan(&g[0], &g[1]); err != nil {
			oldRows.Close()
			return err
		}
		old = append(old, g)
	}
	err = oldRows.Err()
	oldRows.Close()
	if err != nil {
		return err
	}
	type key struct{ series, res, t int64 }
	deltas := map[key]qualityBucketData{}
	for i := 1; i < len(facts); i++ {
		a, b := facts[i-1], facts[i]
		diff, ok := qualityFactPair(a, b)
		if !ok || gap.Start > b.Time || gap.End < a.Time {
			continue
		}
		blocked := false
		for _, g := range old {
			if g[0] <= b.Time && g[1] >= a.Time {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		for _, res := range qualityResolutions {
			if a.Time/res != b.Time/res {
				if err = qualityIncomingEdgeTx(ctx, tx, b.Series, res, b.Time, a, b, diff, -1); err != nil {
					return err
				}
				continue
			}
			k := key{b.Series, res, b.Time / res * res}
			d, ok := deltas[k]
			if !ok {
				d = qualityNewBucket()
			}
			d.JitterSum -= diff
			d.Stats.JitterPairs--
			deltas[k] = d
		}
	}
	for k, d := range deltas {
		if err = qualityBucketUpdateTx(ctx, tx, k.series, k.res, k.t, d); err != nil {
			return err
		}
	}
	sr, err := tx.QueryContext(ctx, `SELECT s.id FROM quality_series s WHERE s.agent_id=? AND (EXISTS(SELECT 1 FROM quality_intervals i WHERE i.target_id=s.target_id AND i.start_at<=? AND (i.end_at IS NULL OR i.end_at>=?)) OR EXISTS(SELECT 1 FROM quality_buckets b WHERE b.series_id=s.id AND b.resolution=3600000 AND b.bucket_start<=? AND b.bucket_start+3600000>=?))`, id, gap.End, gap.Start, gap.End, gap.Start)
	if err != nil {
		return err
	}
	ids := []int64{}
	for sr.Next() {
		var sid int64
		if err = sr.Scan(&sid); err != nil {
			sr.Close()
			return err
		}
		ids = append(ids, sid)
	}
	err = sr.Err()
	sr.Close()
	if err != nil {
		return err
	}
	for _, sid := range ids {
		for _, res := range qualityResolutions {
			last := gap.Start / res * res
			if gap.End > gap.Start {
				last = (gap.End - 1) / res * res
			}
			for t := gap.Start / res * res; t <= last; t += res {
				d := qualityNewBucket()
				d.Gaps = 1
				if err = qualityBucketUpdateTx(ctx, tx, sid, res, t, d); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
