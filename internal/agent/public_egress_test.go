package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestM10NegotiationAndStrictServerDowngrade(t *testing.T) {
	var bodies []map[string]any
	downgraded := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		if downgraded {
			if _, ok := body["public_egress"]; ok {
				http.Error(w, "unknown field", 400)
				return
			}
			w.Write([]byte(`{"accepted":true,"capabilities":{}}`))
			return
		}
		w.Write([]byte(`{"accepted":true,"capabilities":{"public_egress_report":true}}`))
	}))
	defer s.Close()
	r, err := New(Config{ServerURL: s.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.collector = reportCollectorFunc(staticReportCollector)
	var seq uint64
	for i := 0; i < 2; i++ {
		if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if _, ok := bodies[0]["public_egress"]; ok {
		t.Fatal("unnegotiated field")
	}
	if _, ok := bodies[1]["public_egress"]; !ok {
		t.Fatal("missing negotiated field")
	}
	downgraded = true
	for i := 0; i < 2; i++ {
		if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if len(bodies) != 5 {
		t.Fatalf("retry count %d", len(bodies))
	}
	for _, body := range bodies[3:] {
		if _, ok := body["public_egress"]; ok {
			t.Fatal("sticky downgraded capability")
		}
	}
	if bodies[2]["sequence"] != bodies[3]["sequence"] {
		t.Fatal("retry changed report identity")
	}
}
