package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// cardKit seeds stored 1-minute buckets under the real targets of a configured, running card (one slot with
// all four lanes), so the whole read path (configuration, intervals, gaps, buckets) is exercised.
type cardKit struct {
	*v4Kit
	cfg   protocol.QualityV4Config
	start int64 // start of the open IPv4 TCP interval (ms)
}

func newCardKit(t *testing.T) *cardKit {
	t.Helper()
	k := v4Base(t, true)
	c := &cardKit{v4Kit: k, cfg: k.get()}
	if err := k.s.db.QueryRow(`SELECT start_at FROM quality_intervals WHERE agent_id=? AND slot='telecom' AND family='ipv4' AND protocol='tcp' AND end_at IS NULL`, k.id).Scan(&c.start); err != nil {
		t.Fatal(err)
	}
	return c
}

// at returns the time n minutes after the first whole minute that follows the interval start.
func (c *cardKit) at(minutes float64) time.Time {
	first := (c.start/qualityMinuteMS + 1) * qualityMinuteMS
	return time.UnixMilli(first + int64(minutes*float64(qualityMinuteMS)))
}

// minute is the index of the first whole minute after the interval start plus n.
func (c *cardKit) minute(n int64) int64 { return c.start/qualityMinuteMS + 1 + n }

func (c *cardKit) series(slot, family, proto string) int64 {
	c.t.Helper()
	target := c.targetID(c.cfg, slot, family, proto)
	if _, err := c.s.db.Exec(`INSERT OR IGNORE INTO quality_series(agent_id,target_id,semantics) VALUES(?,?,?)`, c.id, target, protocol.QualityStagedSemantics); err != nil {
		c.t.Fatal(err)
	}
	var id int64
	if err := c.s.db.QueryRow(`SELECT id FROM quality_series WHERE target_id=? AND semantics=?`, target, protocol.QualityStagedSemantics).Scan(&id); err != nil {
		c.t.Fatal(err)
	}
	return id
}

func (c *cardKit) put(slot, family, proto string, minute int64, b qualityBucketData) {
	c.t.Helper()
	wire, err := qualityEncodeBucket(b)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.s.db.Exec(`INSERT OR REPLACE INTO quality_buckets(series_id,resolution,bucket_start,data) VALUES(?,60000,?,?)`, c.series(slot, family, proto), minute*qualityMinuteMS, wire); err != nil {
		c.t.Fatal(err)
	}
}

func (c *cardKit) versions(b *qualityBucketData, family, proto string) {
	lane := c.lane(c.cfg, "telecom", family, proto)
	slot := slotRevisionOf(lane)
	cfg, _ := parseRevision(c.cfg.ConfigRevision)
	b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax, b.IntervalMax = cfg, cfg, slot, slot, i30
}

func parseRevision(s string) (int64, error) {
	var v int64
	for _, r := range s {
		v = v*10 + int64(r-'0')
	}
	return v, nil
}

// tcp builds a TCP minute: fast samples at 50 ms, slow ones at slowMS, failed connections.
func (c *cardKit) tcp(fast, slow, failed int, slowMS float64) qualityBucketData {
	b := qualityNewBucket()
	b.Stats.Count = fast + slow + failed
	b.Stats.ConnectAttempts = fast + slow + failed
	b.Stats.ConnectSuccesses = fast + slow
	b.Stats.ValidRTTs = fast + slow
	if fast > 0 {
		b.Histogram[uint16(math.Round(50/QualityHistogramGridMS))] += uint32(fast)
		b.RTTSum += 50 * float64(fast)
	}
	if slow > 0 {
		b.Histogram[uint16(math.Round(slowMS/QualityHistogramGridMS))] += uint32(slow)
		b.RTTSum += slowMS * float64(slow)
	}
	c.versions(&b, "ipv4", "tcp")
	return b
}

func (c *cardKit) fillTCP(family string, from, to int64, fast, slow, failed int, slowMS float64) {
	for m := from; m < to; m++ {
		b := c.tcp(fast, slow, failed, slowMS)
		c.versions(&b, family, "tcp")
		c.put("telecom", family, "tcp", m, b)
	}
}

func (c *cardKit) icmp(family string, sent, received int) qualityBucketData {
	b := qualityNewBucket()
	b.Stats.Count = 1
	b.Stats.Sent, b.Stats.Received = sent, received
	c.versions(&b, family, "icmp")
	return b
}

func (c *cardKit) summary(now time.Time, family string) QualityCardSummary {
	c.t.Helper()
	out, err := c.s.QualityCardSummary(c.ctx, c.id, family, now)
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *cardKit) telecom(s QualityCardSummary) QualityCardLane {
	c.t.Helper()
	for _, l := range s.Lanes {
		if l.Slot == "telecom" {
			return l
		}
	}
	c.t.Fatal("no telecom lane")
	return QualityCardLane{}
}

func blockStates(blocks []QualityCardBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(map[QualityCardState]string{QualityCardGray: ".", QualityCardGreen: "g", QualityCardYellow: "y", QualityCardRed: "R"}[b.State])
	}
	return sb.String()
}

func TestCardSummaryShapeAndStatuses(t *testing.T) {
	c := newCardKit(t)
	now := c.at(40)
	s := c.summary(now, "ipv4")
	if len(s.Lanes) != 3 || s.Family != "ipv4" || s.BlockMS != 180000 || s.ThresholdMS != 500 || s.IntervalMS != 30000 || s.SettingsRevision == "" || s.ConfigRevision == "" {
		t.Fatalf("summary %+v", s)
	}
	if s.EvalEnd != QualityEvalEnd(now.UnixMilli()) || s.Now != now.UnixMilli() {
		t.Fatalf("cut-off %d now %d", s.EvalEnd, s.Now)
	}
	if want := floorDiv64(s.EvalEnd-1, 180000)*180000 - 19*180000; s.WindowStart != want {
		t.Fatalf("window start %d, want %d", s.WindowStart, want)
	}
	telecom := c.telecom(s)
	if telecom.TCP.Status != "active" || telecom.ICMP.Status != "active" || len(telecom.TCP.Blocks) != 20 || telecom.TCP.Red == nil || telecom.TCP.Red.Active {
		t.Fatalf("telecom %+v", telecom)
	}
	for i, b := range telecom.TCP.Blocks {
		if b.Start != s.WindowStart+int64(i)*180000 || b.State != QualityCardGray {
			t.Fatalf("block %d of an empty line: %+v", i, b)
		}
	}
	if last := telecom.TCP.Blocks[19]; last.InProgress != (last.Start+180000 > s.EvalEnd) {
		t.Fatalf("last block %+v eval end %d", last, s.EvalEnd)
	}
	for _, l := range s.Lanes[1:] {
		if l.TCP.Status == "active" || l.TCP.Blocks != nil || l.TCP.Red != nil || l.Right.Kind != "none" || l.Right.Samples != "none" {
			t.Fatalf("a lane without a target shows blocks or numbers: %+v", l)
		}
	}
	if s.AlertActive {
		t.Fatal("alert on an empty line")
	}
	// The JSON carries exactly the contract names.
	wire, _ := json.Marshal(s)
	for _, name := range []string{`"eval_end"`, `"block_ms"`, `"window_start"`, `"threshold_ms"`, `"alert_active"`, `"blocks"`, `"in_progress"`, `"red":{"active":false}`, `"right":{"kind":`} {
		if !strings.Contains(string(wire), name) {
			t.Errorf("the answer lacks %s: %s", name, wire)
		}
	}
	if _, err := c.s.QualityCardSummary(c.ctx, c.id, "ipv5", now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("a bad family: %v", err)
	}
	if _, err := c.s.QualityCardSummary(c.ctx, "no-such-agent", "ipv4", now); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("an unknown agent: %v", err)
	}
}

func TestCardBlocksFollowTheStoredMinutesAndTheThreshold(t *testing.T) {
	c := newCardKit(t)
	// 18 minutes of an even line, then 6 minutes where every other sample took 600 ms.
	c.fillTCP("ipv4", c.minute(0), c.minute(18), 2, 0, 0, 0)
	c.fillTCP("ipv4", c.minute(18), c.minute(24), 1, 1, 0, 600)
	now := c.at(27)
	lane := c.telecom(c.summary(now, "ipv4"))
	states := blockStates(lane.TCP.Blocks)
	if !strings.Contains(states, "y") || strings.Contains(states, "R") {
		t.Fatalf("expected yellow, no red: %s", states)
	}
	// The same stored data under a threshold of 700 ms: nothing is slow any more, no data is rewritten.
	if _, err := c.saveThreshold("", 30000, threshold(700), c.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	c.cfg = c.get()
	lane = c.telecom(c.summary(now, "ipv4"))
	if strings.Contains(blockStates(lane.TCP.Blocks), "y") {
		t.Fatalf("a 700 ms threshold still shows yellow: %s", blockStates(lane.TCP.Blocks))
	}
	if s := c.summary(now, "ipv4"); s.ThresholdMS != 700 {
		t.Fatalf("threshold in the answer %d", s.ThresholdMS)
	}
	// And under 599 ms the 600 ms samples are slow again.
	if _, err := c.saveThreshold("", 30000, threshold(599), c.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	lane = c.telecom(c.summary(now, "ipv4"))
	if !strings.Contains(blockStates(lane.TCP.Blocks), "y") {
		t.Fatalf("a 599 ms threshold shows no yellow: %s", blockStates(lane.TCP.Blocks))
	}
}

// Review B2-a: a legal sample 40 s in the future is not part of any block until its minute passes the cut-off.
func TestCardIgnoresAMinuteThatHasNotPassedTheCutOff(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(20), 2, 0, 0, 0)
	// Through the real ingestion: a sample stamped 40 s after the upload time (the allowed skew).
	now := c.at(21).Add(10 * time.Second)
	future := c.userTCPSample(c.cfg, 1, now.Add(40*time.Second), 800)
	if acks := c.upload(now, future); acks[0] != "committed" {
		t.Fatalf("the future sample was refused: %v", acks)
	}
	futureMinute := future.StartedAt / qualityMinuteMS
	sum := func(at time.Time) int {
		lane := c.telecom(c.summary(at, "ipv4"))
		total := 0
		for _, b := range lane.TCP.Blocks {
			if b.Start/qualityMinuteMS <= futureMinute && futureMinute < (b.Start+180000)/qualityMinuteMS {
				total += b.Slow
			}
		}
		return total
	}
	if got := sum(now); got != 0 {
		t.Fatalf("at the upload time the future sample is counted (%d slow)", got)
	}
	// The minute is judged once now-90s passes its end.
	end := (futureMinute + 1) * qualityMinuteMS
	if sum(time.UnixMilli(end+qualityEvalLagMS-1)) != 0 {
		t.Fatal("counted one millisecond before the cut-off")
	}
	if sum(time.UnixMilli(end+qualityEvalLagMS)) != 1 {
		t.Fatal("not counted at the cut-off")
	}
	// B2-c: a late (legal) upload into an already judged minute shows up in the next answer.
	late := c.userTCPSample(c.cfg, 2, time.UnixMilli(c.minute(5)*qualityMinuteMS+10000), 900)
	before := c.summary(now.Add(5*time.Minute), "ipv4")
	if acks := c.upload(now.Add(5*time.Minute), late); acks[0] != "committed" {
		t.Fatalf("late sample: %v", acks)
	}
	after := c.summary(now.Add(5*time.Minute), "ipv4")
	countSlow := func(s QualityCardSummary) int {
		n := 0
		for _, b := range c.telecom(s).TCP.Blocks {
			n += b.Slow
		}
		return n
	}
	if countSlow(after) != countSlow(before)+1 {
		t.Fatalf("a late sample did not change the judged minute: %d -> %d", countSlow(before), countSlow(after))
	}
}

func TestCardRedAndAlertFollowTheCurrentStateNotTheHistory(t *testing.T) {
	c := newCardKit(t)
	// Four dead minutes, then twenty-six good ones (design B4).
	c.fillTCP("ipv4", c.minute(0), c.minute(4), 0, 0, 2, 0)
	c.fillTCP("ipv4", c.minute(4), c.minute(30), 2, 0, 0, 0)
	during := c.summary(time.UnixMilli((c.minute(4)*qualityMinuteMS)+qualityEvalLagMS), "ipv4")
	if lane := c.telecom(during); !lane.TCP.Red.Active || !during.AlertActive || !strings.Contains(blockStates(lane.TCP.Blocks), "R") {
		t.Fatalf("during the outage: red=%v alert=%v blocks=%s", lane.TCP.Red.Active, during.AlertActive, blockStates(lane.TCP.Blocks))
	}
	after := c.summary(time.UnixMilli((c.minute(30)*qualityMinuteMS)+qualityEvalLagMS), "ipv4")
	lane := c.telecom(after)
	if lane.TCP.Red.Active || after.AlertActive {
		t.Fatalf("recovered: red=%v alert=%v", lane.TCP.Red.Active, after.AlertActive)
	}
	if !strings.Contains(blockStates(lane.TCP.Blocks), "R") {
		t.Fatalf("the history lost its red block: %s", blockStates(lane.TCP.Blocks))
	}
	// B4-b: an outage that is still red when the line goes silent turns grey after FRESH minutes.
	c2 := newCardKit(t)
	c2.fillTCP("ipv4", c2.minute(0), c2.minute(4), 0, 0, 2, 0)
	for _, c := range []struct {
		after int64
		red   bool
	}{{4, true}, {5, true}, {6, false}, {20, false}} {
		got := c2.summary(time.UnixMilli(c2.minute(c.after)*qualityMinuteMS+qualityEvalLagMS), "ipv4")
		if got.AlertActive != c.red || c2.telecom(got).TCP.Red.Active != c.red {
			t.Errorf("silent for %d minutes: alert=%v red=%v, want %v", c.after-3, got.AlertActive, c2.telecom(got).TCP.Red.Active, c.red)
		}
	}
}

// Review S5-a: the global alert is about IPv4 TCP whichever family the page shows.
func TestAlertActiveIsAboutIPv4TCPWhateverTheRequestedFamily(t *testing.T) {
	c := newCardKit(t)
	now := time.UnixMilli(c.minute(5)*qualityMinuteMS + qualityEvalLagMS)
	// Only IPv6 TCP is dead: no alert, but the IPv6 face shows the red lane.
	c.fillTCP("ipv6", c.minute(0), c.minute(5), 0, 0, 2, 0)
	c.fillTCP("ipv4", c.minute(0), c.minute(5), 2, 0, 0, 0)
	v6 := c.summary(now, "ipv6")
	if !c.telecom(v6).TCP.Red.Active || v6.AlertActive {
		t.Fatalf("IPv6 only: red=%v alert=%v", c.telecom(v6).TCP.Red.Active, v6.AlertActive)
	}
	if v4 := c.summary(now, "ipv4"); v4.AlertActive || c.telecom(v4).TCP.Red.Active {
		t.Fatal("IPv4 is calm but alerts")
	}
	if alert, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil || alert {
		t.Fatalf("QualityAlertActive with only IPv6 red: %v %v", alert, err)
	}
	// Now IPv4 TCP dies: the alert is on, also when the page shows IPv6.
	c.fillTCP("ipv4", c.minute(0), c.minute(5), 0, 0, 2, 0)
	if !c.summary(now, "ipv6").AlertActive || !c.summary(now, "ipv4").AlertActive {
		t.Fatal("the alert depends on the family of the page")
	}
	if alert, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil || !alert {
		t.Fatalf("QualityAlertActive: %v %v", alert, err)
	}
}

// The N6 gate: each reason that closes the IPv4 TCP lane (or the device) silences the alert although the stored
// minutes are red. The gate is the SQL condition of the v1.0.6 TCP Mini answer.
func TestAlertGateClosesForEveryReason(t *testing.T) {
	type step struct {
		name  string
		close func(c *cardKit)
	}
	for _, s := range []step{
		{"master switch off", func(c *cardKit) { c.enable(false) }},
		{"IPv4 TCP sub-lane switched off", func(c *cardKit) { c.save(30000, v4Slot("telecom", "example.com", 443, "v4icmp", "v6tcp", "v6icmp")) }},
		{"slot switched off", func(c *cardKit) {
			c.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp", "off"))
		}},
		{"host cleared", func(c *cardKit) { c.save(30000, v4Slot("telecom", "", 0)) }},
		{"device revoked", func(c *cardKit) {
			if _, err := c.s.db.Exec(`UPDATE agents SET revoked=1 WHERE id=?`, c.id); err != nil {
				t.Fatal(err)
			}
		}},
		{"device paused", func(c *cardKit) {
			if _, err := c.s.db.Exec(`UPDATE agents SET disabled_at=? WHERE id=?`, c.now.UnixMilli(), c.id); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(s.name, func(t *testing.T) {
			c := newCardKit(t)
			c.fillTCP("ipv4", c.minute(0), c.minute(5), 0, 0, 2, 0)
			now := time.UnixMilli(c.minute(5)*qualityMinuteMS + qualityEvalLagMS)
			if alert, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil || !alert {
				t.Fatalf("fixture: no alert on a dead line: %v %v", alert, err)
			}
			s.close(c)
			alert, err := c.s.QualityAlertActive(c.ctx, c.id, now)
			if err != nil || alert {
				t.Fatalf("alert after %q: %v %v", s.name, alert, err)
			}
			// The card answer agrees, and a lane that is not active shows no blocks and no red.
			summary, err := c.s.QualityCardSummary(c.ctx, c.id, "ipv4", now)
			if err != nil || summary.AlertActive {
				t.Fatalf("card alert after %q: %v %v", s.name, summary.AlertActive, err)
			}
			if tcp := c.telecom(summary).TCP; tcp.Status == "active" && (tcp.Blocks == nil || tcp.Red == nil) {
				t.Fatalf("an active lane without blocks: %+v", tcp)
			} else if tcp.Status != "active" && (tcp.Blocks != nil || tcp.Red != nil) {
				t.Fatalf("%q: lane %s shows blocks or red: %+v", s.name, tcp.Status, tcp)
			}
		})
	}
	t.Run("removal in progress", func(t *testing.T) {
		c := newCardKit(t)
		c.fillTCP("ipv4", c.minute(0), c.minute(5), 0, 0, 2, 0)
		now := time.UnixMilli(c.minute(5)*qualityMinuteMS + qualityEvalLagMS)
		if alert, err := c.s.QualityAlertActive(c.ctx, c.id, now); err != nil || !alert {
			t.Fatalf("fixture: no alert on a dead line: %v %v", alert, err)
		}
		if _, err := c.s.db.Exec(`INSERT INTO agent_removal_operations(operation_id,agent_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,created_at,updated_at) VALUES('0123456789abcdef0123456789abcdef',?,'requested',1,'s',1,'s',1,1)`, c.id); err != nil {
			t.Fatal(err)
		}
		assertNoJudgement(t, c, now, "removal in progress (a row in the table)")
	})
}

// Right-hand numbers (design 1.8).
func TestCardRightHandNumber(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(30), 2, 0, 0, 0)
	for m := int64(0); m < 30; m++ {
		c.put("telecom", "ipv4", "icmp", c.minute(m), c.icmp("ipv4", 3, 3))
	}
	// Two minutes lost one echo each.
	c.put("telecom", "ipv4", "icmp", c.minute(20), c.icmp("ipv4", 3, 2))
	c.put("telecom", "ipv4", "icmp", c.minute(21), c.icmp("ipv4", 3, 2))
	now := c.at(31)
	lane := c.telecom(c.summary(now, "ipv4"))
	if lane.Right.Kind != "icmp_loss" || lane.Right.Denominator != 87 || lane.Right.Numerator != 2 || lane.Right.Samples != "ok" || lane.Right.Percent == nil || math.Abs(*lane.Right.Percent-100*2.0/87) > 1e-9 {
		t.Fatalf("ICMP number %+v", lane.Right)
	}
	// With ICMP switched off the TCP failure rate is shown; DNS failures are not in the denominator.
	c.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v6tcp", "v6icmp"))
	c.cfg = c.get()
	c2 := c
	c2.fillTCP("ipv4", c.minute(0), c.minute(30), 2, 0, 0, 0)
	for m := int64(10); m < 13; m++ {
		b := c2.tcp(1, 0, 1, 0)
		b.Stats.Count += 3 // three resolver failures that are not judged
		b.Stats.Categories["dns_error"] = 3
		c2.put("telecom", "ipv4", "tcp", c2.minute(m), b)
	}
	lane = c.telecom(c.summary(now, "ipv4"))
	if lane.Right.Kind != "tcp_failure" || lane.Right.Numerator != 3 || lane.Right.Denominator != 58 || lane.Right.Samples != "ok" {
		t.Fatalf("TCP number %+v", lane.Right)
	}
	if lane.TCP.DNSFailing {
		t.Fatal("dns_failing without resolver failures at the end")
	}
	// ICMP on but no complete window: kind icmp_loss, no percent, samples none.
	c3 := newCardKit(t)
	c3.fillTCP("ipv4", c3.minute(0), c3.minute(10), 2, 0, 0, 0)
	lane = c3.telecom(c3.summary(c3.at(12), "ipv4"))
	if lane.Right.Kind != "icmp_loss" || lane.Right.Samples != "none" || lane.Right.Percent != nil || lane.Right.Denominator != 0 {
		t.Fatalf("empty ICMP %+v", lane.Right)
	}
	// Few samples: fewer than 30 echoes sent.
	c3.put("telecom", "ipv4", "icmp", c3.minute(5), c3.icmp("ipv4", 12, 10))
	lane = c3.telecom(c3.summary(c3.at(12), "ipv4"))
	if lane.Right.Samples != "few" || lane.Right.Percent == nil {
		t.Fatalf("few ICMP samples %+v", lane.Right)
	}
}

func TestCardDNSFailingNote(t *testing.T) {
	c := newCardKit(t)
	for m := int64(0); m < 6; m++ {
		b := c.tcp(0, 0, 0, 0)
		b.Stats.Count = 2
		b.Stats.Categories["dns_error"] = 2
		c.put("telecom", "ipv4", "tcp", c.minute(m), b)
	}
	lane := c.telecom(c.summary(time.UnixMilli(c.minute(6)*qualityMinuteMS+qualityEvalLagMS), "ipv4"))
	if !lane.TCP.DNSFailing || strings.Contains(blockStates(lane.TCP.Blocks), "R") || strings.Contains(blockStates(lane.TCP.Blocks), "y") {
		t.Fatalf("resolver failures: dns_failing=%v blocks=%s", lane.TCP.DNSFailing, blockStates(lane.TCP.Blocks))
	}
}

func TestMarksAgreeWithTheBlocksOfTheCard(t *testing.T) {
	c := newCardKit(t)
	// A slow stretch (yellow) and a dead stretch (red) inside an otherwise good hour.
	c.fillTCP("ipv4", c.minute(0), c.minute(45), 2, 0, 0, 0)
	c.fillTCP("ipv4", c.minute(10), c.minute(13), 1, 1, 0, 800)
	c.fillTCP("ipv4", c.minute(30), c.minute(36), 0, 0, 2, 0)
	now := c.at(46)
	s := c.summary(now, "ipv4")
	blocks := c.telecom(s).TCP.Blocks
	marks, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.WindowStart, s.EvalEnd, now)
	if err != nil || len(marks.Lanes) != 1 || marks.Lanes[0].Slot != "telecom" || marks.EvalEnd != s.EvalEnd {
		t.Fatalf("marks %+v %v", marks, err)
	}
	covered := map[int64]QualityCardState{}
	for _, r := range marks.Lanes[0].Runs {
		if r.End <= r.Start || (r.End-r.Start)%QualityBlockMS != 0 || r.State == QualityCardGreen || r.State == QualityCardGray {
			t.Fatalf("run %+v", r)
		}
		for t := r.Start; t < r.End; t += QualityBlockMS {
			covered[t] = r.State
		}
	}
	nonGreen := 0
	for _, b := range blocks {
		want := QualityCardState("")
		if b.State == QualityCardYellow || b.State == QualityCardRed {
			want = b.State
			nonGreen++
		}
		if covered[b.Start] != want {
			t.Errorf("block %d: card %s, marks %q", b.Start, b.State, covered[b.Start])
		}
	}
	if nonGreen < 3 {
		t.Fatalf("fixture has too few coloured blocks: %s", blockStates(blocks))
	}
	// Adjacent blocks of the same colour are one run.
	for i := 1; i < len(marks.Lanes[0].Runs); i++ {
		a, b := marks.Lanes[0].Runs[i-1], marks.Lanes[0].Runs[i]
		if a.End == b.Start && a.State == b.State {
			t.Fatalf("runs %+v and %+v should be merged", a, b)
		}
	}
	// Limits: 24 h at most, a family, an order.
	if _, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.WindowStart, s.WindowStart+24*3600*1000+1, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("more than 24 h: %v", err)
	}
	if _, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.EvalEnd, s.WindowStart, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("reversed range: %v", err)
	}
	if _, err := c.s.QualityMarks(c.ctx, c.id, "udp", s.WindowStart, s.EvalEnd, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("family: %v", err)
	}
	// The range stops at the cut-off: later time is never marked.
	if m, err := c.s.QualityMarks(c.ctx, c.id, "ipv4", s.WindowStart, s.EvalEnd+3600*1000, now); err != nil || m.To != s.EvalEnd {
		t.Fatalf("range past the cut-off: %+v %v", m, err)
	}
}

// Review S5-b: a lane whose target is abnormal answers with its status only.
func TestCardTargetAbnormalLaneHasNoBlocks(t *testing.T) {
	c := newCardKit(t)
	now := c.now.Add(10 * time.Minute)
	for i := 1; i <= 3; i++ {
		at := now.Add(-time.Duration(4-i) * 30 * time.Second)
		if acks := c.upload(now, c.sample(c.cfg, "telecom", "ipv4", "tcp", i, at, "not_found")); acks[0] != "committed" {
			t.Fatal(acks)
		}
	}
	c.fillTCP("ipv4", c.minute(0), c.minute(8), 0, 0, 2, 0)
	s := c.summary(now, "ipv4")
	lane := c.telecom(s)
	if lane.TCP.Status != protocol.QualityLaneTargetAbnormal || lane.TCP.Condition == "" || lane.TCP.Blocks != nil || lane.TCP.Red != nil {
		t.Fatalf("target_abnormal: %+v", lane.TCP)
	}
	wire, _ := json.Marshal(lane.TCP)
	if strings.Contains(string(wire), "blocks") || strings.Contains(string(wire), "red") {
		t.Fatalf("the JSON of an abnormal lane carries judgement fields: %s", wire)
	}
	if s.AlertActive {
		t.Fatal("alert from a lane with an abnormal target")
	}
}

// The disk budget reads the database itself. On a file database (the one connection) a read that asked for it
// while its own transaction was open would wait for itself until the deadline; memory databases hide that.
func TestCardAnswersOnAFileDatabaseWithoutWaitingForItsOwnConnection(t *testing.T) {
	s, id, _ := testStore(t, filepath.Join(t.TempDir(), "card.db"))
	t.Cleanup(func() { s.Close() })
	k := &v4Kit{t: t, s: s, id: id, now: time.Unix(1800000000, 0), epoch: 1, session: "v4-session", ctx: context.Background()}
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp"))
	k.enable(true)
	for round := 0; round < 3; round++ {
		s.qualityBudgetMu.Lock()
		s.qualityBudgetAt = time.Time{} // an expired budget: the next request has to measure again
		s.qualityBudgetMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		begin := time.Now()
		if _, err := s.QualityCardSummary(ctx, id, "ipv4", k.now.Add(time.Hour)); err != nil {
			cancel()
			t.Fatalf("card summary on a file database: %v after %s", err, time.Since(begin))
		}
		s.qualityBudgetMu.Lock()
		s.qualityBudgetAt = time.Time{}
		s.qualityBudgetMu.Unlock()
		if _, err := s.QualityMarks(ctx, id, "ipv4", k.now.UnixMilli(), k.now.Add(time.Hour).UnixMilli(), k.now.Add(2*time.Hour)); err != nil {
			cancel()
			t.Fatalf("marks on a file database: %v", err)
		}
		s.qualityBudgetMu.Lock()
		s.qualityBudgetAt = time.Time{}
		s.qualityBudgetMu.Unlock()
		if _, err := s.QualityAlertActive(ctx, id, k.now.Add(time.Hour)); err != nil {
			cancel()
			t.Fatalf("alert on a file database: %v", err)
		}
		cancel()
	}
}

// Review (design 1.8): the right-hand ICMP number counts complete echo windows only. The buckets come out of the
// ingestion's own contribution function, so what the aggregator keeps is what the card reads.
func TestCardRightNumberCountsOnlyCompleteICMPWindows(t *testing.T) {
	c := newCardKit(t)
	target := protocol.QualityTarget{Protocol: "icmp", Host: "example.com"}
	at := time.UnixMilli(c.minute(10) * qualityMinuteMS)
	complete := a2Echo(target, "1", 1, at, []float64{10, 20, -1}) // three echoes sent, two received
	incomplete := a2Echo(target, "1", 2, at.Add(time.Minute), []float64{10, -1, -1})
	d, err := protocol.DecodeQualityDetails(incomplete.Details)
	if err != nil {
		t.Fatal(err)
	}
	d.WindowComplete, d.ProbeComplete = false, false
	incomplete.Details, incomplete.Sent, incomplete.Received = d.Encode(), 2, 1
	for i, sample := range []protocol.QualitySample{complete, incomplete} {
		b, err := qualityContribution(sample, "icmp")
		if err != nil {
			t.Fatal(err)
		}
		c.versions(&b, "ipv4", "icmp")
		c.put("telecom", "ipv4", "icmp", c.minute(10)+int64(i), b)
	}
	c.fillTCP("ipv4", c.minute(0), c.minute(20), 2, 0, 0, 0)
	lane := c.telecom(c.summary(c.at(21), "ipv4"))
	if lane.Right.Kind != "icmp_loss" || lane.Right.Denominator != 3 || lane.Right.Numerator != 1 {
		t.Fatalf("an incomplete window was counted: %+v", lane.Right)
	}
}

// The next-minute twin of the future-sample case (B2-b): a sample 40 s ahead that lands in the following
// minute is held back by the same cut-off.
func TestCardIgnoresAFutureSampleInTheNextMinuteToo(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(20), 2, 0, 0, 0)
	now := time.UnixMilli(c.minute(20)*qualityMinuteMS + 40000)       // 40 s into minute 20
	future := c.userTCPSample(c.cfg, 1, now.Add(40*time.Second), 800) // 20:80 s: inside minute 21
	if future.StartedAt/qualityMinuteMS != c.minute(21) {
		t.Fatalf("fixture: the sample is in minute %d", future.StartedAt/qualityMinuteMS-c.minute(0))
	}
	if acks := c.upload(now, future); acks[0] != "committed" {
		t.Fatalf("upload: %v", acks)
	}
	slow := func(at time.Time) int {
		n := 0
		for _, b := range c.telecom(c.summary(at, "ipv4")).TCP.Blocks {
			n += b.Slow
		}
		return n
	}
	if slow(now) != 0 || slow(now.Add(time.Minute)) != 0 {
		t.Fatal("the minute of a future sample was judged before its cut-off")
	}
	if slow(time.UnixMilli(c.minute(22)*qualityMinuteMS+qualityEvalLagMS)) != 1 {
		t.Fatal("the sample never appeared")
	}
}

// A block takes the largest check interval of its minutes: a card whose interval changed inside a block needs
// the samples of the slower interval (design 1.4).
func TestBlockUsesTheLargestIntervalOfItsMinutes(t *testing.T) {
	tl := tlOf(6).data(0, 3, 0, 0, i30).data(1, 0, 0, 0, i60).data(2, 0, 0, 0, i60)
	tl.Mins[1].Kind, tl.Mins[2].Kind = minuteEmpty, minuteEmpty
	// 3 judged samples: enough at 30 s (m=3); at 60 s (m=2) too. A single sample is enough at neither/either:
	// with minutes of 60 s the block needs 2.
	one := tlOf(6).data(0, 1, 0, 0, i30).data(1, 0, 0, 0, i60)
	one.Mins[1].Kind = minuteEmpty
	if b := one.classifyBlock(0, 6*qualityMinuteMS, i30); b.State != QualityCardGray {
		t.Fatalf("1 sample with a mixed 30/60 s interval: %+v", b)
	}
	if b := tl.classifyBlock(0, 6*qualityMinuteMS, i30); b.State != QualityCardGreen {
		t.Fatalf("3 samples: %+v", b)
	}
}

// Lanes the device is not measuring answer with their status only, and nothing of the judgement: not a block,
// not a red state, not an alert (design 1.9, N6).
func TestCardLanesThatAreNotMeasuringShowNoJudgement(t *testing.T) {
	t.Run("storage pressure", func(t *testing.T) {
		c := newCardKit(t)
		c.fillTCP("ipv4", c.minute(0), c.minute(5), 0, 0, 2, 0)
		now := time.UnixMilli(c.minute(5)*qualityMinuteMS + qualityEvalLagMS)
		c.s.qualityDiskObserve = func() (qualityDisk, error) { return qualityDisk{}, errors.New("disk unreadable") }
		c.s.qualityBudgetMu.Lock()
		c.s.qualityBudgetAt = time.Time{}
		c.s.qualityBudgetMu.Unlock()
		s := c.summary(now, "ipv4")
		if tcp := c.telecom(s).TCP; tcp.Status != protocol.QualityLaneStoragePressure || tcp.Blocks != nil || tcp.Red != nil || s.AlertActive {
			t.Fatalf("storage pressure: %+v alert=%v", tcp, s.AlertActive)
		}
	})
	t.Run("no IPv6 address on the device", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6None))
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v6tcp"))
		k.enable(true)
		s, err := k.s.QualityCardSummary(k.ctx, k.id, "ipv6", k.now.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		tcp := s.Lanes[0].TCP
		if tcp.Status != protocol.QualityLaneNoIPv6 || tcp.Blocks != nil || tcp.Red != nil {
			t.Fatalf("no IPv6: %+v", tcp)
		}
	})
	t.Run("agent without negotiated targets", func(t *testing.T) {
		k := newV4Kit(t)
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
		s, err := k.s.QualityCardSummary(k.ctx, k.id, "ipv4", k.now.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		tcp := s.Lanes[0].TCP
		if tcp.Status == protocol.QualityLaneActive || tcp.Blocks != nil || tcp.Red != nil || s.AlertActive {
			t.Fatalf("not negotiated: %+v", tcp)
		}
	})
}

// A device that never configured network quality still gets a well-formed answer: three lanes with their status,
// the default threshold, no judgement and no alert.
func TestCardAndAlertForADeviceThatNeverConfiguredQuality(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	now := time.Unix(1800000000, 0)
	got, err := s.QualityCardSummary(context.Background(), id, "ipv4", now)
	if err != nil || len(got.Lanes) != 3 || got.ThresholdMS != 500 || got.AlertActive {
		t.Fatalf("%+v %v", got, err)
	}
	for _, lane := range got.Lanes {
		if lane.TCP.Status == "" || lane.TCP.Blocks != nil || lane.TCP.Red != nil || lane.Right.Kind != "none" {
			t.Fatalf("lane %+v", lane)
		}
	}
	if alert, err := s.QualityAlertActive(context.Background(), id, now); err != nil || alert {
		t.Fatalf("alert %v %v", alert, err)
	}
	if marks, err := s.QualityMarks(context.Background(), id, "ipv6", now.UnixMilli()-3600000, now.UnixMilli(), now); err != nil || len(marks.Lanes) != 0 {
		t.Fatalf("marks %+v %v", marks, err)
	}
}

// A range wholly after the evaluation cut-off is answered empty, and the answer does not claim to reach past the cut-off.
func TestMarksFutureRangeStopsAtTheCutoff(t *testing.T) {
	k := newV4Kit(t)
	evalEnd := QualityEvalEnd(k.now.UnixMilli())
	m, err := k.s.QualityMarks(k.ctx, k.id, "ipv4", evalEnd+3600000, evalEnd+7200000, k.now)
	if err != nil {
		t.Fatal(err)
	}
	if m.To > m.EvalEnd || m.From > m.To || len(m.Lanes) != 0 {
		t.Fatalf("%+v", m)
	}
}
