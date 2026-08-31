package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	webJobHTTP    = "10101010101010101010101010101010"
	webJobTCP     = "20202020202020202020202020202020"
	webJobICMP    = "30303030303030303030303030303030"
	webJobLeased  = "40404040404040404040404040404040"
	webJobQueued  = "50505050505050505050505050505050"
	webJobExpired = "60606060606060606060606060606060"
)

func TestWebJobAuthenticationAndQueryValidation(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	unauthenticated := webRequest(app, http.MethodGet, "/api/v1/web/jobs?unknown=x", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized || jobErrorCode(t, unauthenticated) != "unauthorized" {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	for _, suffix := range []string{
		"?unknown=x", "?agent_id=bad", "?schedule_id=bad", "?probe_type=dns", "?status=running",
		"?success=1", "?success=TRUE", "?status=queued&success=true", "?status=expired&finished_after=1",
		"?created_after=0", "?created_after=-1", "?created_after=x", "?created_after=20&created_before=20",
		"?finished_after=20&finished_before=10", "?limit=0", "?limit=101", "?cursor=x",
		"?status=queued&status=leased", "?success=true&success=false",
	} {
		response := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs"+suffix)
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("query=%q status=%d cache=%q body=%s", suffix, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
		code := jobErrorCode(t, response)
		if code != "invalid_query" && code != "invalid_cursor" {
			t.Fatalf("query=%q code=%q body=%s", suffix, code, response.Body.String())
		}
	}
}

func TestWebJobPaginationFiltersAndResultSummary(t *testing.T) {
	app, store, _ := newWebJobTestApp(t)
	defer store.Close()
	createWebFinishedJob(t, store, controlAgentA, webJobHTTP, protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/health", Method: "GET"}},
		protocol.ProbeResult{HTTP: &protocol.HTTPResult{DNSMS: 1, ConnectMS: 2, TLSMS: 3, TTFBMS: 4, TotalMS: 5, StatusCode: 204}}, true, "", "", time.Unix(990, 0))
	createWebFinishedJob(t, store, controlAgentA, webJobTCP, protocol.ProbeTypeTCPConnect,
		controlTCPConfig(), protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 20}},
		false, "connection_refused", "dial tcp: connection refused", time.Unix(980, 0))
	createWebFinishedJob(t, store, controlAgentA, webJobICMP, protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 4}},
		protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{Sent: 4, Received: 3, PacketLossPercent: 25, LatencyMinMS: 10, LatencyAvgMS: 15, LatencyMaxMS: 20}},
		true, "", "", time.Unix(970, 0))
	createControlListJob(t, store, controlAgentA, webJobLeased, time.Unix(960, 0), time.Unix(1100, 0))
	leased, err := store.ClaimJob(context.Background(), controlAgentA, controlClaimRequest("web-leased"), time.Unix(960, 0), 2*time.Minute)
	if err != nil || leased == nil || leased.JobID != webJobLeased {
		t.Fatalf("lease=%+v err=%v", leased, err)
	}
	createControlListJob(t, store, controlAgentA, webJobQueued, time.Unix(950, 0), time.Unix(1100, 0))
	createControlListJob(t, store, controlAgentA, webJobExpired, time.Unix(900, 0), time.Unix(940, 0))
	putControlSchedule(t, store, controlJobSchedule, controlAgentA, "scheduled", protocol.ProbeTypeTCPConnect,
		controlTCPConfig(), true, time.Unix(940, 0))
	outcome, err := store.MaterializeSchedule(context.Background(), controlJobSchedule, time.Unix(940, 0), 10*time.Minute)
	if err != nil || outcome.Result != storage.MaterializeCreated {
		t.Fatalf("materialize=%+v err=%v", outcome, err)
	}

	firstResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs?limit=3")
	var first webJobCollectionView
	decodeWebJobCollection(t, firstResponse, &first)
	if len(first.Items) != 3 || first.Items[0].JobID != webJobHTTP || first.Items[1].JobID != webJobTCP ||
		first.Items[2].JobID != webJobICMP || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	if first.Items[0].ResultSummary == nil || !first.Items[0].ResultSummary.Success ||
		first.Items[1].ResultSummary == nil || first.Items[1].ResultSummary.Success ||
		first.Items[1].ResultSummary.ErrorCategory == nil || *first.Items[1].ResultSummary.ErrorCategory != "connection_refused" {
		t.Fatalf("summaries=%+v", first.Items)
	}
	if first.Items[0].OperationStatus != "success" || first.Items[1].OperationStatus != "failed" || first.Items[2].OperationStatus != "success" {
		t.Fatalf("finished operation states=%+v", first.Items)
	}
	assertWebJobCollectionMinimized(t, firstResponse.Body.String())

	secondResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs?limit=3&cursor="+*first.NextCursor)
	var second webJobCollectionView
	decodeWebJobCollection(t, secondResponse, &second)
	if len(second.Items) != 3 || second.Items[0].JobID != webJobLeased || second.Items[0].Lease == nil ||
		second.Items[1].JobID != webJobQueued || second.Items[2].JobID != outcome.JobID || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	if second.Items[0].OperationStatus != "running" || second.Items[1].OperationStatus != "queued" {
		t.Fatalf("active operation states=%+v", second.Items)
	}
	thirdResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs?limit=3&cursor="+*second.NextCursor)
	var third webJobCollectionView
	decodeWebJobCollection(t, thirdResponse, &third)
	if len(third.Items) != 1 || third.Items[0].JobID != webJobExpired || third.Items[0].Status != storage.JobStatusExpired || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}
	if third.Items[0].OperationStatus != "expired" {
		t.Fatalf("expired operation state=%+v", third.Items[0])
	}

	assertWebJobIDs(t, app, "/api/v1/web/jobs?status=leased", webJobLeased)
	assertWebJobIDs(t, app, "/api/v1/web/jobs?success=true", webJobHTTP, webJobICMP)
	assertWebJobIDs(t, app, "/api/v1/web/jobs?success=false", webJobTCP)
	assertWebJobIDs(t, app, "/api/v1/web/jobs?schedule_id="+controlJobSchedule, outcome.JobID)
	assertWebJobIDs(t, app, "/api/v1/web/jobs?probe_type=http&agent_id="+controlAgentA, webJobHTTP)
	assertWebJobIDs(t, app, "/api/v1/web/jobs?created_after=975000&created_before=995000", webJobHTTP, webJobTCP)

	bound := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs?status=finished&cursor="+*first.NextCursor)
	if bound.Code != http.StatusBadRequest || jobErrorCode(t, bound) != "invalid_cursor" {
		t.Fatalf("filter cursor status=%d body=%s", bound.Code, bound.Body.String())
	}
	scheduleCursor, err := encodeControlCursor(webScheduleCursorResource, "", &storage.CollectionPageKey{
		CreatedAt: time.Unix(990, 0).UnixMilli(), ID: webJobHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	crossResource := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/jobs?cursor="+*scheduleCursor)
	if crossResource.Code != http.StatusBadRequest || jobErrorCode(t, crossResource) != "invalid_cursor" {
		t.Fatalf("cross-resource cursor status=%d body=%s", crossResource.Code, crossResource.Body.String())
	}
}

func TestWebJobTypedDetailsAndReadOnlyBoundary(t *testing.T) {
	app, store, _ := newWebJobTestApp(t)
	defer store.Close()
	createWebFinishedJob(t, store, controlAgentA, webJobHTTP, protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/health", Method: "GET"}},
		protocol.ProbeResult{HTTP: &protocol.HTTPResult{DNSMS: 1, ConnectMS: 2, TLSMS: 3, TTFBMS: 4, TotalMS: 5, StatusCode: 204}}, true, "", "", time.Unix(990, 0))
	createWebFinishedJob(t, store, controlAgentA, webJobTCP, protocol.ProbeTypeTCPConnect,
		controlTCPConfig(), protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 20}},
		false, "connection_refused", "dial tcp: connection refused", time.Unix(980, 0))
	createWebFinishedJob(t, store, controlAgentA, webJobICMP, protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 4}},
		protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{Sent: 4, Received: 3, PacketLossPercent: 25, LatencyMinMS: 10, LatencyAvgMS: 15, LatencyMaxMS: 20}},
		true, "", "", time.Unix(970, 0))

	tests := []struct {
		id     string
		assert func(webJobDetailView) bool
	}{
		{webJobHTTP, func(detail webJobDetailView) bool {
			return detail.Config.URL == "https://secret.example/health" && detail.Result != nil && detail.Result.Measurement.HTTP != nil &&
				detail.Result.Measurement.HTTP.StatusCode == 204 && detail.Result.Measurement.TCPConnect == nil && detail.Result.Measurement.ICMPPing == nil
		}},
		{webJobTCP, func(detail webJobDetailView) bool {
			return detail.Config.Host == "private.example" && detail.Result != nil && !detail.Result.Success &&
				detail.Result.ErrorMessage != nil && *detail.Result.ErrorMessage == "dial tcp: connection refused" &&
				detail.Result.Measurement.TCPConnect != nil && detail.Result.Measurement.TCPConnect.ConnectMS == 20
		}},
		{webJobICMP, func(detail webJobDetailView) bool {
			return detail.Config.Target == "192.0.2.1" && detail.Result != nil && detail.Result.Measurement.ICMPPing != nil &&
				detail.Result.Measurement.ICMPPing.PacketLossPercent == 25
		}},
	}
	for _, test := range tests {
		response := webAgentResponse(t, app, http.MethodGet, webJobPathPrefix+test.id)
		var detail webJobDetailView
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil || !test.assert(detail) {
			t.Fatalf("job=%s status=%d body=%s", test.id, response.Code, response.Body.String())
		}
		assertNoWebJobSecrets(t, response.Body.String())
	}

	path := webJobPathPrefix + webJobHTTP
	for _, test := range []struct {
		method string
		path   string
		code   int
		error  string
	}{
		{http.MethodGet, path + "?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, webJobPathPrefix + strings.Repeat("f", 64), http.StatusNotFound, "job_not_found"},
		{http.MethodGet, webJobPathPrefix + "bad", http.StatusBadRequest, "invalid_request"},
		{http.MethodPost, "/api/v1/web/jobs", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodPut, path, http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodDelete, path, http.StatusMethodNotAllowed, "method_not_allowed"},
	} {
		response := webAgentResponse(t, app, test.method, test.path)
		if response.Code != test.code || jobErrorCode(t, response) != test.error || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method=%s path=%q status=%d cache=%q body=%s", test.method, test.path, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
		if test.code == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("method=%s path=%q allow=%q", test.method, test.path, response.Header().Get("Allow"))
		}
	}
	if _, _, err := store.GetProbeJobSnapshot(context.Background(), webJobHTTP, time.Unix(1000, 0)); err != nil {
		t.Fatalf("read-only Web request changed job: %v", err)
	}
}

func TestJobPageUsesOnlyWebReadAPI(t *testing.T) {
	app, store, _ := newWebJobTestApp(t)
	defer store.Close()
	page := webAgentResponse(t, app, http.MethodGet, "/jobs.html")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/jobs.js") ||
		!strings.Contains(page.Body.String(), "/session.js") || !strings.Contains(page.Body.String(), "id=\"logout\"") {
		t.Fatalf("job page status=%d body=%s", page.Code, page.Body.String())
	}
	script := webAgentResponse(t, app, http.MethodGet, "/jobs.js")
	body := script.Body.String()
	if script.Code != http.StatusOK || !strings.Contains(body, "/api/v1/web/jobs") ||
		strings.Contains(body, "/api/v1/control/") || strings.Contains(body, "method: 'PUT'") ||
		strings.Contains(body, "method: 'POST'") || strings.Contains(body, "method: 'DELETE'") {
		t.Fatalf("job script status=%d body=%s", script.Code, body)
	}
	for _, path := range []string{"/", "/schedules.html"} {
		response := webAgentResponse(t, app, http.MethodGet, path)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/jobs.html") {
			t.Fatalf("navigation path=%q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func newWebJobTestApp(t *testing.T) (*App, *storage.Store, time.Time) {
	t.Helper()
	app, store, _ := newWebAgentTestApp(t)
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	return app, store, now
}

func createWebFinishedJob(t *testing.T, store *storage.Store, agentID, jobID string, probeType protocol.ProbeType,
	config protocol.ProbeConfig, measurement protocol.ProbeResult, success bool, category, message string, createdAt time.Time) {
	t.Helper()
	if err := store.CreateOneShotJob(context.Background(), storage.CreateOneShotJobParams{
		ID: jobID, AgentID: agentID, ProbeType: probeType, Config: config, TimeoutMS: 5000,
		CreatedAt: createdAt.UnixMilli(), NotBefore: createdAt.UnixMilli(), ExpiresAt: createdAt.Add(10 * time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	session := "web-" + jobID[:4]
	job, err := store.ClaimJob(context.Background(), agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: session,
		SupportedProbeTypes: []protocol.ProbeType{probeType},
	}, createdAt, time.Minute)
	if err != nil || job == nil || job.JobID != jobID {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	result := protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: job.LeaseToken, Attempt: job.Attempt,
		AgentEpoch: 1, SessionID: session, StartedAt: createdAt.UnixMilli(),
		FinishedAt: createdAt.Add(25 * time.Millisecond).UnixMilli(), DurationMS: 25,
		Success: success, ErrorCategory: category, ErrorMessage: message, Result: measurement,
	}
	if success {
		result.ResolvedIP = "192.0.2.1"
	}
	if _, err := store.SubmitJobResult(context.Background(), agentID, jobID, result, createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func decodeWebJobCollection(t *testing.T, response *httptest.ResponseRecorder, collection *webJobCollectionView) {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), collection); err != nil {
		t.Fatal(err)
	}
}

func assertWebJobIDs(t *testing.T, app *App, path string, want ...string) {
	t.Helper()
	response := webAgentResponse(t, app, http.MethodGet, path)
	var collection webJobCollectionView
	decodeWebJobCollection(t, response, &collection)
	if len(collection.Items) != len(want) {
		t.Fatalf("path=%q items=%+v want=%v", path, collection.Items, want)
	}
	for index, id := range want {
		if collection.Items[index].JobID != id {
			t.Fatalf("path=%q index=%d got=%s want=%s", path, index, collection.Items[index].JobID, id)
		}
	}
}

func assertWebJobCollectionMinimized(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{`"config"`, `"measurement"`, `"error_message"`, "secret.example", "lease_token", "lease_epoch", "lease_session", "result_hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("collection leaked %q: %s", forbidden, body)
		}
	}
}

func assertNoWebJobSecrets(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"control_token", "token_hash", "lease_token", "lease_epoch", "lease_session", "result_hash", "agent_epoch", "session_id", "sequence", "boot_id"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q leaked: %s", forbidden, body)
		}
	}
}
