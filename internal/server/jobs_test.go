package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func createHTTPTestJob(t *testing.T, store *storage.Store, id, agentID string, at time.Time, expiresAfter time.Duration) {
	t.Helper()
	createHTTPTestJobWithProbe(t, store, id, agentID, protocol.ProbeTypeTCPConnect,
		protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}}, at, expiresAfter)
}

func createHTTPTestJobWithProbe(t *testing.T, store *storage.Store, id, agentID string, probeType protocol.ProbeType, config protocol.ProbeConfig, at time.Time, expiresAfter time.Duration) {
	t.Helper()
	err := store.CreateOneShotJob(context.Background(), storage.CreateOneShotJobParams{
		ID: id, AgentID: agentID, ProbeType: probeType, Config: config,
		TimeoutMS: 5000, CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: at.Add(expiresAfter).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func jobHTTPResponse(t *testing.T, app *App, method, path, token, contentType string, body any) *httptest.ResponseRecorder {
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

func validClaimRequest(epoch uint64, session string) protocol.ClaimRequest {
	return protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: epoch, SessionID: session,
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}
}

func claimHTTPJob(t *testing.T, app *App, token string, request protocol.ClaimRequest) (*protocol.Job, *httptest.ResponseRecorder) {
	t.Helper()
	response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", request)
	if response.Code != http.StatusOK {
		return nil, response
	}
	var job protocol.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return &job, response
}

func validHTTPJobResult(job *protocol.Job) protocol.JobResult {
	return protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: job.LeaseToken, Attempt: job.Attempt,
		AgentEpoch: 1, SessionID: "session-1", StartedAt: 1_000, FinishedAt: 1_025,
		DurationMS: 25, Success: true, ResolvedIP: "192.0.2.1",
		Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 20}},
	}
}

func jobErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response %q: %v", response.Body.String(), err)
	}
	return envelope.Error.Code
}

func newFileJobApp(t *testing.T, path string) (*App, *storage.Store, string, string) {
	t.Helper()
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := auth.NewID()
	token, hash, _ := auth.NewToken()
	if err := store.AddAgent(context.Background(), agentID, "test", hash, time.Unix(100, 0)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	app, err := NewApp(store, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return app, store, agentID, token
}

func updateJobFixture(t *testing.T, path, statement string, arguments ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

func TestClaimJobHTTP(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJob(t, store, "job-1", agentID, now, 10*time.Minute)

	job, response := claimHTTPJob(t, app, token, validClaimRequest(1, "session-1"))
	if response.Code != http.StatusOK || job == nil {
		t.Fatalf("claim status=%d body=%s", response.Code, response.Body.String())
	}
	if job.JobID != "job-1" || job.Attempt != 1 || job.LeaseToken == "" || job.LeaseExpiresAt != now.Add(jobLeaseDuration).UnixMilli() {
		t.Fatalf("claimed job=%+v", job)
	}
	record, err := store.GetProbeJob(context.Background(), job.JobID)
	if err != nil || record.Status != storage.JobStatusLeased || record.LeaseToken != job.LeaseToken {
		t.Fatalf("stored job=%+v err=%v", record, err)
	}

	replayed, replay := claimHTTPJob(t, app, token, validClaimRequest(1, "session-1"))
	if replay.Code != http.StatusOK || replayed == nil || replayed.Attempt != job.Attempt || replayed.LeaseToken != job.LeaseToken {
		t.Fatalf("replayed=%+v status=%d body=%s", replayed, replay.Code, replay.Body.String())
	}

	blocked := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(2, "other-session"))
	if blocked.Code != http.StatusNoContent || blocked.Body.Len() != 0 || blocked.Header().Get("Retry-After") != "10" {
		t.Fatalf("different session status=%d retry=%q body=%q", blocked.Code, blocked.Header().Get("Retry-After"), blocked.Body.String())
	}
}

func TestClaimJobHTTPNoJob(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json; charset=utf-8", validClaimRequest(1, "session"))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Retry-After") != "10" {
		t.Fatalf("status=%d retry=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}

func TestClaimJobHTTPCapabilityFiltering(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJobWithProbe(t, store, "icmp-job", agentID, protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "example.com", Count: 1}}, now, time.Minute)
	createHTTPTestJob(t, store, "tcp-job", agentID, now, time.Minute)

	job, response := claimHTTPJob(t, app, token, validClaimRequest(1, "session"))
	if response.Code != http.StatusOK || job == nil || job.JobID != "tcp-job" || job.ProbeType != protocol.ProbeTypeTCPConnect {
		t.Fatalf("job=%+v status=%d body=%s", job, response.Code, response.Body.String())
	}
	incompatible, err := store.GetProbeJob(context.Background(), "icmp-job")
	if err != nil || incompatible.Status != storage.JobStatusQueued || incompatible.Attempt != 0 {
		t.Fatalf("incompatible job mutated: %+v err=%v", incompatible, err)
	}
}

func TestClaimJobHTTPAuthenticationAndIsolation(t *testing.T) {
	t.Run("missing and invalid auth", func(t *testing.T) {
		app, store, _, _ := testApp(t)
		defer store.Close()
		for _, token := range []string{"", "wrong"} {
			response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(1, "session"))
			if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" {
				t.Fatalf("token=%q status=%d body=%s", token, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), token) && token != "" {
				t.Fatalf("token leaked in response: %s", response.Body.String())
			}
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/jobs/claim", bytes.NewReader([]byte(`{}`)))
		request.Header.Set("Authorization", "Basic wrong")
		request.Header.Set("Content-Type", "application/json")
		malformed := httptest.NewRecorder()
		app.Handler().ServeHTTP(malformed, request)
		if malformed.Code != http.StatusUnauthorized || jobErrorCode(t, malformed) != "unauthorized" {
			t.Fatalf("malformed auth status=%d body=%s", malformed.Code, malformed.Body.String())
		}
	})

	t.Run("authentication storage failure", func(t *testing.T) {
		app, store, _, token := testApp(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(1, "session"))
		if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), token) || strings.Contains(strings.ToLower(response.Body.String()), "sql") {
			t.Fatalf("internal authentication detail leaked: %s", response.Body.String())
		}
	})

	t.Run("revoked agent", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		if changed, err := store.RevokeAgent(context.Background(), agentID, now); err != nil || !changed {
			t.Fatal(err)
		}
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(1, "session"))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		record, _ := store.GetProbeJob(context.Background(), "job")
		if record.Status != storage.JobStatusQueued || record.Attempt != 0 {
			t.Fatalf("revoked claim mutated job: %+v", record)
		}
	})

	t.Run("agent cannot select another agent", func(t *testing.T) {
		app, store, _, tokenA := testApp(t)
		defer store.Close()
		agentB, _ := auth.NewID()
		_, hashB, _ := auth.NewToken()
		if err := store.AddAgent(context.Background(), agentB, "b", hashB, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "b-job", agentB, now, time.Minute)
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", tokenA, "application/json", validClaimRequest(1, "session"))
		if response.Code != http.StatusNoContent {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		record, _ := store.GetProbeJob(context.Background(), "b-job")
		if record.Status != storage.JobStatusQueued || record.Attempt != 0 {
			t.Fatalf("other agent job mutated: %+v", record)
		}
	})
}

func TestClaimJobHTTPValidationAndLimits(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{"malformed", []byte(`{"protocol_version":`), "application/json", http.StatusBadRequest, "invalid_request"},
		{"unknown field", []byte(`{"protocol_version":1,"agent_epoch":1,"session_id":"s","supported_probe_types":["tcp_connect"],"agent_id":"other"}`), "application/json", http.StatusBadRequest, "invalid_request"},
		{"unsupported protocol", []byte(`{"protocol_version":2,"agent_epoch":1,"session_id":"s","supported_probe_types":["tcp_connect"]}`), "application/json", http.StatusBadRequest, "invalid_request"},
		{"unknown capability", []byte(`{"protocol_version":1,"agent_epoch":1,"session_id":"s","supported_probe_types":["shell"]}`), "application/json", http.StatusBadRequest, "invalid_request"},
		{"trailing JSON", []byte(`{"protocol_version":1,"agent_epoch":1,"session_id":"s","supported_probe_types":["tcp_connect"]} {}`), "application/json", http.StatusBadRequest, "invalid_request"},
		{"oversized", bytes.Repeat([]byte(" "), maxClaimBodyBytes+1), "application/json", http.StatusRequestEntityTooLarge, "request_too_large"},
		{"wrong content type", []byte(`{}`), "text/plain", http.StatusUnsupportedMediaType, "invalid_content_type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, test.contentType, test.body)
			if response.Code != test.wantStatus || jobErrorCode(t, response) != test.wantCode {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			record, _ := store.GetProbeJob(context.Background(), "job")
			if record.Status != storage.JobStatusQueued || record.Attempt != 0 {
				t.Fatalf("invalid request mutated job: %+v", record)
			}
		})
	}
	response := jobHTTPResponse(t, app, http.MethodGet, "/api/v1/agent/jobs/claim", token, "application/json", nil)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("wrong method status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestClaimJobHTTPAttemptExhaustionAndInternalError(t *testing.T) {
	t.Run("attempt exhaustion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "attempt.db")
		app, store, agentID, token := newFileJobApp(t, path)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		updateJobFixture(t, path, `UPDATE probe_jobs SET attempt=? WHERE id='job'`, int64(math.MaxInt64))
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(1, "session"))
		if response.Code != http.StatusConflict || jobErrorCode(t, response) != "attempt_exhausted" || strings.Contains(strings.ToLower(response.Body.String()), "sqlite") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		record, _ := store.GetProbeJob(context.Background(), "job")
		if record.Status != storage.JobStatusExpired || record.Attempt != math.MaxInt64 || record.LeaseToken != "" {
			t.Fatalf("exhausted record=%+v", record)
		}
	})

	t.Run("raw storage error is hidden", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		app, store, agentID, token := newFileJobApp(t, path)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		updateJobFixture(t, path, `UPDATE probe_jobs SET config_json='not-json' WHERE id='job'`)
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(1, "session"))
		if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		for _, secret := range []string{"not-json", "decode stored", "sqlite", token} {
			if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(secret)) {
				t.Fatalf("internal detail leaked: %s", response.Body.String())
			}
		}
		record, err := store.GetProbeJob(context.Background(), "job")
		if err == nil || record.Status != "" {
			t.Fatalf("corrupt fixture unexpectedly readable: %+v err=%v", record, err)
		}
	})
}

func TestSubmitJobResultHTTPIdempotency(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
	job, response := claimHTTPJob(t, app, token, validClaimRequest(1, "session-1"))
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	result := validHTTPJobResult(job)
	now = now.Add(time.Second)
	first := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", result)
	assertResultACK(t, first, false)
	record, err := store.GetProbeJob(context.Background(), "job")
	if err != nil || record.Status != storage.JobStatusFinished {
		t.Fatalf("job=%+v err=%v", record, err)
	}
	if _, err := store.GetProbeResult(context.Background(), "job"); err != nil {
		t.Fatal(err)
	}

	retry := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", result)
	assertResultACK(t, retry, true)
	conflict := result
	measurement := *result.Result.TCPConnect
	measurement.ConnectMS++
	conflict.Result.TCPConnect = &measurement
	conflicting := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", conflict)
	if conflicting.Code != http.StatusConflict || jobErrorCode(t, conflicting) != "result_conflict" {
		t.Fatalf("status=%d body=%s", conflicting.Code, conflicting.Body.String())
	}
}

func TestSubmitJobResultHTTPConfigMismatch(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJobWithProbe(t, store, "icmp-job", agentID, protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "example.com", Count: 4}}, now, time.Minute)
	claim := validClaimRequest(1, "session-1")
	claim.SupportedProbeTypes = []protocol.ProbeType{protocol.ProbeTypeICMPPing}
	job, claimed := claimHTTPJob(t, app, token, claim)
	if claimed.Code != http.StatusOK || job == nil {
		t.Fatalf("claim status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	result := protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: job.LeaseToken, Attempt: job.Attempt,
		AgentEpoch: 1, SessionID: "session-1", StartedAt: 1_000, FinishedAt: 1_025,
		DurationMS: 25, Success: true, ResolvedIP: "192.0.2.1",
		Result: protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{
			Sent: 3, Received: 3, PacketLossPercent: 0, LatencyMinMS: 1, LatencyAvgMS: 2, LatencyMaxMS: 3,
		}},
	}
	wrongToken := result
	wrongToken.LeaseToken = "wrong-lease-token"
	fenced := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/icmp-job/result", token, "application/json", wrongToken)
	if fenced.Code != http.StatusConflict || jobErrorCode(t, fenced) != "lease_lost" {
		t.Fatalf("wrong token lost fencing priority: status=%d body=%s", fenced.Code, fenced.Body.String())
	}
	response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/icmp-job/result", token, "application/json", result)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	record, err := store.GetProbeJob(context.Background(), "icmp-job")
	if err != nil || record.Status != storage.JobStatusLeased || record.Attempt != job.Attempt || record.LeaseToken != job.LeaseToken {
		t.Fatalf("job mutated after config mismatch: %+v err=%v", record, err)
	}
	if _, err := store.GetProbeResult(context.Background(), "icmp-job"); !errors.Is(err, storage.ErrJobNotFound) {
		t.Fatalf("config-mismatched result persisted: %v", err)
	}
}

func TestSubmitJobResultHTTPInternalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result-error.db")
	app, store, agentID, token := newFileJobApp(t, path)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
	job, claimed := claimHTTPJob(t, app, token, validClaimRequest(1, "session-1"))
	if claimed.Code != http.StatusOK || job == nil {
		t.Fatalf("claim status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	updateJobFixture(t, path, `CREATE TRIGGER fail_result_insert BEFORE INSERT ON probe_results
		BEGIN SELECT RAISE(ABORT,'injected result failure'); END`)
	response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(job))
	if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"injected result failure", "sqlite", token, job.LeaseToken} {
		if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(secret)) {
			t.Fatalf("internal result detail leaked: %s", response.Body.String())
		}
	}
	record, err := store.GetProbeJob(context.Background(), "job")
	if err != nil || record.Status != storage.JobStatusLeased || record.Attempt != job.Attempt || record.LeaseToken != job.LeaseToken {
		t.Fatalf("internal error mutated job: %+v err=%v", record, err)
	}
	if _, err := store.GetProbeResult(context.Background(), "job"); !errors.Is(err, storage.ErrJobNotFound) {
		t.Fatalf("failed result persisted: %v", err)
	}
}

func assertResultACK(t *testing.T, response *httptest.ResponseRecorder, duplicate bool) {
	t.Helper()
	var ack struct {
		Accepted  bool   `json:"accepted"`
		Duplicate bool   `json:"duplicate"`
		JobStatus string `json:"job_status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !ack.Accepted || ack.Duplicate != duplicate || ack.JobStatus != "finished" {
		t.Fatalf("status=%d ack=%+v body=%s", response.Code, ack, response.Body.String())
	}
}

func TestSubmitJobResultHTTPFencingAndExpiry(t *testing.T) {
	t.Run("stale attempt and wrong token", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, 2*time.Minute)
		first, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "first"))
		now = now.Add(jobLeaseDuration + time.Second)
		second, response := claimHTTPJob(t, app, token, validClaimRequest(2, "second"))
		if response.Code != http.StatusOK || second == nil || second.Attempt != 2 {
			t.Fatalf("second=%+v status=%d body=%s", second, response.Code, response.Body.String())
		}
		stale := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(first))
		if stale.Code != http.StatusConflict || jobErrorCode(t, stale) != "lease_lost" {
			t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
		}
		wrong := validHTTPJobResult(second)
		wrong.LeaseToken = "wrong-token"
		wrongToken := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", wrong)
		if wrongToken.Code != http.StatusConflict || jobErrorCode(t, wrongToken) != "lease_lost" || strings.Contains(wrongToken.Body.String(), wrong.LeaseToken) {
			t.Fatalf("wrong token status=%d body=%s", wrongToken.Code, wrongToken.Body.String())
		}
	})

	t.Run("job expiry does not shorten lease", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, 10*time.Second)
		job, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "session"))
		now = now.Add(20 * time.Second)
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(job))
		assertResultACK(t, response, false)
	})

	t.Run("exact lease boundary is lost", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		job, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "session"))
		now = now.Add(jobLeaseDuration)
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(job))
		if response.Code != http.StatusConflict || jobErrorCode(t, response) != "lease_lost" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("expired job", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, 10*time.Second)
		job, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "first"))
		now = now.Add(jobLeaseDuration + time.Second)
		cleanup := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", token, "application/json", validClaimRequest(2, "second"))
		if cleanup.Code != http.StatusNoContent {
			t.Fatalf("cleanup status=%d body=%s", cleanup.Code, cleanup.Body.String())
		}
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(job))
		if response.Code != http.StatusGone || jobErrorCode(t, response) != "job_expired" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestSubmitJobResultHTTPAuthenticationOwnershipAndValidation(t *testing.T) {
	t.Run("missing and invalid auth", func(t *testing.T) {
		app, store, _, _ := testApp(t)
		defer store.Close()
		for _, token := range []string{"", "wrong"} {
			response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/missing/result", token, "application/json", []byte(`{}`))
			if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" {
				t.Fatalf("token=%q status=%d body=%s", token, response.Code, response.Body.String())
			}
			if token != "" && strings.Contains(response.Body.String(), token) {
				t.Fatalf("token leaked in response: %s", response.Body.String())
			}
		}
	})

	t.Run("ownership is not disclosed", func(t *testing.T) {
		app, store, _, tokenA := testApp(t)
		defer store.Close()
		agentB, _ := auth.NewID()
		tokenB, hashB, _ := auth.NewToken()
		if err := store.AddAgent(context.Background(), agentB, "b", hashB, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "b-job", agentB, now, time.Minute)
		job, _ := claimHTTPJob(t, app, tokenB, validClaimRequest(1, "session"))
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/b-job/result", tokenA, "application/json", validHTTPJobResult(job))
		if response.Code != http.StatusNotFound || jobErrorCode(t, response) != "job_not_found" || strings.Contains(response.Body.String(), agentB) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if _, err := store.GetProbeResult(context.Background(), "b-job"); !errors.Is(err, storage.ErrJobNotFound) {
			t.Fatalf("cross-agent result persisted: %v", err)
		}
	})

	t.Run("revoked agent", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		job, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "session"))
		if changed, err := store.RevokeAgent(context.Background(), agentID, now); err != nil || !changed {
			t.Fatal(err)
		}
		response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, "application/json", validHTTPJobResult(job))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if _, err := store.GetProbeResult(context.Background(), "job"); !errors.Is(err, storage.ErrJobNotFound) {
			t.Fatalf("revoked result persisted: %v", err)
		}
	})

	t.Run("invalid requests do not mutate", func(t *testing.T) {
		app, store, agentID, token := testApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		createHTTPTestJob(t, store, "job", agentID, now, time.Minute)
		job, _ := claimHTTPJob(t, app, token, validClaimRequest(1, "session"))
		valid, _ := json.Marshal(validHTTPJobResult(job))
		var nullResultWire map[string]any
		if err := json.Unmarshal(valid, &nullResultWire); err != nil {
			t.Fatal(err)
		}
		nullResultWire["result"] = nil
		nullResult, _ := json.Marshal(nullResultWire)
		var mismatchedResultWire map[string]any
		if err := json.Unmarshal(valid, &mismatchedResultWire); err != nil {
			t.Fatal(err)
		}
		mismatchedResultWire["result"] = map[string]any{"status_code": 200}
		mismatchedPayload, _ := json.Marshal(mismatchedResultWire)
		withUnknown := bytes.Replace(valid, []byte(`"result":`), []byte(`"agent_id":"other","result":`), 1)
		unsupportedProtocol := bytes.Replace(valid, []byte(`"protocol_version":1`), []byte(`"protocol_version":2`), 1)
		trailingJSON := append(append([]byte(nil), valid...), []byte(` {}`)...)
		tests := []struct {
			name        string
			body        []byte
			contentType string
			wantStatus  int
			wantCode    string
		}{
			{"malformed", []byte(`{"protocol_version":`), "application/json", http.StatusBadRequest, "invalid_request"},
			{"unknown field", withUnknown, "application/json", http.StatusBadRequest, "invalid_request"},
			{"unsupported protocol", unsupportedProtocol, "application/json", http.StatusBadRequest, "invalid_request"},
			{"trailing JSON", trailingJSON, "application/json", http.StatusBadRequest, "invalid_request"},
			{"null result", nullResult, "application/json", http.StatusBadRequest, "invalid_request"},
			{"probe type mismatch", mismatchedPayload, "application/json", http.StatusBadRequest, "invalid_request"},
			{"invalid typed payload", bytes.Replace(valid, []byte(`"connect_ms":20`), []byte(`"connect_ms":-1`), 1), "application/json", http.StatusBadRequest, "invalid_request"},
			{"oversized", bytes.Repeat([]byte(" "), maxResultBodyBytes+1), "application/json", http.StatusRequestEntityTooLarge, "request_too_large"},
			{"wrong content type", valid, "text/plain", http.StatusUnsupportedMediaType, "invalid_content_type"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/job/result", token, test.contentType, test.body)
				if response.Code != test.wantStatus || jobErrorCode(t, response) != test.wantCode {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if _, err := store.GetProbeResult(context.Background(), "job"); !errors.Is(err, storage.ErrJobNotFound) {
					t.Fatalf("invalid result persisted: %v", err)
				}
			})
		}
	})
}

func TestJobResultHTTPRoutingAndMethods(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	for _, path := range []string{
		"/api/v1/agent/jobs//result",
		"/api/v1/agent/jobs/abc/",
		"/api/v1/agent/jobs/abc/result/extra",
		"/api/v1/agent/jobs/abc%2Fdef/result",
		"/api/v1/agent/jobs/abc%5Cdef/result",
		"/api/v1/agent/jobs/%20/result",
	} {
		response := jobHTTPResponse(t, app, http.MethodPost, path, token, "application/json", []byte(`{}`))
		if response.Code != http.StatusNotFound {
			t.Fatalf("path=%q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	response := jobHTTPResponse(t, app, http.MethodGet, "/api/v1/agent/jobs/abc/result", token, "application/json", nil)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("status=%d allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
	notFound := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/missing/result", token, "application/json", []byte(`{}`))
	if notFound.Code != http.StatusNotFound || jobErrorCode(t, notFound) != "job_not_found" {
		t.Fatalf("status=%d body=%s", notFound.Code, notFound.Body.String())
	}
}
