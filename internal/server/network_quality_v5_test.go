package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func v5Path(id, suffix string) string {
	return webAgentPathPrefix + id + "/network-quality/v5/" + suffix
}

func TestV107CardAndMarksEndpoints(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	qualityServerConfig(t, a, s, id, token)

	w := qualityWebRequest(t, a, http.MethodGet, v5Path(id, "card?family=ipv4"), "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var card storage.QualityCardSummary
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if card.Family != "ipv4" || len(card.Lanes) != 3 || card.BlockMS != 180000 || card.ThresholdMS != 500 || card.EvalEnd%60000 != 0 {
		t.Fatalf("card %+v", card)
	}
	for _, lane := range card.Lanes {
		if lane.TCP.Status != "active" || len(lane.TCP.Blocks) != 20 || lane.TCP.Red == nil || lane.TCP.Red.Active {
			t.Fatalf("lane %+v", lane)
		}
	}
	// Only the contract's names are on the wire.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"now", "eval_end", "family", "settings_revision", "config_revision", "threshold_ms", "interval_ms", "block_ms", "window_start", "lanes", "alert_active"} {
		if _, ok := raw[name]; !ok {
			t.Errorf("the card answer lacks %q", name)
		}
	}
	if w = qualityWebRequest(t, a, http.MethodGet, v5Path(id, "card?family=ipv6"), ""); w.Code != 200 {
		t.Fatal("ipv6 card", w.Code, w.Body.String())
	}

	marks := qualityWebRequest(t, a, http.MethodGet, v5Path(id, "marks?family=ipv4&from="+strconv.FormatInt(card.WindowStart, 10)+"&to="+strconv.FormatInt(card.EvalEnd, 10)), "")
	if marks.Code != 200 {
		t.Fatal(marks.Code, marks.Body.String())
	}
	var m storage.QualityMarks
	if err := json.Unmarshal(marks.Body.Bytes(), &m); err != nil || m.Family != "ipv4" || m.EvalEnd != card.EvalEnd || len(m.Lanes) != 3 {
		t.Fatalf("marks %+v %v", m, err)
	}
	for _, l := range m.Lanes {
		if l.Runs == nil {
			t.Fatalf("an empty run list must be [], not null: %+v", l)
		}
	}
}

func TestV107V5EndpointsRefuseBadRequests(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	qualityServerConfig(t, a, s, id, token)
	for name, path := range map[string]string{
		"no family":        v5Path(id, "card"),
		"unknown family":   v5Path(id, "card?family=ipv5"),
		"empty family":     v5Path(id, "card?family="),
		"extra parameter":  v5Path(id, "card?family=ipv4&x=1"),
		"repeated family":  v5Path(id, "card?family=ipv4&family=ipv6"),
		"marks no range":   v5Path(id, "marks?family=ipv4"),
		"marks bad number": v5Path(id, "marks?family=ipv4&from=a&to=2"),
		"marks reversed":   v5Path(id, "marks?family=ipv4&from=2000000&to=1000000"),
		"marks over 24 h":  v5Path(id, "marks?family=ipv4&from=0&to=86400001"),
		"marks bad family": v5Path(id, "marks?family=tcp&from=0&to=60000"),
		"marks extra":      v5Path(id, "marks?family=ipv4&from=0&to=60000&z=1"),
		"marks negative":   v5Path(id, "marks?family=ipv4&from=-5&to=60000"),
	} {
		if w := qualityWebRequest(t, a, http.MethodGet, path, ""); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// Unknown device: 404; no session: not served.
	if w := qualityWebRequest(t, a, http.MethodGet, v5Path("0123456789abcdef0123456789abcdef", "card?family=ipv4"), ""); w.Code != 404 {
		t.Errorf("unknown agent: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{v5Path(id, "card?family=ipv4"), v5Path(id, "marks?family=ipv4&from=0&to=60000")} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code == 200 {
			t.Errorf("%s answered without a session", path)
		}
	}
}

// The card moved to v5/card: the old per-target card read answers 410 so that a page of v1.0.6 asks to be refreshed.
func TestV107CardHistoryAnswersGoneAndOneHourViewReplacesIt(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	qualityServerConfig(t, a, s, id, token)
	target := strings.Repeat("a", 32)
	w := qualityWebRequest(t, a, http.MethodGet, webAgentPathPrefix+id+"/network-quality/v3/card-history?target_id="+target, "")
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "client_outdated") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	// The device details page reads its one-hour view as v3/history?view=1hour: not 410, and not the one-day view.
	w = qualityWebRequest(t, a, http.MethodGet, webAgentPathPrefix+id+"/network-quality/v3/history?target_id="+target+"&view=1hour", "")
	if w.Code == http.StatusGone || w.Code == http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The device details page reads its hour as v3/history?view=1hour: a configured target answers 200 and the answer is exactly QualityCardHistory.
func TestV107OneHourViewOfAConfiguredTargetEqualsCardHistory(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	cfg := qualityServerConfig(t, a, s, id, token)
	// One clock for the request and for the expectation.
	fixed := a.now()
	a.now = func() time.Time { return fixed }
	target := ""
	for _, l := range cfg.Lanes {
		if l.TargetID != nil && l.Family == "ipv4" && l.Protocol == "tcp" {
			target = *l.TargetID
			break
		}
	}
	if target == "" {
		t.Fatal("the configured agent has no ipv4 tcp target")
	}
	w := qualityWebRequest(t, a, http.MethodGet, webAgentPathPrefix+id+"/network-quality/v3/history?target_id="+target+"&view=1hour", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	want, err := s.QualityCardHistory(context.Background(), id, target, a.now())
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	var got, expected any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || json.Unmarshal(wantJSON, &expected) != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(got)
	e, _ := json.Marshal(expected)
	if string(g) != string(e) {
		t.Fatalf("the one-hour view differs from QualityCardHistory:\n%s\n%s", g, e)
	}
	var shape struct {
		View         string `json:"view"`
		ResolutionMS int64  `json:"resolution_ms"`
		Target       struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &shape)
	if shape.View != "1hour-card" || shape.ResolutionMS != 300000 || shape.Target.ID != target {
		t.Fatalf("%+v", shape)
	}
}
