package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
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

// qualityServerConfig negotiates a current Agent session over HTTP and saves the default test targets.
func qualityServerConfig(t *testing.T, a *App, s *storage.Store, id, token string) protocol.QualityV4Config {
	t.Helper()
	r := userReport(id, 1)
	if w := postWithHeaders(t, a, token, "/api/v1/report", r, userHeaders); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return configureUserTargets(t, s, id, a.now(), defaultUserHosts, 443)
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"targets"`) || strings.Contains(w.Body.String(), "network_quality_user_targets") {
		t.Fatal("opt in", w.Body.String())
	}
	user := userReport(id, 3)
	w = postWithHeaders(t, a, token, "/api/v1/report", user, userHeaders)
	if w.Code != 200 || !decodeReportResponse(t, w).Capabilities.NetworkQualityUserTargets {
		t.Fatal("user targets not advertised", w.Code, w.Body.String())
	}
	cfg := configureUserTargets(t, s, id, now, defaultUserHosts, 443)
	sample := userTCPSample(t, cfg, "telecom", 9007199254740993, now, 5, "success")
	invalid := userTCPSample(t, cfg, "telecom", 2, now, 5, "success")
	invalid.ResolvedIP = "not-an-address"
	batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: legacy.SessionID, Samples: []protocol.QualitySample{sample, invalid}}
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
	r := httptest.NewRequest(http.MethodGet, webAgentPathPrefix+id+"/network-quality/v3/tcp-mini", nil)
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
	if unauth.Code == 200 || unauth.Code == 410 {
		t.Fatal("CSRF bypass", unauth.Code)
	}
}

func TestN2aQualityHTTPBodyAndResponseBudgets(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := userReport(id, 1)
	if w := postWithHeaders(t, a, token, "/api/v1/report", r, userHeaders); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
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
	batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: r.SessionID}
	for i := 0; i < 65; i++ {
		batch.Samples = append(batch.Samples, protocol.QualitySample{})
	}
	if w := postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", batch); w.Code != 400 {
		t.Fatal("65 accepted", w.Code)
	}
	// Six long names fit the complete report response only if the configuration is left out of it.
	long := strings.Repeat("a", 61) + "." + strings.Repeat("b", 61) + "." + strings.Repeat("c", 61) + "." + strings.Repeat("d", 61) + ".com"
	cfg := configureUserTargets(t, s, id, a.now(), [3]string{long, long, long}, 65535)
	if len(cfg.Lanes) != 12 {
		t.Fatal(len(cfg.Lanes))
	}
	r.Sequence = 2
	w := postWithHeaders(t, a, token, "/api/v1/report", r, userHeaders)
	if w.Code != 200 || w.Body.Len() > 4096 || !strings.Contains(w.Body.String(), `"accepted":true`) || strings.Contains(w.Body.String(), `"host"`) {
		t.Fatalf("optional budget failure changed resource ACK %d %d %s", w.Code, w.Body.Len(), w.Body.String())
	}
	t.Logf("six long-host targets complete report response bytes=%d", w.Body.Len())
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
	for _, method := range []string{"POST"} {
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
