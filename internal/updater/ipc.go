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
)

const (
	ProtocolVersion = 1
	DefaultSocket   = "/run/404-probe/agent-updater.sock"
	maxIPCBytes     = 8 << 10
)

type Action string

const (
	ActionStart   Action = "start"
	ActionStatus  Action = "status"
	ActionHealthy Action = "healthy"
)

type Request struct {
	ProtocolVersion int    `json:"protocol_version"`
	Action          Action `json:"action"`
	OperationID     string `json:"operation_id"`
	TargetVersion   string `json:"target_version"`
}

func (r Request) Validate() error {
	if r.ProtocolVersion != ProtocolVersion || !validOperationID(r.OperationID) || !buildinfo.IsCanonicalVersion(r.TargetVersion) {
		return errors.New("invalid updater request")
	}
	switch r.Action {
	case ActionStart, ActionStatus, ActionHealthy:
		return nil
	default:
		return errors.New("invalid updater action")
	}
}

type State struct {
	OperationID    string `json:"operation_id"`
	TargetVersion  string `json:"target_version"`
	ReleaseCommit  string `json:"release_commit,omitempty"`
	Status         string `json:"status"`
	FailureCode    string `json:"failure_code,omitempty"`
	FailureMessage string `json:"failure_message,omitempty"`
	UpdatedAt      int64  `json:"updated_at"`
}

type Response struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
	State    State  `json:"state"`
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
