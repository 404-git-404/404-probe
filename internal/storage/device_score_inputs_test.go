package storage

import (
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestDeviceScorePresenceForContinuouslyOnlineAgent(t *testing.T) {
	now := time.Date(2027, 1, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		since time.Duration
		prune bool
		want  time.Duration
	}{
		{"first report at scoring time", 0, false, 0},
		{"first report within the window", 2 * 24 * time.Hour, false, 2 * 24 * time.Hour},
		{"first report before the window", 8 * 24 * time.Hour, false, 7 * 24 * time.Hour},
		{"anchor only", 31 * 24 * time.Hour, true, 7 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, _ := testStore(t, ":memory:")
			defer s.Close()
			ctx := t.Context()
			// A pre-migration agent has no synthetic registration event.
			if _, err := s.db.ExecContext(ctx, "DELETE FROM agent_presence_events WHERE agent_id=?", id); err != nil {
				t.Fatal(err)
			}
			report := validReport(id, 1, "session", "boot", 1, 1, 1)
			if _, accepted, reason, err := s.ProcessReportWithOfflineTimeout(ctx, id, report, now.Add(-tc.since), tc.since+time.Minute); err != nil || !accepted {
				t.Fatalf("report accepted=%t, reason=%s: %v", accepted, reason, err)
			}
			if tc.prune {
				if err := s.SweepPresence(ctx, now); err != nil {
					t.Fatal(err)
				}
			}
			inputs, err := s.ReadDeviceScoreInputs(ctx, id, now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.since == 0 && inputs.Resources == nil {
				t.Error("the current minute's resource sample is missing")
			}
			got := inputs.Presence
			if got.OnlinePercent == nil || *got.OnlinePercent != 100 {
				t.Errorf("presence = %+v, want 100%% online", got)
			}
			if got.Start != now.Add(-tc.want).UnixMilli() || got.OnlineMS != tc.want.Milliseconds() || got.OfflineMS != 0 {
				t.Errorf("presence = %+v, want window since %v ago with no downtime", got, tc.want)
			}
			if tc.prune && (got.InitialState != "online" || len(got.Events) != 0) {
				t.Errorf("presence = %+v, want online anchor and no events", got)
			}
		})
	}
}

func TestDeviceScoreLinePriorityAndAlertWindow(t *testing.T) {
	lines := []NetworkLine{{"test", "ipv4", "tcp"}, {"test", "ipv6", "tcp"}, {"test", "ipv4", "icmp"}, {"test", "ipv6", "icmp"}}
	targets := make(map[NetworkLine]*protocol.QualityTarget)
	for _, line := range lines {
		targets[line] = &protocol.QualityTarget{}
	}
	for _, want := range lines {
		got, ok := preferredDeviceScoreLine("test", targets)
		if !ok || got != want {
			t.Fatalf("preferred %+v, want %+v", got, want)
		}
		delete(targets, want)
	}
	if _, ok := preferredDeviceScoreLine("test", targets); ok {
		t.Fatal("selected missing lane")
	}
	finish, overlapping, other := int64(60), int64(80), int64(120)
	alerts := []NetworkAlert{
		{NetworkLine: lines[0], Start: 100},
		{NetworkLine: lines[1], Start: 90, End: &other},
		{NetworkLine: lines[0], Start: 50, End: &overlapping},
		{NetworkLine: lines[0], Start: 10, End: &finish},
	}
	if got := deviceScoreAlertDuration(alerts, lines[0], 20, 110); got != 70 {
		t.Fatalf("clipped union = %d, want 70", got)
	}
}

func TestDeviceScoreInputsReadStoredNetworkWithDNS(t *testing.T) {
	c := newCardKit(t)
	now := time.Date(2027, 1, 18, 12, 0, 0, 0, time.UTC)
	start := now.Add(-DeviceScoreWindow)
	series := c.series("telecom", "ipv4", "tcp")
	bucket := c.tcp(89, 0, 1, 600)
	bucket.Stats.Count += 10
	bucket.Stats.Categories["dns_error"] = 10
	bucket.TCP = qualityTCPBucket{Samples: 100, Attempts: 90, Loss: 1, Errors: 10, DNS: 10}
	prior, err := c.s.ReadDeviceScoreInputs(c.ctx, c.id, now)
	if err != nil || len(prior.Lines) != 1 || prior.Lines[0].CoveredMS != 0 {
		t.Fatalf("old series coverage: %+v (%v)", prior.Lines, err)
	}
	if _, err := c.s.db.Exec(`UPDATE quality_series SET semantics=? WHERE id=?`, protocol.QualityTCPLossSemantics, series); err != nil {
		t.Fatal(err)
	}
	bucket.FirstID, bucket.LastID = 1, 2
	bucket.FirstTime = start.Add(10 * time.Minute).UnixMilli()
	bucket.LastTime = start.Add(20 * time.Minute).UnixMilli()
	target := c.targetID(c.cfg, "telecom", "ipv4", "tcp")
	for _, at := range []time.Time{start.Add(2 * time.Minute), start.Add(15 * time.Minute)} {
		if _, err := c.s.db.Exec(`INSERT INTO quality_alert_history(agent_id,slot,family,protocol,target_id,revision,start_at,end_at)
   VALUES(?,'telecom','ipv4','tcp',?,1,?,?)`, c.id, target, at.UnixMilli(), at.Add(time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	c.versions(&bucket, "ipv4", "tcp")
	wire, err := qualityEncodeBucket(bucket)
	if err != nil {
		t.Fatal(err)
	}
	for _, resolution := range []int64{deviceScoreCoverageBucket, qualityHourMS} {
		end := start.Add(time.Hour).UnixMilli()
		_, err := c.s.db.Exec(`WITH RECURSIVE buckets(at) AS (
			VALUES(?1) UNION ALL SELECT at+?2 FROM buckets WHERE at+?2<?3
		) INSERT INTO quality_buckets SELECT ?4,?2,at,?5 FROM buckets`,
			start.UnixMilli(), resolution, end, series, wire)
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.s.ReadDeviceScoreInputs(c.ctx, c.id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 1 {
		t.Fatalf("got %+v", out.Lines)
	}
	got := out.Lines[0]
	if got.CoveredMS != time.Hour.Milliseconds() || got.FailurePercent == nil || *got.FailurePercent != 10+100.0/90 || got.P50 == nil || *got.P50 != 50 || got.Evening == nil || got.AlertMS != time.Minute.Milliseconds() {
		t.Fatalf("stored line %+v", got)
	}
}
