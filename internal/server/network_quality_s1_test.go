package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestS1RollupHTTPAuthViewsAndQueryContract(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	cfg := qualityServerConfig(t, a, s, id, token)
	targetID := *userLane(t, cfg, "telecom", "ipv4", "tcp").TargetID
	at := a.now().Add(-time.Second)
	sample := userTCPSample(t, cfg, "telecom", 1, at, 10.13, "success")
	if w := postAgentUpgrade(t, a, token, "/api/v1/agent/network-quality", protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "session", Samples: []protocol.QualitySample{sample}}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := webAgentPathPrefix + id + "/network-quality/v3/history?target_id=" + targetID
	for _, view := range []string{"1day", "1week", "1mo"} {
		w := qualityWebRequest(t, a, http.MethodGet, path+"&view="+view, "")
		if w.Code != 200 {
			t.Fatal(view, w.Code, w.Body.String())
		}
		var h storage.QualityHistoryRollup
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		if h.View != view || h.GridMS != .25 || h.PercentileMaxErrorMS != .125 || len(h.Groups) != 1 || h.Groups[0].Window.Count != 1 {
			t.Fatal(view, w.Body.String())
		}
	}
	for _, suffix := range []string{"", "&view=bad", "&view=1day&view=1week", "&view=1day&extra=x"} {
		w := qualityWebRequest(t, a, http.MethodGet, path+suffix, "")
		if w.Code != 400 {
			t.Fatal("query accepted", suffix, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"&view=1day", nil))
	if w.Code == 200 {
		t.Fatal("unauthenticated history")
	}
}
