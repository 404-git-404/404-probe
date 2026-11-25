package storage

import (
	"testing"

	"404-probe/internal/protocol"
)

func TestQualityTCPLossBuckets(t *testing.T) {
	cases := []struct {
		outcome, stage              string
		attempts, loss, errors, dns int
	}{
		{"success", "connect", 1, 0, 0, 0},
		{"timeout", "connect", 1, 1, 0, 0},
		{"refused", "connect", 1, 0, 1, 0},
		{"network_error", "connect", 1, 0, 1, 0},
		{"dns_error", "dns", 0, 0, 1, 1},
		{"canceled", "", 0, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.outcome, func(t *testing.T) {
			d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, ProbeComplete: c.stage != "", ConnectComplete: c.stage == "connect"}
			old := protocol.QualitySample{Outcome: c.outcome, Details: d.Encode()}
			d.ConnectTimeoutMS = 1000
			fresh := protocol.QualitySample{Outcome: c.outcome, Details: d.Encode()}
			if qualitySampleSemantics(old) != protocol.QualityStagedSemantics || qualitySampleSemantics(fresh) != protocol.QualityTCPLossSemantics {
				t.Fatal("TCP semantic versions were merged")
			}
			oldBucket, err := qualityContribution(old, "tcp")
			if err != nil {
				t.Fatal(err)
			}
			if stats := networkStats(oldBucket, "tcp"); !stats.OldTCP || stats.PacketLossPercent != nil {
				t.Fatalf("old stats: %+v", stats)
			}
			b, err := qualityContribution(fresh, "tcp")
			if err != nil {
				t.Fatal(err)
			}
			wire, err := qualityEncodeBucket(b)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := qualityDecodeBucket(wire)
			want := qualityTCPBucket{Samples: 1, Attempts: c.attempts, Loss: c.loss, Errors: c.errors, DNS: c.dns}
			if err != nil || roundTrip.TCP != want {
				t.Fatalf("got %+v, want %+v (%v)", roundTrip.TCP, want, err)
			}
		})
	}
}

func TestQualityTCPTimeoutHighLatencyBoundary(t *testing.T) {
	for _, tc := range []struct {
		rtts []float64
		want int
	}{
		{nil, 1000}, {[]float64{601}, 3000}, {[]float64{900, 300}, 1000},
		{[]float64{700, 500, 601}, 3000}, {[]float64{1100, 50, 50}, 1000},
	} {
		if got := qualityTCPTimeoutFor(tc.rtts); got != tc.want {
			t.Fatalf("RTTs %v: timeout %d", tc.rtts, got)
		}
	}
}

func TestQualityCellThresholds(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		p50, loss, errors *float64
		want              QualityCardState
	}{
		{nil, nil, nil, QualityCardGray},
		{p(500), p(0), p(0), QualityCardGreen},
		{p(500.25), nil, nil, QualityCardYellow},
		{p(1000), nil, nil, QualityCardYellow},
		{p(1000.25), nil, nil, QualityCardRed},
		{p(1), p(.1), p(0), QualityCardYellow},
		{p(1), p(19.99), p(0), QualityCardYellow},
		{p(1), p(20), p(0), QualityCardRed},
		{p(1), p(0), p(.1), QualityCardYellow},
		{p(1), p(0), p(19.99), QualityCardYellow},
		{p(550), p(0), p(20), QualityCardRed},
		{p(1100), p(0), p(4), QualityCardRed},
	} {
		stats := NetworkStats{Samples: 6, P50: c.p50, PacketLossPercent: c.loss, ErrorPercent: c.errors}
		if got, reason := qualityCellColor(stats, 500); got != c.want || reason == "" {
			t.Fatalf("stats %+v: %s %s", stats, got, reason)
		}
	}
}

func TestQualityLossAlertThresholdAndContinuity(t *testing.T) {
	for _, c := range []struct {
		minutes, lost, gap int
		want               bool
	}{
		{5, 19, -1, false}, {5, 20, -1, true}, {4, 20, -1, false}, {5, 20, 2, false},
	} {
		tl := tlOf(c.minutes)
		for m := 0; m < c.minutes; m++ {
			tl.data(m, 100, c.lost, c.lost, i30)
			tl.Mins[m].Bucket = &qualityBucketData{TCP: qualityTCPBucket{Samples: 100, Attempts: 100, Loss: c.lost}}
		}
		if c.gap >= 0 {
			tl.unusable(c.gap)
		}
		if got := tl.red(c.minutes-1, i30); got != c.want {
			t.Fatalf("case %+v: red=%t", c, got)
		}
	}
}

func TestDeviceScoreUsesGreaterLossAndAddsErrors(t *testing.T) {
	tcp, icmp, errors := 4.0, 30.0, 3.0
	if v := deviceScoreTCPFailure(&tcp, &errors, &icmp); v == nil || *v != 33 {
		t.Fatalf("TCP/ICMP max plus errors=%v", v)
	}
	if v := deviceScoreTCPFailure(&tcp, &errors, nil); v == nil || *v != 7 {
		t.Fatal("TCP fallback")
	}
	if v := deviceScoreTCPFailure(nil, nil, &icmp); v != nil {
		t.Fatal("score published without new TCP data")
	}
}

func TestNetworkTCPLossIngestAndMarker(t *testing.T) {
	c := newCardKit(t)
	old := c.userTCPSample(c.cfg, 1, c.at(1), 75)
	fresh := c.userTCPSample(c.cfg, 2, c.at(2), 90)
	details, err := protocol.DecodeQualityDetails(fresh.Details)
	if err != nil || details == nil {
		t.Fatalf("details %v: %v", details, err)
	}
	details.ConnectTimeoutMS = 1000
	fresh.Details = details.Encode()
	now := c.at(7)
	if acks := c.upload(now, old, fresh); len(acks) != 2 || acks[0] != "committed" || acks[1] != "committed" {
		t.Fatalf("mixed batch: %v", acks)
	}
	out, err := c.s.NetworkHistory(c.ctx, c.id, r39TCP, c.at(0).UnixMilli(), now.UnixMilli(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Points) < 3 || !out.Points[1].OldTCP || out.Points[1].PacketLossPercent != nil || out.Points[2].OldTCP ||
		out.Points[2].PacketLossPercent == nil || *out.Points[2].PacketLossPercent != 0 ||
		out.SemanticsChangeAt == nil || *out.SemanticsChangeAt != fresh.StartedAt {
		t.Fatalf("range TCP semantic separation: %+v", out)
	}
	card := c.telecom(c.summary(now, "ipv4"))
	if card.TCP.SemanticsChangeAt == nil || *card.TCP.SemanticsChangeAt != fresh.StartedAt {
		t.Fatalf("missing card marker: %+v", card.TCP.SemanticsChangeAt)
	}
}

func TestQualityTCPLossConfigNegotiatesHighLatency(t *testing.T) {
	c := newCardKit(t)
	sample := c.userTCPSample(c.cfg, 1, c.at(1), 700)
	now := c.at(2)
	if acks := c.upload(now, sample); len(acks) != 1 || acks[0] != "committed" {
		t.Fatalf("sample: %v", acks)
	}
	for _, negotiated := range []bool{false, true} {
		cfg, err := c.s.GetQualitySessionConfigAt(c.ctx, c.id, c.epoch, c.session, 2, true, negotiated, now)
		if err != nil {
			t.Fatal(err)
		}
		target := c.targetID(c.cfg, "telecom", "ipv4", "tcp")
		found := false
		for _, lane := range cfg.Targets {
			if lane.ID != target {
				continue
			}
			found = true
			want := 0
			if negotiated {
				want = 3000
			}
			if lane.ConnectTimeoutMS != want {
				t.Fatalf("negotiated=%t, got %d want %d", negotiated, lane.ConnectTimeoutMS, want)
			}
		}
		if !found {
			t.Fatal("missing target")
		}
	}
}
