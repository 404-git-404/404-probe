package storage

import (
	"context"
	"database/sql"
	"time"

	"404-probe/internal/protocol"
)

const (
	NetworkStatusPointLimit = 2017
	networkSparkPointLimit  = 48
	qualityHourMS           = int64(3600000)
)

type NetworkLine struct {
	Slot     string `json:"slot"`
	Family   string `json:"family"`
	Protocol string `json:"protocol"`
}

func (l NetworkLine) valid() bool {
	return protocol.QualityObjectID(l.Slot) && protocol.QualityFamily(l.Family) && protocol.QualityProtocol(l.Protocol)
}

type NetworkStats struct {
	Samples           int              `json:"samples"`
	P50               *float64         `json:"p50_ms"`
	P95               *float64         `json:"p95_ms"`
	Mean              *float64         `json:"mean_ms"`
	Jitter            *float64         `json:"jitter_ms"`
	FailurePercent    *float64         `json:"failure_percent"`
	PacketLossPercent *float64         `json:"packet_loss_percent"`
	Categories        map[string]int   `json:"categories"`
	ErrorPercent      *float64         `json:"error_percent"`
	LossCount         int              `json:"loss_count"`
	LossAttempts      int              `json:"loss_attempts"`
	ErrorCount        int              `json:"error_count"`
	ErrorAttempts     int              `json:"error_attempts"`
	MaxMS             *float64         `json:"max_ms"`
	OldTCP            bool             `json:"old_tcp,omitempty"`
	RelaxedTCP        bool             `json:"relaxed_tcp,omitempty"`
	State             QualityCardState `json:"state,omitempty"`
	Reason            string           `json:"reason,omitempty"`
	Completeness      string           `json:"completeness"`
}

type NetworkPoint struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	NetworkStats
}

type NetworkTargetChange struct {
	At           int64  `json:"at"`
	FromTargetID string `json:"from_target_id"`
	ToTargetID   string `json:"to_target_id"`
	Estimated    bool   `json:"estimated"`
}

type NetworkHistory struct {
	NetworkLine
	Start             int64                 `json:"start"`
	End               int64                 `json:"end"`
	ResolutionMS      int64                 `json:"resolution_ms"`
	Stats             NetworkStats          `json:"stats"`
	EveningP50        *float64              `json:"evening_p50_ms"`
	Points            []NetworkPoint        `json:"points"`
	LastChangeAt      *int64                `json:"last_target_change_at"`
	SemanticsChangeAt *int64                `json:"semantics_change_at,omitempty"`
	Changes           []NetworkTargetChange `json:"target_changes"`
	Gaps              []protocol.QualityGap `json:"gaps"`
}

func networkStatusResolution(start, end int64, now time.Time) (int64, error) {
	at := now.UnixMilli()
	if start < at-QualityHistoryRetention.Milliseconds() || end > at || start >= end || start <= 0 {
		return 0, ErrQualityInvalid
	}
	switch age := at - start; {
	case age <= (24 * time.Hour).Milliseconds():
		return qualityMinuteMS, nil
	case age <= (7 * 24 * time.Hour).Milliseconds():
		return 5 * qualityMinuteMS, nil
	default:
		return qualityHourMS, nil
	}
}

func networkStats(bucket qualityBucketData, proto string) NetworkStats {
	stats := qualityBucketStats(bucket)
	out := NetworkStats{Samples: stats.Count, P50: stats.P50, P95: stats.P95, Mean: stats.Mean, Jitter: stats.Jitter,
		Categories: stats.Categories, Completeness: stats.Completeness}
	if out.Categories == nil {
		out.Categories = map[string]int{}
	}
	out.MaxMS = qualityMaximum(bucket.Histogram)
	if proto == "icmp" {
		out.PacketLossPercent = stats.PacketLossPercent
		out.LossCount, out.LossAttempts = stats.Sent-stats.Received, stats.Sent
	} else {
		out.OldTCP = stats.Count > bucket.TCP.Samples
		out.RelaxedTCP = bucket.TCP.Relaxed > 0
		out.LossCount, out.LossAttempts = bucket.TCP.Loss, bucket.TCP.Attempts
		out.ErrorCount, out.ErrorAttempts = bucket.TCP.Errors, bucket.TCP.Attempts+bucket.TCP.DNS
		out.PacketLossPercent = qualityPercent(out.LossCount, out.LossAttempts)
		out.ErrorPercent = qualityPercent(out.ErrorCount, out.ErrorAttempts)
		if bucket.TCP.Samples == 0 {
			dns := stats.Categories["dns_error"]
			out.ErrorCount = dns + stats.Categories["refused"] + stats.Categories["network_error"]
			if out.ErrorCount > 0 || stats.ConnectSuccesses > 0 {
				out.ErrorAttempts = stats.ConnectAttempts + dns
				out.ErrorPercent = qualityPercent(out.ErrorCount, out.ErrorAttempts)
			}
		}
	}
	if proto == "tcp" && bucket.TCP.Samples == 0 && stats.ConnectAttempts > 0 {
		v := 100 * float64(stats.ConnectAttempts-stats.ConnectSuccesses) / float64(stats.ConnectAttempts)
		out.FailurePercent = &v
	}
	return out
}

const networkBucketSelect = `WITH lanes AS MATERIALIZED (
	SELECT s.id,s.target_id,json_extract(t.payload,'$.slot') AS slot,
		json_extract(t.payload,'$.family') AS family,json_extract(t.payload,'$.protocol') AS protocol
	FROM quality_series s JOIN quality_targets t ON t.id=s.target_id WHERE s.agent_id=? AND (
  s.semantics=? OR s.semantics=? AND (? OR json_extract(t.payload,'$.protocol')='icmp')
 )
	)
	SELECT l.slot,l.family,l.protocol,l.target_id,b.bucket_start,b.data
	FROM lanes l CROSS JOIN quality_buckets b ON b.series_id=l.id
	WHERE b.resolution=? AND b.bucket_start>=? AND b.bucket_start<?
	AND (?='' OR l.slot=?) AND (?='' OR l.family=?) AND (?='' OR l.protocol=?)
	ORDER BY b.bucket_start,l.target_id`

// R39 uses staged measurements, whose TCP RTT excludes DNS and whose ICMP
// RTT represents individual echo replies. The legacy metric remains in v3.
func scanNetworkBuckets(ctx context.Context, tx *sql.Tx, id string, line NetworkLine, start, end, resolution int64,
	consume func(NetworkLine, string, int64, qualityBucketData)) error {
	rows, err := tx.QueryContext(ctx, networkBucketSelect, networkBucketArgs(id, line, start, end, resolution, true)...)
	return scanNetworkBucketRows(rows, err, consume)
}

func networkBucketArgs(id string, line NetworkLine, start, end, resolution int64, includeOldTCP bool) []any {
	return []any{id, protocol.QualityTCPLossSemantics, protocol.QualityStagedSemantics, includeOldTCP, resolution, start / resolution * resolution, end,
		line.Slot, line.Slot, line.Family, line.Family, line.Protocol, line.Protocol}
}

func scanNetworkBucketRows(rows *sql.Rows, err error, consume func(NetworkLine, string, int64, qualityBucketData)) error {
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lane NetworkLine
		var target string
		var at int64
		var wire []byte
		if err := rows.Scan(&lane.Slot, &lane.Family, &lane.Protocol, &target, &at, &wire); err != nil {
			return err
		}
		bucket, err := qualityDecodeBucket(wire)
		if err != nil {
			return err
		}
		consume(lane, target, at, bucket)
	}
	return rows.Err()
}

func (s *Store) beginNetworkStatusRead(ctx context.Context) (*sql.Tx, error) {
	db := s.networkStatusDB
	if db == nil {
		db = s.db
	}
	return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

func networkAgentExists(ctx context.Context, tx *sql.Tx, id string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAgentNotFound
	}
	return nil
}

var beijingTime = time.FixedZone("Asia/Shanghai", 8*3600)

func networkEvening(at int64) bool {
	hour := time.UnixMilli(at).In(beijingTime).Hour()
	return hour >= 20 && hour < 23
}

func (s *Store) NetworkHistory(ctx context.Context, id string, line NetworkLine, start, end int64, now time.Time) (NetworkHistory, error) {
	res, err := networkStatusResolution(start, end, now)
	if err != nil || !line.valid() {
		return NetworkHistory{}, ErrQualityInvalid
	}
	first := start / res * res
	n := int(ceilDiv64(end-first, res))
	if n > NetworkStatusPointLimit {
		return NetworkHistory{}, ErrQualityInvalid
	}
	out := NetworkHistory{NetworkLine: line, Start: start, End: end, ResolutionMS: res,
		Points: make([]NetworkPoint, n), Changes: []NetworkTargetChange{}, Gaps: []protocol.QualityGap{}}
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := networkAgentExists(ctx, tx, id); err != nil {
		return out, err
	}
	threshold, err := networkCellThreshold(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if line.Protocol == "tcp" {
		out.SemanticsChangeAt, err = networkTCPSemanticsChange(ctx, tx, id, line, start, end)
		if err != nil {
			return out, err
		}
	}
	whole, evening := qualityNewBucket(), qualityNewBucket()
	buckets := make([]qualityBucketData, n)
	err = scanNetworkBuckets(ctx, tx, id, line, start, end, res, func(_ NetworkLine, _ string, at int64, b qualityBucketData) {
		i := (at - first) / res
		if buckets[i].Histogram == nil {
			buckets[i] = b
		} else {
			qualityMergeBucket(&buckets[i], b)
		}
		qualityMergeBucket(&whole, b)
		if networkEvening(at) {
			qualityMergeBucket(&evening, b)
		}
	})
	if err != nil {
		return out, err
	}
	if err := loadNetworkGaps(ctx, tx, id, start, end, &out); err != nil {
		return out, err
	}
	if len(out.Gaps) > 0 {
		whole.Gaps++
	}
	out.Stats = networkStats(whole, line.Protocol)
	out.EveningP50 = networkStats(evening, line.Protocol).P50
	for i, bucket := range buckets {
		at := first + int64(i)*res
		for _, gap := range out.Gaps {
			if qualityGapOverlaps(at, at+res, gap) {
				bucket.Gaps++
				break
			}
		}
		stats := networkStats(bucket, line.Protocol)
		stats.State, stats.Reason = qualityCellColor(stats, threshold)
		out.Points[i] = NetworkPoint{at, at + res, stats}
	}
	changes, err := networkTargetChanges(ctx, tx, id, line, now.Add(-QualityHistoryRetention).UnixMilli(), now.UnixMilli()+1)
	if err != nil {
		return out, err
	}
	for _, change := range changes {
		out.LastChangeAt = &change.At
		if change.At >= start && change.At < end {
			out.Changes = append(out.Changes, change)
		}
	}
	return out, tx.Commit()
}

func loadNetworkGaps(ctx context.Context, tx *sql.Tx, id string, start, end int64, out *NetworkHistory) error {
	rows, err := tx.QueryContext(ctx, `SELECT start_at,end_at,reason,dropped FROM quality_gaps
		WHERE agent_id=? AND start_at<? AND end_at>=? ORDER BY start_at,id LIMIT 4096`, id, end, start)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gap protocol.QualityGap
		var dropped sql.NullInt64
		if err := rows.Scan(&gap.Start, &gap.End, &gap.Reason, &dropped); err != nil {
			return err
		}
		if dropped.Valid {
			count := int(dropped.Int64)
			gap.Dropped = &count
		}
		out.Gaps = append(out.Gaps, gap)
	}
	return rows.Err()
}

func networkTargetChanges(ctx context.Context, tx *sql.Tx, id string, line NetworkLine, start, end int64) ([]NetworkTargetChange, error) {
	rows, err := tx.QueryContext(ctx, `WITH activations AS (
		SELECT start_at at,target_id,revision,0 estimated FROM quality_target_history
		WHERE agent_id=?1 AND slot=?2 AND family=?3 AND protocol=?4 AND start_at<?5
		UNION ALL
		SELECT COALESCE(t.first_active_at,t.created_at),t.id,t.created_revision,1 FROM quality_targets t
		WHERE t.agent_id=?1 AND json_extract(t.payload,'$.slot')=?2 AND json_extract(t.payload,'$.family')=?3
		AND json_extract(t.payload,'$.protocol')=?4 AND COALESCE(t.first_active_at,t.created_at)<?5
		AND EXISTS(SELECT 1 FROM quality_series s WHERE s.target_id=t.id)
		AND NOT EXISTS(SELECT 1 FROM quality_target_history h WHERE h.agent_id=?1 AND h.slot=?2 AND h.family=?3 AND h.protocol=?4
			AND h.target_id=t.id AND h.start_at<=COALESCE(t.first_active_at,t.created_at))
	) SELECT at,target_id,estimated FROM activations ORDER BY at,revision,target_id`, id, line.Slot, line.Family, line.Protocol, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []NetworkTargetChange{}
	var previous string
	for rows.Next() {
		var c NetworkTargetChange
		if err := rows.Scan(&c.At, &c.ToTargetID, &c.Estimated); err != nil {
			return nil, err
		}
		if c.At >= start && previous != "" && c.ToTargetID != previous {
			c.FromTargetID = previous
			changes = append(changes, c)
		}
		previous = c.ToTargetID
	}
	return changes, rows.Err()
}

type NetworkHeatCell struct {
	Hour              int              `json:"hour"`
	Start             int64            `json:"start"`
	P50               *float64         `json:"p50_ms"`
	State             QualityCardState `json:"state"`
	Reason            string           `json:"reason"`
	OldTCP            bool             `json:"old_tcp,omitempty"`
	FailurePercent    *float64         `json:"failure_percent"`
	PacketLossPercent *float64         `json:"packet_loss_percent"`
	Samples           int              `json:"samples"`
}

type NetworkHeatDay struct {
	Date  string              `json:"date"`
	Hours [24]NetworkHeatCell `json:"hours"`
}

type NetworkHeatmap struct {
	NetworkLine
	Timezone string           `json:"timezone"`
	Days     []NetworkHeatDay `json:"days"`
}

func (s *Store) NetworkHeatmap(ctx context.Context, id string, line NetworkLine, days int, now time.Time) (NetworkHeatmap, error) {
	if !line.valid() || (days != 7 && days != 30) {
		return NetworkHeatmap{}, ErrQualityInvalid
	}
	out := NetworkHeatmap{NetworkLine: line, Timezone: beijingTime.String(), Days: make([]NetworkHeatDay, days)}
	local := now.In(beijingTime)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijingTime).AddDate(0, 0, 1-days)
	start := midnight.UnixMilli()
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := networkAgentExists(ctx, tx, id); err != nil {
		return out, err
	}
	threshold, err := networkCellThreshold(ctx, tx, id)
	if err != nil {
		return out, err
	}
	buckets := make([]qualityBucketData, days*24)
	if err := scanNetworkBuckets(ctx, tx, id, line, start, now.UnixMilli(), qualityHourMS, func(_ NetworkLine, _ string, at int64, b qualityBucketData) {
		i := (at - start) / qualityHourMS
		if buckets[i].Histogram == nil {
			buckets[i] = b
		} else {
			qualityMergeBucket(&buckets[i], b)
		}
	}); err != nil {
		return out, err
	}
	for day := range out.Days {
		out.Days[day].Date = midnight.AddDate(0, 0, day).Format(time.DateOnly)
		for h := range out.Days[day].Hours {
			i := day*24 + h
			stats := networkStats(buckets[i], line.Protocol)
			state, reason := qualityCellColor(stats, threshold)
			out.Days[day].Hours[h] = NetworkHeatCell{h, start + int64(i)*qualityHourMS, stats.P50, state, reason, stats.OldTCP, stats.FailurePercent, stats.PacketLossPercent, stats.Samples}
		}
	}
	return out, tx.Commit()
}

func networkSparkStep(first, end, resolution int64) int64 {
	return max(1, ceilDiv64(end-first, resolution*networkSparkPointLimit)) * resolution
}
