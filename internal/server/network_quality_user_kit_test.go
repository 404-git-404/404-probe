package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// userCapabilities is the second-step capability of a current Agent.
func userCapabilities() *protocol.QualityCapabilities {
	c := &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityStagedSemantics, Targets: protocol.QualityTargetsUserV1, IPv6Address: protocol.QualityIPv6Present}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Lanes = append(c.Lanes, protocol.QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	return c
}

// userReport is a report of a current Agent after both negotiation steps.
func userReport(id string, seq uint64) protocol.Report {
	r := reportFor(id, seq)
	r.NetworkQuality = true
	r.NetworkQualityV2 = userCapabilities()
	r.QualityTargetsHeader = true
	return r
}

// grantUserTargets feeds one negotiated report to the store the way the report handler does.
func grantUserTargets(t *testing.T, s *storage.Store, id string, r protocol.Report, now time.Time) {
	t.Helper()
	if _, ok, reason, err := s.ProcessReport(context.Background(), id, r, now); err != nil || !ok {
		t.Fatal("report", ok, reason, err)
	}
	if err := s.ReconcileQualityAuthorization(context.Background(), id, storage.QualityNegotiationFromReport(r), now); err != nil {
		t.Fatal(err)
	}
}

// userSlot builds a custom slot with IPv4 TCP and ICMP selected.
func userSlot(slot, host string, port int) protocol.QualityV4Slot {
	return protocol.QualityV4Slot{Slot: slot, Kind: "custom", Region: "R-" + slot, ISP: "I-" + slot, Host: host, Port: port, SlotEnabled: true, IPv4TCP: true, IPv4ICMP: true}
}

// configureUserTargets saves the three slots with the given hosts and switches detection on.
func configureUserTargets(t *testing.T, s *storage.Store, id string, now time.Time, hosts [3]string, port int) protocol.QualityV4Config {
	t.Helper()
	ctx := context.Background()
	current, err := s.GetQualityConfigV4(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	var slots []protocol.QualityV4Slot
	for i, slot := range protocol.QualitySlotOrder() {
		slots = append(slots, userSlot(slot, hosts[i], port))
	}
	saved, err := s.SaveQualityConfigV4(ctx, id, protocol.QualityV4SaveRequest{ExpectedRevision: current.SettingsRevision, IntervalMS: 30000, Slots: slots}, now)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	enabled, err := s.SetQualityEnabledV4(ctx, id, protocol.QualityV4EnabledRequest{ExpectedRevision: saved.SettingsRevision, Enabled: &on}, now)
	if err != nil {
		t.Fatal(err)
	}
	return enabled
}

var defaultUserHosts = [3]string{"example.com", "example.org", "example.net"}

func userLane(t *testing.T, cfg protocol.QualityV4Config, slot, family, proto string) protocol.QualityV4Lane {
	t.Helper()
	for _, l := range cfg.Lanes {
		if l.Slot == slot && l.Family == family && l.Protocol == proto {
			return l
		}
	}
	t.Fatalf("no lane %s/%s/%s", slot, family, proto)
	return protocol.QualityV4Lane{}
}

// userTCPSample is a valid staged IPv4 TCP sample for the lane.
func userTCPSample(t *testing.T, cfg protocol.QualityV4Config, slot string, seq int64, at time.Time, value float64, outcome string) protocol.QualitySample {
	t.Helper()
	lane := userLane(t, cfg, slot, "ipv4", "tcp")
	if lane.TargetID == nil {
		t.Fatalf("lane %s has no target", slot)
	}
	duration := value
	if duration < 20 {
		duration = 20
	}
	sample := protocol.QualitySample{Sequence: strconv.FormatInt(seq, 10), TargetID: *lane.TargetID, ConfigRevision: cfg.ConfigRevision, SlotRevision: lane.SlotRevision,
		ScheduledAt: at.UnixMilli(), StartedAt: at.UnixMilli(), FinishedAt: at.UnixMilli() + int64(duration), DurationMS: duration, Outcome: outcome, ResolvedIP: "93.184.216.34"}
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "success", DNSMS: 5, ProbeComplete: true, ConnectAttempted: true, ConnectComplete: true, ConnectMS: &value, IntervalMS: cfg.EffectiveIntervalMS}
	if outcome == "success" {
		sample.LatencyMS = &value
	} else {
		d.ConnectComplete = false
		d.ConnectMS = nil
		d.ErrorStage = "connect"
		d.ErrorCode = outcome
	}
	sample.Details = d.Encode()
	return sample
}

// postWithHeaders posts JSON as an Agent with extra request headers.
func postWithHeaders(t *testing.T, a *App, token, path string, value any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, request)
	return response
}

// userHeaders are what a current Agent sends with every quality request.
var userHeaders = map[string]string{
	protocol.QualityVersionHeader:   "2",
	protocol.QualitySemanticsHeader: protocol.QualityStagedSemantics,
	protocol.QualityTargetsHeader:   protocol.QualityTargetsUserV1,
}

func decodeReportResponse(t *testing.T, w *httptest.ResponseRecorder) protocol.ReportResponse {
	t.Helper()
	var response protocol.ReportResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return response
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// Schema 35 retired the TCP Mini evidence; a page of v1.0.6 that is still open gets the "refresh" answer.
func TestV107TCPMiniEndpointAnswersGone(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	w := qualityWebRequest(t, a, http.MethodGet, webAgentPathPrefix+id+"/network-quality/v3/tcp-mini?timezone=UTC", "")
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "client_outdated") {
		t.Fatal(w.Code, w.Body.String())
	}
}
