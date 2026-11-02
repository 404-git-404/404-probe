// Package updatecheck maintains a passive, persisted stable-release hint.
package updatecheck

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"

	"404-probe/internal/buildinfo"
)

const endpoint = "https://api.github.com/repos/404-git-404/404-probe/releases/latest"
const userAgent = "404-probe-update-check"
const interval = 12 * time.Hour
const maxBody = 64 << 10

type State struct {
	Enabled   bool   `json:"enabled"`
	Latest    string `json:"latest"`
	Newer     bool   `json:"newer"`
	CheckedAt int64  `json:"checked_at"`
}

type Checker struct {
	mu            sync.Mutex
	state         State
	info          buildinfo.Info
	persist       func(State) error
	logger        *slog.Logger
	client        *http.Client
	wake          chan struct{}
	cancel        context.CancelFunc
	requestCancel context.CancelFunc
	done          chan struct{}
	closed        bool
	lastWarning   time.Time
	// Private test seams: production has no address/config/environment override.
	fetchURL string
	delay    func() time.Duration
	now      func() time.Time
}

func newChecker(initial State, info buildinfo.Info, logger *slog.Logger) *Checker {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Checker{state: initial, persist: func(State) error { return nil }, info: info, logger: logger, wake: make(chan struct{}, 1), fetchURL: endpoint, delay: randomDelay, now: time.Now}
	transport := &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 8 * time.Second,
	}
	c.client = &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	return c
}

func randomDelay() time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(9*time.Minute)))
	if err != nil {
		return 10 * time.Minute
	}
	return time.Minute + time.Duration(n.Int64())
}

// Snapshot never performs I/O or schedules a check.
func (c *Checker) Snapshot() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	comparison, ok := buildinfo.CompareReleaseVersions(s.Latest, c.info.Version)
	s.Newer = s.Enabled && c.info.UpgradeEligible() && buildinfo.IsCanonicalVersion(s.Latest) && ok && comparison > 0
	return s
}

func (c *Checker) SetEnabled(enabled bool) (State, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return State{}, errors.New("update checker closed")
	}
	next := c.state
	next.Enabled = enabled
	if err := c.persist(next); err != nil {
		c.mu.Unlock()
		return State{}, err
	}
	changed := c.state.Enabled != enabled
	c.state = next
	if changed && c.requestCancel != nil {
		c.requestCancel()
	}
	c.mu.Unlock()
	if changed {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return c.Snapshot(), nil
}

func (c *Checker) Start(parent context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil || c.closed {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() { defer close(c.done); c.run(ctx) }()
}

func (c *Checker) Close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.requestCancel != nil {
		c.requestCancel()
	}
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	c.client.CloseIdleConnections()
}

func (c *Checker) initialDelay() time.Duration {
	delay := c.delay()
	c.mu.Lock()
	checked := c.state.CheckedAt
	c.mu.Unlock()
	// Re-enabling/restarting cannot circumvent the last successful check.
	if checked > 0 {
		if remaining := time.UnixMilli(checked).Add(interval).Sub(c.now()); remaining > delay {
			delay = remaining
		}
	}
	return delay
}

func (c *Checker) run(ctx context.Context) {
	delay := c.initialDelay()
	failures := 0
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.wake:
			timer.Stop()
			failures = 0
			delay = c.initialDelay()
			continue
		case <-timer.C:
		}
		err := c.check(ctx)
		if err == nil {
			failures = 0
			delay = interval
		} else {
			failures++
			if failures > 12 {
				failures = 12
			}
			delay = time.Duration(failures) * 5 * time.Minute
			if ctx.Err() == nil {
				c.warn("request failed; retaining last successful result")
			}
		}
	}
}

func (c *Checker) check(ctx context.Context) error {
	c.mu.Lock()
	if !c.state.Enabled || c.closed || ctx.Err() != nil {
		c.mu.Unlock()
		return nil
	}
	if c.state.CheckedAt > 0 && c.now().Before(time.UnixMilli(c.state.CheckedAt).Add(interval)) {
		c.mu.Unlock()
		return nil
	}
	requestCtx, cancel := context.WithCancel(ctx)
	c.requestCancel = cancel
	c.mu.Unlock()
	defer cancel()
	latest, err := c.fetch(requestCtx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestCancel = nil
	if err != nil {
		return err
	}
	if !c.state.Enabled || c.closed || requestCtx.Err() != nil {
		return context.Canceled
	}
	c.state.Latest, c.state.CheckedAt = latest, c.now().UnixMilli()
	if err := c.persist(c.state); err != nil {
		c.warnLocked("could not persist result; retaining memory cache")
	}
	return nil
}

func (c *Checker) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fetchURL, nil)
	if err != nil {
		return "", err
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return "", errors.New("HTTPS required")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("unexpected status")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxBody {
		return "", errors.New("response too large")
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if json.Unmarshal(body, &release) != nil || !buildinfo.IsCanonicalVersion(release.Tag) {
		return "", errors.New("invalid release tag")
	}
	return release.Tag, nil
}

func (c *Checker) warn(message string) { c.mu.Lock(); defer c.mu.Unlock(); c.warnLocked(message) }
func (c *Checker) warnLocked(message string) {
	if c.lastWarning.IsZero() || c.now().Sub(c.lastWarning) >= 15*time.Minute {
		c.logger.Warn("update check: " + message)
		c.lastWarning = c.now()
	}
}
