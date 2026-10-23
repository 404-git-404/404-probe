package storage

import (
	"fmt"
	"math"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// ---------- helpers: hand-made minute timelines ----------

// tlOf makes a timeline of n minutes, all empty (usable, no judged sample), from minute index 0.
func tlOf(n int) *qualityTimeline {
	tl := &qualityTimeline{First: 0, Mins: make([]qualityMinute, n)}
	for i := range tl.Mins {
		tl.Mins[i] = qualityMinute{Kind: minuteEmpty, MaxBin: -1}
	}
	return tl
}

// data sets a minute with j judged, b bad and f failed samples; the interval is the given one (ms).
func (tl *qualityTimeline) data(m int, j, b, f int, intervalMS int) *qualityTimeline {
	tl.Mins[m] = qualityMinute{Kind: minuteData, J: j, B: b, F: f, S: b - f, IntervalMS: intervalMS, MaxBin: -1}
	return tl
}

func (tl *qualityTimeline) unusable(ms ...int) *qualityTimeline {
	for _, m := range ms {
		tl.Mins[m] = qualityMinute{Kind: minuteUnusable, MaxBin: -1}
	}
	return tl
}

// fill sets minutes [from, to) to the same data minute.
func (tl *qualityTimeline) fill(from, to, j, b, f, intervalMS int) *qualityTimeline {
	for m := from; m < to; m++ {
		tl.data(m, j, b, f, intervalMS)
	}
	return tl
}

func (tl *qualityTimeline) red(m int, interval int) bool { return tl.redAt(int64(m), interval) }

func (tl *qualityTimeline) redMinutes(interval int) []int {
	var out []int
	for m := range tl.Mins {
		if tl.Mins[m].Kind != minuteUnusable && tl.red(m, interval) {
			out = append(out, m)
		}
	}
	return out
}

const (
	i30  = 30000
	i60  = 60000
	i120 = 120000
	i300 = 300000
)

// ---------- evaluation cut-off (review B2) ----------

func TestQualityEvalEndFollowsTheNinetySecondRule(t *testing.T) {
	at := func(h, m, s int) int64 { return time.Date(2026, 10, 23, h, m, s, 0, time.UTC).UnixMilli() }
	for _, c := range []struct {
		now, want int64
		note      string
	}{
		{at(12, 0, 10), at(11, 58, 0), "now=12:00:10"},
		{at(12, 2, 29), at(12, 0, 0), "one second before the minute becomes judged"},
		{at(12, 2, 30), at(12, 1, 0), "the cut-off moves exactly 90 s after the minute end"},
		{at(12, 2, 30) + 999, at(12, 1, 0), "within the second"},
		{at(12, 3, 29), at(12, 1, 0), ""},
		{at(12, 3, 30), at(12, 2, 0), ""},
	} {
		if got := QualityEvalEnd(c.now); got != c.want {
			t.Errorf("%s: eval end %s, want %s", c.note, time.UnixMilli(got).UTC().Format("15:04:05"), time.UnixMilli(c.want).UTC().Format("15:04:05"))
		}
	}
	// B2-a: a legal sample 40 s in the future (12:00:50 while it is 12:00:10) lies in the minute 12:00-12:01,
	// whose end is after the cut-off: the minute is not judged until 12:02:30.
	sampleMinuteEnd := time.Date(2026, 10, 23, 12, 1, 0, 0, time.UTC).UnixMilli()
	if sampleMinuteEnd <= QualityEvalEnd(time.Date(2026, 10, 23, 12, 0, 10, 0, time.UTC).UnixMilli()) {
		t.Fatal("the future sample's minute is judged at 12:00:10")
	}
	if sampleMinuteEnd > QualityEvalEnd(time.Date(2026, 10, 23, 12, 2, 30, 0, time.UTC).UnixMilli()) {
		t.Fatal("the minute must be judged from 12:02:30")
	}
	// Every judged minute ends before now-90s, so no sample of it can be later than now.
	for s := int64(0); s < 600; s++ {
		now := at(12, 0, 0) + s*1000
		if QualityEvalEnd(now) > now-qualityEvalLagMS {
			t.Fatalf("eval end %d is later than now-90s at now=%d", QualityEvalEnd(now), now)
		}
	}
}

// ---------- classification of a sample (review S1) ----------

func TestSlowClassificationBoundariesOfTheGrid(t *testing.T) {
	const threshold = 500
	hist := func(rtt float64) map[uint16]uint32 {
		return map[uint16]uint32{uint16(math.Round(rtt / QualityHistogramGridMS)): 1}
	}
	for _, c := range []struct {
		rtt  float64
		slow bool
		note string
	}{
		{threshold - .25, false, "T-0.25"},
		{threshold - .125, false, "T-0.125 (grid bin 2000)"},
		{threshold, false, "T"},
		{threshold + .125, true, "T+0.125: the first slow RTT"},
		{threshold + .25, true, "T+0.25"},
		{500.1, false, "500.1 is not slow on the grid (the exact rule would call it slow, see design 1.3)"},
		{1000, true, "far above"},
		{0, false, "zero"},
	} {
		if got := qualityBinsAbove(hist(c.rtt), threshold) == 1; got != c.slow {
			t.Errorf("RTT %v (%s): slow=%v, want %v", c.rtt, c.note, got, c.slow)
		}
	}
	// A different threshold moves the line with it, over the same stored bins.
	if qualityBinsAbove(hist(600), 500) != 1 || qualityBinsAbove(hist(600), 600) != 0 || qualityBinsAbove(hist(600), 599) != 1 {
		t.Fatal("the threshold does not move the line")
	}
}

func TestBlockCountsAreTheSumOfTheirMinutesAndAMissingMinuteIsNotNormal(t *testing.T) {
	frame := qualityLaneFrame{SlotRevision: 4, IntervalStart: 0, HasInterval: true, ThresholdMS: 500, TCP: true}
	bucket := func(valid, attempts, successes int, bins map[uint16]uint32) *qualityBucketData {
		b := qualityNewBucket()
		b.Stats.Count, b.Stats.ValidRTTs, b.Stats.ConnectAttempts, b.Stats.ConnectSuccesses = attempts, valid, attempts, successes
		b.Histogram = bins
		b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax, b.IntervalMax = 1, 1, 4, 4, i30
		return &b
	}
	// 4 samples: 2 fast, 1 slow (RTT 600), 1 refused.
	m := qualityJudgeMinute(frame, 10*qualityMinuteMS, bucket(3, 4, 3, map[uint16]uint32{400: 2, 2400: 1}), false)
	if m.Kind != minuteData || m.J != 4 || m.S != 1 || m.F != 1 || m.B != 2 || m.Unjudged != 0 {
		t.Fatalf("minute %+v", m)
	}
	// A connection that succeeded but has no valid RTT (probe incomplete) is not judged: J = valid RTTs + failures.
	odd := bucket(2, 4, 3, map[uint16]uint32{400: 2})
	if m := qualityJudgeMinute(frame, 10*qualityMinuteMS, odd, false); m.J != 3 || m.F != 1 || m.Unjudged != 1 {
		t.Fatalf("a connect success without RTT: %+v", m)
	}
	// Three buckets added up equal the merged bucket counted at once.
	tl := qualityTimeline{First: 0}
	for i := int64(0); i < 3; i++ {
		tl.Mins = append(tl.Mins, qualityJudgeMinute(frame, i*qualityMinuteMS, bucket(3, 4, 3, map[uint16]uint32{400: 2, 2400: 1}), false))
	}
	blk := tl.classifyBlock(0, 3*qualityMinuteMS, i30)
	if blk.Judged != 12 || blk.Slow != 3 || blk.Failed != 3 || blk.State != QualityCardYellow || blk.InProgress {
		t.Fatalf("block %+v", blk)
	}
	// A missing bucket is "no sample", never "normal": the block has fewer judged samples, not extra normal ones.
	tl.Mins[1] = qualityJudgeMinute(frame, qualityMinuteMS, nil, false)
	if blk = tl.classifyBlock(0, 3*qualityMinuteMS, i30); blk.Judged != 8 || tl.Mins[1].Kind != minuteEmpty {
		t.Fatalf("block with a missing minute %+v", blk)
	}
}

// ---------- the red rule: constants and counterexamples (design 1.5.2, review B3/B4) ----------

func TestJudgeParametersFollowTheDesignTable(t *testing.T) {
	for _, c := range []struct {
		interval                    float64
		look, fresh, gap, minJudged int
	}{
		{0.5, 10, 2, 1, 10},
		{1, 10, 2, 2, 5},
		{2, 12, 4, 3, 4},
		{5, 30, 10, 8, 4},
	} {
		p := qualityJudgeParamsFor(c.interval)
		if p.lookN != c.look || p.fresh != c.fresh || p.gap != c.gap || p.minJudged != c.minJudged {
			t.Errorf("interval %v: %+v, want look=%d fresh=%d gap=%d minJudged=%d", c.interval, p, c.look, c.fresh, c.gap, c.minJudged)
		}
	}
	for _, c := range []struct{ ms, want int }{{30000, 3}, {60000, 2}, {90000, 1}, {120000, 1}, {180000, 1}, {240000, 1}, {300000, 1}} {
		if got := qualityMinSamples(c.ms); got != c.want {
			t.Errorf("interval %d: m=%d, want %d", c.ms, got, c.want)
		}
	}
}

func TestRedB3IsolatedFailuresNeverTurnRed(t *testing.T) {
	// B3-a: 30 s, failures in minutes 0 and 2, nothing in minute 1.
	tl := tlOf(10).data(0, 1, 1, 1, i30).data(2, 1, 1, 1, i30)
	if tl.red(2, i30) || len(tl.redMinutes(i30)) != 0 {
		t.Fatal("B3-a: two isolated failures with a missing minute are red")
	}
	// ... and the 3-minute block: J=2 < m=3, not red, gray.
	if b := tl.classifyBlock(0, 10*qualityMinuteMS, i30); b.State != QualityCardGray || b.Judged != 2 {
		t.Fatalf("B3-a block: %+v", b)
	}
	// B3-b: the middle minute holds DNS failures only: a bucket with no judged sample is an empty minute.
	frame := qualityLaneFrame{SlotRevision: 2, HasInterval: true, ThresholdMS: 500, TCP: true}
	dns := qualityNewBucket()
	dns.Stats.Count = 1
	dns.Stats.Categories["dns_error"] = 1
	dns.ConfigMin, dns.ConfigMax, dns.SlotMin, dns.SlotMax, dns.IntervalMax = 1, 1, 2, 2, i30
	tl.Mins[1] = qualityJudgeMinute(frame, qualityMinuteMS, &dns, false)
	if tl.Mins[1].Kind != minuteEmpty || tl.Mins[1].DNSFailures != 1 || tl.red(2, i30) {
		t.Fatalf("B3-b: %+v red=%v", tl.Mins[1], tl.red(2, i30))
	}
	// B3-c: a known gap, a restart or a config boundary in the middle minute: not red even when every
	// minute around it is dead.
	dead := tlOf(10).fill(0, 3, 2, 2, 2, i30).unusable(1)
	if len(dead.redMinutes(i30)) != 0 {
		t.Fatalf("B3-c: %v", dead.redMinutes(i30))
	}
	// B3-d: 300 s: minutes 0 and 5 only (2 bad samples) stay yellow; 0, 5, 10 turn red.
	two := tlOf(30).data(0, 1, 1, 1, i300).data(5, 1, 1, 1, i300)
	if len(two.redMinutes(i300)) != 0 {
		t.Fatal("B3-d: 2 failures at 300 s are red")
	}
	three := tlOf(30).data(0, 1, 1, 1, i300).data(5, 1, 1, 1, i300).data(10, 1, 1, 1, i300)
	if !three.red(10, i300) || three.red(9, i300) {
		t.Fatalf("B3-d: three failures red=%v at 10 (red at 9=%v)", three.red(10, i300), three.red(9, i300))
	}
	// B3-e: at every interval 1 and 2 failures, anywhere, never red.
	for _, interval := range []int{i30, i60, i120, i300} {
		for first := 0; first < 20; first++ {
			for gap := 0; gap < 14; gap++ {
				one := tlOf(40).data(first, 1, 1, 1, interval)
				pair := tlOf(40).data(first, 1, 1, 1, interval).data(first+gap, 1, 1, 1, interval)
				if len(one.redMinutes(interval)) != 0 || len(pair.redMinutes(interval)) != 0 {
					t.Fatalf("B3-e: interval %d, failures at %d and +%d are red", interval, first, gap)
				}
			}
		}
		// isolated slow samples too
		slow := tlOf(40).fill(0, 40, 2, 0, 0, interval).data(10, 2, 1, 0, interval).data(18, 2, 1, 0, interval)
		if len(slow.redMinutes(interval)) != 0 {
			t.Fatalf("B3-e: two isolated slow samples are red at interval %d", interval)
		}
	}
}

func TestRedB3SlowNeedsDensity(t *testing.T) {
	// B3-f: 30 s: 4 judged samples with 3 slow ones (a majority) are not enough, n < 10.
	sparse := tlOf(12).data(5, 1, 1, 0, i30).data(6, 1, 1, 0, i30).data(7, 1, 1, 0, i30).data(8, 1, 0, 0, i30)
	if len(sparse.redMinutes(i30)) != 0 {
		t.Fatal("B3-f: a sparse majority is red")
	}
	// A dense window: 10 judged samples, 6 slow, spread over 5 minutes -> red; 5 slow (a tie) -> not red;
	// the same 6 slow samples inside 4 minutes (cover < 5) -> not red.
	dense := func(badPerMinute []int) *qualityTimeline {
		tl := tlOf(20)
		for m, b := range badPerMinute {
			tl.data(m, 2, b, 0, i30)
		}
		return tl
	}
	for _, c := range []struct {
		name string
		bad  []int // bad samples per minute, 2 judged each
		red  bool
	}{
		{"majority and cover 5", []int{2, 2, 2, 2, 2}, true},
		{"exact tie is not a majority", []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, false},
		{"majority but the bad samples cover only 4 minutes", []int{2, 2, 2, 2, 0, 0}, false},
		{"cover 5 with a calm middle", []int{2, 2, 0, 0, 2}, true},
	} {
		tl := dense(c.bad)
		got := false
		for m := range c.bad {
			if tl.red(m, i30) {
				got = true
			}
		}
		if got != c.red {
			t.Errorf("%s: red=%v, want %v", c.name, got, c.red)
		}
	}
}

func TestRedDeadRunConstants(t *testing.T) {
	// 30 s: 3 consecutive dead minutes are red; the same two minutes (4 failures) are not: the cover is 2.
	if !tlOf(8).fill(0, 3, 2, 2, 2, i30).red(2, i30) {
		t.Fatal("three dead minutes at 30 s are not red")
	}
	if tlOf(8).fill(0, 2, 2, 2, 2, i30).red(1, i30) {
		t.Fatal("two dead minutes (cover 2) are red")
	}
	// 60 s: one failure per minute, three minutes -> red at the third.
	tl := tlOf(8).fill(0, 3, 1, 1, 1, i60)
	if tl.red(1, i60) || !tl.red(2, i60) {
		t.Fatalf("60 s: red at 1=%v at 2=%v", tl.red(1, i60), tl.red(2, i60))
	}
	// 120 s: failures at 0, 2, 4 -> 3 failures, cover 5 -> red at 4; at 0, 2 only 2 failures.
	tl = tlOf(12).data(0, 1, 1, 1, i120).data(2, 1, 1, 1, i120).data(4, 1, 1, 1, i120)
	if tl.red(2, i120) || !tl.red(4, i120) {
		t.Fatalf("120 s: red at 2=%v at 4=%v", tl.red(2, i120), tl.red(4, i120))
	}
	// A success in the run ends it: failures, one success, failures: the run is only the last two minutes.
	tl = tlOf(14).fill(0, 3, 2, 2, 2, i30).fill(3, 10, 2, 0, 0, i30).fill(10, 12, 2, 2, 2, i30)
	if tl.red(11, i30) {
		t.Fatal("a run of two dead minutes after seven good ones is red")
	}
	// The first success releases the dead-run rule at once (the bad-majority rule may keep a long outage red
	// a few minutes longer: that is the "stays red while most of the window is bad" of design 1.5.3).
	tl = tlOf(20).fill(0, 10, 2, 0, 0, i30).fill(10, 13, 2, 2, 2, i30).data(13, 2, 0, 0, i30)
	if !tl.red(12, i30) || tl.red(13, i30) {
		t.Fatalf("release: red at 12=%v at 13=%v", tl.red(12, i30), tl.red(13, i30))
	}
	long := tlOf(20).fill(0, 5, 2, 2, 2, i30).data(5, 2, 0, 0, i30).fill(6, 12, 2, 0, 0, i30)
	var lastRed int
	for m := range long.Mins {
		if long.red(m, i30) {
			lastRed = m
		}
	}
	if lastRed < 5 || lastRed > 8 {
		t.Fatalf("a 5 minute outage stays red until minute %d after the recovery began at 5", lastRed)
	}
	// The gap between failing minutes: 30 s allows none, 60 s allows one empty minute, 300 s allows seven.
	for _, c := range []struct {
		interval, spacing int
		red               bool
	}{
		{i30, 1, true}, {i30, 2, false}, {i60, 2, true}, {i60, 3, false}, {i300, 8, true}, {i300, 9, false},
	} {
		tl := tlOf(40)
		for k := 0; k < 3; k++ {
			tl.data(k*c.spacing, 1, 1, 1, c.interval)
		}
		got := tl.red(2*c.spacing, c.interval)
		if got != c.red {
			t.Errorf("interval %d spacing %d: red=%v, want %v", c.interval, c.spacing, got, c.red)
		}
	}
}

func TestRedB4RecoveredLineIsNotCurrentlyRed(t *testing.T) {
	// 30 s: minutes 0-3 two failures each, minutes 4-29 two normal samples each.
	tl := tlOf(30)
	for m := 0; m < 30; m++ {
		if m < 4 {
			tl.data(m, 2, 2, 2, i30)
		} else {
			tl.data(m, 2, 0, 0, i30)
		}
	}
	if got := tl.redMinutes(i30); fmt.Sprint(got) != "[2 3]" {
		t.Fatalf("red minutes %v, want [2 3]", got)
	}
	if tl.red(29, i30) {
		t.Fatal("B4: the recovered line is red now")
	}
	// The history keeps its red block, the current state does not.
	if b := tl.classifyBlock(0, 30*qualityMinuteMS, i30); b.State != QualityCardRed {
		t.Fatalf("the history block of minutes 0-2 is %s", b.State)
	}
}

func TestRedFreshnessIsRelativeToTheEvaluatedMinute(t *testing.T) {
	// B4-b (F3): 30 s, FRESH = 2: failures in minutes 0-3, then nothing at all.
	tl := tlOf(30).fill(0, 4, 2, 2, 2, i30)
	for m, want := range map[int]bool{2: true, 3: true, 4: true, 5: false, 6: false, 29: false} {
		if got := tl.red(m, i30); got != want {
			t.Errorf("m_cur=%d: red=%v, want %v", m, got, want)
		}
	}
	// Whatever the interval, red stops exactly FRESH minutes after the last data minute.
	for _, interval := range []int{i30, i60, i120, i300} {
		p := qualityJudgeParamsFor(float64(interval) / 60000)
		spacing := int(math.Ceil(float64(interval) / 60000))
		tl := tlOf(120)
		last := 0
		for k := 0; k < 4; k++ {
			last = k * spacing
			tl.data(last, 2, 2, 2, interval)
		}
		if !tl.red(last, interval) {
			t.Fatalf("interval %d: the fixture is not red at its last failure", interval)
		}
		if !tl.red(last+p.fresh-1, interval) || tl.red(last+p.fresh, interval) {
			t.Errorf("interval %d: red at last+FRESH-1=%v, at last+FRESH=%v (FRESH=%d)", interval, tl.red(last+p.fresh-1, interval), tl.red(last+p.fresh, interval), p.fresh)
		}
	}
	// A line that stops right after a calm 26 minutes was never red at the end: that is not B4-b.
	calm := tlOf(40).fill(0, 30, 2, 0, 0, i30)
	if calm.red(35, i30) {
		t.Fatal("a calm line that went silent is red")
	}
}

func TestRedSeesOnlyThePast(t *testing.T) {
	tl := tlOf(20).fill(0, 5, 2, 2, 2, i30)
	before := tl.red(4, i30)
	tl.fill(5, 20, 2, 0, 0, i30)
	if tl.red(4, i30) != before || !before {
		t.Fatal("the future changed the verdict of minute 4")
	}
}

func TestRedF2EmptyMinutesCanBeCrossedUnusableOnesCannot(t *testing.T) {
	// F2-a: 300 s, failures at 0, 5, 10 and no bucket in between: red (GAP 8 allows it).
	empty := tlOf(30).data(0, 1, 1, 1, i300).data(5, 1, 1, 1, i300).data(10, 1, 1, 1, i300)
	if !empty.red(10, i300) {
		t.Fatal("F2-a: empty minutes between the failures interrupt the run")
	}
	// F2-b: the same, but a minute in between is covered by a known gap (or a boundary): not red.
	for _, m := range []int{1, 4, 6, 9} {
		cut := tlOf(30).data(0, 1, 1, 1, i300).data(5, 1, 1, 1, i300).data(10, 1, 1, 1, i300).unusable(m)
		if cut.red(10, i300) {
			t.Errorf("F2-b: a known-unusable minute %d did not interrupt the run", m)
		}
	}
	// F2-c: a minute with a bucket holding only resolver failures behaves exactly like a minute with no bucket.
	frame := qualityLaneFrame{SlotRevision: 2, HasInterval: true, ThresholdMS: 500, TCP: true}
	dns := qualityNewBucket()
	dns.Stats.Count = 2
	dns.Stats.Categories["dns_error"] = 2
	dns.ConfigMin, dns.ConfigMax, dns.SlotMin, dns.SlotMax = 1, 1, 2, 2
	withDNS := tlOf(30).data(0, 1, 1, 1, i300).data(5, 1, 1, 1, i300).data(10, 1, 1, 1, i300)
	withDNS.Mins[7] = qualityJudgeMinute(frame, 7*qualityMinuteMS, &dns, false)
	if withDNS.red(10, i300) != empty.red(10, i300) || withDNS.Mins[7].Kind != minuteEmpty {
		t.Fatal("F2-c: a DNS-only minute differs from a missing one")
	}
}

func TestRedF4DNSOnlyMinuteIsAnEmptyMinuteForTheGapRule(t *testing.T) {
	// F4-a: 60 s: failure, DNS-only minute, failure, failure -> 3 failures over 4 minutes, red.
	tl := tlOf(10).data(0, 1, 1, 1, i60).data(2, 1, 1, 1, i60).data(3, 1, 1, 1, i60)
	if !tl.red(3, i60) {
		t.Fatal("F4-a: not red")
	}
	// F4-b: 30 s: the same shape (two failures per minute) is not red at minute 3 (GAP 1 breaks at the empty
	// minute, cover 2) and is red once minutes 2-4 all fail.
	tl = tlOf(10).fill(2, 4, 2, 2, 2, i30).data(0, 2, 2, 2, i30)
	if tl.red(3, i30) {
		t.Fatal("F4-b: red at minute 3")
	}
	tl.data(4, 2, 2, 2, i30)
	if !tl.red(4, i30) {
		t.Fatal("F4-b: minutes 2-4 failing are not red")
	}
}

// ---------- usable minutes (review B1, F1) ----------

func judgeBucket(slot, cfgMin, cfgMax, slotMin, slotMax int64, j, failed int) *qualityBucketData {
	b := qualityNewBucket()
	b.Stats.Count, b.Stats.ConnectAttempts, b.Stats.ConnectSuccesses, b.Stats.ValidRTTs = j, j, j-failed, j-failed
	if j-failed > 0 {
		b.Histogram[400] = uint32(j - failed)
	}
	b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax, b.IntervalMax = cfgMin, cfgMax, slotMin, slotMax, i30
	return &b
}

func timelineOfBuckets(frame qualityLaneFrame, first, last int64, buckets map[int64]*qualityBucketData, gaps ...protocol.QualityGap) *qualityTimeline {
	stored := map[int64]qualityBucketData{}
	for m, b := range buckets {
		stored[m] = *b
	}
	tl := qualityBuildTimeline(frame, first, last, stored, gaps)
	return &tl
}

func TestUsableMinutesF1UniqueResultsPerShape(t *testing.T) {
	const slot = 7
	frame := qualityLaneFrame{SlotRevision: slot, IntervalStart: 0, HasInterval: true, ThresholdMS: 500, TCP: true}
	three := func(a, b, c *qualityBucketData, gaps ...protocol.QualityGap) QualityCardBlockForTest {
		tl := timelineOfBuckets(frame, 0, 3, map[int64]*qualityBucketData{0: a, 1: b, 2: c}, gaps...)
		return QualityCardBlockForTest{tl.classifyBlock(0, 3*qualityMinuteMS, i30), tl}
	}
	// B1-a: every minute bucket holds two configuration versions: three unusable minutes, J=0, gray, partial.
	mixed := func() *qualityBucketData { return judgeBucket(7, 1, 2, slot, slot, 2, 0) }
	got := three(mixed(), mixed(), mixed())
	if got.State != QualityCardGray || got.Judged != 0 || !got.Partial {
		t.Fatalf("B1-a: %+v", got.QualityCardBlock)
	}
	// B1-a2 (F1b): three buckets, each pure, the configuration version changes between minutes: all usable.
	got = three(judgeBucket(7, 1, 1, slot, slot, 2, 0), judgeBucket(7, 2, 2, slot, slot, 2, 0), judgeBucket(7, 2, 2, slot, slot, 2, 0))
	if got.State != QualityCardGreen || got.Judged != 6 || got.Partial {
		t.Fatalf("B1-a2: %+v", got.QualityCardBlock)
	}
	// B1-b (F1c): a known gap over only one minute: that minute is dropped, the rest stands.
	ok := func() *qualityBucketData { return judgeBucket(7, 1, 1, slot, slot, 2, 0) }
	gap := protocol.QualityGap{Start: qualityMinuteMS + 5, End: qualityMinuteMS + 9, Reason: "agent_restart"}
	got = three(ok(), ok(), ok(), gap)
	if got.State != QualityCardGreen || got.Judged != 4 || !got.Partial {
		t.Fatalf("B1-b one minute: %+v", got.QualityCardBlock)
	}
	// ... over all three: J=0, gray.
	wide := protocol.QualityGap{Start: 0, End: 3 * qualityMinuteMS, Reason: "agent_restart"}
	got = three(ok(), ok(), ok(), wide)
	if got.State != QualityCardGray || got.Judged != 0 || !got.Partial {
		t.Fatalf("B1-b all minutes: %+v", got.QualityCardBlock)
	}
	// A point gap belongs to the minute that contains it; the closed end of a range does not leak into the next minute.
	point := protocol.QualityGap{Start: 2 * qualityMinuteMS, End: 2 * qualityMinuteMS, Reason: "agent_restart"}
	got = three(ok(), ok(), ok(), point)
	if got.Judged != 4 || got.tl.Mins[2].Kind != minuteUnusable || got.tl.Mins[1].Kind != minuteData {
		t.Fatalf("point gap: %+v", got.QualityCardBlock)
	}
	touching := protocol.QualityGap{Start: 0, End: qualityMinuteMS, Reason: "agent_restart"}
	got = three(ok(), ok(), ok(), touching)
	if got.tl.Mins[0].Kind != minuteUnusable || got.tl.Mins[1].Kind != minuteData {
		t.Fatal("a gap that ends where the next minute begins must not mark that minute")
	}
	// B1-b2: a gap covers a minute that has no bucket at all: unusable, not empty.
	tl := timelineOfBuckets(frame, 0, 3, map[int64]*qualityBucketData{0: ok(), 2: ok()}, gap)
	if tl.Mins[1].Kind != minuteUnusable {
		t.Fatalf("B1-b2: %+v", tl.Mins[1])
	}
	// B1-f: the slot version of the bucket is not the lane's current one.
	got = three(judgeBucket(7, 1, 1, slot-1, slot-1, 2, 0), ok(), ok())
	if got.tl.Mins[0].Kind != minuteUnusable || got.Judged != 4 {
		t.Fatalf("B1-f: %+v", got.QualityCardBlock)
	}
	// A bucket whose version is unknown (0) is never trusted.
	got = three(judgeBucket(7, 0, 0, slot, slot, 2, 0), ok(), ok())
	if got.tl.Mins[0].Kind != minuteUnusable {
		t.Fatal("version 0 was trusted")
	}
	// B1-g: ordinary not-executed samples do not make a minute unusable: it only has fewer judged samples.
	partial := judgeBucket(7, 1, 1, slot, slot, 2, 0)
	partial.Stats.Count, partial.Stats.NotExecuted, partial.Partial = 5, 3, 1
	got = three(partial, ok(), ok())
	if got.tl.Mins[0].Kind != minuteData || got.tl.Mins[0].J != 2 || got.tl.Mins[0].Unjudged != 3 || got.Partial {
		t.Fatalf("B1-g: %+v %+v", got.QualityCardBlock, got.tl.Mins[0])
	}
}

// QualityCardBlockForTest keeps the timeline next to its block.
type QualityCardBlockForTest struct {
	QualityCardBlock
	tl *qualityTimeline
}

func TestUsableMinutesIntervalStartAndBoundaries(t *testing.T) {
	const slot = 7
	// B1-e/h: the lane's interval opened 30 s into minute 10; minutes up to 10 are not usable (minute 10 may
	// mix older samples), usable from minute 11.
	start := 10*qualityMinuteMS + 30000
	frame := qualityLaneFrame{SlotRevision: slot, IntervalStart: start, HasInterval: true, ThresholdMS: 500, TCP: true}
	fail := func() *qualityBucketData { return judgeBucket(7, 1, 1, slot, slot, 2, 2) }
	buckets := map[int64]*qualityBucketData{}
	for m := int64(0); m <= 10; m++ {
		buckets[m] = fail() // a long run of failures before and in the start minute
	}
	buckets[11] = fail() // a single failing minute after the start
	tl := timelineOfBuckets(frame, 0, 14, buckets)
	for m := int64(0); m <= 10; m++ {
		if tl.Mins[m].Kind != minuteUnusable {
			t.Fatalf("minute %d before the interval start is %v", m, tl.Mins[m].Kind)
		}
	}
	if tl.Mins[9].Outside != true || tl.Mins[10].Outside != false {
		t.Fatalf("outside marks: minute 9 %v, the start minute %v", tl.Mins[9].Outside, tl.Mins[10].Outside)
	}
	if tl.Mins[11].Kind != minuteData {
		t.Fatalf("the first minute after the start: %+v", tl.Mins[11])
	}
	for m := 0; m < 14; m++ {
		if tl.red(m, i30) {
			t.Fatalf("B1-h: minute %d is red on one failing minute after the start", m)
		}
	}
	// A block entirely before the start is gray and not partial; the block of the start minute is partial.
	if b := tl.classifyBlock(0, 14*qualityMinuteMS, i30); b.State != QualityCardGray || b.Partial {
		t.Fatalf("a block before the start: %+v", b)
	}
	if b := tl.classifyBlock(9*qualityMinuteMS, 14*qualityMinuteMS, i30); !b.Partial {
		t.Fatalf("the block of the start minute: %+v", b)
	}
	// An interval that starts exactly on a minute uses that minute.
	frame.IntervalStart = 10 * qualityMinuteMS
	if tl := timelineOfBuckets(frame, 0, 12, buckets); tl.Mins[10].Kind != minuteData || tl.Mins[9].Kind != minuteUnusable {
		t.Fatalf("aligned start: %+v %+v", tl.Mins[9], tl.Mins[10])
	}
	// No open interval: nothing is usable.
	frame.HasInterval = false
	if tl := timelineOfBuckets(frame, 0, 12, buckets); tl.Mins[11].Kind != minuteUnusable {
		t.Fatal("a lane without an interval has usable minutes")
	}

	// B1-c: failures on both sides of a slot-version boundary: the old side is not evidence. Minutes 0-4
	// carry the old slot version, minutes 5-6 the new one; only two dead minutes are usable: not red.
	frame = qualityLaneFrame{SlotRevision: slot, HasInterval: true, ThresholdMS: 500, TCP: true}
	boundary := map[int64]*qualityBucketData{}
	for m := int64(0); m <= 4; m++ {
		boundary[m] = judgeBucket(7, 1, 1, slot-1, slot-1, 2, 2)
	}
	boundary[5], boundary[6] = fail(), fail()
	if tl := timelineOfBuckets(frame, 0, 8, boundary); len(tl.redMinutes(i30)) != 0 {
		t.Fatalf("B1-c: red %v", tl.redMinutes(i30))
	}
	// B1-c2: only the configuration version changed: the run is not cut.
	cfgChange := map[int64]*qualityBucketData{
		0: judgeBucket(7, 1, 1, slot, slot, 2, 2), 1: judgeBucket(7, 2, 2, slot, slot, 2, 2), 2: judgeBucket(7, 2, 2, slot, slot, 2, 2),
	}
	if tl := timelineOfBuckets(frame, 0, 4, cfgChange); !tl.red(2, i30) {
		t.Fatal("B1-c2: a configuration-only change cut the run")
	}
}

// ---------- E1-E7: the standard answers on synthetic minutes (design 1.5.3) ----------

func TestRedStandardAnswersE1ToE7At30Seconds(t *testing.T) {
	const total = 120
	calm := func() *qualityTimeline { return tlOf(total).fill(0, total, 2, 0, 0, i30) }
	type event struct {
		name   string
		build  func() *qualityTimeline
		should bool
		within []int // the minutes in which red may begin at the latest
	}
	events := []event{
		{"E1 one spike", func() *qualityTimeline { return calm().data(30, 2, 1, 0, i30) }, false, nil},
		{"E2 two isolated spikes", func() *qualityTimeline { return calm().data(30, 2, 1, 0, i30).data(40, 2, 1, 0, i30) }, false, nil},
		{"E3 slow for 10 minutes", func() *qualityTimeline { return calm().fill(30, 40, 2, 2, 0, i30) }, true, []int{30, 36}},
		{"E4 60% SYN loss for 30 minutes", func() *qualityTimeline {
			tl := calm()
			for m := 30; m < 60; m++ {
				bad := []int{2, 1, 1, 1, 1}[m%5]
				tl.data(m, 2, bad, bad, i30)
			}
			return tl
		}, true, []int{30, 40}},
		{"E5 dead for 4 minutes", func() *qualityTimeline { return calm().fill(30, 34, 2, 2, 2, i30) }, true, []int{30, 33}},
		{"E6 dead for 12 minutes", func() *qualityTimeline { return calm().fill(30, 42, 2, 2, 2, i30) }, true, []int{30, 33}},
		{"E7 slow for 3 minutes", func() *qualityTimeline { return calm().fill(30, 33, 2, 2, 0, i30) }, false, nil},
	}
	for _, e := range events {
		tl := e.build()
		red := tl.redMinutes(i30)
		if (len(red) > 0) != e.should {
			t.Errorf("%s: red minutes %v, should be red: %v", e.name, red, e.should)
			continue
		}
		if e.should && (red[0] < e.within[0] || red[0] > e.within[1]+3) {
			t.Errorf("%s: red starts at minute %d, expected within %v (+3)", e.name, red[0], e.within)
		}
		// the line is calm again long after the event
		if tl.red(total-1, i30) {
			t.Errorf("%s: still red at the end", e.name)
		}
	}
}

// A line that is never reachable turns red at the third minute and stays red.
func TestRedPermanentlyDeadLine(t *testing.T) {
	for _, c := range []struct {
		interval  int
		firstRed  int
		spacingOf int
	}{{i30, 2, 1}, {i60, 2, 1}, {i120, 4, 2}, {i300, 10, 5}} {
		tl := tlOf(60)
		for m := 0; m < 60; m += c.spacingOf {
			tl.data(m, 1, 1, 1, c.interval)
		}
		red := tl.redMinutes(c.interval)
		if len(red) == 0 || red[0] != c.firstRed {
			t.Errorf("interval %d: first red minute %v, want %d", c.interval, red, c.firstRed)
		}
		if !tl.red(55, c.interval) {
			t.Errorf("interval %d: not red at the end of a dead line", c.interval)
		}
	}
}

// Blocks: J = 0 is always gray, 0 < J < m is gray unless red, in progress marks the last block.
func TestBlockStatesGrayRedAndInProgress(t *testing.T) {
	// 30 s, m = 3.
	tl := tlOf(12).fill(0, 3, 2, 0, 0, i30)
	if b := tl.classifyBlock(0, 12*qualityMinuteMS, i30); b.State != QualityCardGreen {
		t.Fatalf("green block: %+v", b)
	}
	tl.Mins[0].J, tl.Mins[1].J, tl.Mins[2].J = 1, 1, 0
	if b := tl.classifyBlock(0, 12*qualityMinuteMS, i30); b.State != QualityCardGray {
		t.Fatalf("J=2 < m=3 must be gray: %+v", b)
	}
	tl.Mins[2].J = 1
	if b := tl.classifyBlock(0, 12*qualityMinuteMS, i30); b.State != QualityCardGreen {
		t.Fatalf("J=3 = m must be judged: %+v", b)
	}
	// J=0 after a red block: gray, never red.
	dead := tlOf(12).fill(0, 3, 2, 2, 2, i30)
	if b := dead.classifyBlock(0, 12*qualityMinuteMS, i30); b.State != QualityCardRed {
		t.Fatalf("dead block: %+v", b)
	}
	if b := dead.classifyBlock(3*qualityMinuteMS, 12*qualityMinuteMS, i30); b.State != QualityCardGray || b.Judged != 0 {
		t.Fatalf("a block without judged samples after a red one: %+v", b)
	}
	// 0 < J < m with a red condition shows red.
	red := tlOf(12).fill(0, 4, 1, 1, 1, i30) // one failure per minute: J=1 per minute
	if b := red.classifyBlock(3*qualityMinuteMS, 12*qualityMinuteMS, i30); b.State != QualityCardRed || b.Judged != 1 {
		t.Fatalf("0 < J < m and red: %+v", b)
	}
	// In progress: the block ends after the cut-off, minutes after it are not read.
	mid := tlOf(6).fill(0, 6, 2, 0, 0, i30)
	if b := mid.classifyBlock(3*qualityMinuteMS, 4*qualityMinuteMS, i30); !b.InProgress || b.Judged != 2 {
		t.Fatalf("in progress: %+v", b)
	}
	if b := mid.classifyBlock(0, 3*qualityMinuteMS, i30); b.InProgress {
		t.Fatalf("a block that ends at the cut-off is complete: %+v", b)
	}
}

// Grey on a long interval: the sample count a block needs follows the interval.
func TestBlockEnoughSamplesPerInterval(t *testing.T) {
	for _, c := range []struct {
		interval, m int
	}{{30000, 3}, {60000, 2}, {90000, 1}, {120000, 1}, {240000, 1}, {300000, 1}} {
		for n := 0; n <= 6; n++ {
			tl := tlOf(6)
			if n > 0 {
				tl.data(0, n, 0, 0, c.interval)
			}
			b := tl.classifyBlock(0, 6*qualityMinuteMS, c.interval)
			want := QualityCardGreen
			switch {
			case n == 0:
				want = QualityCardGray
			case n < c.m:
				want = QualityCardGray
			}
			if b.State != want {
				t.Errorf("interval %d, %d samples: %s, want %s", c.interval, n, b.State, want)
			}
		}
	}
}

// ---------- every outcome through the ingest's own aggregation (design 1.2) ----------

func outcomeSample(outcome string, probeComplete, connectComplete bool, ms float64, extra func(*protocol.QualityDetails)) protocol.QualitySample {
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "success", ProbeComplete: probeComplete, ConnectAttempted: connectComplete, ConnectComplete: connectComplete, IntervalMS: 30000}
	s := protocol.QualitySample{Sequence: "1", StartedAt: 1000, FinishedAt: 1020, DurationMS: 20, Outcome: outcome, ResolvedIP: "93.184.216.34"}
	if outcome == "success" {
		d.ConnectMS = &ms
		s.LatencyMS = &ms
	}
	if extra != nil {
		extra(&d)
	}
	s.Details = d.Encode()
	return s
}

func TestEveryOutcomeIsClassifiedAsJudgedOrNot(t *testing.T) {
	frame := qualityLaneFrame{SlotRevision: 2, HasInterval: true, ThresholdMS: 500, TCP: true}
	dnsFailure := func(d *protocol.QualityDetails) {
		d.DNSOutcome, d.ErrorStage, d.ErrorCode = "not_found", "dns", "not_found"
	}
	for _, c := range []struct {
		name     string
		sample   protocol.QualitySample
		j, s, f  int
		unjudged int
	}{
		{"fast success", outcomeSample("success", true, true, 100, nil), 1, 0, 0, 0},
		{"success exactly at the threshold", outcomeSample("success", true, true, 500, nil), 1, 0, 0, 0},
		{"success one grid step over the threshold", outcomeSample("success", true, true, 500.25, nil), 1, 1, 0, 0},
		{"slow success", outcomeSample("success", true, true, 900, nil), 1, 1, 0, 0},
		{"timeout after the connect stage", outcomeSample("timeout", true, true, 0, nil), 1, 0, 1, 0},
		{"refused", outcomeSample("refused", true, true, 0, nil), 1, 0, 1, 0},
		{"network error", outcomeSample("network_error", true, true, 0, nil), 1, 0, 1, 0},
		{"resolver failure", outcomeSample("dns_error", true, false, 0, dnsFailure), 0, 0, 0, 1},
		{"canceled", outcomeSample("canceled", false, false, 0, nil), 0, 0, 0, 1},
		{"not run", outcomeSample("not_run", false, false, 0, nil), 0, 0, 0, 1},
		{"unsafe address", outcomeSample("unsafe_address", false, false, 0, nil), 0, 0, 0, 1},
		{"permission denied", outcomeSample("permission_denied", false, false, 0, nil), 0, 0, 0, 1},
		{"unsupported", outcomeSample("unsupported", false, false, 0, nil), 0, 0, 0, 1},
		{"invalid configuration", outcomeSample("invalid_config", false, false, 0, nil), 0, 0, 0, 1},
		{"connected but the probe is incomplete", outcomeSample("success", false, true, 80, nil), 0, 0, 0, 1},
	} {
		b, err := qualityContribution(c.sample, "tcp")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax = 1, 1, 2, 2
		m := qualityJudgeMinute(frame, 5*qualityMinuteMS, &b, false)
		if m.J != c.j || m.S != c.s || m.F != c.f || m.Unjudged != c.unjudged {
			t.Errorf("%s: J=%d S=%d F=%d unjudged=%d, want J=%d S=%d F=%d unjudged=%d", c.name, m.J, m.S, m.F, m.Unjudged, c.j, c.s, c.f, c.unjudged)
		}
		if (m.Kind == minuteData) != (c.j > 0) {
			t.Errorf("%s: minute kind %v", c.name, m.Kind)
		}
	}
	// The same stored RTT is classified by the threshold of the moment: 600 ms is slow at 500 and 599, not at 600.
	b, _ := qualityContribution(outcomeSample("success", true, true, 600, nil), "tcp")
	b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax = 1, 1, 2, 2
	for _, threshold := range []int{50, 500, 599, 600, 3000} {
		frame.ThresholdMS = threshold
		if m := qualityJudgeMinute(frame, 5*qualityMinuteMS, &b, false); (m.S == 1) != (600 > threshold) {
			t.Errorf("threshold %d: slow=%d", threshold, m.S)
		}
	}
}
