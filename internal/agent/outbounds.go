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
	local := clashClient{endpoint: r.config.ClashAPIURL, secret: r.config.ClashAPISecret, client: r.client}
	ticker := time.NewTicker(r.config.OutboundInterval)
	defer ticker.Stop()
	var lastAvailable *bool
	for {
		selectors, discoverErr := local.discover(ctx)
		if ctx.Err() != nil {
			return
		}
		available := discoverErr == nil
		if lastAvailable == nil || *lastAvailable != available {
			if discoverErr != nil {
				r.logger.Warn("sing-box Clash API unavailable; retaining last outbound snapshot", "error", discoverErr)
			} else if lastAvailable != nil {
				r.logger.Info("sing-box Clash API available again")
			}
			state := available
			lastAvailable = &state
		}
		snapshot := protocol.OutboundSnapshot{Available: available, Selectors: selectors}
		if err := r.postOutboundSnapshot(ctx, snapshot); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrAgentDisabled) || errors.Is(err, ErrAgentRevoked) {
				return
			}
			r.logger.Warn("publish outbound snapshot failed; will retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
