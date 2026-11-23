package storage

import (
	"context"
	"database/sql"
	"time"

	"404-probe/internal/protocol"
)

type NetworkSparkPoint struct {
	Start int64    `json:"start"`
	P50   *float64 `json:"p50_ms"`
}

type NetworkFleetLine struct {
	NetworkLine
	Target         *protocol.QualityTarget `json:"target"`
	Status         string                  `json:"status"`
	Stats          NetworkStats            `json:"stats"`
	LatestP50      *float64                `json:"latest_p50_ms"`
	LatestBucketAt *int64                  `json:"latest_bucket_at"`
	EveningP50     *float64                `json:"evening_p50_ms"`
	Spark          []NetworkSparkPoint     `json:"spark"`
	Alert          *NetworkAlert           `json:"alert"`
}

type NetworkMachine struct {
	ID                   string             `json:"agent_id"`
	Name                 string             `json:"name"`
	CountryCode          string             `json:"country_code"`
	ServerLocal          bool               `json:"server_local"`
	Status               string             `json:"status"`
	LastSeen             *int64             `json:"last_seen"`
	OfflineSince         *int64             `json:"offline_since"`
	ThresholdMS          int                `json:"slow_threshold_ms"`
	Alerts30Days         int                `json:"alerts_30d"`
	CurrentAlerts        int                `json:"current_alerts"`
	QualityClockOffsetMS *int64             `json:"quality_clock_offset_ms"`
	QualityClockSkewed   bool               `json:"quality_clock_skewed"`
	Lines                []NetworkFleetLine `json:"lines"`
}

type NetworkFleet struct {
	Start    int64            `json:"start"`
	End      int64            `json:"end"`
	Now      int64            `json:"now"`
	Machines []NetworkMachine `json:"machines"`
}

type networkFleetAccumulator struct {
	whole, evening qualityBucketData
	spark          []networkFleetSpark
	latest         []networkFleetBucket
	latestAt       int64
}

type networkFleetSpark struct {
	bucket qualityBucketData
	merged bool
}

func (s *networkFleetSpark) add(b qualityBucketData) {
	if s.bucket.Histogram == nil {
		s.bucket = b
		return
	}
	if !s.merged {
		// The first bucket's maps may still be used for the latest point.
		merged := qualityNewBucket()
		qualityMergeBucket(&merged, s.bucket)
		s.bucket, s.merged = merged, true
	}
	qualityMergeBucket(&s.bucket, b)
}

type networkBucketSpan struct{ start, end, resolution int64 }

// Use the largest stored buckets that fit inside the selected grid and a spark
// cell. Fringe buckets stay at the range resolution so they do not contribute
// whole hours outside a narrow selection.
func networkFleetSpans(start, end, res, sparkStart, step int64) []networkBucketSpan {
	spans := []networkBucketSpan{}
	first := start / res * res
	last := ceilDiv64(end, res) * res
	for at := first; at < last; {
		cellEnd := min(last, sparkStart+(1+(at-sparkStart)/step)*step)
		use := res
		for _, candidate := range [...]int64{networkFleetDayMS, networkFleetShortMS, qualityHourMS, 5 * qualityMinuteMS} {
			if candidate > res && at%candidate == 0 && at+candidate <= cellEnd {
				use = candidate
				break
			}
		}
		if i := len(spans) - 1; i >= 0 && spans[i].resolution == use {
			spans[i].end = at + use
		} else {
			spans = append(spans, networkBucketSpan{at, at + use, use})
		}
		at += use
	}
	return spans
}

func (s *Store) NetworkFleet(ctx context.Context, agentID, family, proto string, start, end int64, now time.Time, offlineTimeout time.Duration) (NetworkFleet, error) {
	out := NetworkFleet{Start: start, End: end, Now: now.UnixMilli(), Machines: []NetworkMachine{}}
	res, err := networkStatusResolution(start, end, now)
	if err != nil || (family != "" && family != "ipv4" && family != "ipv6") || (proto != "" && proto != "tcp" && proto != "icmp") {
		return out, ErrQualityInvalid
	}
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,COALESCE(s.country_code,''),l.agent_id IS NOT NULL,
		a.disabled_at IS NOT NULL,s.last_seen,COALESCE(c.slow_threshold_ms,?),
		CASE WHEN q.epoch=s.epoch AND q.session_id=s.session_id THEN q.received_at-q.sample_at END,
		(SELECT COUNT(*) FROM quality_alert_history h WHERE h.agent_id=a.id AND (h.end_at IS NULL OR h.end_at>=?))
		FROM agents a LEFT JOIN agent_state s ON s.agent_id=a.id
		LEFT JOIN quality_config c ON c.agent_id=a.id LEFT JOIN server_local_agent l ON l.agent_id=a.id
		LEFT JOIN quality_clock_estimates q ON q.agent_id=a.id
		WHERE a.revoked=0 AND (?='' OR a.id=?) ORDER BY a.name COLLATE NOCASE,a.id`,
		protocol.QualitySlowThresholdDefaultMS, now.Add(-QualityHistoryRetention).UnixMilli(), agentID, agentID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var machine NetworkMachine
		var paused bool
		if err := rows.Scan(&machine.ID, &machine.Name, &machine.CountryCode, &machine.ServerLocal, &paused, &machine.LastSeen,
			&machine.ThresholdMS, &machine.QualityClockOffsetMS, &machine.Alerts30Days); err != nil {
			rows.Close()
			return out, err
		}
		machine.Status = "offline"
		switch {
		case paused:
			machine.Status = "paused"
		case machine.LastSeen != nil:
			deadline := *machine.LastSeen + offlineTimeout.Milliseconds()
			if deadline >= out.Now {
				machine.Status = "online"
			} else {
				machine.OfflineSince = &deadline
			}
		}
		machine.QualityClockSkewed = QualityClockSkewed(machine.QualityClockOffsetMS)
		out.Machines = append(out.Machines, machine)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if agentID != "" && len(out.Machines) == 0 {
		return out, ErrAgentNotFound
	}

	first := start / res * res
	// Use an aligned chart grid for the long windows so each point can
	// reuse a complete fleet rollup with no change to the range edges.
	sparkResolution := res
	switch {
	case end-first > 7*networkFleetDayMS:
		sparkResolution = networkFleetDayMS
	case end-first >= 24*qualityHourMS:
		sparkResolution = networkFleetShortMS
	}
	sparkStart := first / sparkResolution * sparkResolution
	step := networkSparkStep(sparkStart, end, sparkResolution)
	spans := networkFleetSpans(start, end, res, sparkStart, step)
	raw, err := tx.PrepareContext(ctx, networkBucketSelect)
	if err != nil {
		return out, err
	}
	defer raw.Close()
	rollup, err := tx.PrepareContext(ctx, networkFleetRollupSelect)
	if err != nil {
		return out, err
	}
	defer rollup.Close()
	buckets := networkFleetBucketReader{raw: raw, rollup: rollup, staleBefore: networkFleetStaleBefore()}
	for i := range out.Machines {
		if err := fillNetworkMachine(ctx, tx, &out.Machines[i], NetworkLine{Family: family, Protocol: proto},
			sparkStart, end, step, spans, out.Now, buckets); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

func fillNetworkMachine(ctx context.Context, tx *sql.Tx, machine *NetworkMachine, filter NetworkLine, first, end, step int64,
	spans []networkBucketSpan, now int64, buckets networkFleetBucketReader) error {
	current, err := networkCurrentTargets(ctx, tx, machine.ID)
	if err != nil {
		return err
	}
	alerts, err := readNetworkAlerts(ctx, tx, machine.ID, now, true)
	if err != nil {
		return err
	}
	open := make(map[NetworkLine]NetworkAlert, len(alerts))
	for _, alert := range alerts {
		open[alert.NetworkLine] = alert
	}
	machine.CurrentAlerts = len(open)
	if machine.Status != "online" {
		machine.CurrentAlerts = 0
	}
	n := int(ceilDiv64(end-first, step))
	acc := map[NetworkLine]*networkFleetAccumulator{}
	for _, lane := range qualityLanes() {
		line := NetworkLine(lane)
		if (filter.Family == "" || line.Family == filter.Family) && (filter.Protocol == "" || line.Protocol == filter.Protocol) {
			acc[line] = &networkFleetAccumulator{whole: qualityNewBucket(), evening: qualityNewBucket(), spark: make([]networkFleetSpark, n)}
		}
	}
	for _, span := range spans {
		err := buckets.scan(ctx, machine.ID, filter, span,
			func(line NetworkLine, b networkFleetBucket) {
				a := acc[line]
				if a == nil {
					return
				}
				qualityMergeBucket(&a.whole, b.whole)
				if b.evening.Stats.Count > 0 {
					qualityMergeBucket(&a.evening, b.evening)
				}
				i := (b.at - first) / step
				a.spark[i].add(b.whole)
				if b.lastAt > a.latestAt {
					a.latestAt, a.latest = b.lastAt, a.latest[:0]
				}
				if b.lastAt == a.latestAt {
					a.latest = append(a.latest, b)
				}
			})
		if err != nil {
			return err
		}
	}
	recent := map[NetworkLine]qualityBucketData{}
	if machine.Status == "online" && len(current) > 0 {
		err := buckets.scanRaw(ctx, machine.ID, filter, networkBucketSpan{now - 15*qualityMinuteMS, now, 5 * qualityMinuteMS},
			func(line NetworkLine, target string, _ int64, b qualityBucketData) {
				if current[line] == nil || current[line].ID != target {
					return
				}
				a := recent[line]
				if a.Histogram == nil {
					a = qualityNewBucket()
				}
				qualityMergeBucket(&a, b)
				recent[line] = a
			})
		if err != nil {
			return err
		}
	}
	machine.Lines = make([]NetworkFleetLine, 0, len(acc))
	for _, lane := range qualityLanes() {
		line := NetworkLine(lane)
		a := acc[line]
		if a == nil {
			continue
		}
		out := NetworkFleetLine{NetworkLine: line, Target: current[line], Status: "no_data",
			Stats: networkStats(a.whole, line.Protocol), EveningP50: networkStats(a.evening, line.Protocol).P50,
			Spark: make([]NetworkSparkPoint, n)}
		for i, b := range a.spark {
			out.Spark[i] = NetworkSparkPoint{Start: first + int64(i)*step, P50: networkP50(b.bucket)}
		}
		if a.latestAt > 0 {
			var err error
			out.LatestBucketAt = &a.latestAt
			out.LatestP50, err = networkLatestP50(a.latest)
			if err != nil {
				return err
			}
		}
		if machine.Status == "online" {
			if alert, ok := open[line]; ok && out.Target != nil && alert.TargetID == out.Target.ID {
				out.Status, out.Alert = "alert", &alert
			} else {
				out.Status = networkCurrentStatus(networkStats(recent[line], line.Protocol), machine.ThresholdMS)
			}
		}
		machine.Lines = append(machine.Lines, out)
	}
	return nil
}

func networkLatestP50(rows []networkFleetBucket) (*float64, error) {
	last := qualityNewBucket()
	for _, row := range rows {
		b := row.whole
		if row.lastWire != nil {
			var err error
			b, err = qualityDecodeBucket(row.lastWire)
			if err != nil {
				return nil, err
			}
		}
		qualityMergeBucket(&last, b)
	}
	return networkP50(last), nil
}

func networkP50(b qualityBucketData) *float64 {
	if b.Stats.ValidRTTs == 0 {
		return nil
	}
	p50 := qualityHistogramQuantile(b.Histogram, b.Stats.ValidRTTs, .5)
	return &p50
}

func networkCurrentStatus(stats NetworkStats, threshold int) string {
	if stats.P50 == nil && stats.FailurePercent == nil && stats.PacketLossPercent == nil {
		return "no_data"
	}
	if (stats.P50 != nil && *stats.P50 > float64(threshold)) ||
		(stats.FailurePercent != nil && *stats.FailurePercent > 5) ||
		(stats.PacketLossPercent != nil && *stats.PacketLossPercent > 5) {
		return "slow"
	}
	return "normal"
}

func networkCurrentTargets(ctx context.Context, tx *sql.Tx, id string) (map[NetworkLine]*protocol.QualityTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.target_id,t.payload,COALESCE(s.kind,''),COALESCE(s.isp,'')
		FROM quality_intervals i JOIN quality_targets t ON t.id=i.target_id
		LEFT JOIN quality_slot_settings s ON s.agent_id=i.agent_id AND s.slot=i.slot
		WHERE i.agent_id=? AND i.end_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[NetworkLine]*protocol.QualityTarget{}
	for rows.Next() {
		var id, payload, kind, isp string
		if err := rows.Scan(&id, &payload, &kind, &isp); err != nil {
			return nil, err
		}
		target, err := qualityTargetFromPayload(id, payload)
		if err != nil {
			return nil, err
		}
		target.Kind, target.ISP = kind, isp
		out[NetworkLine{target.Slot, target.Family, target.Protocol}] = &target
	}
	return out, rows.Err()
}
