package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const firstControlScheduleID = "abcdef0123456789abcdef0123456789"

type controlScheduleTestRequest struct {
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	ProbeType       protocol.ProbeType `json:"probe_type"`
	Config          any                `json:"config"`
	TimeoutMS       int                `json:"timeout_ms"`
	IntervalSeconds int                `json:"interval_seconds"`
	Enabled         bool               `json:"enabled"`
}

func validControlScheduleRequest(agentID string, probeType protocol.ProbeType) controlScheduleTestRequest {
	request := controlScheduleTestRequest{
		AgentID: agentID, Name: "homepage", ProbeType: probeType, TimeoutMS: 5000, IntervalSeconds: 60, Enabled: true,
	}
	switch probeType {
	case protocol.ProbeTypeHTTP:
		request.Config = protocol.HTTPConfig{URL: "https://example.com/health", Method: "GET"}
	case protocol.ProbeTypeTCPConnect:
		request.Config = protocol.TCPConnectConfig{Host: "127.0.0.1", Port: 443}
	case protocol.ProbeTypeICMPPing:
		request.Config = protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}
	}
	return request
}

func TestControlScheduleAuthenticationAndAmbiguousPaths(t *testing.T) {
	ambiguousPaths := []string{
		"/api/v1/control/schedules/../x",
		"/api/v1/control/schedules/%2e%2e/x",
		"/api/v1/control/schedules/./" + firstControlScheduleID,
		"/api/v1/control/schedules/%2e/" + firstControlScheduleID,
		"/api/v1/control/schedules//" + firstControlScheduleID,
		controlSchedulePathPrefix + "0123456789abcdef%2f123456789abcdef",
		controlSchedulePathPrefix + "0123456789abcdef%5c123456789abcdef",
	}
	disabled, disabledStore, agentID, _ := testApp(t)
	defer disabledStore.Close()
	path := controlSchedulePathPrefix + firstControlScheduleID
	request := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
	if response := controlHTTPResponse(t, disabled, http.MethodPut, path, "", "application/json", request); response.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d body=%s", response.Code, response.Body.String())
	}
	for _, ambiguous := range ambiguousPaths {
		response := controlHTTPResponse(t, disabled, http.MethodGet, ambiguous, "", "", nil)
		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
			t.Fatalf("disabled ambiguous=%q status=%d location=%q body=%s", ambiguous, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}

	app, store, agentID, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	request = validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
	for _, token := range []string{"", "wrong", agentToken} {
		response := controlHTTPResponse(t, app, http.MethodPut, path, token, "application/json", request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" || jobErrorCode(t, response) != "unauthorized" {
			t.Fatalf("token=%q status=%d headers=%v body=%s", token, response.Code, response.Header(), response.Body.String())
		}
	}
	for _, ambiguous := range ambiguousPaths {
		for _, token := range []string{"", "wrong", agentToken} {
			response := controlHTTPResponse(t, app, http.MethodGet, ambiguous, token, "", nil)
			if response.Code != http.StatusUnauthorized || response.Header().Get("Location") != "" {
				t.Fatalf("ambiguous=%q token=%q status=%d location=%q", ambiguous, token, response.Code, response.Header().Get("Location"))
			}
		}
		response := controlHTTPResponse(t, app, http.MethodGet, ambiguous, controlToken, "", nil)
		if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" || response.Header().Get("Location") != "" {
			t.Fatalf("ambiguous=%q status=%d location=%q body=%s", ambiguous, response.Code, response.Header().Get("Location"), response.Body.String())
		}
	}
}

func TestControlSchedulePutGetUpdateDelete(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	path := controlSchedulePathPrefix + firstControlScheduleID
	request := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
	created := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json; charset=utf-8", request)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != path {
		t.Fatalf("create status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	var initial controlScheduleView
	if err := json.Unmarshal(created.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.ScheduleID != firstControlScheduleID || initial.AgentID != agentID || initial.CreatedAt != now.UnixMilli() || initial.UpdatedAt != initial.CreatedAt || initial.NextRunAt != initial.CreatedAt {
		t.Fatalf("initial=%+v", initial)
	}
	if job, err := store.ClaimJob(context.Background(), agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: "no-scheduler", SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeHTTP},
	}, now, time.Minute); err != nil || job != nil {
		t.Fatalf("Phase 5B materialized job=%+v err=%v", job, err)
	}

	now = now.Add(time.Minute)
	replay := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayed controlScheduleView
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil || !reflect.DeepEqual(replayed, initial) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}

	now = now.Add(time.Minute)
	request.Name = "renamed"
	renamedResponse := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	var renamed controlScheduleView
	if renamedResponse.Code != http.StatusOK || json.Unmarshal(renamedResponse.Body.Bytes(), &renamed) != nil || renamed.UpdatedAt != now.UnixMilli() || renamed.NextRunAt != initial.NextRunAt {
		t.Fatalf("rename status=%d view=%+v body=%s", renamedResponse.Code, renamed, renamedResponse.Body.String())
	}

	now = now.Add(time.Minute)
	request.IntervalSeconds = 120
	updatedResponse := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
	var updated controlScheduleView
	if updatedResponse.Code != http.StatusOK || json.Unmarshal(updatedResponse.Body.Bytes(), &updated) != nil || updated.NextRunAt != now.UnixMilli() {
		t.Fatalf("update status=%d view=%+v body=%s", updatedResponse.Code, updated, updatedResponse.Body.String())
	}
	get := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), updatedResponse.Body.Bytes()) {
		t.Fatalf("get status=%d body=%s update=%s", get.Code, get.Body.String(), updatedResponse.Body.String())
	}
	for _, forbidden := range []string{"control_token", "token_hash", "lease_token", "result_hash", controlToken} {
		if strings.Contains(get.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, get.Body.String())
		}
	}
	method := controlHTTPResponse(t, app, http.MethodPost, path, controlToken, "application/json", request)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, PUT, DELETE" || jobErrorCode(t, method) != "method_not_allowed" {
		t.Fatalf("method status=%d allow=%q body=%s", method.Code, method.Header().Get("Allow"), method.Body.String())
	}
	deleted := controlHTTPResponse(t, app, http.MethodDelete, path, controlToken, "", nil)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	missing := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	if missing.Code != http.StatusNotFound || jobErrorCode(t, missing) != "schedule_not_found" {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	deletedAgain := controlHTTPResponse(t, app, http.MethodDelete, path, controlToken, "", nil)
	if deletedAgain.Code != http.StatusNotFound || jobErrorCode(t, deletedAgain) != "schedule_not_found" {
		t.Fatalf("delete missing status=%d body=%s", deletedAgain.Code, deletedAgain.Body.String())
	}
	collectionUnauthorized := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/schedules", "", "", nil)
	if collectionUnauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("collection unauthorized status=%d", collectionUnauthorized.Code)
	}
	collection := controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/schedules", controlToken, "", nil)
	if collection.Code != http.StatusOK || collection.Body.String() != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("collection status=%d body=%s", collection.Code, collection.Body.String())
	}
}

func TestControlScheduleValidationAndProbeTypes(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	path := controlSchedulePathPrefix + firstControlScheduleID
	valid := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
	encoded, _ := json.Marshal(valid)
	tests := []struct {
		name        string
		path        string
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{name: "content type", path: path, contentType: "text/plain", body: encoded, status: http.StatusUnsupportedMediaType, code: "invalid_content_type"},
		{name: "unknown", path: path, contentType: "application/json", body: append(encoded[:len(encoded)-1], []byte(`,"unknown":1}`)...), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "multiple", path: path, contentType: "application/json", body: append(encoded, []byte(` {}`)...), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "null", path: path, contentType: "application/json", body: []byte(`null`), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "oversized", path: path, contentType: "application/json", body: bytes.Repeat([]byte(" "), maxControlJobBodyBytes+1), status: http.StatusRequestEntityTooLarge, code: "request_too_large"},
		{name: "short ID", path: controlSchedulePathPrefix + "abc", contentType: "application/json", body: encoded, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "uppercase ID", path: controlSchedulePathPrefix + strings.ToUpper(firstControlScheduleID), contentType: "application/json", body: encoded, status: http.StatusBadRequest, code: "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := controlHTTPResponse(t, app, http.MethodPut, test.path, controlToken, test.contentType, test.body)
			if response.Code != test.status || jobErrorCode(t, response) != test.code {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	invalidRequests := []controlScheduleTestRequest{
		func() controlScheduleTestRequest { value := valid; value.AgentID = ""; return value }(),
		func() controlScheduleTestRequest { value := valid; value.Name = " name"; return value }(),
		func() controlScheduleTestRequest { value := valid; value.Name = "bad\nname"; return value }(),
		func() controlScheduleTestRequest { value := valid; value.Name = strings.Repeat("x", 129); return value }(),
		func() controlScheduleTestRequest { value := valid; value.TimeoutMS = 99; return value }(),
		func() controlScheduleTestRequest { value := valid; value.TimeoutMS = 30001; return value }(),
		func() controlScheduleTestRequest { value := valid; value.IntervalSeconds = 29; return value }(),
		func() controlScheduleTestRequest { value := valid; value.IntervalSeconds = 604801; return value }(),
	}
	for index, request := range invalidRequests {
		response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
		if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
			t.Fatalf("invalid index=%d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	missingEnabled := fmt.Sprintf(`{"agent_id":%q,"name":"x","probe_type":"http","config":{"url":"https://example.com","method":"GET"},"timeout_ms":5000,"interval_seconds":60}`, agentID)
	nullEnabled := missingEnabled[:len(missingEnabled)-1] + `,"enabled":null}`
	for _, body := range []string{missingEnabled, nullEnabled} {
		response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", []byte(body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing enabled status=%d body=%s", response.Code, response.Body.String())
		}
	}

	for index, probeType := range []protocol.ProbeType{protocol.ProbeTypeHTTP, protocol.ProbeTypeTCPConnect, protocol.ProbeTypeICMPPing} {
		id := fmt.Sprintf("%032x", index+1)
		response := controlHTTPResponse(t, app, http.MethodPut, controlSchedulePathPrefix+id, controlToken, "application/json", validControlScheduleRequest(agentID, probeType))
		if response.Code != http.StatusCreated {
			t.Fatalf("type=%s status=%d body=%s", probeType, response.Code, response.Body.String())
		}
	}
}

func TestControlScheduleConcurrentIdenticalPut(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	request := validControlScheduleRequest(agentID, protocol.ProbeTypeTCPConnect)
	path := controlSchedulePathPrefix + firstControlScheduleID
	const workers = 20
	statuses := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request).Code
		}()
	}
	wg.Wait()
	close(statuses)
	created, ok := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("unexpected status=%d", status)
		}
	}
	if created != 1 || ok != workers-1 {
		t.Fatalf("created=%d ok=%d", created, ok)
	}
}

func TestControlScheduleConflictRevocationAndLimit(t *testing.T) {
	t.Run("conflict and revocation", func(t *testing.T) {
		app, store, agentID, _, controlToken := newControlTestApp(t)
		defer store.Close()
		now := time.Unix(1000, 0)
		app.now = func() time.Time { return now }
		path := controlSchedulePathPrefix + firstControlScheduleID
		request := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
		if response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request); response.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
		conflict := request
		conflict.AgentID = "ffffffffffffffffffffffffffffffff"
		response := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", conflict)
		if response.Code != http.StatusConflict || jobErrorCode(t, response) != "schedule_conflict" {
			t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
		}
		missing := controlHTTPResponse(t, app, http.MethodPut, controlSchedulePathPrefix+"ffffffffffffffffffffffffffffffff", controlToken, "application/json", conflict)
		if missing.Code != http.StatusNotFound || jobErrorCode(t, missing) != "agent_not_found" {
			t.Fatalf("missing agent status=%d body=%s", missing.Code, missing.Body.String())
		}
		now = now.Add(time.Minute)
		if revoked, err := store.RevokeAgent(context.Background(), agentID, now); err != nil || !revoked {
			t.Fatalf("revoke=%t err=%v", revoked, err)
		}
		replay := controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", request)
		if replay.Code != http.StatusOK {
			t.Fatalf("revoked replay status=%d body=%s", replay.Code, replay.Body.String())
		}
		changed := request
		changed.Name = "changed while enabled"
		response = controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", changed)
		if response.Code != http.StatusNotFound || jobErrorCode(t, response) != "agent_not_found" {
			t.Fatalf("revoked enabled update status=%d body=%s", response.Code, response.Body.String())
		}
		changed.Enabled = false
		response = controlHTTPResponse(t, app, http.MethodPut, path, controlToken, "application/json", changed)
		if response.Code != http.StatusOK {
			t.Fatalf("revoked disable status=%d body=%s", response.Code, response.Body.String())
		}
		deleted := controlHTTPResponse(t, app, http.MethodDelete, path, controlToken, "", nil)
		if deleted.Code != http.StatusNoContent {
			t.Fatalf("revoked delete status=%d body=%s", deleted.Code, deleted.Body.String())
		}
	})

	t.Run("limit", func(t *testing.T) {
		app, store, agentID, _, controlToken := newControlTestApp(t)
		defer store.Close()
		request := validControlScheduleRequest(agentID, protocol.ProbeTypeTCPConnect)
		for index := range storage.MaxSchedulesPerAgent {
			id := fmt.Sprintf("%032x", index+1)
			response := controlHTTPResponse(t, app, http.MethodPut, controlSchedulePathPrefix+id, controlToken, "application/json", request)
			if response.Code != http.StatusCreated {
				t.Fatalf("fill index=%d status=%d body=%s", index, response.Code, response.Body.String())
			}
		}
		overflow := controlHTTPResponse(t, app, http.MethodPut, controlSchedulePathPrefix+"ffffffffffffffffffffffffffffffff", controlToken, "application/json", request)
		if overflow.Code != http.StatusTooManyRequests || jobErrorCode(t, overflow) != "schedule_limit" {
			t.Fatalf("overflow status=%d body=%s", overflow.Code, overflow.Body.String())
		}
		replay := controlHTTPResponse(t, app, http.MethodPut, controlSchedulePathPrefix+fmt.Sprintf("%032x", 1), controlToken, "application/json", request)
		if replay.Code != http.StatusOK {
			t.Fatalf("replay at limit status=%d body=%s", replay.Code, replay.Body.String())
		}
	})
}

func TestControlScheduleCorruptDataIsInternalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule-corrupt.db")
	app, store, agentID, controlToken := newFileControlTestApp(t, path)
	defer store.Close()
	request := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
	endpoint := controlSchedulePathPrefix + firstControlScheduleID
	if response := controlHTTPResponse(t, app, http.MethodPut, endpoint, controlToken, "application/json", request); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	updateJobFixture(t, path, `UPDATE probe_schedules SET config_json='{}' WHERE id=?`, firstControlScheduleID)
	response := controlHTTPResponse(t, app, http.MethodGet, endpoint, controlToken, "", nil)
	if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
		t.Fatalf("corrupt GET status=%d body=%s", response.Code, response.Body.String())
	}
	response = controlHTTPResponse(t, app, http.MethodPut, endpoint, controlToken, "application/json", request)
	if response.Code != http.StatusInternalServerError || jobErrorCode(t, response) != "internal_error" {
		t.Fatalf("corrupt PUT status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlScheduleRequestExactBoundaries(t *testing.T) {
	agentID, _ := auth.NewID()
	for _, test := range []struct {
		name     string
		request  controlScheduleTestRequest
		wantFail bool
	}{
		{name: "minimum", request: func() controlScheduleTestRequest {
			value := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
			value.Name = "x"
			value.TimeoutMS = 100
			value.IntervalSeconds = 30
			return value
		}()},
		{name: "maximum", request: func() controlScheduleTestRequest {
			value := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
			value.Name = strings.Repeat("x", 128)
			value.TimeoutMS = 30000
			value.IntervalSeconds = 604800
			return value
		}()},
		{name: "name over", request: func() controlScheduleTestRequest {
			value := validControlScheduleRequest(agentID, protocol.ProbeTypeHTTP)
			value.Name = strings.Repeat("x", 129)
			return value
		}(), wantFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.request)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodeControlPutScheduleRequest(body)
			if (err != nil) != test.wantFail {
				t.Fatalf("error=%v wantFail=%t", err, test.wantFail)
			}
		})
	}
}
