package updater

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type removalRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn removalRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type recordingAgentRemovalLauncher struct {
	supported bool
	operation string
	token     string
	err       error
}

func TestRemovalReceiptClientUsesOnlyFixedHTTPSOperationAndFailsClosed(t *testing.T) {
	const operationID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const token = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcde_-"
	state := removalRequestState{OperationID: operationID, ReceiptToken: token, ServerURL: "https://probe.example", Phase: "agent_uninstalled"}
	calls := 0
	client := &http.Client{Transport: removalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodPost || request.URL.String() != "https://probe.example/api/v1/agent/removals/"+operationID+"/receipt" ||
			request.Header.Get("Authorization") != "Bearer "+token || request.URL.RawQuery != "" {
			t.Fatalf("receipt request escaped fixed endpoint: method=%q url=%q", request.Method, request.URL)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || string(body) != `{"receipt":"agent_uninstalled"}` {
			t.Fatalf("receipt body=%q err=%v", body, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"completed":true}`)), Header: make(http.Header), Request: request}, nil
	})}
	if err := submitAgentRemovalReceiptWithClient(context.Background(), state, client); err != nil || calls != 1 {
		t.Fatalf("receipt calls=%d err=%v", calls, err)
	}
	if _, err := validateReceiptServerURL("https://probe.example/path"); err == nil {
		t.Fatal("receipt accepted a non-origin Server URL")
	}

	redirectClient := &http.Client{Transport: removalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://other.example/receipt"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	if err := submitAgentRemovalReceiptWithClient(context.Background(), state, redirectClient); err == nil || !strings.Contains(err.Error(), "unverified") || calls != 2 {
		t.Fatalf("redirect receipt calls=%d err=%v", calls, err)
	}
	unauthorizedClient := &http.Client{Transport: removalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"expired"}`)), Header: make(http.Header), Request: request}, nil
	})}
	if err := submitAgentRemovalReceiptWithClient(context.Background(), state, unauthorizedClient); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("expired credential was treated as success: %v", err)
	}
}

func (l *recordingAgentRemovalLauncher) SupportsRemoteRemoval() bool { return l.supported }

func (l *recordingAgentRemovalLauncher) Start(_ context.Context, operationID, token string) error {
	l.operation, l.token = operationID, token
	return l.err
}

func TestUpdaterRemovalRequestIsNarrowlyValidatedAndDelegated(t *testing.T) {
	const operationID = "0123456789abcdef0123456789abcdef"
	const receiptToken = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcde_-"
	request := Request{ProtocolVersion: ProtocolVersion, Action: ActionRemove, OperationID: operationID, ReceiptToken: receiptToken}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Request{
		{ProtocolVersion: ProtocolVersion, Action: ActionCapabilities, OperationID: operationID},
		{ProtocolVersion: ProtocolVersion, Action: ActionCapabilities, ReceiptToken: receiptToken},
		{ProtocolVersion: ProtocolVersion, Action: ActionRemove, OperationID: operationID, TargetVersion: "v1.2.3", ReceiptToken: receiptToken},
		{ProtocolVersion: ProtocolVersion, Action: ActionRemove, OperationID: operationID, ReceiptToken: receiptToken + "/path"},
		{ProtocolVersion: ProtocolVersion, Action: ActionRemove, OperationID: "../etc", ReceiptToken: receiptToken},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid updater request accepted: %+v", invalid)
		}
	}
	capabilityRequest := Request{ProtocolVersion: ProtocolVersion, Action: ActionCapabilities}
	if err := capabilityRequest.Validate(); err != nil {
		t.Fatal(err)
	}

	launcher := &recordingAgentRemovalLauncher{supported: true}
	engine, err := NewEngine(EngineConfig{
		CurrentVersion: "v1.0.0", StateDirectory: t.TempDir(),
		LiveBinary: filepath.Join(t.TempDir(), "agent"), StagedBinary: filepath.Join(t.TempDir(), "candidate"),
		PreviousBinary: filepath.Join(t.TempDir(), "previous"), Inspect: func(string) (CandidateBuildInfo, error) { return CandidateBuildInfo{}, nil },
		ServiceCommand: func(context.Context, string) error { return nil }, AgentRemoval: launcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !engine.SupportsRemoteRemoval() {
		t.Fatal("configured fixed remover was not advertised")
	}
	if err := engine.StartAgentRemoval(request); err != nil || launcher.operation != operationID || launcher.token != receiptToken {
		t.Fatalf("delegated operation=%q token=%q err=%v", launcher.operation, launcher.token, err)
	}

	launcher.supported = false
	if engine.SupportsRemoteRemoval() {
		t.Fatal("unavailable remover was advertised")
	}
	if err := engine.StartAgentRemoval(request); err == nil {
		t.Fatal("unavailable remover accepted a destructive operation")
	}
	launcher.supported = true
	launcher.err = errors.New("worker start failed")
	if err := engine.StartAgentRemoval(request); !errors.Is(err, launcher.err) {
		t.Fatalf("worker start error=%v", err)
	}
}
