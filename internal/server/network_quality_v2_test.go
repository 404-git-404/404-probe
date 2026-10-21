package server

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func a1ServerCapabilities() *protocol.QualityCapabilities {
	c := &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityV2Semantics}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Lanes = append(c.Lanes, protocol.QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	return c
}

// openLanes counts the lanes the Server currently authorizes to run.
func openLanes(t *testing.T, a *App, id string) int {
	t.Helper()
	cfg, err := a.store.GetQualityConfigV4(context.Background(), id, a.now())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range cfg.Lanes {
		if l.Status == protocol.QualityLaneActive {
			n++
		}
	}
	return n
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	wire, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// An Agent only receives user-entered targets after the two-step negotiation of its current session,
// and the resource report is acknowledged at every step (design 4.2, 10.1).
func TestR07QualityNegotiationOverHTTP(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	reportPath := "/api/v1/report"
	configPath := "/api/v1/agent/network-quality/config"
	oldHeaders := map[string]string{protocol.QualityVersionHeader: "2", protocol.QualitySemanticsHeader: protocol.QualityV2Semantics}
	var seq, epoch uint64 = 0, 1
	session := "session-1"
	send := func(headers map[string]string, caps *protocol.QualityCapabilities) (protocol.ReportResponse, string) {
		t.Helper()
		seq++
		r := reportFor(id, seq)
		r.Epoch, r.SessionID = epoch, session
		r.NetworkQuality = true
		r.NetworkQualityV2 = caps
		w := postWithHeaders(t, a, token, reportPath, r, headers)
		response := decodeReportResponse(t, w)
		if w.Code != 200 || !response.Accepted {
			t.Fatalf("resource report not acknowledged: %d %s", w.Code, w.Body.String())
		}
		return response, w.Body.String()
	}
	restart := func() { epoch++; session = "session-" + strconv.FormatUint(epoch, 10); seq = 0 }
	pull := func(headers map[string]string) (int, protocol.QualityConfig, string) {
		t.Helper()
		req := protocol.QualityConfigRequest{Version: 2, Epoch: strconv.FormatUint(epoch, 10), SessionID: session}
		w := postWithHeaders(t, a, token, configPath, req, headers)
		var cfg protocol.QualityConfig
		_ = json.Unmarshal(w.Body.Bytes(), &cfg)
		return w.Code, cfg, w.Body.String()
	}
	assertOldFormat := func(t *testing.T, cfg protocol.QualityConfig, body string) {
		t.Helper()
		if cfg.Version != 2 || !cfg.Supported || cfg.Enabled || cfg.IPv6 || len(cfg.Targets) != 0 || cfg.State != "agent_upgrade_required" || cfg.RegionID != "" ||
			cfg.IntervalMS != 30000 || strings.Contains(body, `"capabilities"`) || containsAny(body, "example.com", "example.org", "example.net", "443") {
			t.Fatalf("not the old format: %s", body)
		}
	}

	// An Agent of the previous generation: v2 capability, no targets header.
	response, _ := send(oldHeaders, a1ServerCapabilities())
	if response.Capabilities.NetworkQualityUserTargets {
		t.Fatal("user targets advertised to an Agent that did not ask")
	}
	cfg := configureUserTargets(t, s, id, a.now(), defaultUserHosts, 443)
	if openLanes(t, a, id) != 0 || cfg.AgentSupport != protocol.QualitySupportUpgradeRequired {
		t.Fatalf("an old Agent was given work: %+v", cfg)
	}
	response, body := send(oldHeaders, a1ServerCapabilities())
	if response.NetworkQuality == nil {
		t.Fatal("no inline configuration for the old Agent", body)
	}
	assertOldFormat(t, *response.NetworkQuality, string(mustJSON(t, *response.NetworkQuality)))
	if code, got, body := pull(oldHeaders); code != 200 {
		t.Fatal(code, body)
	} else {
		assertOldFormat(t, got, body)
	}

	// The Agent is upgraded and restarted: a new session starts over at step one.
	restart()
	response, _ = send(userHeaders, nil)
	if !response.Capabilities.NetworkQualityUserTargets {
		t.Fatal("step one did not get the user targets capability")
	}
	if code, _, body := pull(userHeaders); code != 409 {
		t.Fatalf("pull before step two: %d %s", code, body)
	}
	if openLanes(t, a, id) != 0 {
		t.Fatal("step one granted work")
	}

	// Step two: the full capability with targets=user_v1 completes the negotiation.
	response, body = send(userHeaders, userCapabilities())
	if openLanes(t, a, id) != 6 {
		t.Fatalf("lanes after step two: %d", openLanes(t, a, id))
	}
	if response.NetworkQuality == nil || len(response.NetworkQuality.Targets) == 0 {
		t.Fatalf("no inline user configuration: %s", body)
	}
	inline := *response.NetworkQuality
	active := 0
	for _, target := range inline.Targets {
		if target.Source != "user" || target.Region != "" {
			t.Fatalf("target %+v", target)
		}
		if target.Status == "active" {
			active++
		}
	}
	if !inline.Enabled || active != 6 || inline.Capabilities == nil {
		t.Fatalf("inline configuration: %s", body)
	}
	// The pull path makes the same decision and gives the same result.
	code, pulled, body := pull(userHeaders)
	if code != 200 {
		t.Fatal(code, body)
	}
	if string(mustJSON(t, inline)) != string(mustJSON(t, pulled)) {
		t.Fatalf("inline and pull differ:\n%s\n%s", mustJSON(t, inline), mustJSON(t, pulled))
	}
	// A pull that lost the header gets the stop configuration (a middlebox that strips it).
	code, stripped, body := pull(oldHeaders)
	if code != 200 {
		t.Fatal(code, body)
	}
	assertOldFormat(t, stripped, body)

	// A report without the header revokes: lanes close, resource ACKs continue.
	response, body = send(oldHeaders, userCapabilities())
	if openLanes(t, a, id) != 0 {
		t.Fatal("a headerless report left the lanes running")
	}
	if response.NetworkQuality == nil {
		t.Fatal(body)
	}
	assertOldFormat(t, *response.NetworkQuality, string(mustJSON(t, *response.NetworkQuality)))
	if code, got, body := pull(userHeaders); code != 200 {
		t.Fatal(code, body)
	} else {
		assertOldFormat(t, got, body)
	}
	// The header comes back with the capability: the saved configuration runs again.
	send(userHeaders, userCapabilities())
	if openLanes(t, a, id) != 6 {
		t.Fatalf("lanes after regrant: %d", openLanes(t, a, id))
	}
	// A request for a session that is no longer current is refused.
	stale := protocol.QualityConfigRequest{Version: 2, Epoch: "1", SessionID: "session-1"}
	if w := postWithHeaders(t, a, token, configPath, stale, userHeaders); w.Code != http.StatusConflict {
		t.Fatal("stale session", w.Code, w.Body.String())
	}
	// Alpine is excluded whatever the Agent says.
	seq++
	r := userReport(id, seq)
	r.Epoch, r.SessionID = epoch, session
	r.OS = "Alpine 3.22"
	w := postWithHeaders(t, a, token, reportPath, r, userHeaders)
	if w.Code != 200 {
		t.Fatal("Alpine report", w.Code, w.Body.String())
	}
	got := decodeReportResponse(t, w)
	if got.NetworkQuality == nil || got.NetworkQuality.Enabled || len(got.NetworkQuality.Targets) != 0 {
		t.Fatal("Server gave Alpine work", w.Body.String())
	}
}

// Old routes of the catalog era answer 410 so that a page left open during a Server upgrade says so
// instead of failing silently (design 4.1).
func TestR07OldQualityRoutesAnswerClientOutdated(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	base := webAgentPathPrefix + id + "/network-quality/"
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/web/network-quality/catalog"},
		{"GET", base + "config"}, {"PUT", base + "config"},
		{"GET", base + "v2/config"}, {"PUT", base + "v2/config"},
		{"GET", base + "v3/card-config"},
	} {
		w := qualityWebRequest(t, a, tc.method, tc.path, `{"expected_revision":"0"}`)
		var body struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusGone || body.Error.Code != "client_outdated" || body.Error.Message != "页面版本已过期，请刷新页面后重试" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
