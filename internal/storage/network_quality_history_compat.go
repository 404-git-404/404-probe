package storage

import (
	"404-probe/internal/protocol"
	"context"
	"time"
)

func (s *Store) qualityNeedsArchive(ctx context.Context, id, target string, hours int, now time.Time) (bool, error) {
	if hours != 24 {
		return false, nil
	}
	var ready, archived bool
	if err := s.db.QueryRowContext(ctx, `SELECT complete FROM quality_backfill WHERE id=1`).Scan(&ready); err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_buckets b JOIN quality_series q ON q.id=b.series_id WHERE q.agent_id=? AND q.target_id=? AND b.resolution=300000 AND b.bucket_start>=? AND b.bucket_start<?)`, id, target, now.Add(-24*time.Hour).UnixMilli(), now.Add(-QualityRawRetention).UnixMilli()).Scan(&archived)
	return archived, err
}

// Recent/current fields retain their raw semantics. Once Raw ages out, the
// 24-hour blocks and window use the explicitly published aligned grid.
func (s *Store) QualityHistoryV2(ctx context.Context, id, target string, hours int, now time.Time) (QualityHistoryV2, error) {
	out, err := s.qualityHistoryV2Raw(ctx, id, target, hours, now)
	if err != nil {
		return out, err
	}
	needed, err := s.qualityNeedsArchive(ctx, id, target, hours, now)
	if err != nil || !needed {
		return out, err
	}
	res := int64(300000)
	first := now.UnixMilli()/res*res - 287*res
	stored, err := s.qualityStoredBuckets(ctx, id, target, res, first, now.UnixMilli())
	if err != nil {
		return out, err
	}
	out.WindowStart = first
	out.HistogramGridMS, out.PercentileMaxErrorMS = QualityHistogramGridMS, QualityPercentileMaxErrorMS
	out.Algorithm = "weighted-grid-rtt-adjacent-v3"
	prior := map[string]QualityHistoryGroup{}
	for _, g := range out.Groups {
		prior[g.Semantics] = g
	}
	out.Groups = []QualityHistoryGroup{}
	for _, sem := range []string{protocol.QualityV2Semantics, protocol.QualityStagedSemantics} {
		byTime, ok := stored[sem]
		if !ok {
			continue
		}
		g, ok := prior[sem]
		if !ok {
			g = QualityHistoryGroup{Semantics: sem, Metric: qualityMetric(out.Target.Protocol, sem), Stale: true, FreshnessBudgetMS: 76000, Recent: qualityBucketStats(qualityNewBucket())}
		}
		g.Blocks = []QualityBlockV2{}
		merged := qualityNewBucket()
		for i := 0; i < 288; i++ {
			a := first + int64(i)*res
			end := min(a+res, now.UnixMilli())
			b, ok := byTime[a]
			if !ok {
				b = qualityNewBucket()
			}
			if i == 287 {
				b, err = s.qualityOpenBucket(ctx, id, target, sem, out.Target.Protocol, a, end, b)
				if err != nil {
					return out, err
				}
			}
			qualityMergeBucket(&merged, b)
			g.Blocks = append(g.Blocks, QualityBlockV2{a, end, i == 287, qualityBucketStats(b)})
		}
		g.Window = qualityBucketStats(merged)
		out.Groups = append(out.Groups, g)
	}
	return out, nil
}

func qualityLegacyStats(v QualityStatsV2) QualityStats {
	o := QualityStats{Count: v.Count, Attempts: v.ProbeAttempts, Successes: v.ProbeSuccesses, Failures: v.ProbeAttempts - v.ProbeSuccesses, NotExecuted: v.NotExecuted, Categories: v.Categories, PacketLossPercent: v.PacketLossPercent, Sent: v.Sent, Received: v.Received, P50: v.P50, P95: v.P95, Spread: v.Spread}
	if o.Attempts > 0 {
		x := 100 * float64(o.Failures) / float64(o.Attempts)
		o.FailurePercent = &x
	}
	return o
}
func (s *Store) QualityHistory(ctx context.Context, id, target string, hours int, now time.Time) (QualityHistory, error) {
	out, err := s.qualityHistoryRaw(ctx, id, target, hours, now)
	if err != nil {
		return out, err
	}
	needed, err := s.qualityNeedsArchive(ctx, id, target, hours, now)
	if err != nil || !needed {
		return out, err
	}
	v2, err := s.QualityHistoryV2(ctx, id, target, hours, now)
	if err != nil {
		return out, err
	}
	for _, g := range v2.Groups {
		if g.Semantics != protocol.QualityV2Semantics {
			continue
		}
		out.Window = qualityLegacyStats(g.Window)
		out.Blocks = []QualityBlock{}
		for _, b := range g.Blocks {
			gap := b.Count == 0 || b.NotExecuted > 0 || b.ExecutionCoverage == "known_gap"
			out.Blocks = append(out.Blocks, QualityBlock{b.Start, b.End, b.InProgress, gap, qualityLegacyStats(b.QualityStatsV2)})
		}
		out.Algorithm = "weighted-grid-success-v3"
		out.WindowStart = v2.WindowStart
		out.HistogramGridMS, out.PercentileMaxErrorMS = QualityHistogramGridMS, QualityPercentileMaxErrorMS
	}
	return out, nil
}
