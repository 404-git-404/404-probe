package agent

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

type countryCodeLookupHTTPClient struct {
	base jobHTTPClient
}

func (c countryCodeLookupHTTPClient) claim(ctx context.Context, request protocol.AgentCountryCodeLookupClaimRequest) (*protocol.AgentCountryCodeLookupDelivery, error) {
	status, body, err := c.base.postJSON(ctx, "/api/v1/agent/country-code-lookups/claim", request)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		if len(strings.TrimSpace(string(body))) != 0 {
			return nil, errors.New("country-code lookup claim response has a body with HTTP 204")
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, decodeJobHTTPError(status, body)
	}
	var delivery protocol.AgentCountryCodeLookupDelivery
	if err := decodeStrictJobResponse(body, &delivery); err != nil || delivery.Validate() != nil {
		return nil, errors.New("invalid country-code lookup delivery")
	}
	return &delivery, nil
}

func (c countryCodeLookupHTTPClient) submit(ctx context.Context, operationID string, result protocol.AgentCountryCodeLookupResultRequest) (protocol.AgentCountryCodeLookupResultResponse, error) {
	path := "/api/v1/agent/country-code-lookups/" + url.PathEscape(operationID) + "/result"
	status, body, err := c.base.postJSON(ctx, path, result)
	if err != nil {
		return protocol.AgentCountryCodeLookupResultResponse{}, err
	}
	if status != http.StatusOK {
		return protocol.AgentCountryCodeLookupResultResponse{}, decodeJobHTTPError(status, body)
	}
	var response protocol.AgentCountryCodeLookupResultResponse
	if err := decodeStrictJobResponse(body, &response); err != nil || !response.Accepted || (response.Status != "succeeded" && response.Status != "failed") {
		return protocol.AgentCountryCodeLookupResultResponse{}, errors.New("country-code lookup result was not accepted")
	}
	return response, nil
}

func (r *Runner) runCountryCodeLookupWorker(ctx context.Context) {
	client := countryCodeLookupHTTPClient{base: jobHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}}
	emptyCycles := 0
	for {
		if ctx.Err() != nil {
			return
		}
		claimed := r.runCountryCodeLookupCycle(ctx, client)
		delay := r.config.JobInterval
		if claimed {
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

func (r *Runner) runCountryCodeLookupCycle(ctx context.Context, client countryCodeLookupHTTPClient) bool {
	if !r.managementAPISupported.Load() {
		return false
	}
	delivery, err := client.claim(ctx, protocol.AgentCountryCodeLookupClaimRequest{
		ProtocolVersion: protocol.CountryCodeLookupProtocolVersion,
		AgentEpoch:      r.epoch,
		SessionID:       r.sessionID,
	})
	if err != nil || delivery == nil {
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("claim country-code lookup failed; will retry", "error", err)
		}
		return false
	}
	result := protocol.AgentCountryCodeLookupResultRequest{
		ProtocolVersion: protocol.CountryCodeLookupProtocolVersion,
		AgentEpoch:      r.epoch,
		SessionID:       r.sessionID,
	}
	if r.countryCodeLookup == nil {
		result.ErrorCode = protocol.CountryCodeLookupFailure
	} else if code, lookupErr := r.countryCodeLookup(ctx); lookupErr != nil {
		if ctx.Err() != nil {
			return true
		}
		result.ErrorCode = protocol.CountryCodeLookupFailure
	} else if !protocol.ValidCountryCode(code) {
		result.ErrorCode = protocol.CountryCodeLookupFailure
	} else {
		result.CountryCode = code
	}
	for attempt := 0; ; attempt++ {
		if _, err := client.submit(ctx, delivery.OperationID, result); err == nil {
			return true
		} else if ctx.Err() != nil {
			return true
		} else if terminalCountryCodeLookupError(err) {
			r.logger.Warn("country-code lookup result is no longer applicable; result remains unverified", "operation_id", delivery.OperationID, "error", err)
			return true
		} else if attempt == 0 || attempt%5 == 4 {
			r.logger.Warn("submit country-code lookup result failed; retrying the same result", "operation_id", delivery.OperationID, "error", err)
		}
		delay := time.Duration(min(attempt+1, 5)) * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return true
		case <-timer.C:
		}
	}
}

func terminalCountryCodeLookupError(err error) bool {
	var apiError *jobHTTPError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.StatusCode >= 400 && apiError.StatusCode < 500 && apiError.StatusCode != http.StatusRequestTimeout && apiError.StatusCode != http.StatusTooManyRequests
}
