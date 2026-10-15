package server

import (
	"404-probe/internal/storage"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestM09CardReadHTTPBoundsAndAuth(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := reportFor(id, 1)
	r.NetworkQuality = true
	if w := postAgentUpgrade(t, a, token, "/api/v1/report", r); w.Code != 200 {
		t.Fatal(w.Code)
	}
	cfg := qualityServerConfig(t, a, id)
	target := cfg.Targets[0]
	p := webAgentPathPrefix + id + "/network-quality/v3/"
	for _, suffix := range []string{"card-config", "card-history?target_id=" + target.ID} {
		w := qualityWebRequest(t, a, http.MethodGet, p+suffix, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(suffix, w.Code, w.Body.String())
		}
		if suffix != "card-config" {
			var h storage.QualityHistoryRollup
			if e := json.Unmarshal(w.Body.Bytes(), &h); e != nil || h.ResolutionMS != 300000 || h.View != "1hour-card" {
				t.Fatal(h, e)
			}
		}
		unauth := httptest.NewRecorder()
		a.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, p+suffix, nil))
		if unauth.Code == 200 {
			t.Fatal("unauthorized")
		}
	}
	for _, suffix := range []string{"card-config?x=1", "card-history", "card-history?target_id=" + target.ID + "&target_id=" + target.ID, "card-history?target_id=" + target.ID + "&view=1mo"} {
		if w := qualityWebRequest(t, a, http.MethodGet, p+suffix, ""); w.Code != 400 {
			t.Fatal(suffix, w.Code, w.Body.String())
		}
	}
}
