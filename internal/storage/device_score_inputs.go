package storage

import (
	"context"
	"database/sql"
	"time"

	"404-probe/internal/protocol"
)

const deviceScoreNetworkWindow = 7 * 24 * time.Hour
const deviceScoreCoverageBucket = 5 * qualityMinuteMS

type DeviceScoreLine struct {
	NetworkLine
	CoveredMS                 int64
	FailurePercent            *float64
	P50, P95, Jitter, Evening *float64
	AlertMS                   int64
}

type DeviceResourceAverage struct {
	CPU, RAM, Disk float64
}

type DeviceScoreInputs struct {
	Lines     []DeviceScoreLine
	Resources *DeviceResourceAverage
	Presence  PresenceHistory
}

// ReadDeviceScoreInputs keeps long history reads off the report writer. Each
// device's transaction releases the shared history reader before the next one.
func (s *Store) ReadDeviceScoreInputs(ctx context.Context, id string, now time.Time) (DeviceScoreInputs, error) {
	var out DeviceScoreInputs
	end := now.Truncate(5 * time.Minute).UnixMilli()
	start := end - deviceScoreNetworkWindow.Milliseconds()
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	out.Lines, err = readDeviceScoreLines(ctx, tx, id, start, end, now.UnixMilli())
	if err != nil {
		return out, err
	}
	var cpu, ram, disk sql.NullFloat64
	err = tx.QueryRowContext(ctx, `SELECT SUM(cpu_sum)/NULLIF(SUM(samples),0),
		SUM(ram_sum)/NULLIF(SUM(samples),0),SUM(disk_sum)/NULLIF(SUM(samples),0)
		FROM minute_metrics WHERE agent_id=? AND bucket>=? AND bucket<?`,
		id, now.Add(-deviceScoreNetworkWindow).Truncate(time.Minute).UnixMilli(),
		now.Truncate(time.Minute).UnixMilli()).Scan(&cpu, &ram, &disk)
	if err != nil {
		return out, err
	}
	if cpu.Valid && ram.Valid && disk.Valid {
		out.Resources = &DeviceResourceAverage{cpu.Float64, ram.Float64, disk.Float64}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Presence, err = s.AgentPresenceHistory(ctx, id, now.Add(-QualityHistoryRetention).UnixMilli(), now.UnixMilli(), now)
	return out, err
}

func preferredDeviceScoreLine(slot string, targets map[NetworkLine]*protocol.QualityTarget) (NetworkLine, bool) {
	for _, candidate := range [...]NetworkLine{
		{slot, "ipv4", "tcp"}, {slot, "ipv6", "tcp"}, {slot, "ipv4", "icmp"}, {slot, "ipv6", "icmp"},
	} {
		if targets[candidate] != nil {
			return candidate, true
		}
	}
	return NetworkLine{}, false
}

func readDeviceScoreLines(ctx context.Context, tx *sql.Tx, id string, start, end, now int64) ([]DeviceScoreLine, error) {
	targets, err := networkCurrentTargets(ctx, tx, id)
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	var configured bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT objects_configured FROM quality_config WHERE agent_id=?),0)`, id).Scan(&configured); err != nil {
		return nil, err
	}
	objects, err := qualityLoadSlotsTx(ctx, tx, id, qualityConfigRow{ObjectsConfigured: configured})
	if err != nil {
		return nil, err
	}
	lines := make([]DeviceScoreLine, 0, 3)
	acc := map[NetworkLine]*networkFleetAccumulator{}
	for _, object := range objects {
		if !object.ShowOnCard {
			continue
		}
		line, ok := preferredDeviceScoreLine(object.Slot, targets)
		if !ok {
			continue
		}
		covered, err := deviceScoreCoverage(ctx, tx, id, line, start, end)
		if err != nil {
			return nil, err
		}
		lines = append(lines, DeviceScoreLine{NetworkLine: line, CoveredMS: covered})
		if covered >= (end-start)/2 {
			acc[line] = &networkFleetAccumulator{whole: qualityNewBucket(), evening: qualityNewBucket()}
			if icmp := (NetworkLine{line.Slot, line.Family, "icmp"}); line.Protocol == "tcp" && targets[icmp] != nil {
				acc[icmp] = &networkFleetAccumulator{whole: qualityNewBucket(), evening: qualityNewBucket()}
			}
		}
		if len(lines) == 3 {
			break
		}
	}
	if len(acc) == 0 {
		return lines, nil
	}
	raw, err := tx.PrepareContext(ctx, networkBucketSelect)
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	rollup, err := tx.PrepareContext(ctx, networkFleetRollupSelect)
	if err != nil {
		return nil, err
	}
	defer rollup.Close()
	reader := networkFleetBucketReader{raw: raw, rollup: rollup, staleBefore: networkFleetStaleBefore(), newTCPOnly: true}
	for _, span := range networkFleetSpans(start, end, deviceScoreCoverageBucket, start, end-start) {
		err := reader.scan(ctx, id, NetworkLine{}, span, func(line NetworkLine, bucket networkFleetBucket) {
			if a := acc[line]; a != nil {
				qualityMergeBucket(&a.whole, bucket.whole)
				if bucket.evening.Stats.Count > 0 {
					qualityMergeBucket(&a.evening, bucket.evening)
				}
			}
		})
		if err != nil {
			return nil, err
		}
	}
	alerts, err := readNetworkAlerts(ctx, tx, id, now, false)
	if err != nil {
		return nil, err
	}
	for i := range lines {
		line := &lines[i]
		a := acc[line.NetworkLine]
		if a == nil {
			continue
		}
		stats := networkStats(a.whole, line.Protocol)
		line.P50, line.P95, line.Jitter = stats.P50, stats.P95, stats.Jitter
		line.Evening = networkP50(a.evening)
		line.FailurePercent = stats.PacketLossPercent
		if line.Protocol == "tcp" {
			var icmpLoss *float64
			if paired := acc[NetworkLine{line.Slot, line.Family, "icmp"}]; paired != nil {
				icmpLoss = networkStats(paired.whole, "icmp").PacketLossPercent
			}
			line.FailurePercent = deviceScoreTCPFailure(stats.PacketLossPercent, stats.ErrorPercent, icmpLoss)
			// Historical episodes remain visible in K4. The score only counts the
			// part of an episode within the time span of new TCP measurements.
			coveredStart := max(start, a.whole.FirstTime)
			coveredEnd := end
			if a.whole.LastTime > 0 {
				coveredEnd = min(end, (a.whole.LastTime/deviceScoreCoverageBucket+1)*deviceScoreCoverageBucket)
			}
			line.AlertMS = deviceScoreAlertDuration(alerts, line.NetworkLine, coveredStart, coveredEnd)
		}
	}
	return lines, nil
}

func deviceScoreCoverage(ctx context.Context, tx *sql.Tx, id string, line NetworkLine, start, end int64) (int64, error) {
	var buckets int64
	// Deduplicate overlapping targets during a switch. Empty time buckets
	// have no stored row and therefore do not count toward coverage.
	err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT b.bucket_start)
		FROM quality_series s JOIN quality_targets t ON t.id=s.target_id
		CROSS JOIN quality_buckets b ON b.series_id=s.id
		WHERE s.agent_id=? AND s.semantics=? AND b.resolution=? AND b.bucket_start>=? AND b.bucket_start<?
		AND json_extract(t.payload,'$.slot')=? AND json_extract(t.payload,'$.family')=?
		AND json_extract(t.payload,'$.protocol')=?`,
		id, deviceScoreSemantics(line.Protocol), deviceScoreCoverageBucket, start, end,
		line.Slot, line.Family, line.Protocol).Scan(&buckets)
	return buckets * deviceScoreCoverageBucket, err
}

func deviceScoreAlertDuration(alerts []NetworkAlert, line NetworkLine, start, end int64) int64 {
	// Alerts are read in reverse chronological order. Clamp each episode to
	// the score window and merge overlaps across target switches.
	var duration int64
	for _, alert := range alerts {
		if alert.NetworkLine != line {
			continue
		}
		to := end
		if alert.End != nil {
			to = min(to, *alert.End)
		}
		if from := max(start, alert.Start); from < to {
			duration += to - from
			end = from
		}
	}
	return duration
}

func deviceScoreSemantics(proto string) string {
	if proto == "tcp" {
		return protocol.QualityTCPLossSemantics
	}
	return protocol.QualityStagedSemantics
}

func deviceScoreTCPFailure(tcpLoss, errors, icmpLoss *float64) *float64 {
	if tcpLoss == nil && errors == nil {
		return nil
	}
	loss, errorRate := 0.0, 0.0
	for _, value := range []*float64{tcpLoss, icmpLoss} {
		if value != nil {
			loss = max(loss, *value)
		}
	}
	if errors != nil {
		errorRate = *errors
	}
	rate := min(100, loss+errorRate)
	return &rate
}
