package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

type QualityRollupPoint struct {
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	Provisional bool   `json:"provisional"`
	ConfigMin   string `json:"config_revision_min"`
	ConfigMax   string `json:"config_revision_max"`
	SlotMin     string `json:"slot_revision_min"`
	SlotMax     string `json:"slot_revision_max"`
	QualityStatsV2
}
type QualityRollupGroup struct {
	Semantics string               `json:"semantics"`
	Metric    string               `json:"metric"`
	Window    QualityStatsV2       `json:"window"`
	Points    []QualityRollupPoint `json:"points"`
}
type QualityHistoryRollup struct {
	Target               protocol.QualityTarget `json:"target"`
	Now                  int64                  `json:"now"`
	View                 string                 `json:"view"`
	ResolutionMS         int64                  `json:"resolution_ms"`
	WindowStart          int64                  `json:"window_start"`
	Algorithm            string                 `json:"algorithm"`
	HistogramVersion     int                    `json:"histogram_version"`
	GridMS               float64                `json:"grid_ms"`
	PercentileMaxErrorMS float64                `json:"percentile_max_error_ms"`
	MigrationPending     bool                   `json:"migration_pending"`
	Current              []QualityHistoryGroup  `json:"current"`
	Groups               []QualityRollupGroup   `json:"groups"`
}

// Windows use the published aligned grid. Current probes and freshness remain
// raw-backed; histogram values never substitute for a current measurement.
func (s *Store) QualityHistoryRollup(ctx context.Context, id, targetID, view string, now time.Time) (QualityHistoryRollup, error) {
	var res int64
	var n int
	switch view {
	case "1day":
		res, n = 60000, 1440
	case "1week":
		res, n = 300000, 2016
	case "1mo":
		res, n = 3600000, 720
	default:
		return QualityHistoryRollup{}, ErrQualityInvalid
	}
	return s.qualityRollupWindow(ctx, id, targetID, view, now, res, n)
}

// The card reads only its twelve visible five-minute buckets. This reuses the
// established rollup and current semantics without changing stored statistics.
func (s *Store) QualityCardHistory(ctx context.Context, id, targetID string, now time.Time) (QualityHistoryRollup, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.qualityRollupWindow(ctx, id, targetID, "1hour-card", now, 300000, 12)
}

func (s *Store) qualityRollupWindow(ctx context.Context, id, targetID, view string, now time.Time, res int64, n int) (QualityHistoryRollup, error) {
	out := QualityHistoryRollup{Now: now.UnixMilli(), View: view, ResolutionMS: res, Algorithm: "weighted-grid-rtt-adjacent-v3", HistogramVersion: QualityHistogramVersion, GridMS: QualityHistogramGridMS, PercentileMaxErrorMS: QualityPercentileMaxErrorMS, Groups: []QualityRollupGroup{}}
	current, err := s.qualityHistoryV2Raw(ctx, id, targetID, 1, now)
	if err != nil {
		return out, err
	}
	out.Target, out.Current = current.Target, current.Groups
	var complete bool
	if err = s.db.QueryRowContext(ctx, `SELECT complete FROM quality_backfill WHERE id=1`).Scan(&complete); err != nil {
		return out, err
	}
	out.MigrationPending = !complete
	out.WindowStart = out.Now/out.ResolutionMS*out.ResolutionMS - int64(n-1)*out.ResolutionMS
	buckets, err := s.qualityStoredBuckets(ctx, id, targetID, out.ResolutionMS, out.WindowStart, out.Now)
	if err != nil {
		return out, err
	}
	gaps, err := s.qualityStoredGaps(ctx, id, out.WindowStart, out.Now)
	if err != nil {
		return out, err
	}
	for _, sem := range []string{protocol.QualityV2Semantics, protocol.QualityStagedSemantics} {
		byTime, ok := buckets[sem]
		if !ok {
			continue
		}
		group := QualityRollupGroup{Semantics: sem, Metric: qualityMetric(out.Target.Protocol, sem), Points: make([]QualityRollupPoint, 0, n)}
		merged := qualityNewBucket()
		for i := 0; i < n; i++ {
			start := out.WindowStart + int64(i)*out.ResolutionMS
			end := min(start+out.ResolutionMS, out.Now)
			b, exists := byTime[start]
			if !exists {
				b = qualityNewBucket()
			}
			if end < start+out.ResolutionMS {
				b, err = s.qualityOpenBucket(ctx, id, targetID, sem, out.Target.Protocol, start, end, b)
				if err != nil {
					return out, err
				}
			}
			for _, gap := range gaps {
				if qualityGapOverlaps(start, end, gap) {
					b.Gaps = 1
					break
				}
			}
			qualityMergeBucket(&merged, b)
			stats := qualityBucketStats(b)
			if !complete && stats.Count > 0 {
				stats.Completeness = "partial"
			}
			group.Points = append(group.Points, QualityRollupPoint{start, end, !complete || out.Now < start+out.ResolutionMS+int64(6*time.Hour/time.Millisecond), qualityRevisionString(b.ConfigMin), qualityRevisionString(b.ConfigMax), qualityRevisionString(b.SlotMin), qualityRevisionString(b.SlotMax), stats})
		}
		group.Window = qualityBucketStats(merged)
		if !complete && group.Window.Count > 0 {
			group.Window.Completeness = "partial"
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

func qualityRevisionString(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
func qualityMetric(proto, sem string) string {
	if sem == protocol.QualityStagedSemantics {
		if proto == "tcp" {
			return "pure_connect"
		}
		return "echo_rtt"
	}
	if proto == "tcp" {
		return "legacy_dial_including_dns"
	}
	return "single_echo_rtt"
}

func (s *Store) qualityStoredBuckets(ctx context.Context, id, target string, res, start, end int64) (map[string]map[int64]qualityBucketData, error) {
	out := map[string]map[int64]qualityBucketData{}
	// CROSS JOIN fixes this small-to-large join order: at most the target's two
	// semantic series, then their primary-key bucket ranges. The global expiry
	// index must not turn a per-target view into a scan of every agent's history.
	rows, err := s.db.QueryContext(ctx, `SELECT s.semantics,b.bucket_start,b.data FROM quality_series s CROSS JOIN quality_buckets b ON b.series_id=s.id WHERE s.agent_id=? AND s.target_id=? AND b.resolution=? AND b.bucket_start>=? AND b.bucket_start<=? ORDER BY b.bucket_start`, id, target, res, start, end)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var sem string
		var t int64
		var data []byte
		if err = rows.Scan(&sem, &t, &data); err != nil {
			return out, err
		}
		b, e := qualityDecodeBucket(data)
		if e != nil {
			return out, e
		}
		if out[sem] == nil {
			out[sem] = map[int64]qualityBucketData{}
		}
		out[sem][t] = b
	}
	return out, rows.Err()
}

// Accepted clock skew can put observations ahead of now in the open bucket.
// Recompute that one bucket from Raw, excluding those future observations.
func (s *Store) qualityOpenBucket(ctx context.Context, id, target, sem, proto string, start, end int64, stored qualityBucketData) (qualityBucketData, error) {
	b := qualityNewBucket()
	rows, err := s.db.QueryContext(ctx, `SELECT `+qualitySampleColumns+`,epoch,session_id,received_at FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? ORDER BY sample_time,seq`, id, target, start, end)
	if err != nil {
		return b, err
	}
	records := []qualityHistoryRow{}
	position := 0
	for rows.Next() {
		var r qualityHistoryRow
		r.sample, err = qualityReadSample(rows, &r.epoch, &r.session, &r.received)
		if err != nil {
			rows.Close()
			return b, err
		}
		position++
		r.position = position
		actual := protocol.QualityV2Semantics
		if r.sample.Staged() {
			actual = protocol.QualityStagedSemantics
		}
		if actual == sem {
			records = append(records, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	for _, r := range records {
		part, e := qualityContribution(r.sample, proto)
		if e != nil {
			return b, e
		}
		cfg, _ := protocol.QualityInteger(r.sample.ConfigRevision)
		slot, _ := protocol.QualityInteger(r.sample.SlotRevision)
		part.ConfigMin, part.ConfigMax, part.SlotMin, part.SlotMax = cfg, cfg, slot, slot
		qualityMergeBucket(&b, part)
	}
	gaps := []protocol.QualityGap{}
	gr, err := s.db.QueryContext(ctx, `SELECT start_at,end_at,reason FROM quality_gaps WHERE agent_id=? AND start_at<? AND (end_at>? OR (start_at=end_at AND start_at>=?)) AND reason NOT LIKE 'storage_pruned:%'`, id, end, start, start)
	if err != nil {
		return b, err
	}
	for gr.Next() {
		var g protocol.QualityGap
		if err = gr.Scan(&g.Start, &g.End, &g.Reason); err != nil {
			gr.Close()
			return b, err
		}
		gaps = append(gaps, g)
	}
	err = gr.Err()
	gr.Close()
	if err != nil {
		return b, err
	}
	stats := qualityStatsV2(records, proto, gaps, start, end)
	b.Stats = stats
	b.JitterSum = 0
	if stats.Jitter != nil {
		b.JitterSum = *stats.Jitter * float64(stats.JitterPairs)
	}
	if len(gaps) > 0 {
		b.Gaps = 1
	}
	if len(records) > 0 {
		first, last := records[0].sample, records[len(records)-1].sample
		firstSeq, _ := protocol.QualityInteger(first.Sequence)
		lastSeq, _ := protocol.QualityInteger(last.Sequence)
		b.FirstTime, b.FirstSeq, b.LastTime, b.LastSeq = first.StartedAt, firstSeq, last.StartedAt, lastSeq
		if err = s.db.QueryRowContext(ctx, `SELECT f.id FROM quality_facts f JOIN quality_sessions q ON q.id=f.session_key WHERE q.agent_id=? AND q.epoch=? AND q.session_id=? AND f.seq=?`, id, records[0].epoch, records[0].session, firstSeq).Scan(&b.FirstID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		if err = s.db.QueryRowContext(ctx, `SELECT f.id FROM quality_facts f JOIN quality_sessions q ON q.id=f.session_key WHERE q.agent_id=? AND q.epoch=? AND q.session_id=? AND f.seq=?`, id, records[len(records)-1].epoch, records[len(records)-1].session, lastSeq).Scan(&b.LastID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		if b.FirstID == stored.FirstID {
			b.PreviousID, b.IncomingJitter, b.IncomingPair = stored.PreviousID, stored.IncomingJitter, stored.IncomingPair
		}
	}
	return b, nil
}

func (s *Store) qualityStoredGaps(ctx context.Context, id string, start, end int64) ([]protocol.QualityGap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start_at,end_at,reason FROM quality_gaps WHERE agent_id=? AND start_at<=? AND end_at>=? AND reason NOT LIKE 'storage_pruned:%' ORDER BY start_at LIMIT 4096`, id, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := []protocol.QualityGap{}
	for rows.Next() {
		var gap protocol.QualityGap
		if err = rows.Scan(&gap.Start, &gap.End, &gap.Reason); err != nil {
			return nil, err
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

// Historic buckets are UTC [start,end). Point events belong to the bucket
// containing their timestamp. TCP edge blocking retains its closed endpoint
// rule independently, because a restart at either observation breaks adjacency.
func qualityGapOverlaps(start, end int64, g protocol.QualityGap) bool {
	if start >= end {
		return false
	}
	if g.Start == g.End {
		return g.Start >= start && g.Start < end
	}
	return g.Start < end && g.End > start
}
