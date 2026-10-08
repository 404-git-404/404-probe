package updater

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const removalWorkerMaxConfig = 8 << 10

type removalRequestState struct {
	OperationID  string `json:"operation_id"`
	ReceiptToken string `json:"receipt_token,omitempty"`
	ServerURL    string `json:"server_url"`
	Phase        string `json:"phase"`
	ServiceUID   uint32 `json:"service_uid"`
	ServiceGID   uint32 `json:"service_gid"`
}

// AgentRemovalLauncher accepts only an operation ID and receipt-only bearer
// credential. Implementations must use the fixed local removal unit/resources.
type AgentRemovalLauncher interface {
	SupportsRemoteRemoval() bool
	Start(context.Context, string, string) error
}

func validateReceiptServerURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.TrimSpace(value) != value {
		return "", errors.New("invalid receipt Server origin")
	}
	return strings.TrimRight(value, "/"), nil
}

func submitAgentRemovalReceipt(ctx context.Context, state removalRequestState) error {
	client := &http.Client{
		Timeout:       20 * time.Second,
		Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return submitAgentRemovalReceiptWithClient(ctx, state, client)
}

func submitAgentRemovalReceiptWithClient(ctx context.Context, state removalRequestState, client *http.Client) error {
	if !validOperationID(state.OperationID) || !validReceiptToken(state.ReceiptToken) {
		return errors.New("Agent removal receipt state is invalid")
	}
	serverURL, err := validateReceiptServerURL(state.ServerURL)
	if err != nil {
		return errors.New("Agent removal receipt endpoint is invalid")
	}
	body, err := json.Marshal(protocol.AgentRemovalReceiptRequest{Receipt: protocol.AgentRemovalReceiptKind})
	if err != nil {
		return errors.New("could not encode Agent removal receipt")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/v1/agent/removals/"+state.OperationID+"/receipt", bytes.NewReader(body))
	if err != nil {
		return errors.New("could not create Agent removal receipt request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+state.ReceiptToken)
	if client == nil {
		return errors.New("Agent removal receipt client is unavailable")
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copy.Do(request)
	if err != nil {
		return errors.New("Agent removal receipt delivery failed; outcome remains unverified")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, removalWorkerMaxConfig+1))
	if err != nil || len(responseBody) > removalWorkerMaxConfig || response.StatusCode != http.StatusOK {
		return fmt.Errorf("Agent removal receipt outcome remains unverified (HTTP %d)", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	var ack protocol.AgentRemovalReceiptResponse
	if err := decoder.Decode(&ack); err != nil || !ack.Completed {
		return errors.New("Agent removal receipt outcome remains unverified")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Agent removal receipt outcome remains unverified")
	}
	return nil
}
