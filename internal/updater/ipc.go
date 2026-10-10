package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
)

const (
	ProtocolVersion = 1
	maxIPCBytes     = 8 << 10
)

type Action string

const (
	ActionStart        Action = "start"
	ActionStatus       Action = "status"
	ActionHealthy      Action = "healthy"
	ActionCapabilities Action = "capabilities"
	ActionRemove       Action = "remove"
)

type Request struct {
	ProtocolVersion int    `json:"protocol_version"`
	Action          Action `json:"action"`
	OperationID     string `json:"operation_id"`
	TargetVersion   string `json:"target_version"`
	ReceiptToken    string `json:"receipt_token,omitempty"`
	Channel         string `json:"channel,omitempty"`
	TargetCommit    string `json:"target_commit,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
}

func (r Request) Validate() error {
	if r.ProtocolVersion != ProtocolVersion && r.ProtocolVersion != 2 || r.ProtocolVersion == 1 && r.ServerVersion != "" {
		return errors.New("invalid updater request")
	}
	switch r.Action {
	case ActionCapabilities:
		if r.OperationID != "" || r.TargetVersion != "" || r.ReceiptToken != "" || r.Channel != "" || r.TargetCommit != "" || r.ServerVersion != "" {
			return errors.New("invalid updater capability request")
		}
		return nil
	case ActionRemove:
		if r.ProtocolVersion != 1 || !validOperationID(r.OperationID) || r.TargetVersion != "" || !validReceiptToken(r.ReceiptToken) || r.Channel != "" || r.TargetCommit != "" {
			return errors.New("invalid Agent removal request")
		}
		return nil
	case ActionStart, ActionStatus, ActionHealthy:
		if !validOperationID(r.OperationID) || r.ReceiptToken != "" {
			return errors.New("invalid updater request")
		}
		if r.ProtocolVersion == 1 {
			if !buildinfo.IsCanonicalVersion(r.TargetVersion) || r.Channel != "" || r.TargetCommit != "" {
				return errors.New("invalid legacy updater request")
			}
		} else if !protocol.ValidUpgradeTarget(r.Channel, r.TargetVersion) || !validHexCommit(r.TargetCommit) || !buildinfo.IsReleaseVersion(r.ServerVersion) {
			return errors.New("invalid explicit updater request")
		}
		return nil
	default:
		return errors.New("invalid updater action")
	}
}

type State struct {
	SourceVersion   string `json:"source_version,omitempty"`
	LocalMigration  bool   `json:"local_migration,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	Channel         string `json:"channel,omitempty"`
	TargetCommit    string `json:"target_commit,omitempty"`
	OperationID     string `json:"operation_id"`
	TargetVersion   string `json:"target_version"`
	ReleaseCommit   string `json:"release_commit,omitempty"`
	Status          string `json:"status"`
	FailureCode     string `json:"failure_code,omitempty"`
	FailureMessage  string `json:"failure_message,omitempty"`
	UpdatedAt       int64  `json:"updated_at"`
}

type Response struct {
	Accepted     bool                `json:"accepted"`
	Error        string              `json:"error,omitempty"`
	State        State               `json:"state"`
	Capabilities UpdaterCapabilities `json:"capabilities,omitempty"`
}

// A zero capability struct must be absent, rather than capabilities:{}:
// the v0.9.3 decoder rejects even an empty unknown field.
func (r Response) MarshalJSON() ([]byte, error) {
	// SourceVersion is a private recovery fact, never part of either IPC wire.
	r.State.SourceVersion = ""
	var capabilities *UpdaterCapabilities
	if r.Capabilities.RemoteRemoval || r.Capabilities.UpgradeV2 {
		copy := r.Capabilities
		capabilities = &copy
	}
	return json.Marshal(struct {
		Accepted     bool                 `json:"accepted"`
		Error        string               `json:"error,omitempty"`
		State        State                `json:"state"`
		Capabilities *UpdaterCapabilities `json:"capabilities,omitempty"`
	}{r.Accepted, r.Error, r.State, capabilities})
}

type UpdaterCapabilities struct {
	UpgradeV2     bool `json:"upgrade_v2,omitempty"`
	RemoteRemoval bool `json:"remote_removal,omitempty"`
}

type Client struct {
	Socket  string
	Timeout time.Duration
}

func (c Client) Call(ctx context.Context, request Request) (Response, error) {
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	socket := c.Socket
	if socket == "" {
		socket = DefaultSocket
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return Response{}, err
	}
	halfCloser, ok := connection.(interface{ CloseWrite() error })
	if !ok {
		return Response{}, errors.New("updater socket does not support half-close")
	}
	if err := halfCloser.CloseWrite(); err != nil {
		return Response{}, err
	}
	body, err := io.ReadAll(io.LimitReader(connection, maxIPCBytes+1))
	if err != nil {
		return Response{}, err
	}
	if len(body) > maxIPCBytes {
		return Response{}, errors.New("updater response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Response{}, errors.New("updater response contains multiple values")
		}
		return Response{}, err
	}
	return response, nil
}

func validOperationID(value string) bool {
	if len(value) != 32 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validHexCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validReceiptToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
