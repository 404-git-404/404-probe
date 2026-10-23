package storage

import (
	"math"
	"sort"

	"404-probe/internal/protocol"
)

// The R25 network judgement (design v1.0.7 section 1): the pure part. Everything here works on minute
// states built from the stored 1-minute buckets; nothing reads the database and nothing is persisted. A
// request builds the minutes, then block colours, the red rule, the right-hand number and the global alert
// are all read from that one structure, so they cannot disagree.

const (
	// QualityBlockMS is the width of a card block, QualityCardBlocks the number of blocks on a card face.
	QualityBlockMS    = int64(180000)
	QualityCardBlocks = 20
	qualityMinuteMS   = int64(60000)
	// qualityEvalLagMS is how long a minute rests after it ended before it is judged (design 1.1.1).
	qualityEvalLagMS = int64(90000)
	// qualityJudgePrefetchMinutes is the history read in front of the first block: the longest look-back
	// of the red rule (3 x the 30 minute window of a 300 s interval) plus a margin.
	qualityJudgePrefetchMinutes = 96

	// The constants of the red rule (design 1.5.2). They live here and nowhere else.
	judgeDeadCoverMin   = 3.0 // "nothing connects" must last this many minutes
	judgeDeadMinFails   = 3   // any red needs at least this many bad samples
	judgeSlowCoverMin   = 5.0 // "mostly bad" must last this many minutes
	judgeSlowMinBad     = 3
	judgeLookbackFloor  = 10.0
	judgeLookbackPerI   = 6.0
	judgeFreshFloor     = 2.0
	judgeFreshPerI      = 2.0
	judgeDensity        = 0.5
	judgeIntervalWindow = 10 // minutes in which the check interval is read (design 1.1.3)
)

// QualityEvalEnd is the evaluation cut-off of a request made at now (design 1.1.1): minutes that ended at
// or before it are judged, later ones never. It is a multiple of one minute.
func QualityEvalEnd(nowMS int64) int64 {
	return floorDiv64(nowMS-qualityEvalLagMS, qualityMinuteMS) * qualityMinuteMS
}

func floorDiv64(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func ceilDiv64(a, b int64) int64 { return -floorDiv64(-a, b) }

// qualityMinuteKind is one of the three states of a minute (design 1.1.3).
type qualityMinuteKind uint8

const (
	// minuteEmpty: usable, no judged sample (nothing was scheduled or reported, or only samples that are
	// not judged such as DNS failures). It is normal and does not interrupt a run of failures by itself.
	minuteEmpty qualityMinuteKind = iota
	// minuteData: usable with at least one judged sample.
	minuteData
	// minuteUnusable: carries no evidence and interrupts runs (outside the interval, mixed versions, a
	// slot version that is not the current one, a known gap).
	minuteUnusable
)

// qualityMinute is what the judgement keeps of one minute.
type qualityMinute struct {
	Kind qualityMinuteKind
	// Outside marks an unusable minute that ended before the interval started: it is "not measured", not
	// "disturbed", so it does not make a block partial.
	Outside bool
	// J, B, F, S are the judged, bad (slow + failed), failed and slow samples (design 1.2).
	J, B, F, S int
	// Unjudged are the samples of the minute that were not judged (DNS failure, not executed, ...).
	Unjudged int
	// IntervalMS is the largest check interval of the minute's samples (0 when unknown).
	IntervalMS int
	// MaxBin is the highest non-empty histogram bin (grid index), -1 when there is no RTT.
	MaxBin int
	// DNSFailures counts the samples of the minute that failed in the resolver.
	DNSFailures int
	// Count, Sent, Received are the raw sample count and the complete ICMP echo windows of the minute.
	Count, Sent, Received int
}

// qualityTimeline is the dense minute states of one lane from minute index First (inclusive).
type qualityTimeline struct {
	First int64
	Mins  []qualityMinute
}

func (t *qualityTimeline) at(m int64) qualityMinute {
	i := m - t.First
	if i < 0 || i >= int64(len(t.Mins)) {
		return qualityMinute{Kind: minuteUnusable, Outside: true, MaxBin: -1}
	}
	return t.Mins[i]
}

// qualityLaneFrame is what decides whether a minute of a lane can be used.
type qualityLaneFrame struct {
	// SlotRevision is the lane's current slot version; minutes of another slot version are unusable.
	SlotRevision int64
	// IntervalStart is the start of the open interval of the lane (ms); HasInterval is false when there is none.
	IntervalStart int64
	HasInterval   bool
	// ThresholdMS is the slow threshold of the card (whole milliseconds).
	ThresholdMS int
	// TCP selects the TCP judgement (J, B, F, S); false reads only the ICMP echo counts.
	TCP bool
}

// qualityBinsAbove sums the histogram bins above 4T: a sample is slow when its 0.25 ms grid RTT is greater
// than T (design 1.3; the grid rounding is the one of the ingest).
func qualityBinsAbove(h map[uint16]uint32, thresholdMS int) int {
	limit := 4 * thresholdMS
	n := 0
	for bin, count := range h {
		if int(bin) > limit {
			n += int(count)
		}
	}
	return n
}

// qualityJudgeMinute turns one stored bucket (or none) into the minute state of a lane. start is the
// minute's start in ms; knownGap says that a stored gap overlaps the minute.
func qualityJudgeMinute(frame qualityLaneFrame, start int64, bucket *qualityBucketData, knownGap bool) qualityMinute {
	m := qualityMinute{Kind: minuteEmpty, MaxBin: -1}
	end := start + qualityMinuteMS
	if !frame.HasInterval {
		m.Kind, m.Outside = minuteUnusable, true
		return m
	}
	// The minute that holds the interval start is not usable either (it may mix older samples); only a
	// minute that ended before the interval began is plainly outside.
	if start < ceilDiv64(frame.IntervalStart, qualityMinuteMS)*qualityMinuteMS {
		m.Kind, m.Outside = minuteUnusable, end <= frame.IntervalStart
		return m
	}
	if knownGap {
		m.Kind = minuteUnusable
		return m
	}
	if bucket == nil {
		return m
	}
	if bucket.ConfigMin == 0 || bucket.ConfigMin != bucket.ConfigMax || bucket.SlotMin == 0 || bucket.SlotMin != bucket.SlotMax || bucket.SlotMin != frame.SlotRevision {
		m.Kind = minuteUnusable
		return m
	}
	s := bucket.Stats
	m.Count, m.IntervalMS = s.Count, bucket.IntervalMax
	m.Sent, m.Received = s.Sent, s.Received
	m.DNSFailures = s.Categories["dns_error"]
	for bin := range bucket.Histogram {
		if int(bin) > m.MaxBin {
			m.MaxBin = int(bin)
		}
	}
	if !frame.TCP {
		if s.Count > 0 {
			m.Kind = minuteData
		}
		return m
	}
	failed := max(0, s.ConnectAttempts-s.ConnectSuccesses)
	slow := min(qualityBinsAbove(bucket.Histogram, frame.ThresholdMS), s.ValidRTTs)
	m.J = s.ValidRTTs + failed
	m.F = failed
	m.S = slow
	m.B = slow + failed
	m.Unjudged = max(0, s.Count-m.J)
	if m.J > 0 {
		m.Kind = minuteData
	}
	return m
}

// qualityBuildTimeline builds the dense minute states of one lane over minute indexes [first, last).
func qualityBuildTimeline(frame qualityLaneFrame, first, last int64, buckets map[int64]qualityBucketData, gaps []protocol.QualityGap) qualityTimeline {
	n := last - first
	if n < 0 {
		n = 0
	}
	t := qualityTimeline{First: first, Mins: make([]qualityMinute, n)}
	gapped := make([]bool, n)
	for _, g := range gaps {
		// A gap overlaps minute m when it starts before the minute ends and ends after it starts; a point
		// gap belongs to the minute that contains it (the rule of qualityGapOverlaps).
		from := floorDiv64(g.Start, qualityMinuteMS)
		to := ceilDiv64(g.End, qualityMinuteMS) - 1
		if g.Start == g.End {
			to = from
		}
		for m := max(from, first); m <= to && m < last; m++ {
			gapped[m-first] = true
		}
	}
	for i := range t.Mins {
		m := first + int64(i)
		var bucket *qualityBucketData
		if b, ok := buckets[m]; ok {
			bucket = &b
		}
		t.Mins[i] = qualityJudgeMinute(frame, m*qualityMinuteMS, bucket, gapped[i])
	}
	return t
}

// qualityJudgeParams are the derived constants of the red rule for one check interval.
type qualityJudgeParams struct {
	look      float64
	lookN     int
	fresh     int
	gap       int
	coverUnit float64
	minJudged int
}

// qualityJudgeParamsFor derives the constants for a check interval of intervalMin minutes (design 1.5.2).
func qualityJudgeParamsFor(intervalMin float64) qualityJudgeParams {
	if intervalMin <= 0 {
		intervalMin = 0.5
	}
	look := math.Max(judgeLookbackFloor, judgeLookbackPerI*intervalMin)
	return qualityJudgeParams{
		look:      look,
		lookN:     int(math.Ceil(look)),
		fresh:     int(math.Ceil(math.Max(judgeFreshFloor, judgeFreshPerI*intervalMin))),
		gap:       max(1, int(math.Ceil(1.5*intervalMin))),
		coverUnit: math.Max(intervalMin, 1),
		minJudged: max(4, int(math.Ceil(judgeDensity*look/intervalMin))),
	}
}

// qualityIntervalAt is the check interval (minutes) the rule uses at minute t: the largest interval among the
// data minutes of the ten minutes up to t, or the configured one when there is none (design 1.1.3).
func (tl *qualityTimeline) intervalAt(t int64, fallbackMS int) float64 {
	best := 0
	for m := t - judgeIntervalWindow + 1; m <= t; m++ {
		if x := tl.at(m); x.Kind == minuteData && x.IntervalMS > best {
			best = x.IntervalMS
		}
	}
	if best == 0 {
		best = fallbackMS
	}
	if best <= 0 {
		best = int(protocol.QualityMinIntervalMS)
	}
	return float64(best) / float64(qualityMinuteMS)
}

// redAt is the one red rule (design 1.5.2): a stateless function of the minutes up to t. Fresh evidence is
// required relative to t itself, so for the current state t is the last evaluated minute and the last batch of
// failures cannot prove itself fresh for ever.
func (tl *qualityTimeline) redAt(t int64, fallbackIntervalMS int) bool {
	p := qualityJudgeParamsFor(tl.intervalAt(t, fallbackIntervalMS))
	freshFrom := t - int64(p.fresh) + 1
	fresh := false
	for m := freshFrom; m <= t; m++ {
		if tl.at(m).Kind == minuteData {
			fresh = true
			break
		}
	}
	if !fresh {
		return false
	}

	// 1. Nothing connects: the trailing run of dead minutes (every judged sample failed).
	var run []int64
	prev := int64(0)
	havePrev := false
	for m := t; m >= t-int64(p.look*3); m-- {
		x := tl.at(m)
		if x.Kind != minuteData {
			continue
		}
		if x.F != x.J {
			break
		}
		if havePrev {
			if prev-m > int64(p.gap) {
				break
			}
			interrupted := false
			for between := m + 1; between < prev; between++ {
				if tl.at(between).Kind == minuteUnusable {
					interrupted = true
					break
				}
			}
			if interrupted {
				break
			}
		}
		run = append(run, m)
		prev, havePrev = m, true
	}
	if len(run) > 0 {
		fails := 0
		for _, m := range run {
			fails += tl.at(m).F
		}
		cover := float64(run[0]-run[len(run)-1]) + p.coverUnit
		if fails >= judgeDeadMinFails && cover >= judgeDeadCoverMin {
			return true
		}
	}

	// 2. Mostly bad: the bad samples are the strict majority of a dense look-back window and span at least 5 minutes.
	// The window is the look-back minutes after the most recent unusable minute: like a run of failures, it
	// does not reach across a break (a gap, a mixed bucket, an older slot version, the interval start).
	lo := t - int64(p.lookN) + 1
	n, bad := 0, 0
	firstBad, lastBad := int64(0), int64(0)
	haveBad := false
	for m := t; m >= lo; m-- {
		x := tl.at(m)
		if x.Kind == minuteUnusable {
			break
		}
		if x.Kind != minuteData {
			continue
		}
		n += x.J
		bad += x.B
		if x.B > 0 {
			if !haveBad {
				lastBad, haveBad = m, true
			}
			firstBad = m
		}
	}
	if n >= p.minJudged && bad >= judgeSlowMinBad && 2*bad > n && haveBad {
		return float64(lastBad-firstBad)+p.coverUnit >= judgeSlowCoverMin
	}
	return false
}

// QualityCardState is the colour of a 3-minute block.
type QualityCardState string

const (
	QualityCardGray   QualityCardState = "gray"
	QualityCardGreen  QualityCardState = "green"
	QualityCardYellow QualityCardState = "yellow"
	QualityCardRed    QualityCardState = "red"
)

// QualityCardBlock is one 3-minute block of a TCP lane (design 1.10).
type QualityCardBlock struct {
	Start      int64            `json:"start"`
	State      QualityCardState `json:"state"`
	Judged     int              `json:"judged"`
	Slow       int              `json:"slow"`
	Failed     int              `json:"failed"`
	Unjudged   int              `json:"unjudged"`
	MaxMS      *float64         `json:"max_ms,omitempty"`
	InProgress bool             `json:"in_progress"`
	Partial    bool             `json:"partial"`
}

// qualityMinSamples is the "enough samples" threshold of a block: half of the samples a block should hold
// at the given check interval, at least one (design 1.4).
func qualityMinSamples(intervalMS int) int {
	if intervalMS <= 0 {
		intervalMS = int(protocol.QualityMinIntervalMS)
	}
	expected := float64(QualityBlockMS) / float64(intervalMS)
	return max(1, int(math.Ceil(expected/2)))
}

// classifyBlock colours the block that starts at blockStart (ms, a multiple of QualityBlockMS). Only minutes
// that ended at or before evalEnd take part; the block is in progress when it ends after evalEnd.
func (tl *qualityTimeline) classifyBlock(blockStart, evalEnd int64, fallbackIntervalMS int) QualityCardBlock {
	b := QualityCardBlock{Start: blockStart, State: QualityCardGray, InProgress: blockStart+QualityBlockMS > evalEnd}
	first := blockStart / qualityMinuteMS
	intervalMS := 0
	red := false
	maxBin := -1
	for i := int64(0); i < QualityBlockMS/qualityMinuteMS; i++ {
		m := first + i
		if (m+1)*qualityMinuteMS > evalEnd {
			break // this minute is not judged yet
		}
		x := tl.at(m)
		if x.Kind == minuteUnusable {
			if !x.Outside {
				b.Partial = true
			}
			continue
		}
		b.Judged += x.J
		b.Slow += x.S
		b.Failed += x.F
		b.Unjudged += x.Unjudged
		if x.MaxBin > maxBin {
			maxBin = x.MaxBin
		}
		if x.IntervalMS > intervalMS {
			intervalMS = x.IntervalMS
		}
		if !red && tl.redAt(m, fallbackIntervalMS) {
			red = true
		}
	}
	if maxBin >= 0 {
		// The upper edge of the highest non-empty bin: a bin k covers [0.25k-0.125, 0.25k+0.125).
		v := float64(maxBin)*protocolGrid + protocolGrid/2
		b.MaxMS = &v
	}
	if intervalMS == 0 {
		intervalMS = fallbackIntervalMS
	}
	switch {
	case b.Judged == 0:
		b.State = QualityCardGray
	case red:
		b.State = QualityCardRed
	case b.Judged < qualityMinSamples(intervalMS):
		b.State = QualityCardGray
	case b.Slow+b.Failed == 0:
		b.State = QualityCardGreen
	default:
		b.State = QualityCardYellow
	}
	return b
}

const protocolGrid = QualityHistogramGridMS

// qualityRun is a stretch of consecutive blocks with the same non-green colour, for the marks of face B.
type QualityRun struct {
	Start int64            `json:"start"`
	End   int64            `json:"end"`
	State QualityCardState `json:"state"`
}

// qualityRuns merges consecutive yellow or red blocks of the same colour into runs.
func qualityRuns(blocks []QualityCardBlock) []QualityRun {
	runs := []QualityRun{}
	for _, b := range blocks {
		if b.State != QualityCardYellow && b.State != QualityCardRed {
			continue
		}
		if n := len(runs); n > 0 && runs[n-1].End == b.Start && runs[n-1].State == b.State {
			runs[n-1].End = b.Start + QualityBlockMS
			continue
		}
		runs = append(runs, QualityRun{Start: b.Start, End: b.Start + QualityBlockMS, State: b.State})
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Start < runs[j].Start })
	return runs
}
