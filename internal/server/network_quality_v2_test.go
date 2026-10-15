package server

import (
	"404-probe/internal/protocol"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
func TestA1QualityV2HTTPNegotiationUnifiedRegionAndAuth(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := reportFor(id, 1)
	r.NetworkQuality = true
	w := postAgentUpgrade(t, a, token, "/api/v1/report", r)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"network_quality_v2"`) {
		t.Fatal("new field sent to legacy Agent", w.Code, w.Body.String())
	}
	r.Sequence = 2
	bodyDiscovery, _ := json.Marshal(r)
	requestDiscovery := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(bodyDiscovery))
	requestDiscovery.Header.Set("Authorization", "Bearer "+token)
	requestDiscovery.Header.Set("Content-Type", "application/json")
	requestDiscovery.Header.Set(protocol.QualityVersionHeader, "2")
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, requestDiscovery)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"network_quality_v2":true`) {
		t.Fatal("v2 not advertised", w.Code, w.Body.String())
	}
	cfgReq := protocol.QualityConfigRequest{Version: 2, Epoch: "1", SessionID: r.SessionID}
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality/config", cfgReq)
	if w.Code != 409 {
		t.Fatal("unnegotiated config accepted", w.Code, w.Body.String())
	}
	r.Sequence = 3
	r.NetworkQualityV2 = a1ServerCapabilities()
	w = postAgentUpgrade(t, a, token, "/api/v1/report", r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	region := qualityDirectory.Regions[0].ID
	u := protocol.QualityV2ConfigUpdate{ExpectedRevision: "0", Enabled: true, IPv6: true, RegionID: region}
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		for _, family := range []string{"ipv4", "ipv6"} {
			for _, p := range []string{"tcp", "icmp"} {
				host := "127.0.0.1"
				port := 80
				if family == "ipv6" {
					host = "::1"
				}
				if p == "icmp" {
					port = 0
				}
				u.Lanes = append(u.Lanes, protocol.QualityLaneChoice{QualityChoice: protocol.QualityChoice{Slot: slot, Source: "manual", Protocol: p, Host: host, Port: port}, Family: family, Enabled: true})
			}
		}
	}
	path := webAgentPathPrefix + id + "/network-quality/v2/config"
	body, _ := json.Marshal(u)
	w = qualityWebRequest(t, a, http.MethodPut, path, string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var cfg protocol.QualityConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 12 || cfg.RegionID != region || cfg.Semantics != protocol.QualityV2Semantics {
		t.Fatal(cfg)
	}
	for _, target := range cfg.Targets {
		if target.Status != "active" || target.Region != region {
			t.Fatal(target)
		}
	}
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality/config", cfgReq)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatal(w.Code, w.Body.String())
	}
	cfgReq.Version = 0
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality/config", cfgReq)
	var old protocol.QualityConfig
	_ = json.Unmarshal(w.Body.Bytes(), &old)
	if w.Code != 200 || len(old.Targets) != 6 || strings.Contains(w.Body.String(), `"semantics"`) {
		t.Fatal("v1 shape/limit changed", w.Body.String())
	}
	cfgReq.Version = 2
	cfgReq.SessionID = "stale"
	w = postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality/config", cfgReq)
	if w.Code != 409 {
		t.Fatal("stale session", w.Code)
	}
	u.ExpectedRevision = cfg.Revision
	u.Lanes[0].Region = "other-region"
	body, _ = json.Marshal(u)
	w = qualityWebRequest(t, a, http.MethodPut, path, string(body))
	if w.Code != 400 {
		t.Fatal("mixed region accepted", w.Code)
	}
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(string(body)))
	session := addTestWebSession(t, a, req)
	_ = session
	req.Header.Set("Content-Type", "application/json")
	unauth := httptest.NewRecorder()
	a.Handler().ServeHTTP(unauth, req)
	if unauth.Code == 200 {
		t.Fatal("missing CSRF accepted")
	}
	w = qualityWebRequest(t, a, http.MethodPut, path, `{"unexpected":true}`)
	if w.Code != 400 {
		t.Fatal("unknown fields accepted", w.Code)
	}
	w = qualityWebRequest(t, a, http.MethodPut, path, strings.Repeat(" ", 16385))
	if w.Code != 413 {
		t.Fatal("body budget", w.Code)
	}
	r.Sequence = 4
	r.OS = "Alpine 3.22"
	r.NetworkQualityV2 = nil
	w = postAgentUpgrade(t, a, token, "/api/v1/report", r)
	if w.Code != 200 {
		t.Fatal("Alpine report", w.Code, w.Body.String())
	}
	var response protocol.ReportResponse
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.NetworkQuality == nil || response.NetworkQuality.Enabled || len(response.NetworkQuality.Targets) != 0 {
		t.Fatal("Server gave Alpine work", w.Body.String())
	}
}
