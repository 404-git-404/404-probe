package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

func (r *Runner) runOutboundDiscovery(ctx context.Context) {
	local := clashClient{endpoint: r.config.ClashAPIURL, client: r.client, orderPath: r.config.SelectorOrderPath}
	var lastStatus protocol.OutboundStatus
	capabilityReady := (<-chan struct{})(r.interactiveControlReady)
	for {
		r.outboundMu.Lock()
		selectors, orderSource, discoverErr := local.discoverOrdered(ctx)
		if ctx.Err() != nil {
			r.outboundMu.Unlock()
			return
		}
		status := outboundStatus(discoverErr)
		available := status == protocol.OutboundStatusConnected
		r.clashControlReady.Store(available)
		if lastStatus != status {
			if discoverErr != nil {
				r.logger.Warn("sing-box Clash API discovery state changed", "status", status)
			} else if lastStatus != "" {
				r.logger.Info("sing-box Clash API available again")
			}
			lastStatus = status
		}
		snapshot := protocol.OutboundSnapshot{Available: available, Selectors: selectors}
		if available {
			snapshot.OrderSource = orderSource
		}
		if r.interactiveControlSupported.Load() {
			snapshot.Status = status
		}
		publishErr := r.postOutboundSnapshot(ctx, snapshot)
		r.outboundMu.Unlock()
		if publishErr != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(publishErr, ErrAgentDisabled) || errors.Is(publishErr, ErrAgentRevoked) {
				return
			}
			r.logger.Warn("publish outbound snapshot failed; will retry", "error", publishErr)
		}
		delay := r.config.OutboundInterval
		if !available && delay > 10*time.Second {
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-capabilityReady:
			stopTimer(timer)
			capabilityReady = nil
		case <-timer.C:
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func outboundStatus(err error) protocol.OutboundStatus {
	if err == nil {
		return protocol.OutboundStatusConnected
	}
	var typed *selectorSwitchError
	if errors.As(err, &typed) {
		switch typed.category {
		case "clash_api_not_detected":
			return protocol.OutboundStatusNotDetected
		case "clash_api_auth_required":
			return protocol.OutboundStatusAuthRequired
		}
	}
	return protocol.OutboundStatusUnavailable
}

func (r *Runner) postOutboundSnapshot(ctx context.Context, snapshot protocol.OutboundSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.config.ServerURL, "/")+"/api/v1/agent/outbounds", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.config.Token)
	response, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return fmt.Errorf("read outbound API response: %w", err)
	}
	if len(responseBody) > 4096 {
		return errors.New("outbound API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiErr := decodeJobHTTPError(response.StatusCode, responseBody)
		var typed *jobHTTPError
		if errors.As(apiErr, &typed) {
			if typed.StatusCode == http.StatusUnauthorized && typed.Code == "agent_revoked" {
				return ErrAgentRevoked
			}
			if typed.StatusCode == http.StatusLocked && typed.Code == "agent_disabled" {
				return ErrAgentDisabled
			}
		}
		return apiErr
	}
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	if err := decodeStrictJobResponse(responseBody, &ack); err != nil || !ack.Accepted {
		return errors.New("outbound API returned an invalid acknowledgement")
	}
	return nil
}
