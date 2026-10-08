package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/updater"
)

type agentRemovalHTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (r *Runner) updaterSupportsRemoteRemoval(ctx context.Context) bool {
	if r.deferredUnsupported || r.config.UpdaterSocket == "" {
		return false
	}
	response, err := (updater.Client{Socket: r.config.UpdaterSocket, Timeout: 5 * time.Second}).Call(ctx, updater.Request{
		ProtocolVersion: updater.ProtocolVersion,
		Action:          updater.ActionCapabilities,
	})
	return err == nil && response.Accepted && response.Capabilities.RemoteRemoval
}

func (r *Runner) remoteRemovalCapability(ctx context.Context) bool {
	return r.config.EnableRemoteRemoval && r.updaterSupportsRemoteRemoval(ctx)
}

func (c agentRemovalHTTPClient) post(ctx context.Context, path string, value any) (int, []byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := doAgentServerRequest(c.client, request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return 0, nil, err
	}
	if len(limited) > 4096 {
		return 0, nil, errors.New("Agent removal response is too large")
	}
	return response.StatusCode, limited, nil
}

func (c agentRemovalHTTPClient) claim(ctx context.Context, epoch uint64, sessionID string) (*protocol.AgentRemovalDelivery, error) {
	status, body, err := c.post(ctx, "/api/v1/agent/removals/claim", protocol.AgentRemovalClaimRequest{
		ProtocolVersion: protocol.AgentRemovalProtocolVersion,
		AgentEpoch:      epoch,
		SessionID:       sessionID,
	})
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, decodeJobHTTPError(status, body)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var delivery protocol.AgentRemovalDelivery
	if decoder.Decode(&delivery) != nil || delivery.Validate() != nil {
		return nil, errors.New("invalid Agent removal delivery")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid Agent removal delivery")
	}
	return &delivery, nil
}

func (c agentRemovalHTTPClient) markUninstalling(ctx context.Context, operationID string, epoch uint64, sessionID string) error {
	path := "/api/v1/agent/removals/" + operationID + "/status"
	status, body, err := c.post(ctx, path, protocol.AgentRemovalStatusRequest{
		ProtocolVersion: protocol.AgentRemovalProtocolVersion,
		AgentEpoch:      epoch,
		SessionID:       sessionID,
		Status:          protocol.AgentRemovalStatusUninstalling,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return decodeJobHTTPError(status, body)
	}
	return nil
}

func (r *Runner) runRemovalWorker(ctx context.Context) {
	if r.deferredUnsupported {
		return
	}
	server := agentRemovalHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}
	local := updater.Client{Socket: r.config.UpdaterSocket, Timeout: 5 * time.Second}
	emptyCycles := 0
	for {
		if ctx.Err() != nil {
			return
		}
		active := r.runRemovalCycle(ctx, server, local)
		delay := r.config.JobInterval
		if active {
			emptyCycles = 0
		} else {
			delay = idlePollDelay(r.config.JobInterval, emptyCycles)
			emptyCycles++
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-timer.C:
		}
	}
}

func (r *Runner) runRemovalCycle(ctx context.Context, server agentRemovalHTTPClient, local updater.Client) bool {
	if !r.managementAPISupported.Load() || !r.remoteRemovalCapability(ctx) {
		return false
	}
	delivery, err := server.claim(ctx, r.epoch, r.sessionID)
	if err != nil || delivery == nil {
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("claim Agent removal failed; will retry", "error", err)
		}
		return false
	}
	if err := server.markUninstalling(ctx, delivery.OperationID, r.epoch, r.sessionID); err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("mark Agent removal started failed; will retry", "operation_id", delivery.OperationID, "error", err)
		}
		return true
	}
	response, err := local.Call(ctx, updater.Request{
		ProtocolVersion: updater.ProtocolVersion,
		Action:          updater.ActionRemove,
		OperationID:     delivery.OperationID,
		ReceiptToken:    delivery.ReceiptToken,
	})
	if err != nil || !response.Accepted {
		if ctx.Err() == nil {
			r.logger.Warn("start fixed Agent removal worker failed; will retry", "operation_id", delivery.OperationID)
		}
		return true
	}
	return true
}
