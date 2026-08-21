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
	capabilities := append([]protocol.ProbeType(nil), r.executor.SupportedProbeTypes()...)
	if len(capabilities) == 0 {
		r.logger.Info("job worker disabled; executor has no supported probe types")
		<-ctx.Done()
		return
	}

	client := jobHTTPClient{baseURL: strings.TrimRight(r.config.ServerURL, "/"), token: r.config.Token, client: r.client}
	request := protocol.ClaimRequest{
		ProtocolVersion:     protocol.JobProtocolVersion,
		AgentEpoch:          r.epoch,
		SessionID:           r.sessionID,
		SupportedProbeTypes: capabilities,
	}
	if err := request.Validate(); err != nil {
		r.logger.Error("job worker disabled; executor capabilities are invalid", "error", err)
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(r.config.JobInterval)
	defer ticker.Stop()
	for {
		r.runJobCycle(ctx, client, request)
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

	result := r.executeJob(ctx, *job)
	duplicate, err := client.submit(ctx, job.JobID, result)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		var responseError *jobHTTPError
		if errors.As(err, &responseError) && responseError.StatusCode == http.StatusConflict && responseError.Code == "lease_lost" {
			r.logger.Info("job result rejected because lease was lost", "job_id", job.JobID)
			return
		}
		r.logger.Warn("submit job result failed; worker will continue", "job_id", job.JobID, "error", err)
		return
	}
	r.logger.Debug("job result accepted", "job_id", job.JobID, "duplicate", duplicate)
}

func (r *Runner) executeJob(ctx context.Context, job protocol.Job) protocol.JobResult {
	started := time.Now()
	executionContext, cancel := context.WithTimeout(ctx, time.Duration(job.TimeoutMS)*time.Millisecond)
	execution, err := r.executor.Execute(executionContext, job)
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
	default:
		return protocol.ProbeResult{}
	}
}
