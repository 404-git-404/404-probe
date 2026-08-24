package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/agent"
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const firstControlJobID = "0123456789abcdef0123456789abcdef"

type controlTestRequest struct {
	AgentID          string             `json:"agent_id"`
	ProbeType        protocol.ProbeType `json:"probe_type"`
	Config           any                `json:"config"`
	TimeoutMS        int                `json:"timeout_ms"`
	ExpiresInSeconds int64              `json:"expires_in_seconds"`
}

func newControlTestApp(t *testing.T) (*App, *storage.Store, string, string, string) {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := auth.NewID()
	agentToken, agentHash, _ := auth.NewToken()
	if err := store.AddAgent(context.Background(), agentID, "test", agentHash, time.Unix(100, 0)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	controlToken, controlHash, _ := auth.NewToken()
	app, err := NewApp(store, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), WithControlTokenHash(controlHash))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return app, store, agentID, agentToken, controlToken
}

func newFileControlTestApp(t *testing.T, path string) (*App, *storage.Store, string, string) {
	t.Helper()
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := auth.NewID()
	_, agentHash, _ := auth.NewToken()
	if err := store.AddAgent(context.Background(), agentID, "test", agentHash, time.Unix(100, 0)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	controlToken, controlHash, _ := auth.NewToken()
	app, err := NewApp(store, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), WithControlTokenHash(controlHash))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return app, store, agentID, controlToken
}

func validControlTestRequest(agentID string, probeType protocol.ProbeType) controlTestRequest {
	request := controlTestRequest{AgentID: agentID, ProbeType: probeType, TimeoutMS: 5000, ExpiresInSeconds: 300}
	switch probeType {
	case protocol.ProbeTypeTCPConnect:
		request.Config = protocol.TCPConnectConfig{Host: "example.com", Port: 443}
	case protocol.ProbeTypeHTTP:
		request.Config = protocol.HTTPConfig{URL: "https://example.com/health", Method: "GET"}
	case protocol.ProbeTypeICMPPing:
		request.Config = protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}
	}
	return request
}

func controlHTTPResponse(t *testing.T, app *App, method, path, token, contentType string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestControlAPIDisabledAndAuthentication(t *testing.T) {
	disabled, disabledStore, agentID, _ := testApp(t)
	defer disabledStore.Close()
	path := controlJobPathPrefix + firstControlJobID
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	if response := controlHTTPResponse(t, disabled, http.MethodPut, path, "", "application/json", request); response.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d body=%s", response.Code, response.Body.String())
	}
	for _, ambiguous := range []string{
		"/api/v1/control/jobs/../x",
		"/api/v1/control/jobs/%2e%2e/x",
		"/api/v1/control/jobs/./" + firstControlJobID,
		"/api/v1/control/jobs/%2e/" + firstControlJobID,
	} {
		if response := controlHTTPResponse(t, disabled, http.MethodGet, ambiguous, "", "", nil); response.Code != http.StatusNotFound {
			t.Fatalf("disabled ambiguous path=%q status=%d location=%q body=%s", ambiguous, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}

	app, store, agentID, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	request = validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	for _, token := range []string{"", "wrong", agentToken} {
		response := controlHTTPResponse(t, app, http.MethodPut, path, token, "application/json", request)
		if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("token=%q status=%d auth=%q body=%s", token, response.Code, response.Header().Get("WWW-Authenticate"), response.Body.String())
		}
		for _, secret := range []string{token, controlToken} {
			if secret != "" && strings.Contains(response.Body.String(), secret) {
				t.Fatalf("secret leaked in response: %s", response.Body.String())
			}
		}
	}
	response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	if response.Code != http.StatusCreated {
		t.Fatalf("authorized status=%d body=%s", response.Code, response.Body.String())
	}
	for _, ambiguous := range []string{
		"/api/v1/control/jobs/../x",
		"/api/v1/control/jobs/%2e%2e/x",
		"/api/v1/control/jobs/./" + firstControlJobID,
		"/api/v1/control/jobs/%2e/" + firstControlJobID,
	} {
		for _, token := range []string{"", "wrong"} {
			response := controlHTTPResponse(t, app, http.MethodGet, ambiguous, token, "", nil)
			if response.Code != http.StatusUnauthorized || response.Header().Get("Location") != "" {
				t.Fatalf("ambiguous path=%q token=%q status=%d location=%q body=%s", ambiguous, token, response.Code, response.Header().Get("Location"), response.Body.String())
			}
		}
		response := controlHTTPResponse(t, app, http.MethodGet, ambiguous, controlToken, "", nil)
		if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" || response.Header().Get("Location") != "" {
			t.Fatalf("authorized ambiguous path=%q status=%d location=%q body=%s", ambiguous, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}
}

func TestWithControlTokenHashCopiesAndValidatesDigest(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agentID, _ := auth.NewID()
	_, agentHash, _ := auth.NewToken()
	if err := store.AddAgent(context.Background(), agentID, "test", agentHash, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	controlToken, hash, _ := auth.NewToken()
	app, err := NewApp(store, time.Minute, nil, WithControlTokenHash(hash))
	if err != nil {
		t.Fatal(err)
	}
	hash[0] ^= 0xff
	response := controlHTTPResponse(t, app, http.MethodPut, controlJobPathPrefix+firstControlJobID, controlToken, "application/json", validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect))
	if response.Code != http.StatusCreated {
		t.Fatalf("mutating caller hash changed authentication: status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := NewApp(store, time.Minute, nil, WithControlTokenHash([]byte("short"))); err == nil {
		t.Fatal("short control token digest accepted")
	}
}

func TestControlPutGetAndIdempotency(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	createdAt := time.Unix(1000, 0)
	now := createdAt
	app.now = func() time.Time { return now }
	path := controlJobPathPrefix + firstControlJobID
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	first := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json; charset=utf-8", request)
	if first.Code != http.StatusCreated || first.Header().Get("Location") != path {
		t.Fatalf("first status=%d location=%q body=%s", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	var firstView controlJobView
	if err := json.Unmarshal(first.Body.Bytes(), &firstView); err != nil {
		t.Fatal(err)
	}
	if firstView.JobID != firstControlJobID || firstView.AgentID != agentID || firstView.CreatedAt != createdAt.UnixMilli() || firstView.NotBefore != createdAt.UnixMilli() || firstView.ExpiresAt != createdAt.Add(5*time.Minute).UnixMilli() || firstView.Status != storage.JobStatusQueued || firstView.Attempt != 0 || firstView.Lease != nil || firstView.FinishedAt != nil || firstView.Result != nil {
		t.Fatalf("first view=%+v", firstView)
	}

	now = now.Add(10 * time.Second)
	replay := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayView controlJobView
	if err := json.Unmarshal(replay.Body.Bytes(), &replayView); err != nil {
		t.Fatal(err)
	}
	if replayView.CreatedAt != firstView.CreatedAt || replayView.ExpiresAt != firstView.ExpiresAt || replayView.Attempt != firstView.Attempt {
		t.Fatalf("replay changed job: first=%+v replay=%+v", firstView, replayView)
	}
	conflict := request
	conflict.TimeoutMS++
	conflicting := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", conflict)
	if conflicting.Code != http.StatusConflict || jobErrorCode(t, conflicting) != "idempotency_conflict" {
		t.Fatalf("conflict status=%d body=%s", conflicting.Code, conflicting.Body.String())
	}
	missingAgentConflict := request
	missingAgentConflict.AgentID = "missing-agent"
	conflicting = controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", missingAgentConflict)
	if conflicting.Code != http.StatusConflict || jobErrorCode(t, conflicting) != "idempotency_conflict" {
		t.Fatalf("missing-agent conflict status=%d body=%s", conflicting.Code, conflicting.Body.String())
	}
	get := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	for _, forbidden := range []string{"lease_token", "lease_epoch", "lease_session", "result_hash", controlToken} {
		if strings.Contains(get.Body.String(), forbidden) {
			t.Fatalf("forbidden value %q in response: %s", forbidden, get.Body.String())
		}
	}
	method := controlHTTPResponse(t, app, http.MethodPost, path, controlToken, "application/json", request)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, PUT" || jobErrorCode(t, method) != "method_not_allowed" {
		t.Fatalf("method status=%d allow=%q body=%s", method.Code, method.Header().Get("Allow"), method.Body.String())
	}
	missing := controlHTTPResponse(t, app, http.MethodGet, controlJobPathPrefix+"ffffffffffffffffffffffffffffffff", controlToken, "", nil)
	if missing.Code != http.StatusNotFound || jobErrorCode(t, missing) != "job_not_found" {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	collectionUnauthorized := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs", "", "", nil)
	if collectionUnauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("collection unauthorized status=%d body=%s", collectionUnauthorized.Code, collectionUnauthorized.Body.String())
	}
	collection := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs", controlToken, "", nil)
	if collection.Code != http.StatusNotFound {
		t.Fatalf("collection status=%d body=%s", collection.Code, collection.Body.String())
	}
	invalidPath := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/jobs/", controlToken, "", nil)
	if invalidPath.Code != http.StatusBadRequest || jobErrorCode(t, invalidPath) != "invalid_request" {
		t.Fatalf("invalid path status=%d body=%s", invalidPath.Code, invalidPath.Body.String())
	}
}

func TestControlPutSupportsAllProbeTypes(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	for index, probeType := range []protocol.ProbeType{protocol.ProbeTypeHTTP, protocol.ProbeTypeTCPConnect, protocol.ProbeTypeICMPPing} {
		jobID := strings.Repeat(string("012"[index]), 32)
		response := controlHTTPResponse(t, app, http.MethodPut, controlJobPathPrefix+jobID, controlToken, "application/json", validControlTestRequest(agentID, probeType))
		if response.Code != http.StatusCreated {
			t.Fatalf("type=%s status=%d body=%s", probeType, response.Code, response.Body.String())
		}
		stored, err := store.GetProbeJob(context.Background(), jobID)
		if err != nil || stored.ProbeType != probeType {
			t.Fatalf("type=%s stored=%+v err=%v", probeType, stored, err)
		}
	}
}

func TestControlPutValidationAndPaths(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	path := controlJobPathPrefix + firstControlJobID
	valid := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	encodedValid, _ := json.Marshal(valid)
	tests := []struct {
		name        string
		path        string
		contentType string
		body        []byte
		wantStatus  int
		wantCode    string
	}{
		{name: "content type", path: path, contentType: "text/plain", body: encodedValid, wantStatus: http.StatusUnsupportedMediaType, wantCode: "invalid_content_type"},
		{name: "unknown field", path: path, contentType: "application/json", body: append(encodedValid[:len(encodedValid)-1], []byte(`,"unknown":true}`)...), wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "multiple values", path: path, contentType: "application/json", body: append(encodedValid, []byte(` {}`)...), wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "null", path: path, contentType: "application/json", body: []byte(`null`), wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "oversized", path: path, contentType: "application/json", body: bytes.Repeat([]byte(" "), maxControlJobBodyBytes+1), wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
		{name: "short ID", path: controlJobPathPrefix + "abc", contentType: "application/json", body: encodedValid, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "uppercase ID", path: controlJobPathPrefix + strings.ToUpper(firstControlJobID), contentType: "application/json", body: encodedValid, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "encoded slash", path: controlJobPathPrefix + "0123456789abcdef%2F123456789abcdef", contentType: "application/json", body: encodedValid, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := controlHTTPResponse(t, app, http.MethodPut, test.path, controlToken, test.contentType, test.body)
			if response.Code != test.wantStatus || jobErrorCode(t, response) != test.wantCode {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	invalidRequests := []controlTestRequest{
		{AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect, Config: protocol.TCPConnectConfig{Host: "example.com", Port: 443}, TimeoutMS: protocol.MinProbeTimeoutMS - 1, ExpiresInSeconds: 300},
		{AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect, Config: protocol.TCPConnectConfig{Host: "example.com", Port: 443}, TimeoutMS: 5000, ExpiresInSeconds: minJobTTLSeconds - 1},
		{AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect, Config: protocol.TCPConnectConfig{Host: "example.com", Port: 0}, TimeoutMS: 5000, ExpiresInSeconds: 300},
	}
	for index, request := range invalidRequests {
		response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
		if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
			t.Fatalf("invalid request %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}

	app.now = func() time.Time { return time.UnixMilli(math.MaxInt64 - 30_000) }
	overflow := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", valid)
	if overflow.Code != http.StatusBadRequest || jobErrorCode(t, overflow) != "invalid_request" {
		t.Fatalf("overflow status=%d body=%s", overflow.Code, overflow.Body.String())
	}
}

func TestControlJobViewsEffectiveLeaseExpiryAndFinishedResult(t *testing.T) {
	app, store, agentID, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	createdAt := time.Unix(1000, 0)
	now := createdAt
	app.now = func() time.Time { return now }
	path := controlJobPathPrefix + firstControlJobID
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	if response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request); response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	now = now.Add(time.Second)
	job, claimed := claimHTTPJob(t, app, agentToken, validClaimRequest(1, "session-1"))
	if claimed.Code != http.StatusOK || job == nil {
		t.Fatalf("claim status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	leased := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	if leased.Code != http.StatusOK || strings.Contains(leased.Body.String(), job.LeaseToken) {
		t.Fatalf("leased status=%d body=%s", leased.Code, leased.Body.String())
	}
	var leasedView controlJobView
	if err := json.Unmarshal(leased.Body.Bytes(), &leasedView); err != nil || leasedView.Status != storage.JobStatusLeased || leasedView.Lease == nil || leasedView.Result != nil {
		t.Fatalf("leased view=%+v err=%v", leasedView, err)
	}

	now = now.Add(jobLeaseDuration)
	requeued := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	var requeuedView controlJobView
	if err := json.Unmarshal(requeued.Body.Bytes(), &requeuedView); err != nil || requeuedView.Status != storage.JobStatusQueued || requeuedView.Lease != nil || requeuedView.Attempt != 1 {
		t.Fatalf("requeued status=%d view=%+v err=%v body=%s", requeued.Code, requeuedView, err, requeued.Body.String())
	}
	job, claimed = claimHTTPJob(t, app, agentToken, validClaimRequest(2, "session-2"))
	if claimed.Code != http.StatusOK || job == nil {
		t.Fatalf("second claim status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	result := validHTTPJobResult(job)
	result.AgentEpoch = 2
	result.SessionID = "session-2"
	now = now.Add(time.Second)
	submitted := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/"+firstControlJobID+"/result", agentToken, "application/json", result)
	assertResultACK(t, submitted, false)
	finished := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	var finishedView controlJobView
	if err := json.Unmarshal(finished.Body.Bytes(), &finishedView); err != nil || finishedView.Status != storage.JobStatusFinished || finishedView.Result == nil || finishedView.FinishedAt == nil || finishedView.Lease != nil {
		t.Fatalf("finished status=%d view=%+v err=%v body=%s", finished.Code, finishedView, err, finished.Body.String())
	}
	if finishedView.Result.Success != true || string(finishedView.Result.Measurement) != `{"connect_ms":20}` {
		t.Fatalf("finished result=%+v", finishedView.Result)
	}
	replay := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	var replayView controlJobView
	if err := json.Unmarshal(replay.Body.Bytes(), &replayView); replay.Code != http.StatusOK || err != nil || replayView.Status != storage.JobStatusFinished || replayView.Attempt != 2 || replayView.Result == nil {
		t.Fatalf("finished replay status=%d view=%+v err=%v body=%s", replay.Code, replayView, err, replay.Body.String())
	}
}

func TestControlQueueFullReplayAndRevokedAgent(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	for i := 0; i < storage.MaxOutstandingJobsPerAgent; i++ {
		jobID := strings.Repeat("0", 24) + fmt.Sprintf("%08x", i)
		response := controlHTTPResponse(t, app, http.MethodPut, controlJobPathPrefix+jobID, controlToken, "application/json", request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	firstPath := controlJobPathPrefix + strings.Repeat("0", 32)
	replay := controlHTTPResponse(t, app, http.MethodPut, firstPath, controlToken, "application/json", request)
	if replay.Code != http.StatusOK {
		t.Fatalf("full replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	fullPath := controlJobPathPrefix + strings.Repeat("f", 32)
	full := controlHTTPResponse(t, app, http.MethodPut, fullPath, controlToken, "application/json", request)
	if full.Code != http.StatusTooManyRequests || jobErrorCode(t, full) != "queue_full" || full.Header().Get("Retry-After") != "10" {
		t.Fatalf("full status=%d retry=%q body=%s", full.Code, full.Header().Get("Retry-After"), full.Body.String())
	}
	if changed, err := store.RevokeAgent(context.Background(), agentID, time.Now()); err != nil || !changed {
		t.Fatalf("revoke changed=%t err=%v", changed, err)
	}
	revokedReplay := controlHTTPResponse(t, app, http.MethodPut, firstPath, controlToken, "application/json", request)
	if revokedReplay.Code != http.StatusOK {
		t.Fatalf("revoked agent replay status=%d body=%s", revokedReplay.Code, revokedReplay.Body.String())
	}
	revoked := controlHTTPResponse(t, app, http.MethodPut, fullPath, controlToken, "application/json", request)
	if revoked.Code != http.StatusNotFound || jobErrorCode(t, revoked) != "agent_not_found" {
		t.Fatalf("revoked status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestControlConcurrentIdenticalPutCreatesOneJob(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	app.now = func() time.Time { return time.Unix(1000, 0) }
	path := controlJobPathPrefix + firstControlJobID
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	const callers = 16
	var wait sync.WaitGroup
	statuses := make(chan int, callers)
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			statuses <- controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request).Code
		}()
	}
	wait.Wait()
	close(statuses)
	created := 0
	replayed := 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			t.Fatalf("unexpected status=%d", status)
		}
	}
	if created != 1 || replayed != callers-1 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}
	if _, err := store.GetProbeJob(context.Background(), firstControlJobID); err != nil {
		t.Fatal(err)
	}
}

func TestControlTCPJobEndToEnd(t *testing.T) {
	app, store, agentID, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			err = connection.Close()
		}
		accepted <- err
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	request := validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)
	request.Config = protocol.TCPConnectConfig{Host: "127.0.0.1", Port: port}
	path := controlJobPathPrefix + firstControlJobID
	if response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	runner, err := agent.New(agent.Config{
		ServerURL: httpServer.URL, AgentID: agentID, Token: agentToken,
		Interval: time.Hour, JobInterval: 10 * time.Millisecond, Timeout: 2 * time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "agent.state"),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(ctx) }()
	var finished controlJobView
	for ctx.Err() == nil {
		response := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &finished); err != nil {
			t.Fatal(err)
		}
		if finished.Status == storage.JobStatusFinished {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if finished.Status != storage.JobStatusFinished || finished.Result == nil || !finished.Result.Success {
		t.Fatalf("job did not finish successfully: %+v context=%v", finished, ctx.Err())
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	if err := <-runnerDone; err != nil {
		t.Fatal(err)
	}
}

func TestControlGetRejectsCorruptStoredData(t *testing.T) {
	t.Run("invalid config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt-config.db")
		app, store, agentID, controlToken := newFileControlTestApp(t, path)
		defer store.Close()
		jobPath := controlJobPathPrefix + firstControlJobID
		if response := controlHTTPResponse(t, app, http.MethodPut, jobPath, controlToken, "application/json", validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)); response.Code != http.StatusCreated {
			t.Fatal(response.Body.String())
		}
		updateJobFixture(t, path, `UPDATE probe_jobs SET config_json='{"host":"example.com","port":0}' WHERE id=?`, firstControlJobID)
		response := controlHTTPResponse(t, app, http.MethodGet, jobPath, controlToken, "", nil)
		if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" || strings.Contains(strings.ToLower(response.Body.String()), "sqlite") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("finished without result", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt-result.db")
		app, store, agentID, controlToken := newFileControlTestApp(t, path)
		defer store.Close()
		jobPath := controlJobPathPrefix + firstControlJobID
		if response := controlHTTPResponse(t, app, http.MethodPut, jobPath, controlToken, "application/json", validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)); response.Code != http.StatusCreated {
			t.Fatal(response.Body.String())
		}
		updateJobFixture(t, path, `UPDATE probe_jobs SET status='finished',finished_at=? WHERE id=?`, time.Now().UnixMilli(), firstControlJobID)
		response := controlHTTPResponse(t, app, http.MethodGet, jobPath, controlToken, "", nil)
		if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("result hash mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt-hash.db")
		app, store, agentID, controlToken := newFileControlTestApp(t, path)
		defer store.Close()
		jobPath := controlJobPathPrefix + firstControlJobID
		if response := controlHTTPResponse(t, app, http.MethodPut, jobPath, controlToken, "application/json", validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)); response.Code != http.StatusCreated {
			t.Fatal(response.Body.String())
		}
		claimedAt := time.Now()
		job, err := store.ClaimJob(context.Background(), agentID, validClaimRequest(1, "session-1"), claimedAt, jobLeaseDuration)
		if err != nil || job == nil {
			t.Fatalf("claim=%+v err=%v", job, err)
		}
		if _, err := store.SubmitJobResult(context.Background(), agentID, job.JobID, validHTTPJobResult(job), claimedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		updateJobFixture(t, path, `UPDATE probe_results SET result_hash=zeroblob(32) WHERE job_id=?`, firstControlJobID)
		response := controlHTTPResponse(t, app, http.MethodGet, jobPath, controlToken, "", nil)
		if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}
