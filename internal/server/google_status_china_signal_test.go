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

func TestReportAdvertisesGoogleStatusChinaSignal(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	response := postReport(t, app, token, reportFor(id, 1))
	var decoded protocol.ReportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || response.Code != http.StatusOK || !decoded.Capabilities.GoogleStatusChinaSignal || !decoded.Capabilities.GoogleStatus {
		t.Fatalf("code=%d capabilities=%+v err=%v", response.Code, decoded.Capabilities, err)
	}
}

func TestGoogleStatusChinaShapesReachTheWebView(t *testing.T) {
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
	yes, no := true, false
	for _, item := range []struct {
		name     string
		youtube  protocol.YouTubeResult
		errorCat string
		wantJSON []string
		notJSON  []string
	}{
		{"flagged host, page region kept", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &yes}, "",
			[]string{`"status":"cn"`, `"region":"US"`, `"sent_to_china":true`, `"premium_unavailable_notice":true`}, nil},
		{"flagged host, no page message", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &no}, "",
			[]string{`"status":"cn"`, `"premium_unavailable_notice":false`}, nil},
		{"older Agent shape", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "CN", SentToChina: &yes}, "",
			[]string{`"status":"cn"`, `"region":"CN"`}, []string{"premium_unavailable_notice"}},
		{"china signal unverified", protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: protocol.CompatibleYouTubeCategory("china_signal_unverified")}}, "youtube_china_signal_unverified",
			[]string{`"category":"china_signal_unverified"`}, []string{"premium_unavailable_notice"}},
	} {
		t.Run(item.name, func(t *testing.T) {
			now = now.Add(time.Second)
			if got := googleStatusWebResponse(t, a, id); got.Code != 202 {
				t.Fatal(got.Code, got.Body.String())
			}
			job, err := s.ClaimJob(ctx, id, protocol.ClaimRequest{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeGoogleStatus}}, now, time.Minute)
			if err != nil || job == nil {
				t.Fatal(job, err)
			}
			status := protocol.GoogleStatusResult{YouTube: item.youtube, Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown},
				SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}
			result := protocol.JobResult{ProtocolVersion: 1, LeaseToken: job.LeaseToken, Attempt: job.Attempt, AgentEpoch: 1, SessionID: "session",
				StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli(), Success: item.errorCat == "", ErrorCategory: item.errorCat, Result: protocol.ProbeResult{GoogleStatus: &status}}
			if response := jobHTTPResponse(t, a, http.MethodPost, "/api/v1/agent/jobs/"+job.JobID+"/result", token, "application/json", result); response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			response := webAgentResponse(t, a, http.MethodGet, "/api/v1/web/agents/"+id)
			var detail webAgentDetailView
			if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != 200 || detail.GoogleStatus.Result == nil {
				t.Fatal(response.Code, response.Body.String(), err)
			}
			encoded, _ := json.Marshal(detail.GoogleStatus.Result.YouTube)
			for _, want := range item.wantJSON {
				if !strings.Contains(string(encoded), want) {
					t.Errorf("%s missing from %s", want, encoded)
				}
			}
			for _, unwanted := range item.notJSON {
				if strings.Contains(string(encoded), unwanted) {
					t.Errorf("%s present in %s", unwanted, encoded)
				}
			}
		})
	}
}
