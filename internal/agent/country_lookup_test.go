package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestCountryCodeLookupWorkerClaimsOnceAndSubmitsOnlyValidatedResult(t *testing.T) {
	var claimCount, lookupCount, resultCount atomic.Int32
	var submitted protocol.AgentCountryCodeLookupResultRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected authorization header %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v1/agent/country-code-lookups/claim":
			if claimCount.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(protocol.AgentCountryCodeLookupDelivery{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, OperationID: "abcdefabcdefabcdefabcdefabcdefab"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/country-code-lookups/abcdefabcdefabcdefabcdefabcdefab/result":
			defer request.Body.Close()
			if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
				t.Errorf("decode result: %v", err)
			}
			resultCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.AgentCountryCodeLookupResultResponse{Accepted: true, Status: "succeeded"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	runner, err := NewWithExecutor(Config{ServerURL: server.URL, AgentID: "agent", Token: "test-token", Interval: time.Second,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"), UpdaterSocket: "unused"}, nil, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.managementAPISupported.Store(true)
	runner.countryCodeLookup = func(context.Context) (string, error) {
		lookupCount.Add(1)
		return "JP", nil
	}
	client := countryCodeLookupHTTPClient{base: jobHTTPClient{baseURL: server.URL, token: "test-token", client: runner.client}}
	if !runner.runCountryCodeLookupCycle(context.Background(), client) {
		t.Fatal("country-code lookup delivery was not handled")
	}
	if runner.runCountryCodeLookupCycle(context.Background(), client) {
		t.Fatal("completed one-shot lookup was delivered again")
	}
	if claimCount.Load() != 2 || lookupCount.Load() != 1 || resultCount.Load() != 1 {
		t.Fatalf("claim=%d lookup=%d result=%d", claimCount.Load(), lookupCount.Load(), resultCount.Load())
	}
	if submitted.AgentEpoch != runner.epoch || submitted.SessionID != runner.sessionID || submitted.CountryCode != "JP" || submitted.ErrorCode != "" || submitted.Validate() != nil {
		t.Fatalf("submitted result=%+v", submitted)
	}
}

func TestCountryCodeLookupWorkerDoesNotPollBeforeManagementNegotiation(t *testing.T) {
	runner, err := NewWithExecutor(Config{ServerURL: "https://example.invalid", AgentID: "agent", Token: "test-token", Interval: time.Second,
		Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	runner.countryCodeLookup = func(context.Context) (string, error) {
		lookups.Add(1)
		return "US", nil
	}
	if runner.runCountryCodeLookupCycle(context.Background(), countryCodeLookupHTTPClient{}) || lookups.Load() != 0 {
		t.Fatal("lookup ran before management API negotiation")
	}
}
