package protocol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestQualityDecimalAndWorstACK(t *testing.T) {
	for _, s := range []string{"", "0", "01", "+1", "-1", "1.0", "1e3", "9223372036854775808"} {
		if _, err := QualityInteger(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	if _, err := QualityInteger("9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	response := QualityBatchResponse{}
	for i := 0; i < 64; i++ {
		response.Results = append(response.Results, QualityAck{"63", "9223372036854775807", "storage_pressure"})
	}
	for i := 0; i < 8; i++ {
		response.Gaps = append(response.Gaps, QualityAck{"7", "", "storage_pressure"})
	}
	wire, err := json.Marshal(response)
	if err != nil || len(wire)+1 > QualityResponseLimit {
		t.Fatalf("ACK bytes=%d err=%v", len(wire), err)
	}
	t.Logf("worst 64 sequence ACK +8 gap ACK bytes=%d", len(wire)+1)
}
func TestQualitySampleFamilyAndDenominators(t *testing.T) {
	now := time.Unix(200000, 0)
	latency := 12.0
	base := QualitySample{Sequence: "9007199254740993", TargetID: "target", ConfigRevision: "1", SlotRevision: "1", ScheduledAt: now.UnixMilli() - 100, StartedAt: now.UnixMilli() - 100, FinishedAt: now.UnixMilli() - 88, DurationMS: 12, Outcome: "success", ResolvedIP: "127.0.0.1", LatencyMS: &latency}
	target := QualityTarget{Protocol: "tcp", Family: "ipv4"}
	if reason := base.Validate(now, target); reason != "" {
		t.Fatal(reason)
	}
	wire, _ := json.Marshal(base)
	if !strings.Contains(string(wire), `"seq":"9007199254740993"`) {
		t.Fatal(string(wire))
	}
	for _, change := range []func(*QualitySample){func(s *QualitySample) { s.ResolvedIP = "::1" }, func(s *QualitySample) { s.ResolvedIP = "not_an_ip" }, func(s *QualitySample) { s.Sent = 1 }, func(s *QualitySample) { s.DurationMS = math.Inf(1) }, func(s *QualitySample) { v := math.NaN(); s.LatencyMS = &v }} {
		s := base
		change(&s)
		if s.Validate(now, target) == "" {
			t.Fatalf("invalid accepted %+v", s)
		}
	}
	s := base
	s.ResolvedIP = "::ffff:127.0.0.1"
	if s.Validate(now, target) != "" {
		t.Fatal("mapped v4 rejected")
	}
	target.Family = "ipv6"
	if s.Validate(now, target) == "" {
		t.Fatal("mapped v4 accepted as v6")
	}
	target = QualityTarget{Protocol: "icmp", Family: "ipv4"}
	s = base
	s.Outcome = "dns_error"
	s.LatencyMS = nil
	s.ResolvedIP = ""
	if s.Validate(now, target) != "" || s.PacketDenominator() {
		t.Fatal("DNS/setup must not imply packets")
	}
	s.Sent = 1
	if s.Validate(now, target) == "" {
		t.Fatal("fake configured sent accepted")
	}
	for _, outcome := range []string{"canceled", "not_run", "unsupported", "permission_denied", "invalid_config"} {
		s = base
		s.Outcome = outcome
		s.LatencyMS = nil
		if s.Attempt() {
			t.Fatal(outcome)
		}
	}
}
