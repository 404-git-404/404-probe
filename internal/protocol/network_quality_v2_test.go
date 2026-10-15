package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestA2RejectManufacturedCancellationDenominators(t *testing.T) {
	now := time.Now()
	target := QualityTarget{ID: "a2-target", Protocol: "tcp", Family: "ipv4"}
	s := QualitySample{Sequence: "1", ConfigRevision: "1", SlotRevision: "1", TargetID: target.ID, ScheduledAt: now.UnixMilli() - 10, StartedAt: now.UnixMilli() - 10, FinishedAt: now.UnixMilli(), DurationMS: 10, Outcome: "canceled"}
	d := QualityDetails{Semantics: QualityStagedSemantics, DNSMS: 1, DNSOutcome: "dns_error", ProbeComplete: true, ErrorStage: "dns", ErrorCode: "dns_error"}
	s.Details = d.Encode()
	if reason := s.Validate(now, target); reason != "invalid" {
		t.Fatal("cancellation manufactured probe denominator", reason)
	}
	target.Protocol = "icmp"
	s.StartedAt = now.UnixMilli() - 2500
	s.ScheduledAt = s.StartedAt
	s.DurationMS = 2500
	s.Outcome = "no_echo_reply"
	s.ResolvedIP = "127.0.0.1"
	s.Sent = 3
	d = QualityDetails{Semantics: QualityStagedSemantics, DNSOutcome: "literal", ProbeComplete: true, WindowComplete: true, Requested: 3, WindowMS: 2000, ErrorStage: "receive", ErrorCode: "canceled"}
	for i := 0; i < 3; i++ {
		offset := float64(i * 200)
		d.Echoes = append(d.Echoes, QualityEcho{Seq: i + 1, SentMS: &offset, Status: "timeout"})
	}
	s.Details = d.Encode()
	if reason := s.Validate(now, target); reason != "invalid" {
		t.Fatal("cancellation manufactured packet loss denominator", reason)
	}
}

func TestA1CapabilitiesBoundsAndV1Wire(t *testing.T) {
	c := QualityCapabilities{Version: 2, Semantics: QualityV2Semantics}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Lanes = append(c.Lanes, QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*QualityCapabilities){func(c *QualityCapabilities) { c.Version = 3 }, func(c *QualityCapabilities) { c.Semantics = "three_echo" }, func(c *QualityCapabilities) { c.Lanes[3] = c.Lanes[0] }, func(c *QualityCapabilities) { c.ExcludedReason = "platform_excluded:alpine" }, func(c *QualityCapabilities) { c.Lanes[0].Reason = "success" }} {
		bad := c
		bad.Lanes = append([]QualityLaneCapability(nil), c.Lanes...)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid capabilities accepted", bad)
		}
	}
	request, _ := json.Marshal(QualityConfigRequest{Epoch: "1", SessionID: "session"})
	if string(request) != `{"epoch":"1","session_id":"session"}` {
		t.Fatal("v1 request changed", string(request))
	}
	wire, _ := json.Marshal(QualitySample{Sequence: "1"})
	if strings.Contains(string(wire), "details") {
		t.Fatal("v1 sample hash shape changed")
	}
	config, _ := json.Marshal(QualityConfig{Version: 1, Targets: []QualityTarget{}})
	for _, field := range []string{"region_id", "semantics", "state"} {
		if strings.Contains(string(config), field) {
			t.Fatal("v2 field in v1 config", string(config))
		}
	}
	report, _ := json.Marshal(Report{})
	if strings.Contains(string(report), "network_quality_v2") {
		t.Fatal("new fields before negotiation")
	}
	u := QualityV2ConfigUpdate{ExpectedRevision: "0", RegionID: "region"}
	for _, s := range []string{"telecom", "unicom", "mobile"} {
		for _, f := range []string{"ipv4", "ipv6"} {
			for _, p := range []string{"tcp", "icmp"} {
				u.Lanes = append(u.Lanes, QualityLaneChoice{QualityChoice: QualityChoice{Slot: s, Source: "catalog", Protocol: p}, Family: f})
			}
		}
	}
	if u.Validate() != nil {
		t.Fatal("12 lanes rejected")
	}
	u.Lanes = append(u.Lanes, u.Lanes[0])
	if u.Validate() == nil {
		t.Fatal("13/duplicate lanes accepted")
	}
	for _, version := range []int{1, 2} {
		if (QualityBatch{Version: version, Epoch: "1", SessionID: "session", Gaps: []QualityGap{{Start: 1, End: 1, Reason: "restart"}}}).Validate() != nil {
			t.Fatal(version)
		}
	}
	if (QualityBatch{Version: 3, Epoch: "1", SessionID: "session"}).Validate() == nil {
		t.Fatal("unnegotiated version accepted")
	}
}
