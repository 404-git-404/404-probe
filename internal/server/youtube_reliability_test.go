package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestM11YouTubeAPIResultSSEDiagnosticsAndMainTelemetry(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	now := time.Now()
	a.now = func() time.Time { return now }
	ctx := context.Background()
	if got := postReport(t, a, token, reportFor(id, 1)); got.Code != 200 {
		t.Fatal(got.Code)
	}
	if ok, err := s.NegotiateGoogleStatusCapability(ctx, id, true, 1, "session", now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	ch, ok := a.hub.subscribe()
	if !ok {
		t.Fatal("subscribe")
	}
	defer a.hub.unsubscribe(ch)
	get := func() webGoogleStatusView {
		t.Helper()
		response := webAgentResponse(t, a, http.MethodGet, "/api/v1/web/agents/"+id)
		var detail webAgentDetailView
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != 200 {
			t.Fatal(response.Code, response.Body.String(), err)
		}
		return detail.GoogleStatus
	}
	fixtures := map[string]webGoogleStatusView{"no_data": get()}
	for _, item := range []struct{ name, region, reason string }{{"US", "US", ""}, {"CN", "CN", ""}, {"consent", "", "consent_page"}, {"conflict", "", "region_conflict"}, {"timeout", "", "timeout"}} {
		now = now.Add(time.Second)
		if got := googleStatusWebResponse(t, a, id); got.Code != 202 {
			t.Fatal(got.Code, got.Body.String())
		}
		fixtures["pending"] = get()
		for len(ch) > 0 {
			<-ch
		}
		job, err := s.ClaimJob(ctx, id, protocol.ClaimRequest{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeGoogleStatus}}, now, time.Minute)
		if err != nil || job == nil {
			t.Fatal(job, err)
		}
		y := protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: protocol.CompatibleYouTubeCategory(item.reason)}}
		if item.region != "" {
			cn := item.region == "CN"
			y = protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: item.region, SentToChina: &cn}
			if cn {
				y.Status = protocol.YouTubeCN
			}
		}
		result := protocol.JobResult{ProtocolVersion: 1, LeaseToken: job.LeaseToken, Attempt: job.Attempt, AgentEpoch: 1, SessionID: "session", StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli(), DurationMS: 0, Success: item.region != "", Result: protocol.ProbeResult{GoogleStatus: &protocol.GoogleStatusResult{YouTube: y, Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}, SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}}}
		if !result.Success {
			result.ErrorCategory = "youtube_" + item.reason
		}
		response := jobHTTPResponse(t, a, http.MethodPost, "/api/v1/agent/jobs/"+job.JobID+"/result", token, "application/json", result)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		view := get()
		if item.reason != "" && view.Result.YouTube.Error.Category != item.reason {
			t.Fatal(view)
		}
		fixtures[item.name] = view
		select {
		case raw := <-ch:
			var event webAgentDetailView
			if err = json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			v, _ := json.Marshal(view)
			e, _ := json.Marshal(event.GoogleStatus)
			if string(v) != string(e) {
				t.Fatal("API/SSE differ", string(v), string(e))
			}
		case <-time.After(time.Second):
			t.Fatal("missing SSE")
		}
	}
	// A broken YouTube job cannot break the independent metrics report endpoint.
	if got := postReport(t, a, token, reportFor(id, 2)); got.Code != 200 || !strings.Contains(got.Body.String(), `"accepted":true`) {
		t.Fatal(got.Code, got.Body.String())
	}
	now = now.Add(2 * time.Hour)
	fixtures["stale"] = get()
	if !fixtures["stale"].Stale {
		t.Fatal("expired observation is current")
	}
	data, _ := json.Marshal(fixtures)
	t.Log("M11_WEB_FIXTURE_JSON=" + string(data))
}
