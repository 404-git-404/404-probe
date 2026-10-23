package storage

import (
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// Backend review round 1 (Codex): the counterexamples, written so that they fail on 80bd3c7c4.

// observeTimelines records the largest minute timeline any request builds.
func observeTimelines(t *testing.T) *int {
	t.Helper()
	largest := 0
	qualityTimelineHook = func(n int) { largest = max(largest, n) }
	t.Cleanup(func() { qualityTimelineHook = nil })
	return &largest
}

// B1: marks builds only what the request asks for, never the history since the epoch.
func TestCXReviewHistoricalMarksTimelineIsBounded(t *testing.T) {
	c := newCardKit(t)
	now := c.at(40)
	largest := observeTimelines(t)
	cases := []struct {
		name     string
		from, to int64
		bound    int // the most minute states one lane may be given
	}{
		{"one minute 30 days ago", now.Add(-30 * 24 * time.Hour).UnixMilli(), now.Add(-30*24*time.Hour).UnixMilli() + qualityMinuteMS, 0},
		{"from=0", 0, qualityMinuteMS, 0},
		{"from=0 up to 24 hours", 0, qualityMarksMaxSpanMS, 0},
		{"a day that ended 10 days ago", now.Add(-11 * 24 * time.Hour).UnixMilli(), now.Add(-10 * 24 * time.Hour).UnixMilli(), 0},
		{"one hour that ended two hours ago", now.Add(-3 * time.Hour).UnixMilli(), now.Add(-2 * time.Hour).UnixMilli(), 60 + qualityJudgePrefetchMinutes + 6},
		{"the last 24 hours", now.Add(-24 * time.Hour).UnixMilli(), now.UnixMilli(), 1440 + qualityJudgePrefetchMinutes + 6},
		{"a range that reaches into the future", now.Add(-time.Hour).UnixMilli(), now.Add(20 * time.Hour).UnixMilli(), 60 + qualityJudgePrefetchMinutes + 6},
	}
	for _, tc := range cases {
		*largest = 0
		begin := time.Now()
		marks, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", tc.from, tc.to, now)
		if err != nil {
			continue // a safe rejection is as good as a clamp
		}
		if *largest > tc.bound {
			t.Errorf("%s: built %d minute states for one lane, at most %d are needed", tc.name, *largest, tc.bound)
		}
		if marks.To > marks.EvalEnd || marks.From < marks.EvalEnd-qualityMarksMaxSpanMS-QualityBlockMS {
			t.Errorf("%s: the answer covers %d..%d (eval end %d)", tc.name, marks.From, marks.To, marks.EvalEnd)
		}
		if time.Since(begin) > time.Second {
			t.Errorf("%s: took %s", tc.name, time.Since(begin))
		}
	}
}

// B1 (other readers): the card and the alert read a fixed, small span whatever the clock says.
func TestCardAndAlertReadAFixedSpan(t *testing.T) {
	c := newCardKit(t)
	largest := observeTimelines(t)
	now := c.at(40)
	c.summary(now, "ipv4")
	if *largest > 60+qualityJudgePrefetchMinutes+3 {
		t.Fatalf("the card built %d minute states", *largest)
	}
	*largest = 0
	if _, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil {
		t.Fatal(err)
	}
	if *largest > qualityJudgePrefetchMinutes+6 {
		t.Fatalf("the alert built %d minute states", *largest)
	}
}

// B2 (pure): slow evidence on both sides of an unusable minute is not one window.
func TestCXReviewSlowWindowStopsAtUnusableMinute(t *testing.T) {
	tl := tlOf(8).fill(0, 3, 2, 2, 0, i30).unusable(3).fill(4, 7, 2, 2, 0, i30)
	if tl.red(6, i30) {
		t.Fatal("redAt(6)=true: slow evidence crosses unusable minute 3; each side covers only 3 minutes")
	}
	// Control: the same minutes without the break are red, so the test would notice a rule that never fires.
	if whole := tlOf(8).fill(0, 7, 2, 2, 0, i30); !whole.red(6, i30) {
		t.Fatal("seven slow minutes in a row must be red")
	}
	// Enough evidence after the break is red again.
	later := tlOf(14).fill(0, 3, 2, 2, 0, i30).unusable(3).fill(4, 11, 2, 2, 0, i30)
	if !later.red(10, i30) {
		t.Fatal("seven slow minutes after a break are not red")
	}
	// A break anywhere inside the slow stretch keeps the evidence on each side below the rule.
	for _, m := range []int{2, 3, 4} {
		cut := tlOf(8).fill(0, 7, 2, 2, 0, i30).unusable(m)
		if cut.red(6, i30) {
			t.Errorf("a break at minute %d leaves red", m)
		}
	}
}

// B2 (buckets): the break can be a mixed bucket, an old slot version or a known gap, with or without a bucket.
func TestSlowEvidenceStopsAtMixedBucketsAndGaps(t *testing.T) {
	const slot = 7
	frame := qualityLaneFrame{SlotRevision: slot, HasInterval: true, ThresholdMS: 500, TCP: true}
	slow := func() *qualityBucketData {
		b := judgeBucket(7, 1, 1, slot, slot, 2, 0)
		b.Histogram = map[uint16]uint32{3200: 2} // 800 ms
		return b
	}
	build := func(middle *qualityBucketData, gaps ...protocol.QualityGap) *qualityTimeline {
		buckets := map[int64]*qualityBucketData{}
		for _, m := range []int64{0, 1, 2, 4, 5, 6} {
			buckets[m] = slow()
		}
		if middle != nil {
			buckets[3] = middle
		}
		return timelineOfBuckets(frame, 0, 8, buckets, gaps...)
	}
	mixed := slow()
	mixed.ConfigMin, mixed.ConfigMax = 1, 2
	otherSlot := slow()
	otherSlot.SlotMin, otherSlot.SlotMax = slot-1, slot-1
	gap := protocol.QualityGap{Start: 3*qualityMinuteMS + 5, End: 3*qualityMinuteMS + 9, Reason: "agent_restart"}
	for name, tl := range map[string]*qualityTimeline{
		"mixed bucket in the middle":         build(mixed),
		"old slot version in the middle":     build(otherSlot),
		"known gap, no bucket in the middle": build(nil, gap),
		"known gap over a slow bucket":       build(slow(), gap),
	} {
		if tl.red(6, i30) {
			t.Errorf("%s: red at minute 6", name)
		}
	}
	if !build(slow()).red(6, i30) {
		t.Fatal("control: seven slow minutes without a break must be red")
	}
}

// B2 (read path): card, marks and the alert agree on the same break.
func TestCXReviewSlowGapThroughCardAndAlert(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(3), 0, 2, 0, 800)
	c.fillTCP("ipv4", c.minute(4), c.minute(7), 0, 2, 0, 800)
	if _, err := c.s.db.Exec(`INSERT INTO quality_gaps(agent_id,epoch,session_id,start_at,end_at,reason) VALUES(?,1,'review',?,?,'agent_restart')`, c.id, c.minute(3)*qualityMinuteMS, (c.minute(3)+1)*qualityMinuteMS); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli((c.minute(6)+1)*qualityMinuteMS + qualityEvalLagMS)
	s := c.summary(now, "ipv4")
	lane := c.telecom(s)
	alert, err := c.s.QualityAlertActive(c.ctx, c.id, now)
	if err != nil {
		t.Fatal(err)
	}
	if lane.TCP.Red.Active || s.AlertActive || alert {
		t.Fatalf("three slow minutes, a gap, three slow minutes: tcp.red=%v card.alert=%v QualityAlertActive=%v, want all false", lane.TCP.Red.Active, s.AlertActive, alert)
	}
	if strings.Contains(blockStates(lane.TCP.Blocks), "R") {
		t.Fatalf("a red block on the card: %s", blockStates(lane.TCP.Blocks))
	}
	marks, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.WindowStart, s.EvalEnd, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range marks.Lanes {
		for _, run := range l.Runs {
			if run.State == QualityCardRed {
				t.Fatalf("a red mark: %+v", run)
			}
		}
	}
	// Control: the same minutes without the gap are red in all three.
	c2 := newCardKit(t)
	c2.fillTCP("ipv4", c2.minute(0), c2.minute(7), 0, 2, 0, 800)
	now2 := time.UnixMilli((c2.minute(6)+1)*qualityMinuteMS + qualityEvalLagMS)
	s2 := c2.summary(now2, "ipv4")
	alert2, _ := c2.s.QualityAlertActive(c2.ctx, c2.id, now2)
	if !c2.telecom(s2).TCP.Red.Active || !s2.AlertActive || !alert2 {
		t.Fatal("control: seven slow minutes with no gap are not red")
	}
}

// B3: a device whose removal is pending shows no judgement at all; the global gate stays closed.
func pendingRemoval(t *testing.T, c *cardKit, now time.Time) {
	t.Helper()
	r := validReport(c.id, c.epoch, c.session, "boot", 100, 1000, 2000)
	r.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	r.NetworkQuality, r.NetworkQualityV2, r.QualityTargetsHeader = true, v4Caps(protocol.QualityIPv6Present), true
	if _, accepted, _, err := c.s.ProcessReport(c.ctx, c.id, r, now); err != nil || !accepted {
		t.Fatalf("management report %v %v", accepted, err)
	}
	if _, created, err := c.s.CreateAgentRemovalOperation(c.ctx, c.id, "11111111111111111111111111111111", now); err != nil || !created {
		t.Fatalf("create removal: %v %v", created, err)
	}
}

func assertNoJudgement(t *testing.T, c *cardKit, now time.Time, why string) {
	t.Helper()
	s := c.summary(now, "ipv4")
	lane := c.telecom(s)
	if lane.TCP.Status == "active" || len(lane.TCP.Blocks) != 0 || lane.TCP.Red != nil || s.AlertActive || lane.Right.Kind != "none" {
		t.Fatalf("%s: lane %+v right %+v alert=%v", why, lane.TCP, lane.Right, s.AlertActive)
	}
	if lane.ICMP.Status == "active" {
		t.Fatalf("%s: the ICMP lane is still active", why)
	}
	marks, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.WindowStart, s.EvalEnd, now)
	if err != nil || len(marks.Lanes) != 0 {
		t.Fatalf("%s: marks %+v %v", why, marks, err)
	}
	if alert, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil || alert {
		t.Fatalf("%s: alert %v %v", why, alert, err)
	}
}

func TestCXReviewPendingRemovalHasNoJudgement(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(5), 0, 0, 2, 0)
	now := time.UnixMilli(c.minute(5)*qualityMinuteMS + qualityEvalLagMS)
	if s := c.summary(now, "ipv4"); !s.AlertActive || !c.telecom(s).TCP.Red.Active {
		t.Fatal("fixture: no red before the removal")
	}
	pendingRemoval(t, c, now)
	assertNoJudgement(t, c, now, "removal pending")
	// The same holds for the IPv6 face.
	if s := c.summary(now, "ipv6"); c.telecom(s).TCP.Blocks != nil || c.telecom(s).TCP.Red != nil || s.AlertActive {
		t.Fatalf("IPv6 face of a device being removed: %+v", c.telecom(s).TCP)
	}
}

// The plan of a marks request, without a database: whatever from and to say, the minutes read stay inside the
// retained 24 hours and end at the request's own end.
func TestMarksPlanIsBoundedByRetentionAndTheRequestEnd(t *testing.T) {
	evalEnd := QualityEvalEnd(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).UnixMilli())
	day := qualityMarksMaxSpanMS
	cases := []struct {
		name     string
		from, to int64
		ok       bool
	}{
		{"from=0", 0, 60000, false},
		{"from=0, a full day", 0, day, false},
		{"30 days ago", evalEnd - 30*day, evalEnd - 30*day + 60000, false},
		{"ended 10 days ago", evalEnd - 11*day, evalEnd - 10*day, false},
		{"the last 24 hours", evalEnd - day, evalEnd, true},
		{"started before the retained day, ends inside it", evalEnd - day - 3600000, evalEnd - day + 3600000, true},
		{"a range in the future", evalEnd + 3600000, evalEnd + 7200000, false},
		{"reaches into the future", evalEnd - 3600000, evalEnd + 3600000, true},
		{"one block", evalEnd - 600000, evalEnd - 600000 + QualityBlockMS, true},
	}
	for _, c := range cases {
		from, end, first, last, ok := qualityMarksPlan(evalEnd, c.from, c.to)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if last-first > qualityTimelineMaxMinutes || from < evalEnd-day-QualityBlockMS || end > evalEnd || last*qualityMinuteMS > evalEnd || from%QualityBlockMS != 0 {
			t.Errorf("%s: from=%d end=%d first=%d last=%d (%d minutes)", c.name, from, end, first, last, last-first)
		}
		if need := (end-from+QualityBlockMS-1)/QualityBlockMS*3 + qualityJudgePrefetchMinutes; last-first > need {
			t.Errorf("%s: %d minutes read, %d needed", c.name, last-first, need)
		}
	}
	// A reader refuses a span that no honest request produces.
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	tx, err := s.qualityReadTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := s.newQualityJudgeReader(t.Context(), tx, qualityBudget{}, id, time.Now(), 0, qualityTimelineMaxMinutes+1); err == nil {
		t.Fatal("a reader accepted a span of 30 million minutes")
	}
}
