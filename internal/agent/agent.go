package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"404-probe/internal/collector"
	"404-probe/internal/protocol"
)

type Config struct {
	ServerURL         string
	AgentID           string
	Token             string
	Interval          time.Duration
	JobInterval       time.Duration
	DisabledInterval  time.Duration
	Timeout           time.Duration
	AllowInsecureHTTP bool
	StatePath         string
	NetworkIncludes   []string
	NetworkExcludes   []string
	ClashAPIURL       string
	ClashAPISecret    string `json:"-"`
	OutboundInterval  time.Duration
	AgentVersion      string
	UpdaterSocket     string
}

var (
	ErrAgentRevoked  = errors.New("agent credential has been revoked")
	ErrAgentDisabled = errors.New("agent has been disabled")
)

const DefaultDisabledInterval = time.Minute

func (c Config) Validate() error {
	if c.AgentID == "" || c.Token == "" {
		return errors.New("agent ID and token are required")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil || u.Host == "" {
		return errors.New("server URL is invalid")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && c.AllowInsecureHTTP) {
		return errors.New("server URL must use HTTPS; pass --allow-insecure-http only for local development")
	}
	if c.Interval <= 0 || c.JobInterval < 0 || c.DisabledInterval < 0 || c.Timeout <= 0 {
		return errors.New("report interval and timeout must be positive; job and disabled intervals must not be negative")
	}
	if strings.TrimSpace(c.StatePath) == "" {
		return errors.New("state path is required")
	}
	if c.ClashAPIURL == "" {
		if c.ClashAPISecret != "" {
			return errors.New("sing-box Clash secret requires a Clash API URL")
		}
		return nil
	}
	if c.OutboundInterval <= 0 {
		return errors.New("outbound discovery interval must be positive")
	}
	u, err = url.Parse(c.ClashAPIURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("sing-box Clash API URL is invalid")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("sing-box Clash API URL must use a loopback host")
	}
	return nil
}

type Runner struct {
	config                Config
	client                *http.Client
	collector             reportCollector
	logger                *slog.Logger
	epoch                 uint64
	sessionID             string
	executor              Executor
	reportAgentVersion    bool
	versionReportAccepted atomic.Bool
	upgradeAPISupported   atomic.Bool
}

type reportCollector interface {
	Collect(context.Context) (protocol.Report, error)
}

func New(config Config, logger *slog.Logger) (*Runner, error) {
	return NewWithExecutor(config, logger, NewProbeExecutor())
}

// NewWithExecutor constructs a runner with an explicitly supplied job
// executor. New uses the production HTTP, TCP, and conditionally available
// ICMP executor.
func NewWithExecutor(config Config, logger *slog.Logger, executor Executor) (*Runner, error) {
	if config.JobInterval == 0 {
		config.JobInterval = DefaultJobInterval
	}
	if config.DisabledInterval == 0 {
		config.DisabledInterval = DefaultDisabledInterval
	}
	if config.ClashAPIURL != "" && config.OutboundInterval == 0 {
		config.OutboundInterval = time.Minute
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if executor == nil {
		return nil, errors.New("executor is required")
	}
	epoch, err := nextEpoch(config.StatePath)
	if err != nil {
		return nil, fmt.Errorf("advance persistent epoch: %w", err)
	}
	session, err := randomID(16)
	if err != nil {
		return nil, fmt.Errorf("create session ID: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		config: config, client: &http.Client{Timeout: config.Timeout}, logger: logger, epoch: epoch, sessionID: session,
		collector: collector.Collector{Includes: config.NetworkIncludes, Excludes: config.NetworkExcludes},
		executor:  executor,
	}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	var sequence uint64
	immediate := true
	for {
		workerContext, stopWorker := context.WithCancel(ctx)
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.runJobWorker(workerContext)
		}()
		if r.config.UpdaterSocket != "" {
			workers.Add(1)
			go func() {
				defer workers.Done()
				r.runUpgradeWorker(workerContext)
			}()
		}
		if r.config.ClashAPIURL != "" {
			workers.Add(1)
			go func() {
				defer workers.Done()
				r.runOutboundDiscovery(workerContext)
			}()
		}

		err := r.runReportLoop(workerContext, &sequence, immediate)
		stopWorker()
		workers.Wait()
		if errors.Is(err, ErrAgentRevoked) {
			r.logger.Info("agent credential has been revoked; stopping")
			return nil
		}
		if !errors.Is(err, ErrAgentDisabled) {
			return err
		}

		r.logger.Info("agent has been disabled; pausing reports, jobs, and outbound discovery")
		err = r.waitWhileDisabled(ctx, &sequence)
		if errors.Is(err, ErrAgentRevoked) {
			r.logger.Info("agent credential has been revoked; stopping")
			return nil
		}
		if err != nil || ctx.Err() != nil {
			return err
		}
		r.logger.Info("agent has been enabled; resuming reports, jobs, and outbound discovery")
		immediate = false
	}
}

func (r *Runner) runReports(ctx context.Context) error {
	var sequence uint64
	return r.runReportLoop(ctx, &sequence, true)
}

func (r *Runner) runReportLoop(ctx context.Context, sequence *uint64, immediate bool) error {
	if immediate {
		if _, err := r.sendReport(ctx, sequence); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := r.sendReport(ctx, sequence); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) waitWhileDisabled(ctx context.Context, sequence *uint64) error {
	ticker := time.NewTicker(r.config.DisabledInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			accepted, err := r.sendReport(ctx, sequence)
			switch {
			case errors.Is(err, ErrAgentDisabled):
				continue
			case err != nil:
				if errors.Is(err, ErrAgentRevoked) {
					return err
				}
				continue
			case accepted:
				return nil
			default:
				continue
			}
		}
	}
}

func (r *Runner) sendReport(ctx context.Context, sequence *uint64) (bool, error) {
	measurement, err := r.collector.Collect(ctx)
	if err != nil {
		r.logger.Error("collect metrics", "error", err)
		return false, nil
	}
	(*sequence)++
	measurement.AgentID = r.config.AgentID
	if r.reportAgentVersion {
		measurement.AgentVersion = r.config.AgentVersion
		measurement.AgentUpgradeCapable = r.updaterAvailable()
	}
	measurement.Epoch = r.epoch
	measurement.SessionID = r.sessionID
	measurement.Sequence = *sequence
	measurement.CollectedAt = time.Now().UnixMilli()
	response, err := r.post(ctx, measurement)
	if err != nil {
		if errors.Is(err, ErrAgentRevoked) || errors.Is(err, ErrAgentDisabled) {
			return false, err
		}
		r.logger.Warn("report failed; will retry with a fresh sample", "error", err)
		return false, nil
	}
	if !response.Accepted {
		r.logger.Warn("report rejected", "reason", response.Reason, "epoch", r.epoch, "sequence", *sequence)
		return false, nil
	}
	if measurement.AgentVersion != "" {
		r.versionReportAccepted.Store(true)
	}
	if response.Capabilities.AgentVersionReport {
		r.reportAgentVersion = true
	}
	if response.Capabilities.AgentUpgrade {
		r.upgradeAPISupported.Store(true)
	}
	r.logger.Debug("report accepted", "sequence", *sequence)
	return true, nil
}

func (r *Runner) updaterAvailable() bool {
	if r.config.UpdaterSocket == "" {
		return false
	}
	info, err := os.Stat(r.config.UpdaterSocket)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func (r *Runner) post(ctx context.Context, report protocol.Report) (protocol.ReportResponse, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return protocol.ReportResponse{}, err
	}
	endpoint := strings.TrimRight(r.config.ServerURL, "/") + "/api/v1/report"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return protocol.ReportResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.config.Token)
	resp, err := r.client.Do(req)
	if err != nil {
		return protocol.ReportResponse{}, err
	}
	defer resp.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return protocol.ReportResponse{}, fmt.Errorf("read server response: %w", err)
	}
	if len(limited) > 4096 {
		return protocol.ReportResponse{}, errors.New("server response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusLocked {
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(limited, &envelope) == nil && resp.StatusCode == http.StatusUnauthorized && envelope.Error.Code == "agent_revoked" {
				return protocol.ReportResponse{}, ErrAgentRevoked
			}
			if resp.StatusCode == http.StatusLocked && envelope.Error.Code == "agent_disabled" {
				return protocol.ReportResponse{}, ErrAgentDisabled
			}
		}
		return protocol.ReportResponse{}, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}
	var wire struct {
		Accepted     *bool                       `json:"accepted"`
		Reason       string                      `json:"reason"`
		Capabilities protocol.ReportCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(limited, &wire); err != nil {
		return protocol.ReportResponse{}, fmt.Errorf("decode server response: %w", err)
	}
	if wire.Accepted == nil {
		return protocol.ReportResponse{}, errors.New("decode server response: accepted field is required")
	}
	return protocol.ReportResponse{Accepted: *wire.Accepted, Reason: wire.Reason, Capabilities: wire.Capabilities}, nil
}

func randomID(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
