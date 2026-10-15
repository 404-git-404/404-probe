package storage

import (
	"404-probe/internal/protocol"
	"context"
	"math"
	"sort"
	"time"
)

type QualityStatsV2 struct {
	Count                 int            `json:"count"`
	ValidRTTs             int            `json:"valid_rtts"`
	ProbeAttempts         int            `json:"probe_attempts"`
	ProbeSuccesses        int            `json:"probe_successes"`
	ConnectAttempts       int            `json:"connect_attempts"`
	ConnectSuccesses      int            `json:"connect_successes"`
	NotExecuted           int            `json:"not_executed"`
	Categories            map[string]int `json:"categories"`
	Sent                  int            `json:"sent"`
	Received              int            `json:"received"`
	PacketLossPercent     *float64       `json:"packet_loss_percent"`
	ProbeSuccessPercent   *float64       `json:"probe_success_percent"`
	ConnectSuccessPercent *float64       `json:"connect_success_percent"`
	P50                   *float64       `json:"p50_ms"`
	P95                   *float64       `json:"p95_ms"`
	Mean                  *float64       `json:"mean_ms"`
	Spread                *float64       `json:"p95_minus_p50_ms"`
	Jitter                *float64       `json:"jitter_ms"`
	JitterPairs           int            `json:"jitter_pairs"`
	Completeness          string         `json:"completeness"`
	ExecutionCoverage     string         `json:"execution_coverage"`
}
type QualityBlockV2 struct {
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	InProgress bool  `json:"in_progress"`
	QualityStatsV2
}
type QualityHistoryGroup struct {
	Semantics           string                  `json:"semantics"`
	Metric              string                  `json:"metric"`
	Window              QualityStatsV2          `json:"window"`
	Recent              QualityStatsV2          `json:"recent_5min"`
	Blocks              []QualityBlockV2        `json:"blocks"`
	Latest              *protocol.QualitySample `json:"latest_probe"`
	Stale               bool                    `json:"stale"`
	FreshnessBudgetMS   int64                   `json:"freshness_budget_ms"`
	ObservedUploadLagMS int64                   `json:"observed_upload_lag_ms"`
}
type QualityHistoryV2 struct {
	HistogramGridMS      float64                `json:"histogram_grid_ms,omitempty"`
	PercentileMaxErrorMS float64                `json:"percentile_max_error_ms,omitempty"`
	WindowStart          int64                  `json:"window_start,omitempty"`
	Target               protocol.QualityTarget `json:"target"`
	Now                  int64                  `json:"now"`
	Algorithm            string                 `json:"algorithm"`
	Gaps                 []protocol.QualityGap  `json:"gaps"`
	Groups               []QualityHistoryGroup  `json:"groups"`
	Truncated            bool                   `json:"truncated"`
}
type qualityHistoryRow struct {
	position int
	sample   protocol.QualitySample
	epoch    int64
	session  string
	received int64
}

func qualityStatsV2(rows []qualityHistoryRow, proto string, gaps []protocol.QualityGap, start, end int64) QualityStatsV2 {
	// Completeness applies to observed measurement windows, not absent jobs.
	// Expected scheduler coverage cannot be inferred from uploads or counts.
	o := QualityStatsV2{Categories: map[string]int{}, Completeness: "complete", ExecutionCoverage: "unverified"}
	values := []float64{}
	jitterSum := 0.0
	var prior *qualityHistoryRow
	crossesGap := func(a, b int64) bool {
		for _, g := range gaps {
			if g.Start <= b && g.End >= a {
				return true
			}
		}
		return false
	}
	for i := range rows {
		r := &rows[i]
		s := r.sample
		o.Count++
		o.Categories[s.Outcome]++
		d, err := protocol.DecodeQualityDetails(s.Details)
		if err != nil {
			o.NotExecuted++
			o.Completeness = "partial"
			prior = nil
			continue
		}
		complete := s.Attempt()
		success := s.Outcome == "success"
		if d != nil {
			complete = d.ProbeComplete
		}
		if complete {
			o.ProbeAttempts++
			if success {
				o.ProbeSuccesses++
			}
		} else {
			o.NotExecuted++
			o.Completeness = "partial"
		}
		if proto == "icmp" && d != nil {
			if d.WindowComplete && s.Sent > 0 {
				o.Sent += s.Sent
				o.Received += s.Received
			}
			for j, p := range d.Echoes {
				if p.Status == "reply" && p.RTTMS != nil {
					values = append(values, *p.RTTMS)
					if j > 0 && d.Echoes[j-1].Status == "reply" && d.Echoes[j-1].RTTMS != nil {
						jitterSum += math.Abs(*p.RTTMS - *d.Echoes[j-1].RTTMS)
						o.JitterPairs++
					}
				}
			}
			prior = nil
			continue
		}
		if proto == "icmp" && s.PacketDenominator() {
			o.Sent += s.Sent
			o.Received += s.Received
		}
		connectComplete := complete
		if d != nil {
			connectComplete = d.ConnectComplete
		}
		if proto == "tcp" && connectComplete {
			o.ConnectAttempts++
			if success {
				o.ConnectSuccesses++
			}
		}
		if !success || s.LatencyMS == nil || !complete {
			prior = nil
			continue
		}
		values = append(values, *s.LatencyMS)
		if prior != nil {
			cadence := int64(30000)
			if d != nil && d.IntervalMS > 0 {
				cadence = int64(d.IntervalMS)
			}
			p := prior.sample
			delta := s.StartedAt - p.StartedAt
			if r.position == prior.position+1 && r.epoch == prior.epoch && r.session == prior.session && s.SlotRevision == p.SlotRevision && s.ConfigRevision == p.ConfigRevision && s.ResolvedIP == p.ResolvedIP && delta > 0 && delta <= cadence*3/2 && !crossesGap(p.StartedAt, s.StartedAt) {
				jitterSum += math.Abs(*s.LatencyMS - *p.LatencyMS)
				o.JitterPairs++
			}
		}
		prior = r
	}
	o.ValidRTTs = len(values)
	if len(values) > 0 {
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		mean := sum / float64(len(values))
		o.Mean = &mean
		sort.Float64s(values)
		p50 := qualityPercentileSorted(values, .5)
		p95 := qualityPercentileSorted(values, .95)
		spread := p95 - p50
		o.P50 = &p50
		o.P95 = &p95
		o.Spread = &spread
	}
	if o.JitterPairs > 0 {
		j := jitterSum / float64(o.JitterPairs)
		o.Jitter = &j
	}
	if o.Sent > 0 {
		v := 100 * float64(o.Sent-o.Received) / float64(o.Sent)
		o.PacketLossPercent = &v
	}
	if o.ProbeAttempts > 0 {
		v := 100 * float64(o.ProbeSuccesses) / float64(o.ProbeAttempts)
		o.ProbeSuccessPercent = &v
	}
	if o.ConnectAttempts > 0 {
		v := 100 * float64(o.ConnectSuccesses) / float64(o.ConnectAttempts)
		o.ConnectSuccessPercent = &v
	}
	if o.Count == 0 {
		o.Completeness = "no_data"
	} else {
		for _, gap := range gaps {
			if qualityGapOverlaps(start, end, gap) {
				o.Completeness = "partial"
				o.ExecutionCoverage = "known_gap"
				break
			}
		}
	}
	return o
}

func (s *Store) qualityHistoryV2Raw(ctx context.Context, id, targetID string, hours int, now time.Time) (QualityHistoryV2, error) {
	out := QualityHistoryV2{Now: now.UnixMilli(), Algorithm: "linear-valid-rtt-adjacent-v2", Groups: []QualityHistoryGroup{}}
	base, err := s.qualityHistoryRaw(ctx, id, targetID, hours, now)
	if err != nil {
		return out, err
	}
	out.Target = base.Target
	out.Gaps = base.Gaps
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := now.Add(-time.Duration(hours) * time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT `+qualitySampleColumns+`,epoch,session_id,received_at FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? ORDER BY sample_time,seq LIMIT 110001`, id, targetID, start, now.UnixMilli())
	if err != nil {
		return out, err
	}
	groups := map[string][]qualityHistoryRow{}
	count := 0
	for rows.Next() {
		var r qualityHistoryRow
		r.sample, err = qualityReadSample(rows, &r.epoch, &r.session, &r.received)
		if err != nil {
			rows.Close()
			return out, err
		}
		count++
		r.position = count
		if count > 110000 {
			out.Truncated = true
			break
		}
		sem := protocol.QualityV2Semantics
		if r.sample.Staged() {
			sem = protocol.QualityStagedSemantics
		}
		groups[sem] = append(groups[sem], r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	var epoch int64
	var session string
	if err = s.db.QueryRowContext(ctx, `SELECT epoch,session_id FROM agent_state WHERE agent_id=?`, id).Scan(&epoch, &session); err != nil {
		return out, err
	}
	for _, sem := range []string{protocol.QualityV2Semantics, protocol.QualityStagedSemantics} {
		records, ok := groups[sem]
		if !ok {
			continue
		}
		metric := "single_echo_rtt"
		if out.Target.Protocol == "tcp" {
			metric = "legacy_dial_including_dns"
		}
		if sem == protocol.QualityStagedSemantics {
			metric = "echo_rtt"
			if out.Target.Protocol == "tcp" {
				metric = "pure_connect"
			}
		}
		group := QualityHistoryGroup{Semantics: sem, Metric: metric, Stale: true, FreshnessBudgetMS: 76000}
		group.Window = qualityStatsV2(records, out.Target.Protocol, out.Gaps, start, now.UnixMilli())
		recent := []qualityHistoryRow{}
		for _, r := range records {
			if r.sample.StartedAt >= now.Add(-5*time.Minute).UnixMilli() {
				recent = append(recent, r)
			}
		}
		group.Recent = qualityStatsV2(recent, out.Target.Protocol, out.Gaps, now.Add(-5*time.Minute).UnixMilli(), now.UnixMilli())
		interval := int64(60000)
		n := 60
		if hours == 24 {
			interval = 300000
			n = 288
		}
		first := now.UnixMilli()/interval*interval - int64(n-1)*interval
		for i := 0; i < n; i++ {
			a := first + int64(i)*interval
			b := a + interval
			progress := i == n-1
			if b > now.UnixMilli() {
				b = now.UnixMilli()
			}
			subset := []qualityHistoryRow{}
			for _, r := range records {
				if r.sample.StartedAt >= a && r.sample.StartedAt < b {
					subset = append(subset, r)
				}
			}
			group.Blocks = append(group.Blocks, QualityBlockV2{a, b, progress, qualityStatsV2(subset, out.Target.Protocol, out.Gaps, a, b)})
		}
		for i := len(records) - 1; i >= 0; i-- {
			r := records[i]
			group.Latest = &r.sample
			cadence := int64(30000)
			if d, _ := protocol.DecodeQualityDetails(r.sample.Details); d != nil && d.IntervalMS > 0 {
				cadence = int64(d.IntervalMS)
			}
			lag := max(int64(0), r.received-r.sample.FinishedAt)
			group.ObservedUploadLagMS = lag
			// Maximum lane jitter + round timeout + upload period + HTTP budget.
			// Observed upload failures remain gaps; they never extend freshness.
			group.FreshnessBudgetMS = cadence*6/5 + 5000 + 30000 + 5000
			age := now.UnixMilli() - r.sample.StartedAt
			group.Stale = !r.sample.Attempt() || r.position != count || out.Target.Status != "active" || r.sample.SlotRevision != out.Target.SlotRevision || r.epoch != epoch || r.session != session || age > group.FreshnessBudgetMS
			break
		}
		if out.Truncated {
			group.Window.Completeness = "partial"
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}
