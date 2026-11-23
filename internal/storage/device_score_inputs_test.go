package storage

import (
	"testing"
	"time"

	"404-probe/internal/protocol"
)

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
	start := now.Add(-deviceScoreNetworkWindow)
	series := c.series("telecom", "ipv4", "tcp")
	bucket := c.tcp(89, 0, 1, 600)
	bucket.Stats.Count += 10
	bucket.Stats.Categories["dns_error"] = 10
	c.versions(&bucket, "ipv4", "tcp")
	wire, err := qualityEncodeBucket(bucket)
	if err != nil {
		t.Fatal(err)
	}
	for _, resolution := range []int64{deviceScoreCoverageBucket, qualityHourMS} {
		end := start.Add(deviceScoreNetworkWindow / 2).UnixMilli()
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
	if got.CoveredMS != deviceScoreNetworkWindow.Milliseconds()/2 || got.FailurePercent == nil || *got.FailurePercent != 11 || got.P50 == nil || *got.P50 != 50 || got.Evening == nil {
		t.Fatalf("stored line %+v", got)
	}
}
