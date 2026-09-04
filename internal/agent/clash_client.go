package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"404-probe/internal/protocol"
)

const maxClashResponseBytes = 1 << 20

type clashClient struct {
	endpoint string
	client   *http.Client
}

type clashProxy struct {
	Type string   `json:"type"`
	Name string   `json:"name"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

type selectorSwitchError struct {
	category string
	message  string
}

func (e *selectorSwitchError) Error() string { return e.message }

func (c clashClient) discover(ctx context.Context) ([]protocol.OutboundSelector, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.endpoint, "/")+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		category := "clash_api_unavailable"
		var netErr net.Error
		if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
			category = "clash_api_not_detected"
		}
		return nil, &selectorSwitchError{category: category, message: "local Clash API request failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxClashResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Clash API response: %w", err)
	}
	if len(body) > maxClashResponseBytes {
		return nil, errors.New("Clash API response is too large")
	}
	if response.StatusCode != http.StatusOK {
		category := "clash_api_unavailable"
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			category = "clash_api_auth_required"
		}
		return nil, &selectorSwitchError{category: category, message: fmt.Sprintf("Clash API returned HTTP %d", response.StatusCode)}
	}
	var wire struct {
		Proxies map[string]clashProxy `json:"proxies"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, &selectorSwitchError{category: "clash_api_unavailable", message: "decode Clash API response failed"}
	}
	if wire.Proxies == nil {
		return nil, errors.New("decode Clash API response: proxies field is required")
	}
	selectors := make([]protocol.OutboundSelector, 0)
	for key, proxy := range wire.Proxies {
		if proxy.Type != "Selector" {
			continue
		}
		name := proxy.Name
		if name == "" {
			name = key
		}
		selector := protocol.OutboundSelector{Name: name, Current: proxy.Now, Choices: append([]string(nil), proxy.All...)}
		if err := (protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{selector}}).Validate(); err != nil {
			return nil, fmt.Errorf("invalid selector %q: %w", name, err)
		}
		selectors = append(selectors, selector)
	}
	sort.Slice(selectors, func(i, j int) bool { return selectors[i].Name < selectors[j].Name })
	if err := (protocol.OutboundSnapshot{Available: true, Selectors: selectors}).Validate(); err != nil {
		return nil, err
	}
	return selectors, nil
}

func (c clashClient) switchSelector(ctx context.Context, selectorName, choiceName string) (protocol.SelectorSwitchResult, error) {
	selectors, err := c.discover(ctx)
	if err != nil {
		return protocol.SelectorSwitchResult{}, err
	}
	var selector *protocol.OutboundSelector
	for index := range selectors {
		if selectors[index].Name == selectorName {
			selector = &selectors[index]
			break
		}
	}
	if selector == nil {
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "selector_not_found", message: "selector no longer exists"}
	}
	choiceFound := false
	for _, choice := range selector.Choices {
		choiceFound = choiceFound || choice == choiceName
	}
	if !choiceFound {
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "choice_not_found", message: "choice no longer belongs to selector"}
	}
	if selector.Current == choiceName {
		return protocol.SelectorSwitchResult{Current: choiceName, Changed: false}, nil
	}
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: choiceName})
	if err != nil {
		return protocol.SelectorSwitchResult{}, err
	}
	endpoint := strings.TrimRight(c.endpoint, "/") + "/proxies/" + url.PathEscape(selectorName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return protocol.SelectorSwitchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "switch_failed", message: "Clash API switch request failed"}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "clash_api_auth_required", message: "Clash API authentication must be disabled"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "switch_failed", message: fmt.Sprintf("Clash API switch returned HTTP %d", response.StatusCode)}
	}
	readBack, err := c.discover(ctx)
	if err != nil {
		var switchErr *selectorSwitchError
		if errors.As(err, &switchErr) && switchErr.category == "clash_api_auth_required" {
			return protocol.SelectorSwitchResult{}, switchErr
		}
		return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "switch_verification_failed", message: "could not verify selector after switch"}
	}
	for _, candidate := range readBack {
		if candidate.Name == selectorName && candidate.Current == choiceName {
			return protocol.SelectorSwitchResult{Current: choiceName, Changed: true}, nil
		}
	}
	return protocol.SelectorSwitchResult{}, &selectorSwitchError{category: "switch_verification_failed", message: "selector read-back did not match requested choice"}
}
