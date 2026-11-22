package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

var r39TCP = NetworkLine{"telecom", "ipv4", "tcp"}

func TestNetworkRangeResolution(t *testing.T) {
	now := time.Unix(1800000015, 0)
	for _, tc := range []struct {
		age  time.Duration
		want int64
	}{
		{time.Hour, 60000},
		{24 * time.Hour, 60000},
		{24*time.Hour + time.Millisecond, 300000},
		{7 * 24 * time.Hour, 300000},
		{7*24*time.Hour + time.Millisecond, 3600000},
		{30 * 24 * time.Hour, 3600000},
	} {
		if got, err := networkStatusResolution(now.Add(-tc.age).UnixMilli(), now.UnixMilli(), now); got != tc.want || err != nil {
			t.Errorf("age=%v resolution=%d err=%v", tc.age, got, err)
		}
	}
	if _, err := networkStatusResolution(now.Add(-31*24*time.Hour).UnixMilli(), now.UnixMilli(), now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("out of retention: %v", err)
	}
}

func TestNetworkRangeStitchesTargetSwitches(t *testing.T) {
	c := newCardKit(t)
	before := c.targetID(c.cfg, "telecom", "ipv4", "tcp")
	start := c.at(0)
	c.put("telecom", "ipv4", "tcp", c.minute(0), c.tcp(2, 0, 0, 0))
	c.now = c.at(2).Add(17 * time.Second)
	oldSlot := c.cfg.Slots[0]
	c.cfg = c.save(30000, v4Slot("telecom", "new.example.net", 443, "v4tcp"))
	change := c.now.UnixMilli()
	after := c.targetID(c.cfg, "telecom", "ipv4", "tcp")
	later := c.tcp(0, 3, 1, 600)
	later.Stats.Categories["timeout"] = 1
	c.put("telecom", "ipv4", "tcp", c.minute(3), later)
	c.now = c.at(5)
	c.cfg = c.save(30000, oldSlot)
	saved := c.now.UnixMilli()
	back := c.targetID(c.cfg, "telecom", "ipv4", "tcp")
	c.put("telecom", "ipv4", "tcp", c.minute(6), c.tcp(1, 0, 0, 0))
	// The 48-hour authorization table can be pruned independently of the markers.
	if _, err := c.s.db.Exec(`DELETE FROM quality_intervals WHERE end_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	now := c.at(8)
	out, err := c.s.NetworkHistory(c.ctx, c.id, r39TCP, start.UnixMilli(), now.UnixMilli(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Changes) != 2 || out.Changes[0] != (NetworkTargetChange{change, before, after, false}) ||
		out.Changes[1] != (NetworkTargetChange{saved, after, back, false}) {
		t.Fatalf("changes=%+v", out.Changes)
	}
	p := out.Points[3]
	if out.Stats.Samples != 7 || len(out.Points) != 8 || p.Samples != 4 || p.P50 == nil || *p.P50 != 600 ||
		p.FailurePercent == nil || *p.FailurePercent != 25 || p.Categories["timeout"] != 1 || out.Points[1].P50 != nil {
		t.Fatalf("stitched history=%+v", out)
	}
}

func TestNetworkHeatmapAndEveningUseBeijingHours(t *testing.T) {
	c := newCardKit(t)
	now := time.Date(2027, 1, 16, 15, 5, 0, 0, time.UTC)
	start := now.Add(-5 * time.Hour)
	series := c.series("telecom", "ipv4", "tcp")
	for _, h := range []int{11, 12, 13, 14, 15} {
		at := time.Date(2027, 1, 16, h, 0, 0, 0, time.UTC).UnixMilli()
		b := c.tcp(0, 2, 1, float64(h*100))
		wire, err := qualityEncodeBucket(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.s.db.Exec(`INSERT INTO quality_buckets VALUES(?,?,?,?)`, series, qualityHourMS, at, wire); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.s.NetworkHeatmap(c.ctx, c.id, r39TCP, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	row := out.Days[29]
	if row.Date != "2027-01-16" || row.Hours[20].P50 == nil || *row.Hours[20].P50 != 1200 ||
		row.Hours[20].FailurePercent == nil || *row.Hours[20].FailurePercent < 33 || row.Hours[18].Samples != 0 {
		t.Fatalf("Beijing row=%+v", row)
	}
	outHistory, err := c.s.NetworkHistory(c.ctx, c.id, r39TCP, start.Add(-8*24*time.Hour).UnixMilli(), now.UnixMilli(), now)
	if err != nil || outHistory.EveningP50 == nil || *outHistory.EveningP50 != 1300 {
		t.Fatalf("evening=%v err=%v", outHistory.EveningP50, err)
	}
}

func TestNetworkAlertPersistsAcrossDaysAndCloses(t *testing.T) {
	c := newCardKit(t)
	c.fillTCP("ipv4", c.minute(0), c.minute(12), 0, 1, 1, 800)
	firstNow := c.at(12)
	if err := c.s.SweepNetworkAlerts(c.ctx, firstNow); err != nil {
		t.Fatal(err)
	}
	first, err := c.s.NetworkAlerts(c.ctx, c.id, firstNow)
	if err != nil || len(first) != 1 || first[0].End != nil || first[0].WorstP95 == nil || *first[0].WorstP95 != 800 ||
		first[0].WorstFailure == nil || *first[0].WorstFailure != 50 {
		t.Fatalf("initial alerts=%+v err=%v", first, err)
	}
	c.now = firstNow.Add(-time.Second)
	c.report(true, v4Caps(protocol.QualityIPv6Present))
	fleet, err := c.s.NetworkFleet(c.ctx, c.id, "ipv4", "tcp", c.at(0).UnixMilli(), firstNow.UnixMilli(), firstNow, 30*time.Second)
	if err != nil || len(fleet.Machines) != 1 || fleet.Machines[0].Alerts30Days != 1 ||
		len(fleet.Machines[0].Lines) != 3 || fleet.Machines[0].Lines[0].Status != "alert" ||
		fleet.Machines[0].Lines[0].Alert == nil || len(fleet.Machines[0].Lines[0].Spark) > networkSparkPointLimit {
		t.Fatalf("fleet alert=%+v err=%v", fleet, err)
	}
	c.fillTCP("ipv4", c.minute(12), c.minute(1480), 0, 1, 1, 1100)
	for _, minutes := range []float64{720, 1460} {
		if err := c.s.SweepNetworkAlerts(c.ctx, c.at(minutes)); err != nil {
			t.Fatal(err)
		}
	}
	now := c.at(1460)
	if _, _, err := c.s.CleanupQualityBatch(c.ctx, now); err != nil {
		t.Fatal(err)
	}
	open, err := c.s.NetworkAlerts(c.ctx, c.id, now)
	if err != nil || len(open) != 1 || open[0].Start != first[0].Start || open[0].End != nil ||
		open[0].DurationMS <= (24*time.Hour).Milliseconds() || *open[0].WorstP95 != 1100 {
		t.Fatalf("open after one day=%+v err=%v", open, err)
	}
	c.fillTCP("ipv4", c.minute(1480), c.minute(1510), 2, 0, 0, 0)
	if err := c.s.SweepNetworkAlerts(c.ctx, c.at(1510)); err != nil {
		t.Fatal(err)
	}
	closed, err := c.s.NetworkAlerts(c.ctx, c.id, c.at(1510))
	if err != nil || len(closed) != 1 || closed[0].End == nil || *closed[0].End <= closed[0].Start {
		t.Fatalf("closed alerts=%+v err=%v", closed, err)
	}
	future := c.at(1510).Add(31 * 24 * time.Hour)
	if err := c.s.cleanupNetworkStatus(c.ctx, future); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := c.s.db.QueryRow(`SELECT COUNT(*) FROM quality_alert_history WHERE agent_id=?`, c.id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired alert count=%d err=%v", count, err)
	}
}

func TestAgentPresenceExcludesPausesAndHonorsOfflineDeadline(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	at := time.Unix(1800000000, 0)
	report := func(seq uint64, seconds time.Duration) {
		t.Helper()
		if _, accepted, reason, err := s.ProcessReport(ctx, id, validReport(id, 1, "session", "boot", seq, seq, seq), at.Add(seconds*time.Second)); err != nil || !accepted {
			t.Fatalf("accepted=%v reason=%s err=%v", accepted, reason, err)
		}
	}
	report(1, 0)
	report(2, 40) // An offline event must be recovered even before the sweep.
	if changed, err := s.DisableAgent(ctx, id, at.Add(50*time.Second)); err != nil || !changed {
		t.Fatalf("disable=%v err=%v", changed, err)
	}
	if changed, err := s.EnableAgent(ctx, id, at.Add(70*time.Second)); err != nil || !changed {
		t.Fatalf("enable=%v err=%v", changed, err)
	}
	report(3, 80)
	now := at.Add(120 * time.Second)
	for _, sweep := range []bool{false, true} {
		if sweep {
			if err := s.SweepPresence(ctx, now); err != nil {
				t.Fatal(err)
			}
		}
		o, err := s.AgentPresenceHistory(ctx, id, at.UnixMilli(), now.UnixMilli(), now)
		if err != nil || o.OnlineMS != 70000 || o.OfflineMS != 30000 || o.PausedMS != 20000 ||
			o.OnlinePercent == nil || *o.OnlinePercent != 70 || len(o.Events) != 7 ||
			o.Events[1].Kind != "offline" || o.Events[1].At != at.Add(30*time.Second).UnixMilli() {
			t.Fatalf("presence=%+v err=%v", o, err)
		}
	}
	future := now.Add(31 * 24 * time.Hour)
	if err := s.SweepPresence(ctx, future); err != nil {
		t.Fatal(err)
	}
	o, err := s.AgentPresenceHistory(ctx, id, future.Add(-time.Hour).UnixMilli(), future.UnixMilli(), future)
	if err != nil || o.InitialState != "offline" || len(o.Events) != 0 || o.OfflineMS != time.Hour.Milliseconds() {
		t.Fatalf("presence after pruning=%+v err=%v", o, err)
	}
}

func TestDiscontinuousTrafficRatesAreNullAndDoNotEnterMinuteMean(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1800000000, 0).Truncate(time.Minute)
	iface := func(name string, n uint64) protocol.NetworkInterfaceCounter {
		return protocol.NetworkInterfaceCounter{Name: name, RXBytes: n, TXBytes: n}
	}
	for i, report := range []protocol.Report{
		reportWithNetworkCounters(id, 1, "one", "boot", 1, iface("eth0", 100)),
		reportWithNetworkCounters(id, 1, "one", "boot", 2, iface("eth0", 200)),
		reportWithNetworkCounters(id, 2, "two", "boot", 1, iface("eth0", 300)),
		reportWithNetworkCounters(id, 2, "two", "boot", 2, iface("eth0", 400), iface("eth1", 300)),
		reportWithNetworkCounters(id, 2, "two", "boot", 3, iface("eth0", 500), iface("eth1", 400)),
		reportWithNetworkCounters(id, 3, "three", "new-boot", 1, iface("eth0", 10), iface("eth1", 10)),
	} {
		state, accepted, reason, err := s.ProcessReport(ctx, id, report, now.Add(time.Duration(i)*10*time.Second))
		missing := i != 1 && i != 4
		if err != nil || !accepted || state.RateUnavailable != missing {
			t.Fatalf("report %d: accepted=%v reason=%s missing=%v err=%v", i, accepted, reason, state.RateUnavailable, err)
		}
		var persisted sql.NullFloat64
		if err := s.db.QueryRow(`SELECT observed_rx_rate FROM agent_state WHERE agent_id=?`, id).Scan(&persisted); err != nil || persisted.Valid == missing {
			t.Fatalf("persisted report %d: %v err=%v", i, persisted, err)
		}
		if i == 0 {
			p, err := s.History(ctx, id, now)
			if err != nil || len(p) != 1 || !p[0].RateUnavailable {
				t.Fatalf("unknown minute=%+v err=%v", p, err)
			}
		}
	}
	p, err := s.History(ctx, id, now)
	if err != nil || len(p) != 1 || p[0].RateUnavailable || p[0].RXRate != 15 || p[0].TXRate != 15 || p[0].RXTotal != 500 {
		t.Fatalf("minute=%+v err=%v", p, err)
	}
}

func TestNetworkStatusReadSnapshotLeavesReportWriterAvailable(t *testing.T) {
	s, id, _ := testStore(t, filepath.Join(t.TempDir(), "network-status.db"))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	read, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	checkEmpty := func() {
		t.Helper()
		var n int
		if err := read.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_state`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("read snapshot count=%d err=%v", n, err)
		}
	}
	checkEmpty()
	now := time.Unix(1800000000, 0)
	r := validReport(id, 1, "session", "boot", 1, 100, 200)
	if _, accepted, reason, err := s.ProcessReport(ctx, id, r, now); err != nil || !accepted {
		t.Fatalf("writer during status read: accepted=%v reason=%s err=%v", accepted, reason, err)
	}
	checkEmpty()
}
