package server

import (
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
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

func TestN2bAgentConfigFallbackBudgetSessionAndBarriers(t *testing.T) {
	for _, barrier := range []string{"disabled", "revoked", "removal"} {
		t.Run(barrier, func(t *testing.T) {
			a, s, id, token := testApp(t)
			defer s.Close()
			defer a.Shutdown()
			report := reportFor(id, 1)
			report.NetworkQuality = true
			report.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
			if w := postReport(t, a, token, report); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			choices := []protocol.QualityChoice{}
			for _, slot := range []string{"telecom", "unicom", "mobile"} {
				choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "manual", Protocol: "tcp", Host: strings.Repeat("&", 253), Port: 65535})
			}
			if _, err := s.UpdateQualityConfig(context.Background(), id, protocol.QualityConfigUpdate{ExpectedRevision: "0", Enabled: true, IPv6: true, Choices: choices}, resolveQualityTarget, a.now()); err != nil {
				t.Fatal(err)
			}
			report.Sequence = 2
			compact := postReport(t, a, token, report)
			if compact.Code != 200 || strings.Contains(compact.Body.String(), `"targets"`) {
				t.Fatal("oversized config not omitted", compact.Body.String())
			}
			path := "/api/v1/agent/network-quality/config"
			body := protocol.QualityConfigRequest{Epoch: "1", SessionID: report.SessionID}
			w := postAgentUpgrade(t, a, token, path, body)
			if w.Code != 200 || w.Body.Len() > 16384 || w.Body.Len() <= 4096 {
				t.Fatal(w.Code, w.Body.Len())
			}
			var cfg protocol.QualityConfig
			if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || len(cfg.Targets) != 6 {
				t.Fatal(err, cfg)
			}
			t.Logf("actual six escaped maximum-host targets fallback bytes=%d (complete response)", w.Body.Len())
			if barrier == "disabled" {
				otherID, _ := auth.NewID()
				otherToken, otherHash, _ := auth.NewToken()
				if err := s.AddAgent(context.Background(), otherID, "other", otherHash, a.now()); err != nil {
					t.Fatal(err)
				}
				other := reportFor(otherID, 1)
				other.SessionID = "other-session"
				other.NetworkQuality = true
				if response := postReport(t, a, otherToken, other); response.Code != 200 {
					t.Fatal(response.Code)
				}
				if response := postAgentUpgrade(t, a, otherToken, path, body); response.Code == 200 {
					t.Fatal("cross-device session leaked config")
				}
				response := postAgentUpgrade(t, a, otherToken, path, protocol.QualityConfigRequest{Epoch: "1", SessionID: other.SessionID})
				if response.Code != 200 || strings.Contains(response.Body.String(), `"host"`) {
					t.Fatal("other device read first-device config", response.Code, response.Body.String())
				}
			}
			for _, bad := range []string{`{"epoch":"1","session_id":"wrong"}`, `{"epoch":"01","session_id":"session"}`, `{} {}`, strings.Repeat(" ", 513)} {
				request := httptest.NewRequest("POST", path, strings.NewReader(bad))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, request)
				if w.Code == 200 {
					t.Fatal("bad fallback accepted", bad)
				}
			}
			if w := postAgentUpgrade(t, a, token, path+"?x=1", body); w.Code == 200 {
				t.Fatal("query accepted")
			}
			if w := postAgentUpgrade(t, a, "bad-token", path, body); w.Code == 200 {
				t.Fatal("wrong bearer accepted")
			}
			switch barrier {
			case "disabled":
				_, err := s.DisableAgent(context.Background(), id, a.now())
				if err != nil {
					t.Fatal(err)
				}
			case "revoked":
				_, err := s.RevokeAgent(context.Background(), id, a.now())
				if err != nil {
					t.Fatal(err)
				}
			case "removal":
				_, _, err := s.CreateAgentRemovalOperation(context.Background(), id, strings.Repeat("c", 32), a.now())
				if err != nil {
					t.Fatal(err)
				}
			}
			if w := postAgentUpgrade(t, a, token, path, body); w.Code == 200 {
				t.Fatal("barrier bypassed")
			}
			if _, err := s.GetQualitySessionConfig(context.Background(), id, 1, report.SessionID); err == nil {
				t.Fatal("same tx session read bypassed barrier")
			}
			if barrier != "revoked" {
				if _, err := s.GetQualityConfig(context.Background(), id); err != nil {
					t.Fatal("ordinary Web read changed", err)
				}
			}
		})
	}
}

func TestN2bAgentConfigRealHTTPFiveSecondBodyBudget(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	report := reportFor(id, 1)
	report.NetworkQuality = true
	if w := postReport(t, a, token, report); w.Code != 200 {
		t.Fatal(w.Code)
	}
	finished := make(chan time.Duration, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		a.Handler().ServeHTTP(w, r)
		finished <- time.Since(started)
	}))
	server.Config.ReadTimeout = 15 * time.Second
	server.Start()
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	fmt.Fprintf(conn, "POST /api/v1/agent/network-quality/config HTTP/1.1\r\nHost: probe.test\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 500\r\nConnection: close\r\n\r\n{", token)
	select {
	case elapsed := <-finished:
		if elapsed < 4500*time.Millisecond || elapsed > 6500*time.Millisecond {
			t.Fatal(elapsed)
		}
		t.Logf("actual fallback unfinished body elapsed=%v; transport ReadTimeout15s", elapsed)
	case <-time.After(7 * time.Second):
		t.Fatal("fallback budget did not stop blocked body")
	}
}
