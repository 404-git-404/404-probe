package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"
)

type QualityStats struct {
	Count             int            `json:"count"`
	Attempts          int            `json:"attempts"`
	Successes         int            `json:"successes"`
	Failures          int            `json:"failures"`
	NotExecuted       int            `json:"not_executed"`
	Categories        map[string]int `json:"categories"`
	FailurePercent    *float64       `json:"failure_percent"`
	PacketLossPercent *float64       `json:"packet_loss_percent"`
	Sent              int            `json:"sent"`
	Received          int            `json:"received"`
	P50               *float64       `json:"p50_ms"`
	P95               *float64       `json:"p95_ms"`
	Spread            *float64       `json:"delay_variation_ms"`
}
type QualityBlock struct {
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	InProgress bool  `json:"in_progress"`
	Gap        bool  `json:"gap"`
	QualityStats
}
type QualityHistory struct {
	Target        protocol.QualityTarget  `json:"target"`
	Now           int64                   `json:"now"`
	Algorithm     string                  `json:"algorithm"`
	Partial       bool                    `json:"partial"`
	GapReason     string                  `json:"gap_reason"`
	Completeness  string                  `json:"completeness"`
	Gaps          []protocol.QualityGap   `json:"gaps"`
	LatestEpoch   string                  `json:"latest_epoch,omitempty"`
	LatestSession string                  `json:"latest_session,omitempty"`
	Latest        *protocol.QualitySample `json:"latest_success"`
	LatestAgeMS   *int64                  `json:"latest_age_ms"`
	LatestFailure *protocol.QualitySample `json:"latest_failure"`
	Stale         bool                    `json:"stale"`
	Recent        QualityStats            `json:"recent_5min"`
	Window        QualityStats            `json:"window"`
	Blocks        []QualityBlock          `json:"blocks"`
	RecentBlocks  []QualityBlock          `json:"recent_blocks"`
}

func (s *Store) QualityHistory(ctx context.Context, id, targetID string, hours int, now time.Time) (QualityHistory, error) {
	out := QualityHistory{Now: now.UnixMilli(), Algorithm: "linear-success-v1", Completeness: "unverified", Gaps: []protocol.QualityGap{}, Stale: true}
	if hours != 1 && hours != 24 {
		return out, ErrQualityInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	windowStart := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()
	var targetJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM quality_targets WHERE agent_id=? AND id=?`, id, targetID).Scan(&targetJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrAgentNotFound
		}
		return out, err
	}
	if err := json.Unmarshal([]byte(targetJSON), &out.Target); err != nil {
		return out, err
	}
	var currentRevision int64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM quality_slots WHERE agent_id=? AND target_id=?),0),EXISTS(SELECT 1 FROM quality_intervals i JOIN agents a ON a.id=i.agent_id WHERE i.agent_id=? AND i.target_id=? AND i.end_at IS NULL AND a.revoked=0 AND a.disabled_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_removal_operations o WHERE o.agent_id=a.id))`, id, targetID, id, targetID).Scan(&currentRevision, &active); err != nil {
		return out, err
	}
	out.Target.SlotRevision = strconv.FormatInt(currentRevision, 10)
	out.Target.Status = "paused"
	if active {
		out.Target.Status = "active"
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+qualitySampleColumns+` FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? ORDER BY sample_time,seq LIMIT ?`, id, targetID, windowStart, now.UnixMilli(), qualityRawDevice)
	if err != nil {
		return out, err
	}
	var samples []protocol.QualitySample
	for rows.Next() {
		sample, err := qualityReadSample(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		samples = append(samples, sample)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, success := range []int{1, 0} {
		var session string
		var epoch int64
		q := `SELECT ` + qualitySampleColumns + `,epoch,session_id FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? AND attempt=1 AND success=? ORDER BY sample_time DESC,seq DESC LIMIT 1`
		sample, err := qualityReadSample(tx.QueryRowContext(ctx, q, id, targetID, now.Add(-QualityRetention).UnixMilli(), now.UnixMilli(), success), &epoch, &session)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		if success == 1 {
			out.Latest = &sample
			out.LatestEpoch = strconv.FormatInt(epoch, 10)
			out.LatestSession = session
		} else {
			out.LatestFailure = &sample
		}
	}
	var currentEpoch int64
	var currentSession string
	err = tx.QueryRowContext(ctx, `SELECT epoch,session_id FROM agent_state WHERE agent_id=?`, id).Scan(&currentEpoch, &currentSession)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	intervalRows, err := tx.QueryContext(ctx, `SELECT start_at,end_at FROM quality_intervals WHERE agent_id=? AND target_id=? ORDER BY start_at LIMIT 4096`, id, targetID)
	if err != nil {
		return out, err
	}
	var priorEnd *int64
	for intervalRows.Next() {
		var start int64
		var end sql.NullInt64
		if err := intervalRows.Scan(&start, &end); err != nil {
			intervalRows.Close()
			return out, err
		}
		if priorEnd != nil && start > *priorEnd {
			out.Gaps = append(out.Gaps, protocol.QualityGap{Start: *priorEnd, End: start, Reason: "known_pause"})
		}
		if end.Valid {
			value := end.Int64
			priorEnd = &value
		} else {
			priorEnd = nil
		}
	}
	err = intervalRows.Err()
	intervalRows.Close()
	if err != nil {
		return out, err
	}
	if priorEnd != nil && *priorEnd < now.UnixMilli() {
		out.Gaps = append(out.Gaps, protocol.QualityGap{Start: *priorEnd, End: now.UnixMilli(), Reason: "known_pause"})
	}
	gapRows, err := tx.QueryContext(ctx, `SELECT start_at,end_at,reason,dropped FROM quality_gaps WHERE agent_id=? AND end_at>? AND start_at<? ORDER BY start_at LIMIT 4096`, id, windowStart, now.UnixMilli())
	if err != nil {
		return out, err
	}
	for gapRows.Next() {
		var gap protocol.QualityGap
		var dropped sql.NullInt64
		if err := gapRows.Scan(&gap.Start, &gap.End, &gap.Reason, &dropped); err != nil {
			gapRows.Close()
			return out, err
		}
		if dropped.Valid {
			n := int(dropped.Int64)
			gap.Dropped = &n
		}
		out.Gaps = append(out.Gaps, gap)
	}
	err = gapRows.Err()
	gapRows.Close()
	if err != nil {
		return out, err
	}
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT disabled_at FROM agents WHERE id=?`, id).Scan(&disabledAt); err != nil {
		return out, err
	}
	if disabledAt.Valid {
		out.Gaps = append(out.Gaps, protocol.QualityGap{Start: disabledAt.Int64, End: now.UnixMilli(), Reason: "known_pause"})
	}
	filtered := out.Gaps[:0]
	for _, gap := range out.Gaps {
		if gap.End <= windowStart || gap.Start >= now.UnixMilli() {
			continue
		}
		if gap.Start < windowStart {
			gap.Start = windowStart
		}
		if gap.End > now.UnixMilli() {
			gap.End = now.UnixMilli()
		}
		filtered = append(filtered, gap)
	}
	out.Gaps = filtered
	// Release snapshot/read connection before quantile sort or HTTP serialization.
	if err := tx.Rollback(); err != nil {
		return out, err
	}
	recentStart := now.Add(-5 * time.Minute).UnixMilli()
	var window, recent []protocol.QualitySample
	for i := range samples {
		sample := samples[i]
		if sample.StartedAt >= windowStart {
			window = append(window, sample)
		}
		if sample.StartedAt >= recentStart {
			recent = append(recent, sample)
		}
	}
	if out.Latest != nil {
		age := now.UnixMilli() - out.Latest.StartedAt
		out.LatestAgeMS = &age
		sr, _ := protocol.QualityInteger(out.Latest.SlotRevision)
		out.Stale = !active || sr != currentRevision || age > 36000 || out.LatestEpoch != strconv.FormatInt(currentEpoch, 10) || out.LatestSession != currentSession
	}
	out.Recent = qualityStats(recent)
	out.Window = qualityStats(window)
	interval := int64(60000)
	count := 60
	if hours == 24 {
		interval = 300000
		count = 288
	}
	out.Blocks = qualityBlocks(samples, now.UnixMilli(), interval, count)
	out.RecentBlocks = qualityBlocks(samples, now.UnixMilli(), 60000, 20)
	for i := range out.Blocks {
		for _, gap := range out.Gaps {
			if gap.Start < out.Blocks[i].End && gap.End > out.Blocks[i].Start {
				out.Blocks[i].Gap = true
			}
		}
		if out.Blocks[i].Gap {
			out.Partial = true
		}
	}
	for i := range out.RecentBlocks {
		for _, gap := range out.Gaps {
			if gap.Start < out.RecentBlocks[i].End && gap.End > out.RecentBlocks[i].Start {
				out.RecentBlocks[i].Gap = true
			}
		}
	}
	if len(out.Gaps) > 0 {
		out.Partial = true
		out.GapReason = "known_gap"
	} else if out.Partial {
		out.GapReason = "unknown_gap"
	}
	// Partial describes known/observed gaps. Completeness remains unverified: missing
	// scheduled work cannot be reconstructed from absent uploads or expected-two math.
	return out, nil
}

func qualityBlocks(samples []protocol.QualitySample, now, interval int64, count int) []QualityBlock {
	last := now / interval * interval
	start := last - int64(count-1)*interval
	groups := make([][]protocol.QualitySample, count)
	for _, sample := range samples {
		if sample.StartedAt >= start && sample.StartedAt < now {
			index := (sample.StartedAt - start) / interval
			if index >= 0 && index < int64(count) {
				groups[index] = append(groups[index], sample)
			}
		}
	}
	blocks := make([]QualityBlock, count)
	for i := range blocks {
		end := start + int64(i+1)*interval
		progress := i == count-1
		if end > now {
			end = now
		}
		stats := qualityStats(groups[i])
		blocks[i] = QualityBlock{Start: start + int64(i)*interval, End: end, InProgress: progress, Gap: stats.Count == 0 || stats.NotExecuted > 0, QualityStats: stats}
	}
	return blocks
}
func qualityStats(samples []protocol.QualitySample) QualityStats {
	out := QualityStats{Categories: map[string]int{}}
	values := []float64{}
	for _, s := range samples {
		out.Count++
		out.Categories[s.Outcome]++
		if s.Attempt() {
			out.Attempts++
			if s.Outcome != "success" {
				out.Failures++
			}
		} else {
			out.NotExecuted++
		}
		if s.Outcome == "success" && s.LatencyMS != nil {
			out.Successes++
			values = append(values, *s.LatencyMS)
		}
		if s.PacketDenominator() {
			out.Sent += s.Sent
			out.Received += s.Received
		}
	}
	if out.Attempts > 0 {
		v := 100 * float64(out.Failures) / float64(out.Attempts)
		out.FailurePercent = &v
	}
	if out.Sent > 0 {
		v := 100 * float64(out.Sent-out.Received) / float64(out.Sent)
		out.PacketLossPercent = &v
	}
	if len(values) > 0 {
		sort.Float64s(values)
		p50 := qualityPercentileSorted(values, .5)
		p95 := qualityPercentileSorted(values, .95)
		spread := p95 - p50
		out.P50 = &p50
		out.P95 = &p95
		out.Spread = &spread
	}
	return out
}

// Adapted from Komari pkg/metric/aggregate.go, commit 9812acfd1106f29fa2f6d193207b005396bc78e0.
// Copyright (c) 2025 Komari Moniter. MIT notice: network_quality_KOMARI_LICENSE.txt.
func qualityPercentileSorted(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}
	pos := p * float64(len(values)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return values[lower]
	}
	weight := pos - float64(lower)
	return values[lower]*(1-weight) + values[upper]*weight
}
