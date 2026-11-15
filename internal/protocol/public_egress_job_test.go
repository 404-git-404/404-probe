package protocol

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPublicEgressJobConfigHasNoParameters(t *testing.T) {
	config, err := DecodeProbeConfig(ProbeTypePublicEgress, []byte(`{}`))
	if err != nil || config.PublicEgress == nil {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	if typ, err := config.Type(); err != nil || typ != ProbeTypePublicEgress {
		t.Fatalf("type=%q err=%v", typ, err)
	}
	if data, err := MarshalProbeConfig(ProbeTypePublicEgress, config); err != nil || string(data) != `{}` {
		t.Fatalf("marshal %q err=%v", data, err)
	}
	for _, raw := range []string{`{"url":"https://example.com"}`, `{"host":"203.0.113.9"}`, `{"family":"ipv4"}`, `{"extra":1}`, `[]`, `null`, `"x"`, `1`, ``} {
		if _, err := DecodeProbeConfig(ProbeTypePublicEgress, []byte(raw)); err == nil {
			t.Errorf("config %q was accepted", raw)
		}
	}
	if err := (ProbeConfig{}).Validate(ProbeTypePublicEgress); err == nil {
		t.Error("an empty union validated as a public_egress config")
	}
	if err := (ProbeConfig{PublicEgress: &PublicEgressConfig{}, GoogleStatus: &GoogleStatusConfig{}}).Validate(ProbeTypePublicEgress); err == nil {
		t.Error("two typed configs validated")
	}
}

func TestPublicEgressJobTypeAndClaimLimits(t *testing.T) {
	if ProbeTypePublicEgress.Validate() != nil || ProbeTypePublicEgress.IsNetworkProbe() {
		t.Fatal("public_egress must be a valid, non-network (schedule-less) probe type")
	}
	request := ClaimRequest{ProtocolVersion: JobProtocolVersion, AgentEpoch: 1, SessionID: "s",
		SupportedProbeTypes: []ProbeType{ProbeTypeICMPPing, ProbeTypeTCPConnect, ProbeTypeHTTP, ProbeTypeSelectorSwitch, ProbeTypeGoogleStatus, ProbeTypePublicEgress}}
	if err := request.Validate(); err != nil {
		t.Fatalf("all six capabilities: %v", err)
	}
	if _, err := DecodeClaimRequest([]byte(`{"protocol_version":1,"agent_epoch":1,"session_id":"s","supported_probe_types":["public_egress"]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPublicEgressResultValidation(t *testing.T) {
	good := PublicEgressResult{
		IPv4: EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified"},
		IPv6: EgressObservation{Family: "ipv6", Status: "unavailable", Reason: "no_route"},
	}
	if err := good.Validate(); err != nil || !good.Succeeded() {
		t.Fatalf("good result: %v", err)
	}
	if (PublicEgressResult{IPv4: EgressObservation{Family: "ipv4", Status: "unknown", Reason: "timeout"}, IPv6: EgressObservation{Family: "ipv6", Status: "unknown", Reason: "dns"}}).Succeeded() {
		t.Fatal("two failures counted as success")
	}
	mutate := func(edit func(*PublicEgressResult)) PublicEgressResult { value := good; edit(&value); return value }
	for name, bad := range map[string]PublicEgressResult{
		"private v4":        mutate(func(r *PublicEgressResult) { r.IPv4.IP = "10.1.2.3" }),
		"documentation v4":  mutate(func(r *PublicEgressResult) { r.IPv4.IP = "203.0.113.5" }),
		"v6 in the v4 slot": mutate(func(r *PublicEgressResult) { r.IPv4.IP = "2606:4700:4700::1111" }),
		"wrong family":      mutate(func(r *PublicEgressResult) { r.IPv4.Family = "ipv6" }),
		"unverified":        mutate(func(r *PublicEgressResult) { r.IPv4.Reason = "network" }),
		"address on failure": mutate(func(r *PublicEgressResult) {
			r.IPv6 = EgressObservation{Family: "ipv6", Status: "unknown", IP: "2606:4700:4700::1111", Reason: "timeout"}
		}),
		"unknown reason": mutate(func(r *PublicEgressResult) {
			r.IPv6 = EgressObservation{Family: "ipv6", Status: "unknown", Reason: "not_checked"}
		}),
		"bad status":         mutate(func(r *PublicEgressResult) { r.IPv6.Status = "ok" }),
		"agent last success": mutate(func(r *PublicEgressResult) { r.IPv4.LastSuccess = &EgressSuccess{IP: "8.8.8.8", CheckedAt: 1} }),
		"negative time":      mutate(func(r *PublicEgressResult) { r.IPv4.CheckedAt = -1 }),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := DecodeProbeResult(ProbeTypePublicEgress, []byte(`{"ipv4":{"family":"ipv4","status":"available","ip":"8.8.8.8","checked_at":0,"valid_until":0,"reason":"verified"},"ipv6":{"family":"ipv6","status":"unavailable","checked_at":0,"valid_until":0,"reason":"no_route"},"extra":1}`)); err == nil {
		t.Error("an unknown result field was accepted")
	}
}

func TestPublicEgressJobValidityConstants(t *testing.T) {
	if EgressJobInterval != 7*24*time.Hour || EgressJobSuccessTTL != 8*24*time.Hour || EgressJobSuccessTTL <= EgressJobInterval {
		t.Fatalf("interval=%v ttl=%v", EgressJobInterval, EgressJobSuccessTTL)
	}
	// The stricter limits for telemetry from v1.0.3 Agents are unchanged.
	long := EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified", CheckedAt: 1000, ValidUntil: 1000 + EgressJobSuccessTTL.Milliseconds()}
	if got := NormalizeEgress(long, "ipv4", 1000, 1000); got.Reason != "invalid_report" {
		t.Fatalf("telemetry with an eight-day lifetime was accepted: %+v", got)
	}
	if got := NormalizeStoredEgress(long, "ipv4", 1000, 1000); got.Status != "available" {
		t.Fatalf("stored job result rejected: %+v", got)
	}
}

func TestPublicEgressJobSiteFields(t *testing.T) {
	const shape = `{"ipv4":{"family":"ipv4","status":"available","ip":"8.8.8.8","checked_at":0,"valid_until":0,"reason":"verified"%s},"ipv6":{"family":"ipv6","status":"unknown","checked_at":0,"valid_until":0,"reason":"timeout","site":"cloudflare","tried":3}}`
	for _, tt := range []struct {
		name, fields, site string
		tried              int
	}{
		{"old Agent", "", "", 0},
		{"valid", `,"site":"icanhazip","tried":2`, EgressSiteIcanhazip, 2},
		{"unknown site", `,"site":"unlisted","tried":1`, "", 1},
		{"count out of range", `,"site":"ipify","tried":4`, EgressSiteIPify, 0},
		{"wrong JSON types", `,"site":[],"tried":"3"`, "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(shape, tt.fields))
			result, err := DecodeProbeResult(ProbeTypePublicEgress, raw)
			if err != nil {
				t.Fatal(err)
			}
			v4, v6 := result.PublicEgress.IPv4, result.PublicEgress.IPv6
			if v4.IP != "8.8.8.8" || v4.Site != tt.site || v4.Tried != tt.tried ||
				v6.Reason != "timeout" || v6.Site != "" || v6.Tried != 3 {
				t.Fatalf("IPv4 %+v, IPv6 %+v", v4, v6)
			}
			wire, err := MarshalProbeResult(ProbeTypePublicEgress, result)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := DecodeProbeResult(ProbeTypePublicEgress, wire)
			if err != nil || *roundTrip.PublicEgress != *result.PublicEgress {
				t.Fatalf("round trip: %s, %v", wire, err)
			}
		})
	}
	for _, extra := range []string{`,"unknown":true`, `,"last_success":{"ip":"8.8.8.8","checked_at":1,"unknown":true}`} {
		if _, err := DecodeProbeResult(ProbeTypePublicEgress, []byte(fmt.Sprintf(shape, extra))); err == nil {
			t.Fatalf("accepted invalid core fields: %s", extra)
		}
	}
	const stored = `{"version":1,"ipv4":{"family":"ipv4","status":"available","ip":"8.8.8.8","checked_at":1000,"valid_until":2000,"reason":"verified","site":true,"tried":2}}`
	var telemetry PublicEgress
	if !strictEgressJSON([]byte(stored), &telemetry) {
		t.Fatal("failed to decode legacy telemetry envelope")
	}
	for _, normalize := range []func(EgressObservation, string, int64, int64) EgressObservation{NormalizeEgress, NormalizeStoredEgress} {
		got := normalize(telemetry.IPv4, "ipv4", 1000, 1000)
		if got.Site != "" || got.Tried != 2 || got.Status != "available" {
			t.Fatalf("normalized: %+v", got)
		}
	}
	wire, err := MarshalProbeResult(ProbeTypePublicEgress, ProbeResult{PublicEgress: &PublicEgressResult{
		IPv4: EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified", Site: "arbitrary", Tried: -1},
		IPv6: UnknownEgress("ipv6", "timeout"),
	}})
	if err != nil || strings.Contains(string(wire), `"site"`) || strings.Contains(string(wire), `"tried"`) {
		t.Fatalf("invalid metadata entered the wire: %s, %v", wire, err)
	}
}
