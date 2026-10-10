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
	protocol int
	baseURL  string
	token    string
	client   *http.Client
}

func (c upgradeHTTPClient) claim(ctx context.Context) (*protocol.UpgradeOperation, error) {
	pv := c.protocol
	if pv == 0 {
		pv = 1
	}
	status, body, err := c.post(ctx, "/api/v1/agent/upgrades/claim", protocol.UpgradeClaimRequest{ProtocolVersion: pv})
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
	if operation.ProtocolVersion == 2 && pv != 2 {
		return nil, errors.New("upgrade protocol was not negotiated")
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
	response, err := doAgentServerRequest(c.client, request)
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
	if r.deferredUnsupported {
		return
	}
	server := upgradeHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}
	local := updater.Client{Socket: r.config.UpdaterSocket, Timeout: 5 * time.Second}
	emptyCycles := 0
	for {
		if ctx.Err() != nil {
			return
		}
		active := r.runUpgradeCycle(ctx, server, local)
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

func (r *Runner) runUpgradeCycle(ctx context.Context, server upgradeHTTPClient, local updater.Client) bool {
	if r.deferredUnsupported {
		return false
	}
	if r.upgradeV2ServerSupported.Load() || r.config.AgentVersion == "v1.0.1" {
		response, err := local.Call(ctx, updater.Request{ProtocolVersion: 2, Action: updater.ActionCapabilities})
		r.upgradeV2UpdaterSupported.Store(r.upgradeV2ServerSupported.Load() && err == nil && response.Accepted && response.Capabilities.UpgradeV2)
		if err == nil && response.Accepted && response.State.LocalMigration && !isTerminalMigrationStatus(response.State.Status) {
			if response.State.TargetVersion == "v1.0.1" && response.State.Status == "health_check" && r.config.AgentVersion == "v1.0.1" && r.versionReportAccepted.Load() {
				_, _ = local.Call(ctx, updater.Request{ProtocolVersion: 1, Action: updater.ActionHealthy, OperationID: response.State.OperationID, TargetVersion: "v1.0.1"})
			}
			return true
		}
	} else {
		r.upgradeV2UpdaterSupported.Store(false)
	}
	if !r.upgradeAPISupported.Load() {
		return false
	}
	if r.upgradeV2ServerSupported.Load() && r.upgradeV2UpdaterSupported.Load() {
		server.protocol = 2
	}
	operation, err := server.claim(ctx)
	if err != nil || operation == nil {
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("claim upgrade failed; will retry", "error", err)
		}
		return false
	}
	request := updater.Request{ProtocolVersion: updater.ProtocolVersion, OperationID: operation.OperationID, TargetVersion: operation.TargetVersion}
	if operation.ProtocolVersion == 2 {
		request.ProtocolVersion = 2
		request.Channel = operation.Channel
		request.TargetCommit = operation.TargetCommit
		request.ServerVersion = operation.ServerVersion
	}
	comparison, comparable := buildinfo.CompareReleaseVersions(r.config.AgentVersion, operation.TargetVersion)
	if operation.Status == "claimed" && comparable && comparison < 0 {
		request.Action = updater.ActionStart
		if _, err := local.Call(ctx, request); err != nil {
			_ = server.update(ctx, operation.OperationID, "failed", "install_failed", "restricted local updater is unavailable")
			return true
		}
	}
	request.Action = updater.ActionStatus
	response, err := local.Call(ctx, request)
	if err != nil || !response.Accepted {
		return true
	}
	if response.State.OperationID != operation.OperationID || response.State.TargetVersion != operation.TargetVersion || operation.ProtocolVersion == 2 && (response.State.ProtocolVersion != 2 || response.State.Channel != operation.Channel || response.State.TargetCommit != operation.TargetCommit || response.State.ServerVersion != operation.ServerVersion) {
		return true
	}
	if response.State.Status == "health_check" && r.config.AgentVersion == operation.TargetVersion && r.versionReportAccepted.Load() {
		request.Action = updater.ActionHealthy
		_, _ = local.Call(ctx, request)
		request.Action = updater.ActionStatus
		response, _ = local.Call(ctx, request)
		if !response.Accepted || response.State.OperationID != operation.OperationID || response.State.TargetVersion != operation.TargetVersion || operation.ProtocolVersion == 2 && (response.State.ProtocolVersion != 2 || response.State.Channel != operation.Channel || response.State.TargetCommit != operation.TargetCommit || response.State.ServerVersion != operation.ServerVersion) {
			return true
		}
	}
	if response.State.Status != "" {
		if err := syncUpgradeStatus(ctx, server, *operation, response.State); err != nil && ctx.Err() == nil {
			r.logger.Warn("publish upgrade status failed; will retry", "operation_id", operation.OperationID, "error", err)
		}
	}
	return true
}

func isTerminalMigrationStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "rolled_back"
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
