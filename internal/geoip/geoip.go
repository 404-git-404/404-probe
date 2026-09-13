package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"404-probe/internal/protocol"
)

const (
	Endpoint         = "https://ipwho.is/?fields=success,country_code"
	DefaultTimeout   = 4 * time.Second
	maxResponseBytes = 1024
)

var ErrLookupFailed = errors.New("country lookup failed")

func Client(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Installation location must be the Agent host's direct egress. Do not
	// inherit HTTP(S)_PROXY or send the request through an extra application
	// proxy. Transparent host/network routing may still affect the observed
	// egress and can be corrected with the Server-side manual override.
	transport.Proxy = nil
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// LookupCountryCode performs one bounded lookup and returns only a validated
// ISO alpha-2 code. Callers must decide when to invoke it; it does not cache,
// retry, schedule, or persist the response.
func LookupCountryCode(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("%w: request", ErrLookupFailed)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: request", ErrLookupFailed)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxResponseBytes {
		return "", fmt.Errorf("%w: response", ErrLookupFailed)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return "", fmt.Errorf("%w: response", ErrLookupFailed)
	}
	var payload struct {
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || !payload.Success || !protocol.ValidCountryCode(payload.CountryCode) {
		return "", fmt.Errorf("%w: invalid result", ErrLookupFailed)
	}
	return payload.CountryCode, nil
}
