package storage

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

func a2TCP(t protocol.QualityTarget, revision string, seq int64, at time.Time, ms float64, outcome string) protocol.QualitySample {
	s := qualitySample(t, revision, seq, at, ms, outcome)
	s.ResolvedIP = t.Host
	s.DurationMS = 2500
	s.FinishedAt = s.StartedAt + 2500
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "literal", ProbeComplete: true, ConnectAttempted: true, ConnectComplete: true, ConnectMS: &ms, IntervalMS: 30000}
	if outcome != "success" {
		d.ErrorStage = "connect"
		d.ErrorCode = outcome
	}
	s.Details = d.Encode()
	return s
}
func a2Echo(t protocol.QualityTarget, revision string, seq int64, at time.Time, values []float64) protocol.QualitySample {
	s := qualitySample(t, revision, seq, at, 0, "no_echo_reply")
	s.ResolvedIP = t.Host
	s.DurationMS = 2500
	s.FinishedAt = s.StartedAt + 2500
	s.LatencyMS = nil
	s.Sent = 3
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "literal", ProbeComplete: true, WindowComplete: true, Requested: 3, WindowMS: 2000}
	sum := 0.0
	for i, v := range values {
		offset := float64(i * 200)
		p := protocol.QualityEcho{Seq: i + 1, SentMS: &offset, Status: "timeout"}
		if v >= 0 {
			p.Status = "reply"
			p.RTTMS = &v
			s.Received++
			sum += v
		}
		d.Echoes = append(d.Echoes, p)
	}
	if s.Received > 0 {
		s.Outcome = "partial"
	}
	if s.Received == 3 {
		s.Outcome = "success"
		mean := sum / 3
		s.LatencyMS = &mean
	}
	s.Details = d.Encode()
	return s
}
func closeA2(t *testing.T, p *float64, want float64) {
	t.Helper()
	if p == nil || math.Abs(*p-want) > 1e-8 {
		t.Fatalf("got %v want %v", p, want)
	}
}
func TestA2StatisticsActualDenominatorsAndBreaks(t *testing.T) {
	target := protocol.QualityTarget{Protocol: "tcp"}
	at := time.Now().Add(-time.Minute)
	rows := []qualityHistoryRow{}
	for i, v := range []float64{10, 20, -1, 40, 80} {
		outcome := "success"
		if v < 0 {
			outcome = "refused"
			v = 1
		}
		rows = append(rows, qualityHistoryRow{sample: a2TCP(target, "1", int64(i+1), at.Add(time.Duration(i)*30*time.Second), v, outcome), epoch: 1, session: "s", position: i + 1})
	}
	stats := qualityStatsV2(rows, "tcp", nil, at.UnixMilli(), at.Add(3*time.Minute).UnixMilli())
	closeA2(t, stats.Jitter, 25)
	if stats.JitterPairs != 2 || stats.ValidRTTs != 4 || stats.ProbeAttempts != 5 || stats.ConnectAttempts != 5 {
		t.Fatal(stats)
	}
	closeA2(t, stats.ConnectSuccessPercent, 80)
	rows[1].position = 3
	rows[2].position = 4
	rows[3].position = 5
	rows[4].position = 6
	stats = qualityStatsV2(rows, "tcp", nil, 0, time.Now().UnixMilli())
	closeA2(t, stats.Jitter, 40)
	if stats.JitterPairs != 1 {
		t.Fatal("semantic boundary bridged", stats)
	}
	target.Protocol = "icmp"
	rows = []qualityHistoryRow{{sample: a2Echo(target, "1", 1, at, []float64{10, 20, 40})}, {sample: a2Echo(target, "1", 2, at.Add(30*time.Second), []float64{15, 35, -1})}}
	stats = qualityStatsV2(rows, "icmp", nil, 0, time.Now().UnixMilli())
	closeA2(t, stats.P50, 20)
	closeA2(t, stats.P95, 39)
	closeA2(t, stats.Jitter, 50.0/3)
	closeA2(t, stats.PacketLossPercent, 100.0/6)
	if stats.JitterPairs != 3 || stats.ValidRTTs != 5 || stats.Sent != 6 || stats.Received != 5 {
		t.Fatal(stats)
	}
	d, _ := protocol.DecodeQualityDetails(rows[1].sample.Details)
	d.ProbeComplete = false
	d.WindowComplete = false
	d.Echoes[2].Status = "canceled"
	rows[1].sample.Outcome = "canceled"
	rows[1].sample.Details = d.Encode()
	stats = qualityStatsV2(rows, "icmp", nil, 0, time.Now().UnixMilli())
	closeA2(t, stats.PacketLossPercent, 0)
	if stats.Sent != 3 || stats.ValidRTTs != 5 || stats.Completeness != "partial" {
		t.Fatal("incomplete window became loss", stats)
	}
}
func TestA2ActualSQLiteHistorySemanticsRetryAndCurrentFailure(t *testing.T) {
	s, id, old, now := qualityFixture(t, ":memory:")
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	cfg, err := s.UpdateQualityConfigV2(context.Background(), id, a1LaneConfig(old.Revision), qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	target := a1Target(t, cfg, "telecom", "ipv4", "icmp")
	samples := []protocol.QualitySample{a2Echo(target, cfg.Revision, 1, now.Add(-time.Minute), []float64{10, 20, 40}), a2Echo(target, cfg.Revision, 2, now.Add(-30*time.Second), []float64{15, 35, -1})}
	b := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: samples}
	ack, err := s.SaveQualityBatch(context.Background(), id, b, now)
	if err != nil || ack.Results[0][2] != "committed" || ack.Results[1][2] != "committed" {
		t.Fatal(ack, err)
	}
	ack, err = s.SaveQualityBatch(context.Background(), id, b, now)
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal(ack, err)
	}
	h, err := s.QualityHistoryV2(context.Background(), id, target.ID, 1, now)
	if err != nil || len(h.Groups) != 1 {
		t.Fatal(h, err)
	}
	stats := h.Groups[0].Window
	closeA2(t, stats.PacketLossPercent, 100.0/6)
	closeA2(t, stats.P95, 39)
	legacy := qualitySample(target, cfg.Revision, 3, now.Add(-10*time.Second), 999, "success")
	legacy.Sent = 1
	legacy.Received = 1
	ack, err = s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{legacy}}, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	h, err = s.QualityHistoryV2(context.Background(), id, target.ID, 1, now)
	if err != nil || len(h.Groups) != 2 {
		t.Fatal(h, err)
	}
	closeA2(t, h.Groups[0].Window.P95, 999)
	closeA2(t, h.Groups[1].Window.P95, 39)
	if !h.Groups[1].Stale {
		t.Fatal("older semantics considered current")
	}
	oldHistory, err := s.QualityHistory(context.Background(), id, target.ID, 1, now)
	if err != nil || oldHistory.Window.Count != 1 {
		t.Fatal(oldHistory, err)
	}
	closeA2(t, oldHistory.Window.P95, 999)
	failure := a2Echo(target, cfg.Revision, 4, now.Add(-5*time.Second), []float64{-1, -1, -1})
	d, _ := protocol.DecodeQualityDetails(failure.Details)
	d.ProbeComplete = false
	d.WindowComplete = false
	d.ErrorStage = "socket"
	d.ErrorCode = "permission_denied"
	for i := range d.Echoes {
		d.Echoes[i].Status = "not_sent"
		d.Echoes[i].SentMS = nil
	}
	failure.Sent = 0
	failure.Outcome = "permission_denied"
	failure.Details = d.Encode()
	ack, err = s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{failure}}, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	h, err = s.QualityHistoryV2(context.Background(), id, target.ID, 1, now)
	if err != nil || h.Groups[1].Latest.Outcome != "permission_denied" || !h.Groups[1].Stale {
		t.Fatal(h, err)
	}
	wire, _ := json.Marshal(h)
	t.Log(string(wire))
	if h.Groups[1].FreshnessBudgetMS != 76000 {
		t.Fatal("unexpected timing budget")
	}
}
func TestA2CapacityPlansFairSmallLargeRootAndRecovery(t *testing.T) {
	for _, n := range []int{10, 30, 50, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			for _, size := range []int64{2 << 30, 256 << 30} {
				d := qualityDisk{DB: 1 << 20, Free: size * 9 / 10, Total: size, RootFree: size * 9 / 10, RootTotal: size}
				p := qualityDiskPlan(d, 1<<20, 1536, n, n*12)
				if p.Pressure {
					if size == 2<<30 && p.Scale == 10 {
						t.Logf("capacity unavailable within bounded cadence: agents=%d budget=%d", n, p.Bytes)
						continue
					}
					t.Fatal(p)
				}
				raw, minute := p.limits(12)
				if raw < 1 || minute < 1 || raw*n > p.RawLimit || minute*n > p.MinuteLimit {
					t.Fatal("unfair/unbounded", p, raw, minute)
				}
				if size == 256<<30 && (p.Scale != 1 || raw < 12*7920 || minute < 12*3168) {
					t.Fatal("48h normal capacity not met", p)
				}
				t.Logf("agents=%d volume=%d budget=%d interval=%d raw/agent=%d minute/agent=%d", n, size, p.Bytes, p.Scale*30000, raw, minute)
			}
		})
	}
	d := qualityDisk{Free: 8 << 30, Total: 16 << 30, RootFree: 8 << 20, RootTotal: 1 << 30}
	p := qualityDiskPlan(d, 0, 1536, 10, 120)
	if !p.Pressure {
		t.Fatal("root filesystem full ignored")
	}
	d.RootFree = 800 << 20
	p = qualityDiskPlan(d, 0, 1536, 10, 120)
	if p.Pressure {
		t.Fatal("headroom recovery failed", p)
	}
	d = qualityDisk{DB: 128 << 20, WAL: 192 << 20, Free: 1 << 30, Total: 8 << 30, RootFree: 1 << 30, RootTotal: 8 << 30}
	p = qualityDiskPlan(d, 128<<20, 1536, 1, 12)
	if p.Pressure {
		t.Fatal("reusable WAL ignored available headroom", p)
	}
}

func TestA2QuotaShrinkRotatesAndRetiresProtectedHistory(t *testing.T) {
	s, id, old, now := qualityFixture(t, ":memory:")
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	cfg, err := s.UpdateQualityConfigV2(context.Background(), id, a1LaneConfig(old.Revision), qualityTestResolver, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	target := a1Target(t, cfg, "telecom", "ipv4", "tcp")
	samples := []protocol.QualitySample{}
	for i := 0; i < 120; i++ {
		samples = append(samples, a2TCP(target, cfg.Revision, int64(i+1), now.Add(-time.Hour).Add(time.Duration(i)*time.Second), 10, "success"))
	}
	for i := 0; i < len(samples); i += 60 {
		ack, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: samples[i : i+60]}, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range ack.Results {
			if a[2] != "committed" {
				t.Fatal(ack)
			}
		}
	}
	s.qualityBudgetCache = qualityBudget{Scale: 1, RawLimit: 96, MinuteLimit: 96, Agents: 1, Lanes: 12}
	s.qualityBudgetAt = time.Now()
	fresh := a2TCP(target, cfg.Revision, 121, now.Add(-time.Second), 10, "success")
	b := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{fresh}}
	ack, err := s.SaveQualityBatch(context.Background(), id, b, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	deleted, _, err := s.CleanupQualityBatch(context.Background(), now.Add(time.Hour))
	if err != nil || deleted != 0 {
		t.Fatal(deleted, err)
	}
	ack, err = s.SaveQualityBatch(context.Background(), id, b, now.Add(time.Hour))
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal(ack, err)
	}
	b.Samples = samples[:1]
	ack, err = s.SaveQualityBatch(context.Background(), id, b, now)
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal("48h receipt lost after raw rotation", ack, err)
	}
	qualityCounts(t, s, id, 1)
	changed := samples[0]
	changed.StartedAt = fresh.StartedAt
	changed.ScheduledAt = fresh.ScheduledAt
	changed.FinishedAt = fresh.FinishedAt
	b.Samples = []protocol.QualitySample{changed}
	ack, err = s.SaveQualityBatch(context.Background(), id, b, now)
	if err != nil || ack.Results[0][2] != "conflict" {
		t.Fatal("changed-payload receipt failed conflict", ack, err)
	}
	t.Log("schema27 quota shrink: Raw rotated atomically, 48h receipt preserves duplicate/conflict identity; aggregate observations retained without double counting")
}
