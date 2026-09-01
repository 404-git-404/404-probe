package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/updater"
)

type upgradeHTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (c upgradeHTTPClient) claim(ctx context.Context) (*protocol.UpgradeOperation, error) {
	status, body, err := c.post(ctx, "/api/v1/agent/upgrades/claim", protocol.UpgradeClaimRequest{ProtocolVersion: protocol.UpgradeProtocolVersion})
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, decodeJobHTTPError(status, body)
	}
	var operation protocol.UpgradeOperation
	if decodeStrictJobResponse(body, &operation) != nil || operation.Validate() != nil {
		return nil, errors.New("invalid upgrade claim response")
	}
	return &operation, nil
}

func (c upgradeHTTPClient) update(ctx context.Context, operationID, status, code, message string) error {
	request := protocol.UpgradeStatusRequest{ProtocolVersion: protocol.UpgradeProtocolVersion, Status: status, FailureCode: code, FailureMessage: message}
	path := "/api/v1/agent/upgrades/" + url.PathEscape(operationID) + "/status"
	responseStatus, body, err := c.post(ctx, path, request)
	if err != nil {
		return err
	}
	if responseStatus != http.StatusOK {
		return decodeJobHTTPError(responseStatus, body)
	}
	return nil
}

func (c upgradeHTTPClient) post(ctx context.Context, path string, value any) (int, []byte, error) {
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
	response, err := c.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxUpgradeBodyBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(limited) > protocol.MaxUpgradeBodyBytes {
		return 0, nil, errors.New("upgrade API response is too large")
	}
	return response.StatusCode, limited, nil
}

func (r *Runner) runUpgradeWorker(ctx context.Context) {
	server := upgradeHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}
	local := updater.Client{Socket: r.config.UpdaterSocket, Timeout: 5 * time.Second}
	ticker := time.NewTicker(r.config.JobInterval)
	defer ticker.Stop()
	for {
		r.runUpgradeCycle(ctx, server, local)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) runUpgradeCycle(ctx context.Context, server upgradeHTTPClient, local updater.Client) {
	if !r.upgradeAPISupported.Load() {
		return
	}
	operation, err := server.claim(ctx)
	if err != nil || operation == nil {
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("claim upgrade failed; will retry", "error", err)
		}
		return
	}
	request := updater.Request{ProtocolVersion: updater.ProtocolVersion, OperationID: operation.OperationID, TargetVersion: operation.TargetVersion}
	comparison, comparable := buildinfo.CompareVersions(r.config.AgentVersion, operation.TargetVersion)
	if operation.Status == "claimed" && comparable && comparison < 0 {
		request.Action = updater.ActionStart
		if _, err := local.Call(ctx, request); err != nil {
			_ = server.update(ctx, operation.OperationID, "failed", "install_failed", "restricted local updater is unavailable")
			return
		}
	}
	request.Action = updater.ActionStatus
	response, err := local.Call(ctx, request)
	if err != nil || !response.Accepted {
		return
	}
	if response.State.Status == "health_check" && r.config.AgentVersion == operation.TargetVersion && r.versionReportAccepted.Load() {
		request.Action = updater.ActionHealthy
		_, _ = local.Call(ctx, request)
		request.Action = updater.ActionStatus
		response, _ = local.Call(ctx, request)
	}
	if response.State.Status != "" {
		if err := syncUpgradeStatus(ctx, server, *operation, response.State); err != nil && ctx.Err() == nil {
			r.logger.Warn("publish upgrade status failed; will retry", "operation_id", operation.OperationID, "error", err)
		}
	}
}

func syncUpgradeStatus(ctx context.Context, client upgradeHTTPClient, operation protocol.UpgradeOperation, local updater.State) error {
	sequence := []string{"claimed", "downloading", "verifying", "staging", "installing", "restarting", "health_check", "succeeded"}
	from := statusIndex(sequence, operation.Status)
	to := statusIndex(sequence, local.Status)
	if local.Status == "failed" || local.Status == "rolled_back" {
		if local.Status == "rolled_back" {
			for index := from + 1; index <= statusIndex(sequence, "health_check"); index++ {
				if err := client.update(ctx, operation.OperationID, sequence[index], "", ""); err != nil {
					return err
				}
			}
		}
		return client.update(ctx, operation.OperationID, local.Status, local.FailureCode, local.FailureMessage)
	}
	if from < 0 || to < from {
		return nil
	}
	for index := from + 1; index <= to; index++ {
		if err := client.update(ctx, operation.OperationID, sequence[index], "", ""); err != nil {
			return fmt.Errorf("publish %s: %w", sequence[index], err)
		}
	}
	return nil
}

func statusIndex(sequence []string, status string) int {
	for index, candidate := range sequence {
		if candidate == status {
			return index
		}
	}
	return -1
}
