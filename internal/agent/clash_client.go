package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"404-probe/internal/protocol"
)

const maxClashResponseBytes = 1 << 20

type clashClient struct {
	endpoint string
	secret   string
	client   *http.Client
}

type clashProxy struct {
	Type string   `json:"type"`
	Name string   `json:"name"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

func (c clashClient) discover(ctx context.Context) ([]protocol.OutboundSelector, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.endpoint, "/")+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("Clash API returned HTTP %d", response.StatusCode)
	}
	var wire struct {
		Proxies map[string]clashProxy `json:"proxies"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("decode Clash API response: %w", err)
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
