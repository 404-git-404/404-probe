package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	controlJobFinished = "ffffffffffffffffffffffffffffffff"
	controlJobFailed   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	controlJobLeased   = "dddddddddddddddddddddddddddddddd"
	controlJobQueued   = "cccccccccccccccccccccccccccccccc"
	controlJobExpired  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	controlJobSchedule = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestControlJobCollectionAuthenticationAndValidation(t *testing.T) {
	disabled, disabledStore, _, _ := testApp(t)
	defer disabledStore.Close()
	response := controlHTTPResponse(t, disabled, http.MethodGet, "/api/v1/control/jobs?unknown=x", "", "", nil)
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("disabled status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}

	app, store, _, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	for _, token := range []string{"", "wrong", agentToken} {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs?unknown=x", token, "", nil)
		if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" ||
			response.Header().Get("WWW-Authenticate") != "Bearer" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("token=%q status=%d headers=%v body=%s", token, response.Code, response.Header(), response.Body.String())
		}
	}

	for _, suffix := range []string{
		"?unknown=x", "?agent_id=bad", "?schedule_id=bad", "?probe_type=dns", "?status=running",
		"?success=1", "?success=TRUE", "?status=queued&success=true", "?status=expired&finished_after=1",
		"?created_after=0", "?created_after=-1", "?created_after=x", "?created_after=20&created_before=20",
		"?finished_after=20&finished_before=10", "?limit=0", "?limit=101", "?cursor=x",
		"?status=queued&status=leased", "?success=true&success=false",
	} {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs"+suffix, controlToken, "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query=%q status=%d body=%s", suffix, response.Code, response.Body.String())
		}
		code := jobErrorCode(t, response)
		if code != "invalid_query" && code != "invalid_cursor" {
			t.Fatalf("query=%q code=%q body=%s", suffix, code, response.Body.String())
		}
	}
}

func TestControlJobCollectionPaginationFiltersAndSummary(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	queryNow := time.Unix(1000, 0)
	app.now = func() time.Time { return queryNow }
	createFinishedControlJob(t, store, agentID, controlJobFinished, time.Unix(990, 0), true, "")
	createFinishedControlJob(t, store, agentID, controlJobFailed, time.Unix(985, 0), false, "connection_refused")
	createControlListJob(t, store, agentID, controlJobLeased, time.Unix(980, 0), time.Unix(1100, 0))
	leased, err := store.ClaimJob(context.Background(), agentID, controlClaimRequest("list-leased"), time.Unix(980, 0), 2*time.Minute)
	if err != nil || leased == nil || leased.JobID != controlJobLeased {
		t.Fatalf("lease job=%+v error=%v", leased, err)
	}
	createControlListJob(t, store, agentID, controlJobQueued, time.Unix(970, 0), time.Unix(1100, 0))
	createControlListJob(t, store, agentID, controlJobExpired, time.Unix(900, 0), time.Unix(950, 0))
	putControlSchedule(t, store, controlJobSchedule, agentID, "scheduled", protocol.ProbeTypeTCPConnect,
		controlTCPConfig(), true, time.Unix(960, 0))
	outcome, err := store.MaterializeSchedule(context.Background(), controlJobSchedule, time.Unix(960, 0), 10*time.Minute)
	if err != nil || outcome.Result != storage.MaterializeCreated {
		t.Fatalf("materialize=%+v error=%v", outcome, err)
	}

	first := getControlJobCollection(t, app, "/api/v1/control/jobs?limit=2", controlToken)
	if len(first.Items) != 2 || first.Items[0].JobID != controlJobFinished || first.Items[1].JobID != controlJobFailed || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	if first.Items[0].ResultSummary == nil || !first.Items[0].ResultSummary.Success ||
		first.Items[0].ResultSummary.FinishedAt != time.Unix(990, 0).Add(25*time.Millisecond).UnixMilli() ||
		first.Items[0].FinishedAt == nil {
		t.Fatalf("success summary=%+v", first.Items[0])
	}
	if first.Items[1].ResultSummary == nil || first.Items[1].ResultSummary.Success ||
		first.Items[1].ResultSummary.ErrorCategory == nil || *first.Items[1].ResultSummary.ErrorCategory != "connection_refused" {
		t.Fatalf("failure summary=%+v", first.Items[1])
	}

	second := getControlJobCollection(t, app, "/api/v1/control/jobs?limit=3&cursor="+*first.NextCursor, controlToken)
	if len(second.Items) != 3 || second.Items[0].JobID != controlJobLeased || second.Items[0].Lease == nil ||
		second.Items[1].JobID != controlJobQueued || second.Items[2].JobID != outcome.JobID || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	third := getControlJobCollection(t, app, "/api/v1/control/jobs?limit=3&cursor="+*second.NextCursor, controlToken)
	if len(third.Items) != 1 || third.Items[0].JobID != controlJobExpired || third.Items[0].Status != storage.JobStatusExpired || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}

	for _, forbidden := range []string{`"config"`, "lease_token", "lease_epoch", "lease_session", "result_hash", `"measurement"`, `"error_message"`} {
		if strings.Contains(firstRawControlJobCollection(t, app, controlToken), forbidden) {
			t.Fatalf("forbidden field %q leaked", forbidden)
		}
	}
	assertControlJobIDs(t, app, "/api/v1/control/jobs?status=leased", controlToken, controlJobLeased)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?success=true", controlToken, controlJobFinished)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?success=false", controlToken, controlJobFailed)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?schedule_id="+controlJobSchedule, controlToken, outcome.JobID)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?probe_type=tcp_connect&agent_id="+agentID, controlToken,
		controlJobFinished, controlJobFailed, controlJobLeased, controlJobQueued, outcome.JobID, controlJobExpired)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?created_after=980000&created_before=991000", controlToken,
		controlJobFinished, controlJobFailed)
	assertControlJobIDs(t, app, "/api/v1/control/jobs?finished_after=985000&finished_before=992000", controlToken,
		controlJobFinished, controlJobFailed)

	response := controlHTTPResponse(t, app, http.MethodGet,
		"/api/v1/control/jobs?status=finished&cursor="+*first.NextCursor, controlToken, "", nil)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("filter cursor status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlJobCollectionMethodAndDetailQuery(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	createControlListJob(t, store, agentID, controlJobQueued, time.Unix(970, 0), time.Unix(1100, 0))
	app.now = func() time.Time { return time.Unix(1000, 0) }

	response := controlHTTPResponse(t, app, http.MethodPost, "/api/v1/control/jobs", controlToken, "", nil)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet ||
		response.Header().Get("Cache-Control") != "no-store" || jobErrorCode(t, response) != "method_not_allowed" {
		t.Fatalf("method status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	response = controlHTTPResponse(t, app, http.MethodGet, controlJobPathPrefix+controlJobQueued+"?unknown=x", controlToken, "", nil)
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" || jobErrorCode(t, response) != "invalid_query" {
		t.Fatalf("detail query status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func createControlListJob(t *testing.T, store *storage.Store, agentID, jobID string, createdAt, expiresAt time.Time) {
	t.Helper()
	if err := store.CreateOneShotJob(context.Background(), storage.CreateOneShotJobParams{
		ID: jobID, AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect, Config: controlTCPConfig(),
		TimeoutMS: 5000, CreatedAt: createdAt.UnixMilli(), NotBefore: createdAt.UnixMilli(), ExpiresAt: expiresAt.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
}

func createFinishedControlJob(t *testing.T, store *storage.Store, agentID, jobID string, createdAt time.Time, success bool, category string) {
	t.Helper()
	createControlListJob(t, store, agentID, jobID, createdAt, createdAt.Add(10*time.Minute))
	job, err := store.ClaimJob(context.Background(), agentID, controlClaimRequest("finish-"+jobID[:4]), createdAt, time.Minute)
	if err != nil || job == nil || job.JobID != jobID {
		t.Fatalf("claim job=%+v error=%v", job, err)
	}
	result := validHTTPJobResult(job)
	result.StartedAt = createdAt.UnixMilli()
	result.FinishedAt = createdAt.Add(25 * time.Millisecond).UnixMilli()
	result.Success = success
	result.ErrorCategory = category
	result.ErrorMessage = ""
	if _, err := store.SubmitJobResult(context.Background(), agentID, jobID, result, createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func controlClaimRequest(session string) protocol.ClaimRequest {
	return protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: session,
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}
}

func controlTCPConfig() protocol.ProbeConfig {
	return protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "private.example", Port: 443}}
}

func getControlJobCollection(t *testing.T, app *App, path, token string) controlJobCollectionView {
	t.Helper()
	response := controlHTTPResponse(t, app, http.MethodGet, path, token, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("path=%q status=%d cache=%q body=%s", path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var collection controlJobCollectionView
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	return collection
}

func assertControlJobIDs(t *testing.T, app *App, path, token string, want ...string) {
	t.Helper()
	collection := getControlJobCollection(t, app, path, token)
	if len(collection.Items) != len(want) {
		t.Fatalf("path=%q items=%+v want=%v", path, collection.Items, want)
	}
	for index := range want {
		if collection.Items[index].JobID != want[index] {
			t.Fatalf("path=%q items=%+v want=%v", path, collection.Items, want)
		}
	}
}

func firstRawControlJobCollection(t *testing.T, app *App, token string) string {
	t.Helper()
	response := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs", token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}
