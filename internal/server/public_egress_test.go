package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestM10OptionalMalformedShapesDoNotRejectMetrics(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	for i, raw := range []string{`"bad"`, `[]`, `true`, `42`, `null`, `{}`, `{"version":1,"ipv4":false,"ipv6":{"family":"ipv6","status":"unknown","reason":"not_checked","checked_at":0,"valid_until":0}}`, `{"version":1,"ipv4":{"family":"ipv4","status":"available","ip":"10.0.0.1","reason":"verified","checked_at":1,"valid_until":2}}`} {
		r := reportFor(id, uint64(i+1))
		data, _ := json.Marshal(r)
		body := strings.TrimSuffix(string(data), "}") + `,"public_egress":` + raw + `}`
		response := postReport(t, a, token, []byte(body))
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"accepted":true`) {
			t.Fatalf("%s %d %s", raw, response.Code, response.Body.String())
		}
	}
	data, _ := json.Marshal(reportFor(id, 100))
	body := strings.TrimSuffix(string(data), "}") + `,"unrecognized_top_level":true}`
	if got := postReport(t, a, token, []byte(body)); got.Code != 400 {
		t.Fatal("lost strict root", got.Code)
	}
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("https://probe.test/api/v1/web/agents/%s", id), nil)
	addTestWebSession(t, a, request)
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"public_egress"`) || !strings.Contains(response.Body.String(), `"status":"unknown"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestM10AcceptedReportSQLiteAPIAndSSEAgree(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	now := time.Now()
	a.now = func() time.Time { return now }
	ch, ok := a.hub.subscribe()
	if !ok {
		t.Fatal("subscription")
	}
	defer a.hub.unsubscribe(ch)
	if _, err := s.SetPublicEgressEnabled(context.Background(), id, true, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	r := reportFor(id, 1)
	r.PublicEgress = &protocol.PublicEgress{Version: 1, IPv4: protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", CheckedAt: now.UnixMilli(), ValidUntil: now.Add(protocol.EgressSuccessTTL).UnixMilli(), Reason: "verified"}, IPv6: protocol.EgressObservation{Family: "ipv6", Status: "unavailable", CheckedAt: now.UnixMilli(), ValidUntil: now.Add(protocol.EgressFailureTTL).UnixMilli(), Reason: "no_route"}}
	if got := postReport(t, a, token, r); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var event webAgentEventView
	select {
	case data := <-ch:
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing SSE")
	}
	response := webAgentResponse(t, a, http.MethodGet, "/api/v1/web/agents/"+id)
	var detail webAgentDetailView
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || detail.State == nil || event.State == nil {
		t.Fatal(response.Code, response.Body.String())
	}
	apiJSON, _ := json.Marshal(detail.State.PublicEgress)
	eventJSON, _ := json.Marshal(event.State.PublicEgress)
	if string(apiJSON) != string(eventJSON) || detail.State.PublicEgress.IPv4.Stale || detail.State.PublicEgress.IPv4.IP != "8.8.8.8" || detail.State.PublicEgress.IPv6.Status != "unavailable" {
		t.Fatal(string(apiJSON), string(eventJSON))
	}
	t.Log("API/SSE public_egress", string(apiJSON))
	states, err := s.ListStates(context.Background())
	if err != nil || len(states) != 1 || states[0].PublicEgress.IPv4.IP != "8.8.8.8" {
		t.Fatal(states, err)
	}
	now = now.Add(time.Hour)
	response = webAgentResponse(t, a, http.MethodGet, "/api/v1/web/agents/"+id)
	if err = json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.State.PublicEgress.IPv4.Stale || detail.State.PublicEgress.IPv4.Status != "available" || detail.State.PublicEgress.IPv4.LastSuccess == nil {
		t.Fatal("expiry changed base meaning", response.Body.String())
	}
}
