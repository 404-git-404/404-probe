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

	"404-probe/internal/protocol"
)

const (
	DefaultJobInterval  = 10 * time.Second
	maxJobResponseBytes = 64 * 1024
)

var ErrUnsupportedProbeType = errors.New("unsupported probe type")

// Execution is the probe-specific portion of a JobResult. The worker owns
// lease credentials, agent identity, and timing metadata.
type Execution struct {
	Success       bool
	ResolvedIP    string
	ErrorCategory string
	ErrorMessage  string
	Result        protocol.ProbeResult
}

// Executor reports only probe types it can genuinely execute. Execute must
// return promptly when its context is canceled.
type Executor interface {
	SupportedProbeTypes() []protocol.ProbeType
	Execute(context.Context, protocol.Job) (Execution, error)
}

type googleStatusCapability interface{ SupportsGoogleStatus() bool }

// UnsupportedExecutor advertises no capabilities and returns a stable error
// if called directly.
type UnsupportedExecutor struct{}

func (UnsupportedExecutor) SupportedProbeTypes() []protocol.ProbeType { return nil }

func (UnsupportedExecutor) Execute(_ context.Context, job protocol.Job) (Execution, error) {
	return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
}

type jobHTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type jobHTTPError struct {
	StatusCode int
	Code       string
}

func (e *jobHTTPError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("job API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("job API returned HTTP %d (%s)", e.StatusCode, e.Code)
}

func (c jobHTTPClient) claim(ctx context.Context, request protocol.ClaimRequest) (*protocol.Job, error) {
	var job protocol.Job
	status, body, err := c.postJSON(ctx, "/api/v1/agent/jobs/claim", request)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		if len(bytes.TrimSpace(body)) != 0 {
			return nil, errors.New("claim response has a body with HTTP 204")
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, decodeJobHTTPError(status, body)
	}
	if err := decodeStrictJobResponse(body, &job); err != nil {
		return nil, fmt.Errorf("decode claim response: %w", err)
	}
	return &job, nil
}

func (c jobHTTPClient) claimControl(ctx context.Context, request protocol.ControlClaimRequest) (*protocol.Job, error) {
	var job protocol.Job
	status, body, err := c.postJSON(ctx, "/api/v1/agent/control/claim", request)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		if len(bytes.TrimSpace(body)) != 0 {
			return nil, errors.New("control claim response has a body with HTTP 204")
		}
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, decodeJobHTTPError(status, body)
	}
	if err := decodeStrictJobResponse(body, &job); err != nil {
		return nil, fmt.Errorf("decode control claim response: %w", err)
	}
	if job.ProbeType != protocol.ProbeTypeSelectorSwitch {
		return nil, errors.New("control claim returned a non-selector operation")
	}
	return &job, nil
}

func (c jobHTTPClient) submit(ctx context.Context, jobID string, result protocol.JobResult) (bool, error) {
	status, body, err := c.postJSON(ctx, "/api/v1/agent/jobs/"+url.PathEscape(jobID)+"/result", result)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, decodeJobHTTPError(status, body)
	}
	var ack struct {
		Accepted  bool   `json:"accepted"`
		Duplicate bool   `json:"duplicate"`
		JobStatus string `json:"job_status"`
	}
	if err := decodeStrictJobResponse(body, &ack); err != nil {
		return false, fmt.Errorf("decode result response: %w", err)
	}
	if !ack.Accepted || ack.JobStatus != "finished" {
		return false, errors.New("result response is not an accepted finished acknowledgement")
	}
	return ack.Duplicate, nil
}

func (c jobHTTPClient) postJSON(ctx context.Context, path string, value any) (int, []byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, maxJobResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read job API response: %w", err)
	}
	if len(limited) > maxJobResponseBytes {
		return 0, nil, errors.New("job API response is too large")
	}
	return response.StatusCode, limited, nil
}

func decodeJobHTTPError(status int, body []byte) error {
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &response)
	return &jobHTTPError{StatusCode: status, Code: response.Error.Code}
}

func decodeStrictJobResponse(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (r *Runner) runJobWorker(ctx context.Context) {
	baseCapabilities := append([]protocol.ProbeType(nil), r.executor.SupportedProbeTypes()...)
	filtered := baseCapabilities[:0]
	for _, capability := range baseCapabilities {
		if capability != protocol.ProbeTypeGoogleStatus {
			filtered = append(filtered, capability)
		}
	}
	baseCapabilities = filtered
	capabilities := append([]protocol.ProbeType(nil), baseCapabilities...)
	if len(capabilities) == 0 && !r.clashIntegrationEnabled() && !r.googleStatusAvailable {
		r.logger.Info("job worker disabled; executor has no supported probe types")
		<-ctx.Done()
		return
	}

	client := jobHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}
	ticker := time.NewTicker(r.config.JobInterval)
	defer ticker.Stop()
	for {
		cycleCapabilities := append([]protocol.ProbeType(nil), baseCapabilities...)
		if r.clashIntegrationEnabled() && r.clashControlReady.Load() && !r.interactiveControlSupported.Load() {
			cycleCapabilities = append(cycleCapabilities, protocol.ProbeTypeSelectorSwitch)
		}
		if r.googleStatusAvailable && r.googleStatusSupported.Load() {
			cycleCapabilities = append(cycleCapabilities, protocol.ProbeTypeGoogleStatus)
		}
		if len(cycleCapabilities) != 0 {
			r.runJobCycle(ctx, client, protocol.ClaimRequest{ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, SupportedProbeTypes: cycleCapabilities})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) runJobCycle(ctx context.Context, client jobHTTPClient, request protocol.ClaimRequest) {
	job, err := client.claim(ctx, request)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("claim job failed; will retry", "error", err)
		}
		return
	}
	if job == nil {
		return
	}
	if job.ProbeType == protocol.ProbeTypeSelectorSwitch {
		r.runSelectorJob(ctx, client, *job)
		return
	}

	result := r.executeJob(ctx, *job)
	r.submitJobResult(ctx, client, job.JobID, result)
}

// runSelectorJob covers the legacy-to-interactive lane transition. The Server
// intentionally replays an active same-session lease, so two local workers can
// briefly receive the same job and attempt. Execute it once, retain the derived
// result for a failed submission retry, and never repeat the Clash mutation.
func (r *Runner) runSelectorJob(ctx context.Context, client jobHTTPClient, job protocol.Job) {
	r.selectorJobMu.Lock()
	defer r.selectorJobMu.Unlock()
	key := fmt.Sprintf("%s/%d", job.JobID, job.Attempt)
	if r.selectorJobKey != key {
		r.selectorJobKey = key
		r.selectorJobResult = protocol.JobResult{}
		r.selectorJobResultReady = false
		r.selectorJobSubmitted = false
	}
	if r.selectorJobSubmitted {
		r.logger.Debug("selector job replay already submitted", "job_id", job.JobID, "attempt", job.Attempt)
		return
	}
	if !r.selectorJobResultReady {
		r.selectorJobResult = r.executeJob(ctx, job)
		r.selectorJobResultReady = true
	}
	if r.submitJobResult(ctx, client, job.JobID, r.selectorJobResult) {
		r.selectorJobSubmitted = true
	}
}

func (r *Runner) submitJobResult(ctx context.Context, client jobHTTPClient, jobID string, result protocol.JobResult) bool {
	duplicate, err := client.submit(ctx, jobID, result)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		var responseError *jobHTTPError
		if errors.As(err, &responseError) && responseError.StatusCode == http.StatusConflict && responseError.Code == "lease_lost" {
			r.logger.Info("job result rejected because lease was lost", "job_id", jobID)
			return false
		}
		r.logger.Warn("submit job result failed; worker will continue", "job_id", jobID, "error", err)
		return false
	}
	r.logger.Debug("job result accepted", "job_id", jobID, "duplicate", duplicate)
	return true
}

func (r *Runner) runControlWorker(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-r.interactiveControlReady:
	}
	httpClient := *r.client
	httpClient.Timeout = 35 * time.Second
	client := jobHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: &httpClient}
	request := protocol.ControlClaimRequest{ProtocolVersion: protocol.ControlProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID}
	for ctx.Err() == nil {
		if !r.interactiveControlSupported.Load() || !r.clashControlReady.Load() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		job, err := client.claimControl(ctx, request)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("claim interactive control failed; will retry", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if job == nil {
			continue
		}
		r.runSelectorJob(ctx, client, *job)
	}
}

func (r *Runner) executeJob(ctx context.Context, job protocol.Job) protocol.JobResult {
	started := time.Now()
	executionContext, cancel := context.WithTimeout(ctx, time.Duration(job.TimeoutMS)*time.Millisecond)
	var execution Execution
	var err error
	if job.ProbeType == protocol.ProbeTypeSelectorSwitch {
		execution, err = r.executeSelectorSwitch(executionContext, job)
	} else {
		execution, err = r.executor.Execute(executionContext, job)
	}
	cancel()
	finished := time.Now()

	if err != nil {
		category := "executor_error"
		message := "probe executor failed"
		if errors.Is(err, ErrUnsupportedProbeType) {
			category = "unsupported_probe_type"
			message = "probe type is not supported by this executor"
		}
		execution = Execution{Success: false, ErrorCategory: category, ErrorMessage: message, Result: emptyProbeResult(job)}
	}
	result := protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion,
		LeaseToken:      job.LeaseToken,
		Attempt:         job.Attempt,
		AgentEpoch:      r.epoch,
		SessionID:       r.sessionID,
		StartedAt:       started.UnixMilli(),
		FinishedAt:      finished.UnixMilli(),
		DurationMS:      float64(finished.Sub(started)) / float64(time.Millisecond),
		Success:         execution.Success,
		ResolvedIP:      execution.ResolvedIP,
		ErrorCategory:   execution.ErrorCategory,
		ErrorMessage:    execution.ErrorMessage,
		Result:          execution.Result,
	}
	if err := result.Validate(job.ProbeType); err != nil {
		result.Success = false
		result.ResolvedIP = ""
		result.ErrorCategory = "invalid_executor_result"
		result.ErrorMessage = "probe executor returned an invalid result"
		result.Result = emptyProbeResult(job)
	}
	return result
}

func emptyProbeResult(job protocol.Job) protocol.ProbeResult {
	switch job.ProbeType {
	case protocol.ProbeTypeICMPPing:
		sent := 1
		if job.Config.ICMPPing != nil && job.Config.ICMPPing.Count > 0 {
			sent = job.Config.ICMPPing.Count
		}
		return protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{Sent: sent, PacketLossPercent: 100}}
	case protocol.ProbeTypeTCPConnect:
		return protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{}}
	case protocol.ProbeTypeHTTP:
		return protocol.ProbeResult{HTTP: &protocol.HTTPResult{}}
	case protocol.ProbeTypeSelectorSwitch:
		return protocol.ProbeResult{SelectorSwitch: &protocol.SelectorSwitchResult{}}
	case protocol.ProbeTypeGoogleStatus:
		return protocol.ProbeResult{GoogleStatus: &protocol.GoogleStatusResult{
			YouTube: protocol.YouTubeResult{Status: protocol.YouTubeUnknown},
			Search:  protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown},
			SignIn:  protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown},
			Gemini:  protocol.GeminiResult{Status: protocol.GeminiUnknown},
		}}
	default:
		return protocol.ProbeResult{}
	}
}

func (r *Runner) executeSelectorSwitch(ctx context.Context, job protocol.Job) (Execution, error) {
	if !r.clashIntegrationEnabled() || job.Config.SelectorSwitch == nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	client := clashClient{endpoint: r.config.ClashAPIURL, client: r.client}
	result, err := client.switchSelector(ctx, job.Config.SelectorSwitch.Selector, job.Config.SelectorSwitch.Choice)
	if err != nil {
		category := "clash_api_unavailable"
		message := "local Clash API is unavailable"
		var switchErr *selectorSwitchError
		if errors.As(err, &switchErr) {
			category, message = switchErr.category, switchErr.message
		}
		return Execution{Success: false, ErrorCategory: category, ErrorMessage: message,
			Result: protocol.ProbeResult{SelectorSwitch: &protocol.SelectorSwitchResult{}}}, nil
	}
	selectors, discoverErr := client.discover(ctx)
	if discoverErr == nil {
		snapshot := protocol.OutboundSnapshot{Available: true, Selectors: selectors}
		if r.interactiveControlSupported.Load() {
			snapshot.Status = protocol.OutboundStatusConnected
		}
		if publishErr := r.postOutboundSnapshot(ctx, snapshot); publishErr != nil && ctx.Err() == nil {
			r.logger.Warn("publish post-switch outbound snapshot failed; discovery will retry", "error", publishErr)
		}
	}
	return Execution{Success: true, Result: protocol.ProbeResult{SelectorSwitch: &result}}, nil
}
