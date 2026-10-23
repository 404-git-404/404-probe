package storage

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"404-probe/internal/protocol"
)

// The read side of the R25 judgement (design 1.6, 1.10): every call reads the stored 1-minute buckets, the
// stored gaps and the current lane configuration in ONE read transaction (the database has one connection,
// so nothing here may touch s.db while the transaction is open), builds the minute states of the lanes it
// needs and answers from them. Nothing is cached or persisted.

// qualityTimelineMaxMinutes bounds the minute states one lane may be given: 24 hours of marks, the read-ahead for
// the look-back of the red rule and the alignment to blocks. No request may ask for more.
const qualityTimelineMaxMinutes = 1440 + qualityJudgePrefetchMinutes + 6

// qualityTimelineHook lets tests observe how many minute states a request builds; nil in production.
var qualityTimelineHook func(minutes int)

const qualityMarksMaxSpanMS = int64(24 * 60 * 60 * 1000)

type QualityLaneRed struct {
	Active bool `json:"active"`
}

type QualityCardTCPLane struct {
	Status    string `json:"status"`
	Condition string `json:"condition"`
	// Blocks are the 20 blocks of an active lane; every other status has none, and no red state either.
	Blocks []QualityCardBlock `json:"blocks,omitempty"`
	Red    *QualityLaneRed    `json:"red,omitempty"`
	// DNSFailing is set when the lane's latest minutes hold only resolver failures: those samples are not
	// judged, so without this the lane would just look empty.
	DNSFailing bool `json:"dns_failing,omitempty"`
}

type QualityCardICMPLane struct {
	Status    string `json:"status"`
	Condition string `json:"condition"`
}

type QualityCardRight struct {
	Kind        string   `json:"kind"` // icmp_loss, tcp_failure or none
	Percent     *float64 `json:"percent,omitempty"`
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Samples     string   `json:"samples"` // ok, few or none
}

type QualityCardLane struct {
	Slot  string              `json:"slot"`
	TCP   QualityCardTCPLane  `json:"tcp"`
	ICMP  QualityCardICMPLane `json:"icmp"`
	Right QualityCardRight    `json:"right"`
}

// QualityCardSummary is the answer of v5/card: the face A of one card for one address family.
type QualityCardSummary struct {
	Now              int64             `json:"now"`
	EvalEnd          int64             `json:"eval_end"`
	Family           string            `json:"family"`
	SettingsRevision string            `json:"settings_revision"`
	ConfigRevision   string            `json:"config_revision"`
	ThresholdMS      int               `json:"threshold_ms"`
	IntervalMS       int               `json:"interval_ms"`
	BlockMS          int64             `json:"block_ms"`
	WindowStart      int64             `json:"window_start"`
	Lanes            []QualityCardLane `json:"lanes"`
	// AlertActive is the global alert: any IPv4 TCP lane is red now. It does not depend on Family.
	AlertActive bool `json:"alert_active"`
}

type QualityMarksLane struct {
	Slot string       `json:"slot"`
	Runs []QualityRun `json:"runs"`
}

// QualityMarks is the answer of v5/marks: the non-green, non-gray blocks of the TCP lanes of one family.
type QualityMarks struct {
	EvalEnd int64              `json:"eval_end"`
	Family  string             `json:"family"`
	From    int64              `json:"from"`
	To      int64              `json:"to"`
	Lanes   []QualityMarksLane `json:"lanes"`
}

// qualityJudgeReader holds what one request knows. Lane timelines are built when first asked for.
type qualityJudgeReader struct {
	tx       *sql.Tx
	id       string
	cfg      protocol.QualityV4Config
	evalEnd  int64
	first    int64 // first minute index read
	last     int64 // minute index after the last one read (exclusive): never beyond what the request needs
	removing bool  // a removal of the device is pending: nothing is judged
	opens    map[string]qualityOpenStart
	gaps     []protocol.QualityGap
	timeline map[string]qualityTimeline
}

type qualityOpenStart struct {
	target string
	start  int64
}

func (r *qualityJudgeReader) lane(slot, family, proto string) (protocol.QualityV4Lane, bool) {
	for _, l := range r.cfg.Lanes {
		if l.Slot == slot && l.Family == family && l.Protocol == proto {
			if r.removing {
				// The device is being removed: its lanes are not measuring, whatever the configuration says.
				l.Status, l.Condition = protocol.QualityLaneDeviceInactive, ""
			}
			return l, true
		}
	}
	return protocol.QualityV4Lane{}, false
}

// newQualityJudgeReader loads the configuration, the open intervals and the gaps of the minutes from
// firstMinute to the evaluation cut-off.
func (s *Store) newQualityJudgeReader(ctx context.Context, tx *sql.Tx, budget qualityBudget, id string, now time.Time, firstMinute, lastMinute int64) (*qualityJudgeReader, error) {
	if lastMinute-firstMinute > qualityTimelineMaxMinutes {
		return nil, ErrQualityInvalid
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrAgentNotFound
	}
	cfg, err := qualityV4ConfigTx(ctx, tx, id, now, budget)
	if err != nil {
		return nil, err
	}
	r := &qualityJudgeReader{tx: tx, id: id, cfg: cfg, evalEnd: QualityEvalEnd(now.UnixMilli()), first: firstMinute, last: lastMinute,
		opens: map[string]qualityOpenStart{}, timeline: map[string]qualityTimeline{}}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_removal_operations WHERE agent_id=?)`, id).Scan(&r.removing); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT slot,family,protocol,target_id,start_at FROM quality_intervals WHERE agent_id=? AND end_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l qualityLane
		var o qualityOpenStart
		if err := rows.Scan(&l.Slot, &l.Family, &l.Protocol, &o.target, &o.start); err != nil {
			rows.Close()
			return nil, err
		}
		r.opens[l.key()] = o
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	gapRows, err := tx.QueryContext(ctx, `SELECT start_at,end_at,reason FROM quality_gaps WHERE agent_id=? AND start_at<=? AND end_at>=? AND reason NOT LIKE 'storage_pruned:%' ORDER BY start_at LIMIT 4096`, id, lastMinute*qualityMinuteMS, firstMinute*qualityMinuteMS)
	if err != nil {
		return nil, err
	}
	defer gapRows.Close()
	for gapRows.Next() {
		var g protocol.QualityGap
		if err := gapRows.Scan(&g.Start, &g.End, &g.Reason); err != nil {
			return nil, err
		}
		r.gaps = append(r.gaps, g)
	}
	return r, gapRows.Err()
}

// timelineOf builds the minute states of an active lane; ok is false when the lane is not active.
func (r *qualityJudgeReader) timelineOf(ctx context.Context, slot, family, proto string) (qualityTimeline, bool, error) {
	key := slot + "/" + family + "/" + proto
	if tl, ok := r.timeline[key]; ok {
		return tl, true, nil
	}
	lane, found := r.lane(slot, family, proto)
	if !found || lane.Status != protocol.QualityLaneActive || lane.TargetID == nil {
		return qualityTimeline{}, false, nil
	}
	frame := qualityLaneFrame{ThresholdMS: r.cfg.SlowThresholdMS, TCP: proto == "tcp"}
	frame.SlotRevision, _ = strconv.ParseInt(lane.SlotRevision, 10, 64)
	if o, ok := r.opens[key]; ok && o.target == *lane.TargetID {
		frame.IntervalStart, frame.HasInterval = o.start, true
	}
	last := r.last
	buckets := map[int64]qualityBucketData{}
	rows, err := r.tx.QueryContext(ctx, `SELECT b.bucket_start,b.data FROM quality_series s CROSS JOIN quality_buckets b ON b.series_id=s.id WHERE s.agent_id=? AND s.target_id=? AND s.semantics=? AND b.resolution=60000 AND b.bucket_start>=? AND b.bucket_start<?`,
		r.id, *lane.TargetID, protocol.QualityStagedSemantics, r.first*qualityMinuteMS, last*qualityMinuteMS)
	if err != nil {
		return qualityTimeline{}, false, err
	}
	for rows.Next() {
		var start int64
		var data []byte
		if err := rows.Scan(&start, &data); err != nil {
			rows.Close()
			return qualityTimeline{}, false, err
		}
		b, err := qualityDecodeBucket(data)
		if err != nil {
			rows.Close()
			return qualityTimeline{}, false, err
		}
		buckets[start/qualityMinuteMS] = b
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return qualityTimeline{}, false, err
	}
	tl := qualityBuildTimeline(frame, r.first, last, buckets, r.gaps)
	if qualityTimelineHook != nil {
		qualityTimelineHook(len(tl.Mins))
	}
	r.timeline[key] = tl
	return tl, true, nil
}

// laneRed is the current red state of an active lane: the red rule at the last evaluated minute.
func (r *qualityJudgeReader) laneRed(tl *qualityTimeline) bool {
	return tl.redAt(r.evalEnd/qualityMinuteMS-1, r.cfg.EffectiveIntervalMS)
}

// alert is the global alert: the N6 gate and any red IPv4 TCP lane.
func (r *qualityJudgeReader) alert(ctx context.Context) (bool, error) {
	open, err := qualityAlertGate(ctx, r.tx, r.id)
	if err != nil || !open {
		return false, err
	}
	for _, slot := range protocol.QualitySlotOrder() {
		tl, ok, err := r.timelineOf(ctx, slot, "ipv4", "tcp")
		if err != nil {
			return false, err
		}
		if ok && r.laneRed(&tl) {
			return true, nil
		}
	}
	return false, nil
}

func qualityValidFamily(family string) bool { return family == "ipv4" || family == "ipv6" }

func (s *Store) qualityReadTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// QualityCardSummary is the face A of one card for one address family (design 1.10).
func (s *Store) QualityCardSummary(ctx context.Context, agentID, family string, now time.Time) (QualityCardSummary, error) {
	if !qualityValidFamily(family) {
		return QualityCardSummary{}, ErrQualityInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx) // it reads the database itself: never while the read transaction holds the one connection
	tx, err := s.qualityReadTx(ctx)
	if err != nil {
		return QualityCardSummary{}, err
	}
	defer tx.Rollback()
	evalEnd := QualityEvalEnd(now.UnixMilli())
	lastBlock := floorDiv64(evalEnd-1, QualityBlockMS) * QualityBlockMS
	windowStart := lastBlock - int64(QualityCardBlocks-1)*QualityBlockMS
	r, err := s.newQualityJudgeReader(ctx, tx, budget, agentID, now, windowStart/qualityMinuteMS-qualityJudgePrefetchMinutes, evalEnd/qualityMinuteMS)
	if err != nil {
		return QualityCardSummary{}, err
	}
	out := QualityCardSummary{Now: now.UnixMilli(), EvalEnd: evalEnd, Family: family, SettingsRevision: r.cfg.SettingsRevision, ConfigRevision: r.cfg.ConfigRevision,
		ThresholdMS: r.cfg.SlowThresholdMS, IntervalMS: r.cfg.EffectiveIntervalMS, BlockMS: QualityBlockMS, WindowStart: windowStart, Lanes: []QualityCardLane{}}
	for _, slot := range protocol.QualitySlotOrder() {
		lane := QualityCardLane{Slot: slot, Right: QualityCardRight{Kind: "none", Samples: "none"}}
		if l, ok := r.lane(slot, family, "tcp"); ok {
			lane.TCP.Status, lane.TCP.Condition = l.Status, l.Condition
		}
		if l, ok := r.lane(slot, family, "icmp"); ok {
			lane.ICMP.Status, lane.ICMP.Condition = l.Status, l.Condition
		}
		tcp, tcpActive, err := r.timelineOf(ctx, slot, family, "tcp")
		if err != nil {
			return out, err
		}
		if tcpActive {
			lane.TCP.Blocks = make([]QualityCardBlock, 0, QualityCardBlocks)
			for i := 0; i < QualityCardBlocks; i++ {
				lane.TCP.Blocks = append(lane.TCP.Blocks, tcp.classifyBlock(windowStart+int64(i)*QualityBlockMS, evalEnd, r.cfg.EffectiveIntervalMS))
			}
			lane.TCP.Red = &QualityLaneRed{Active: r.laneRed(&tcp)}
			lane.TCP.DNSFailing = qualityDNSFailing(&tcp, evalEnd)
		}
		icmp, icmpActive, err := r.timelineOf(ctx, slot, family, "icmp")
		if err != nil {
			return out, err
		}
		switch {
		case icmpActive:
			lane.Right = qualityICMPRight(&icmp, windowStart, evalEnd)
		case tcpActive:
			lane.Right = qualityTCPRight(&tcp, windowStart, evalEnd)
		}
		out.Lanes = append(out.Lanes, lane)
	}
	if out.AlertActive, err = r.alert(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// QualityAlertActive is the global alert of one Agent (design 1.9): the N6 gate and any IPv4 TCP lane that is
// red now. It is the single place the later alert channels (M16) will ask.
func (s *Store) QualityAlertActive(ctx context.Context, agentID string, now time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx)
	tx, err := s.qualityReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	evalEnd := QualityEvalEnd(now.UnixMilli())
	r, err := s.newQualityJudgeReader(ctx, tx, budget, agentID, now, evalEnd/qualityMinuteMS-qualityJudgePrefetchMinutes-3, evalEnd/qualityMinuteMS)
	if err != nil {
		return false, err
	}
	return r.alert(ctx)
}

// qualityMarksPlan decides what a marks request reads (design 1.10): the blocks from alignedFrom to end, the
// minutes needed to judge them (firstMinute .. lastMinute, exclusive) and nothing else. The range is clamped to the
// 24 hours of 1-minute buckets that exist and to the evaluation cut-off, so a request can never make the Server
// build a timeline longer than that, whatever from and to say. ok is false when nothing of the range is left.
func qualityMarksPlan(evalEnd, from, to int64) (alignedFrom, end, firstMinute, lastMinute int64, ok bool) {
	oldest := floorDiv64(evalEnd-qualityMarksMaxSpanMS, QualityBlockMS) * QualityBlockMS
	alignedFrom = max(floorDiv64(from, QualityBlockMS)*QualityBlockMS, oldest)
	end = min(to, evalEnd)
	if end <= alignedFrom {
		return alignedFrom, end, 0, 0, false
	}
	lastMinute = min(evalEnd/qualityMinuteMS, ceilDiv64(end, QualityBlockMS)*QualityBlockMS/qualityMinuteMS)
	return alignedFrom, end, alignedFrom/qualityMinuteMS - qualityJudgePrefetchMinutes, lastMinute, true
}

// QualityMarks is the face B marks: yellow and red runs of the TCP lanes of one family in [from, to]. The range
// is clamped to the retained history and the cut-off (qualityMarksPlan); the answer says what it covers.
func (s *Store) QualityMarks(ctx context.Context, agentID, family string, from, to int64, now time.Time) (QualityMarks, error) {
	if !qualityValidFamily(family) || from < 0 || to <= from || to-from > qualityMarksMaxSpanMS {
		return QualityMarks{}, ErrQualityInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx)
	tx, err := s.qualityReadTx(ctx)
	if err != nil {
		return QualityMarks{}, err
	}
	defer tx.Rollback()
	evalEnd := QualityEvalEnd(now.UnixMilli())
	alignedFrom, end, firstMinute, lastMinute, ok := qualityMarksPlan(evalEnd, from, to)
	if !ok {
		// Nothing of the range is retained or judged yet: read no minutes, but still answer for a real agent.
		firstMinute, lastMinute = evalEnd/qualityMinuteMS, evalEnd/qualityMinuteMS
	}
	r, err := s.newQualityJudgeReader(ctx, tx, budget, agentID, now, firstMinute, lastMinute)
	if err != nil {
		return QualityMarks{}, err
	}
	out := QualityMarks{EvalEnd: r.evalEnd, Family: family, From: alignedFrom, To: max(end, alignedFrom), Lanes: []QualityMarksLane{}}
	if !ok {
		return out, nil
	}
	for _, slot := range protocol.QualitySlotOrder() {
		tl, active, err := r.timelineOf(ctx, slot, family, "tcp")
		if err != nil {
			return out, err
		}
		if !active {
			continue
		}
		blocks := []QualityCardBlock{}
		for start := alignedFrom; start < end; start += QualityBlockMS {
			blocks = append(blocks, tl.classifyBlock(start, r.evalEnd, r.cfg.EffectiveIntervalMS))
		}
		out.Lanes = append(out.Lanes, QualityMarksLane{Slot: slot, Runs: qualityRuns(blocks)})
	}
	return out, nil
}

// qualityWindowMinutes calls f for every judged minute of the 20 blocks of the card.
func qualityWindowMinutes(tl *qualityTimeline, windowStart, evalEnd int64, f func(qualityMinute)) {
	for m := windowStart / qualityMinuteMS; (m+1)*qualityMinuteMS <= evalEnd; m++ {
		if x := tl.at(m); x.Kind != minuteUnusable {
			f(x)
		}
	}
}

func qualitySampleClass(n, few, none int) string {
	switch {
	case n < none:
		return "none"
	case n < few:
		return "few"
	}
	return "ok"
}

func qualityPercent(part, whole int) *float64 {
	if whole <= 0 {
		return nil
	}
	v := 100 * float64(part) / float64(whole)
	return &v
}

// qualityICMPRight is "packet loss" of the ICMP lane over the card window (design 1.8): complete echo windows
// only, which is what the bucket's Sent and Received hold.
func qualityICMPRight(tl *qualityTimeline, windowStart, evalEnd int64) QualityCardRight {
	sent, received := 0, 0
	qualityWindowMinutes(tl, windowStart, evalEnd, func(x qualityMinute) { sent += x.Sent; received += x.Received })
	lost := max(0, sent-received)
	out := QualityCardRight{Kind: "icmp_loss", Numerator: lost, Denominator: sent, Samples: qualitySampleClass(sent, 30, 3)}
	if sent > 0 {
		out.Percent = qualityPercent(lost, sent)
	}
	return out
}

// qualityTCPRight is the connection failure rate of the TCP lane (design 1.8): failed over judged samples.
func qualityTCPRight(tl *qualityTimeline, windowStart, evalEnd int64) QualityCardRight {
	judged, failed := 0, 0
	qualityWindowMinutes(tl, windowStart, evalEnd, func(x qualityMinute) { judged += x.J; failed += x.F })
	out := QualityCardRight{Kind: "tcp_failure", Numerator: failed, Denominator: judged, Samples: qualitySampleClass(judged, 10, 3)}
	if judged > 0 {
		out.Percent = qualityPercent(failed, judged)
	}
	return out
}

// qualityDNSFailing reports a lane whose three latest evaluated minutes hold resolver failures and nothing else.
func qualityDNSFailing(tl *qualityTimeline, evalEnd int64) bool {
	judged, dns := 0, 0
	last := evalEnd/qualityMinuteMS - 1
	for m := last - 2; m <= last; m++ {
		x := tl.at(m)
		if x.Kind == minuteUnusable {
			continue
		}
		judged += x.J
		dns += x.DNSFailures
	}
	return judged == 0 && dns > 0
}
