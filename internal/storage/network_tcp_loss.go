package storage

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"404-probe/internal/protocol"
)

func qualityTargetTCPTimeout(ctx context.Context, tx *sql.Tx, target string, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT f.rtt FROM quality_facts f JOIN quality_series s ON s.id=f.series_id
  WHERE f.target_id=? AND f.sample_time>=? AND f.sample_time<=? AND f.eligible=1 AND s.semantics IN (?,?)
  ORDER BY f.sample_time DESC,f.seq DESC,f.id DESC LIMIT 60`,
		target, now.Add(-6*time.Hour).UnixMilli(), now.UnixMilli(), protocol.QualityStagedSemantics, protocol.QualityTCPLossSemantics)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var rtts []float64
	for rows.Next() {
		var rtt float64
		if err := rows.Scan(&rtt); err != nil {
			return 0, err
		}
		rtts = append(rtts, rtt)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return qualityTCPTimeoutFor(rtts), nil
}

func qualityTCPTimeoutFor(rtts []float64) int {
	if n := len(rtts); n > 0 {
		sort.Float64s(rtts)
		if (rtts[(n-1)/2]+rtts[n/2])/2 > 600 {
			return protocol.QualityTCPHighLatencyTimeoutMS
		}
	}
	return protocol.QualityTCPDefaultTimeoutMS
}

func qualitySampleSemantics(sample protocol.QualitySample) string {
	d, err := protocol.DecodeQualityDetails(sample.Details)
	if err != nil || d == nil || d.Semantics != protocol.QualityStagedSemantics {
		return protocol.QualityV2Semantics
	}
	if protocol.QualityValidTCPTimeout(d.ConnectTimeoutMS) {
		return protocol.QualityTCPLossSemantics
	}
	return protocol.QualityStagedSemantics
}

type qualityTCPBucket struct {
	Samples, Attempts, Loss, Errors, DNS, Relaxed int
}

func (b *qualityTCPBucket) add(other qualityTCPBucket) {
	b.Samples += other.Samples
	b.Attempts += other.Attempts
	b.Loss += other.Loss
	b.Errors += other.Errors
	b.DNS += other.DNS
	b.Relaxed += other.Relaxed
}

func qualityTCPContribution(sample protocol.QualitySample, d *protocol.QualityDetails) qualityTCPBucket {
	b := qualityTCPBucket{Samples: 1}
	if d.ConnectComplete {
		b.Attempts = 1
	}
	switch sample.Outcome {
	case "timeout":
		if d.ConnectComplete {
			b.Loss = 1
		}
	case "dns_error", "refused", "network_error":
		if d.ProbeComplete {
			b.Errors = 1
			if sample.Outcome == "dns_error" {
				b.DNS = 1
			}
		}
	}
	if d.ConnectTimeoutMS == protocol.QualityTCPHighLatencyTimeoutMS {
		b.Relaxed = 1
	}
	return b
}

func qualityMaximum(hist map[uint16]uint32) *float64 {
	if len(hist) == 0 {
		return nil
	}
	var highest uint16
	for bin := range hist {
		highest = max(highest, bin)
	}
	value := float64(highest) * QualityHistogramGridMS
	return &value
}

func qualityCellColor(stats NetworkStats, threshold int) (QualityCardState, string) {
	if stats.Samples == 0 {
		return QualityCardGray, "no_data"
	}
	if stats.P50 != nil && *stats.P50 > float64(2*threshold) {
		return QualityCardRed, "latency_red"
	}
	if stats.PacketLossPercent != nil && *stats.PacketLossPercent >= 20 {
		return QualityCardRed, "loss_red"
	}
	if stats.ErrorPercent != nil && *stats.ErrorPercent >= 20 {
		return QualityCardRed, "error_red"
	}
	if stats.P50 != nil && *stats.P50 > float64(threshold) {
		return QualityCardYellow, "latency_yellow"
	}
	if stats.PacketLossPercent != nil && *stats.PacketLossPercent > 0 {
		return QualityCardYellow, "loss_yellow"
	}
	if stats.ErrorPercent != nil && *stats.ErrorPercent > 0 {
		return QualityCardYellow, "error_yellow"
	}
	if stats.P50 == nil && stats.PacketLossPercent == nil && stats.ErrorPercent == nil {
		return QualityCardGray, "no_data"
	}
	return QualityCardGreen, "normal"
}

func networkTCPSemanticsChange(ctx context.Context, tx *sql.Tx, id string, line NetworkLine, start, end int64) (*int64, error) {
	// Seek the earliest retained hour per target and compare the exact sample times.
	rows, err := tx.QueryContext(ctx, `SELECT s.semantics,(
  SELECT b.data FROM quality_buckets b WHERE b.series_id=s.id AND b.resolution=3600000
  ORDER BY b.bucket_start LIMIT 1
 )
 FROM quality_series s JOIN quality_targets t ON t.id=s.target_id
 WHERE s.agent_id=? AND s.semantics IN (?,?)
  AND json_extract(t.payload,'$.slot')=? AND json_extract(t.payload,'$.family')=?
  AND json_extract(t.payload,'$.protocol')='tcp'`,
		id, protocol.QualityTCPLossSemantics, protocol.QualityStagedSemantics, line.Slot, line.Family)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var old, fresh int64
	for rows.Next() {
		var sem string
		var wire []byte
		if err := rows.Scan(&sem, &wire); err != nil {
			return nil, err
		}
		if len(wire) == 0 {
			continue
		}
		b, err := qualityDecodeBucket(wire)
		if err != nil {
			return nil, err
		}
		if b.FirstTime <= 0 {
			continue
		}
		earliest := &old
		if sem == protocol.QualityTCPLossSemantics {
			earliest = &fresh
		}
		if *earliest == 0 || b.FirstTime < *earliest {
			*earliest = b.FirstTime
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if old == 0 || old >= fresh || fresh < start || fresh >= end {
		return nil, nil
	}
	return &fresh, nil
}

func networkCellThreshold(ctx context.Context, tx *sql.Tx, id string) (int, error) {
	var threshold int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT slow_threshold_ms FROM quality_config WHERE agent_id=?),?)`,
		id, protocol.QualitySlowThresholdDefaultMS).Scan(&threshold)
	return threshold, err
}
