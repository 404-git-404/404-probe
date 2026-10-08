package server

import (
	"404-probe/internal/protocol"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func qualityWebRequest(t *testing.T, a *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	session := addTestWebSession(t, a, r)
	if method == http.MethodPut {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-CSRF-Token", session.csrfToken)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func qualityServerConfig(t *testing.T, a *App, id string) protocol.QualityConfig {
	t.Helper()
	body := `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"manual","protocol":"tcp","host":"127.0.0.1","port":80},{"slot":"unicom","source":"manual","protocol":"tcp","host":"127.0.0.1","port":80},{"slot":"mobile","source":"manual","protocol":"tcp","host":"127.0.0.1","port":80}]}`
	w := qualityWebRequest(t, a, http.MethodPut, webAgentPathPrefix+id+"/network-quality/config", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var c protocol.QualityConfig
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestN2aQualityHTTPCompatibilityAuthAndPartialACK(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := a.now()
	legacy := reportFor(id, 1)
	w := postAgentUpgrade(t, a, token, "/api/v1/report", legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"network_quality":true`) || strings.Contains(w.Body.String(), `"targets"`) {
		t.Fatal("legacy negotiation", w.Body.String())
	}
	newReport := legacy
	newReport.Sequence = 2
	newReport.NetworkQuality = true
	w = postAgentUpgrade(t, a, token, "/api/v1/report", newReport)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"targets"`) {
		t.Fatal("opt in", w.Body.String())
	}
	cfg := qualityServerConfig(t, a, id)
	target := cfg.Targets[0]
	latency := 5.0
	sample := protocol.QualitySample{Sequence: "9007199254740993", TargetID: target.ID, ConfigRevision: cfg.Revision, SlotRevision: target.SlotRevision, ScheduledAt: now.UnixMilli(), StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli() + 5, DurationMS: 5, Outcome: "success", ResolvedIP: "127.0.0.1", LatencyMS: &latency}
	invalid := sample
	invalid.Sequence = "2"
	invalid.ResolvedIP = "::1"
	batch := protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: legacy.SessionID, Samples: []protocol.QualitySample{sample, invalid}}
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", batch)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ack protocol.QualityBatchResponse
	json.Unmarshal(w.Body.Bytes(), &ack)
	if ack.Results[0][2] != "committed" || ack.Results[1][2] != "invalid" {
		t.Fatal(ack)
	}
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", batch)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "duplicate") {
		t.Fatal("lost ACK retry", w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, webAgentPathPrefix+id+"/network-quality/config", nil)
	unauth := httptest.NewRecorder()
	a.Handler().ServeHTTP(unauth, r)
	if unauth.Code == 200 {
		t.Fatal("unauth GET")
	}
	r = httptest.NewRequest(http.MethodPut, webAgentPathPrefix+id+"/network-quality/config", strings.NewReader(`{}`))
	addTestWebSession(t, a, r)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	unauth = httptest.NewRecorder()
	a.Handler().ServeHTTP(unauth, r)
	if unauth.Code == 200 {
		t.Fatal("CSRF bypass")
	}
	catalog := qualityWebRequest(t, a, "GET", "/api/v1/web/network-quality/catalog", "")
	if catalog.Code != 200 || strings.Contains(catalog.Body.String(), "zstaticcdn.com:80") {
		t.Fatal("picker exposes endpoints")
	}
}

func TestN2aQualityHTTPBodyAndResponseBudgets(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := reportFor(id, 1)
	r.NetworkQuality = true
	if _, ok, _, err := s.ProcessReport(context.Background(), id, r, a.now()); err != nil || !ok {
		t.Fatal(err)
	}
	for _, body := range []string{`{} {}`, `{"version":1,"epoch":1,"session_id":"x","samples":[]}`, strings.Repeat(" ", protocol.QualityBodyLimit+1)} {
		request := httptest.NewRequest("POST", "/api/v1/agent/network-quality", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, request)
		if w.Code == 200 {
			t.Fatal("invalid body accepted")
		}
	}
	batch := protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: r.SessionID}
	for i := 0; i < 65; i++ {
		batch.Samples = append(batch.Samples, protocol.QualitySample{})
	}
	if w := postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", batch); w.Code != 400 {
		t.Fatal("65 accepted", w.Code)
	}
	configPath := webAgentPathPrefix + id + "/network-quality/config"
	for _, body := range []string{`{} {}`, strings.Repeat(" ", 4097)} {
		if w := qualityWebRequest(t, a, "PUT", configPath, body); w.Code == 200 {
			t.Fatal("config body accepted")
		}
	}
	longHost := strings.Repeat("a", 200) + ".invalid"
	choices := []protocol.QualityChoice{}
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "manual", Protocol: "tcp", Host: longHost, Port: 65535})
	}
	cfg, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: "0", Enabled: true, IPv6: true, Choices: choices}, resolveQualityTarget, a.now())
	if err != nil || len(cfg.Targets) != 6 {
		t.Fatal(err)
	}
	r.Sequence = 2
	w := postAgentUpgrade(t, a, token, "/api/v1/report", r)
	if w.Code != 200 || w.Body.Len() > 4096 {
		t.Fatalf("report budget %d %d %s", w.Code, w.Body.Len(), w.Body.String())
	}
	t.Logf("six long-host targets complete report response bytes=%d config_present=%t", w.Body.Len(), strings.Contains(w.Body.String(), "targets"))
	// Oversized optional config cannot change a committed resource ACK.
	for i := range choices {
		choices[i].Host = strings.Repeat("&", 253)
	}
	oversized, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, IPv6: true, Choices: choices}, resolveQualityTarget, a.now())
	if err != nil {
		t.Fatal(err)
	}
	r.Sequence = 3
	w = postAgentUpgrade(t, a, token, "/api/v1/report", r)
	if w.Code != 200 || w.Body.Len() > 4096 || !strings.Contains(w.Body.String(), `"accepted":true`) || strings.Contains(w.Body.String(), `"targets"`) {
		t.Fatal("optional budget failure changed resource ACK", w.Code, w.Body.String())
	}
	for _, region := range qualityDirectory.Regions {
		for slot, families := range region.Endpoints {
			if families["ipv4"].Endpoint == nil {
				target, err := resolveQualityTarget(protocol.QualityChoice{Slot: slot, Source: "catalog", Region: region.ID, Protocol: "tcp"}, "ipv4")
				if err != nil || target.Host != "" {
					t.Fatal("missing ISP guessed")
				}
				missing, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: oversized.Revision, Enabled: true, Choices: []protocol.QualityChoice{{Slot: slot, Source: "catalog", Protocol: "tcp", Region: region.ID}}}, resolveQualityTarget, a.now())
				if err != nil {
					t.Fatal(err)
				}
				for _, tgt := range missing.Targets {
					if tgt.Slot == slot {
						if tgt.Region != region.ID || tgt.Source != "catalog" || tgt.Protocol != "tcp" || tgt.Status != "unavailable" {
							t.Fatal("missing selection lost", tgt)
						}
					}
				}
				return
			}
		}
	}
	t.Fatal("missing catalog fixture absent")
}

func TestN2aQualityUnavailableManualConfigRoundTrip(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	path := webAgentPathPrefix + id + "/network-quality/config"
	for _, kind := range []string{"tcp", "icmp"} {
		t.Run(kind, func(t *testing.T) {
			read := func(method, body string) protocol.QualityConfig {
				t.Helper()
				w := qualityWebRequest(t, a, method, path, body)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				var cfg protocol.QualityConfig
				if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
					t.Fatal(err)
				}
				return cfg
			}
			before := read("GET", "")
			port := 443
			if kind == "icmp" {
				port = 0
			}
			update := protocol.QualityConfigUpdate{ExpectedRevision: before.Revision, Enabled: true, IPv6: false,
				Choices: []protocol.QualityChoice{{Slot: "telecom", Source: "manual", Protocol: kind, Host: "::1", Port: port}}}
			encoded, _ := json.Marshal(update)
			saved := read("PUT", string(encoded))
			check := func(cfg protocol.QualityConfig) protocol.QualityTarget {
				t.Helper()
				if cfg.IPv6 || len(cfg.Targets) != 3 {
					t.Fatal("disabled family leaked", cfg)
				}
				for _, target := range cfg.Targets {
					if target.Slot == "telecom" {
						if target.Family != "ipv4" || target.Status != "unavailable" || target.ID != "" || target.EndpointVersion != "" || target.Source != "manual" || target.Protocol != kind || target.Host != "::1" || target.Port != port {
							t.Fatal("saved input lost or made executable", target)
						}
						return target
					}
					if target.Source != "catalog" || target.Host != "" || target.Port != 0 {
						t.Fatal("catalog missing endpoint guessed", target)
					}
				}
				t.Fatal("manual slot absent")
				return protocol.QualityTarget{}
			}
			check(saved)
			got := read("GET", "")
			manual := check(got)
			update.ExpectedRevision = got.Revision
			update.Choices[0] = protocol.QualityChoice{Slot: manual.Slot, Source: manual.Source, Protocol: manual.Protocol, Host: manual.Host, Port: manual.Port}
			encoded, _ = json.Marshal(update)
			repeated := read("PUT", string(encoded))
			check(repeated)
			if repeated.Revision != saved.Revision {
				t.Fatal("same input changed revision", saved.Revision, repeated.Revision)
			}
			storeConfig, err := s.GetQualityConfig(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			check(storeConfig)
		})
	}
}

func TestN2aQualityRealHTTPFiveSecondSlowBodies(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	report := reportFor(id, 1)
	report.NetworkQuality = true
	if _, ok, _, err := s.ProcessReport(context.Background(), id, report, a.now()); err != nil || !ok {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			completed := make(chan time.Duration, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started := time.Now()
				a.Handler().ServeHTTP(w, r)
				completed <- time.Since(started)
			}))
			server.Config.ReadTimeout = 15 * time.Second
			server.Start()
			defer server.Close()
			connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(8 * time.Second))
			path := "/api/v1/agent/network-quality"
			headers := "Authorization: Bearer " + token + "\r\n"
			if method == "PUT" {
				path = webAgentPathPrefix + id + "/network-quality/config"
				request := httptest.NewRequest("PUT", path, nil)
				session := addTestWebSession(t, a, request)
				headers = "Cookie: " + request.Header.Get("Cookie") + "\r\nOrigin: https://probe.test\r\nSec-Fetch-Site: same-origin\r\nX-CSRF-Token: " + session.csrfToken + "\r\n"
			}
			fmt.Fprintf(connection, "%s %s HTTP/1.1\r\nHost: probe.test\r\nContent-Type: application/json\r\nContent-Length: 1000\r\nConnection: close\r\n%s\r\n{", method, path, headers)
			select {
			case elapsed := <-completed:
				if elapsed < 4500*time.Millisecond || elapsed > 6500*time.Millisecond {
					t.Fatalf("body not budgeted: %v", elapsed)
				}
				t.Logf("real %s unfinished body handler elapsed=%v, server ReadTimeout=15s", method, elapsed)
			case <-time.After(7 * time.Second):
				t.Fatal("handler remained blocked beyond budget")
			}
			response, err := http.ReadResponse(bufio.NewReader(connection), nil)
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					t.Fatal("unfinished body success ACK")
				}
			}
		})
	}
}
