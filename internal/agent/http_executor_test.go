package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestHTTPExecutorSuccessfulGET(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %q", request.Method)
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("probe request included authorization")
		}
		requestSeen <- struct{}{}
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy"))
	}))
	defer server.Close()

	execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(server.URL, nil))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestSeen:
	default:
		t.Fatal("HTTP request did not reach target")
	}
	if !execution.Success || execution.ErrorCategory != "" {
		t.Fatalf("execution = %+v", execution)
	}
	result := execution.Result.HTTP
	if result == nil || result.StatusCode != http.StatusOK || result.BodyBytes != uint64(len("healthy")) || result.BodyTruncated || result.TotalMS <= 0 || result.TTFBMS <= 0 {
		t.Fatalf("result = %+v", result)
	}
	if execution.ResolvedIP != "127.0.0.1" && execution.ResolvedIP != "::1" {
		t.Fatalf("resolved IP = %q", execution.ResolvedIP)
	}
	if err := execution.Result.Validate(protocol.ProbeTypeHTTP); err != nil {
		t.Fatalf("result is invalid: %v", err)
	}
}

func TestHTTPExecutorStatusMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		status, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/"))
		if err != nil {
			t.Errorf("status path = %q", request.URL.Path)
			status = http.StatusInternalServerError
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	executor := NewHTTPExecutor()

	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		valid, err := executor.Execute(context.Background(), validHTTPExecutorJob(fmt.Sprintf("%s/%d", server.URL, status), nil))
		if err != nil || !valid.Success || valid.Result.HTTP.StatusCode != status {
			t.Fatalf("status %d execution = %+v err=%v", status, valid, err)
		}
	}
	expected := http.StatusOK
	mismatch, err := executor.Execute(context.Background(), validHTTPExecutorJob(fmt.Sprintf("%s/%d", server.URL, http.StatusNotFound), &expected))
	if err != nil || mismatch.Success || mismatch.ErrorCategory != "unexpected_status" || mismatch.Result.HTTP.StatusCode != http.StatusNotFound {
		t.Fatalf("expected-status execution = %+v err=%v", mismatch, err)
	}
}

func TestHTTPExecutorRejectsUnsupportedMethodWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	job := validHTTPExecutorJob(server.URL, nil)
	job.Config.HTTP.Method = http.MethodHead

	execution, err := NewHTTPExecutor().Execute(context.Background(), job)
	if err != nil || execution.Success || execution.ErrorCategory != "unsupported_method" || requests.Load() != 0 {
		t.Fatalf("execution = %+v err=%v requests=%d", execution, err, requests.Load())
	}
}

func TestHTTPExecutorRejectsUnsafeURLs(t *testing.T) {
	for name, target := range map[string]string{
		"empty":     "",
		"ftp":       "ftp://example.com/file",
		"file":      "file:///etc/passwd",
		"malformed": "http://[::1",
		"userinfo":  "http://user:pass@example.com/",
		"control":   "http://example.com/\n",
	} {
		t.Run(name, func(t *testing.T) {
			execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(target, nil))
			if err != nil || execution.Success || execution.ErrorCategory != "invalid_config" {
				t.Fatalf("execution = %+v err=%v", execution, err)
			}
			if err := execution.Result.Validate(protocol.ProbeTypeHTTP); err != nil {
				t.Fatalf("failure result is invalid: %v", err)
			}
		})
	}
}

func TestHTTPExecutorBodyLimit(t *testing.T) {
	for _, test := range []struct {
		name        string
		size        int
		wantSuccess bool
	}{
		{name: "exact limit", size: maxHTTPProbeBodyBytes, wantSuccess: true},
		{name: "over limit", size: maxHTTPProbeBodyBytes + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", test.size))
			}))
			defer server.Close()
			execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(server.URL, nil))
			if err != nil || execution.Success != test.wantSuccess {
				t.Fatalf("execution = %+v err=%v", execution, err)
			}
			if test.wantSuccess {
				if execution.Result.HTTP.BodyBytes != uint64(test.size) || execution.Result.HTTP.BodyTruncated {
					t.Fatalf("result = %+v", execution.Result.HTTP)
				}
			} else if execution.ErrorCategory != "response_body_too_large" || !execution.Result.HTTP.BodyTruncated || execution.Result.HTTP.BodyBytes != maxHTTPProbeBodyBytes+1 {
				t.Fatalf("result = %+v execution=%+v", execution.Result.HTTP, execution)
			}
		})
	}
}

func TestHTTPExecutorRedirectLimitAndSafety(t *testing.T) {
	for _, test := range []struct {
		name        string
		redirects   int
		wantSuccess bool
	}{
		{name: "five redirects", redirects: maxHTTPRedirects, wantSuccess: true},
		{name: "six redirects", redirects: maxHTTPRedirects + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := redirectTestServer(t, test.redirects)
			defer server.Close()
			execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(server.URL+"/0", nil))
			if err != nil || execution.Success != test.wantSuccess {
				t.Fatalf("execution = %+v err=%v", execution, err)
			}
			if !test.wantSuccess && execution.ErrorCategory != "redirect_limit" {
				t.Fatalf("category = %q", execution.ErrorCategory)
			}
		})
	}

	unsafe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		target := *request.URL
		target.Scheme = "http"
		target.Host = request.Host
		target.User = url.UserPassword("user", "secret")
		http.Redirect(w, request, target.String(), http.StatusFound)
	}))
	defer unsafe.Close()
	execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(unsafe.URL, nil))
	if err != nil || execution.Success || execution.ErrorCategory != "unsafe_redirect" {
		t.Fatalf("unsafe redirect execution = %+v err=%v", execution, err)
	}
}

func TestHTTPExecutorTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		started := time.Now()
		execution, err := NewHTTPExecutor().Execute(ctx, validHTTPExecutorJob(server.URL, nil))
		if err != nil || execution.Success || execution.ErrorCategory != "timeout" || time.Since(started) >= 90*time.Millisecond {
			t.Fatalf("execution = %+v err=%v elapsed=%s", execution, err, time.Since(started))
		}
	})

	t.Run("timeout while reading body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(100 * time.Millisecond)
			_, _ = io.WriteString(w, "late body")
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		execution, err := NewHTTPExecutor().Execute(ctx, validHTTPExecutorJob(server.URL, nil))
		if err != nil || execution.Success || execution.ErrorCategory != "timeout" {
			t.Fatalf("execution = %+v err=%v", execution, err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		requestStarted := make(chan struct{})
		requestCanceled := make(chan struct{})
		executor := NewHTTPExecutor()
		executor.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-request.Context().Done()
			close(requestCanceled)
			return nil, request.Context().Err()
		})}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan Execution, 1)
		go func() {
			execution, err := executor.Execute(ctx, validHTTPExecutorJob("http://example.test", nil))
			if err != nil {
				t.Errorf("execute: %v", err)
			}
			done <- execution
		}()
		waitSignal(t, requestStarted, "probe request start")
		cancel()
		waitSignal(t, requestCanceled, "probe request cancellation")
		select {
		case execution := <-done:
			if execution.Success || execution.ErrorCategory != "canceled" {
				t.Fatalf("execution = %+v", execution)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("executor did not stop after cancellation")
		}
	})
}

func TestHTTPExecutorNetworkAndTLSFailuresAreResults(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		executor := NewHTTPExecutor()
		executor.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: request.URL.String(), Err: errors.New("connection refused")}
		})}
		execution, err := executor.Execute(context.Background(), validHTTPExecutorJob("http://example.test", nil))
		if err != nil || execution.Success || execution.ErrorCategory != "network_error" {
			t.Fatalf("execution = %+v err=%v", execution, err)
		}
	})

	t.Run("default TLS verification", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		execution, err := NewHTTPExecutor().Execute(context.Background(), validHTTPExecutorJob(server.URL, nil))
		if err != nil || execution.Success || execution.ErrorCategory != "tls_error" {
			t.Fatalf("execution = %+v err=%v", execution, err)
		}
	})
}

func TestHTTPExecutorRejectsOtherProbeTypes(t *testing.T) {
	job := validWorkerJob()
	execution, err := NewHTTPExecutor().Execute(context.Background(), job)
	if !errors.Is(err, ErrUnsupportedProbeType) || execution.Success {
		t.Fatalf("execution = %+v err=%v", execution, err)
	}
}

func TestNewUsesHTTPExecutor(t *testing.T) {
	runner, err := New(Config{
		ServerURL: "https://example.test", AgentID: "agent", Token: "token",
		Interval: time.Second, Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := runner.executor.SupportedProbeTypes()
	if len(capabilities) != 1 || capabilities[0] != protocol.ProbeTypeHTTP {
		t.Fatalf("capabilities = %v", capabilities)
	}
}

func TestHTTPExecutorWorkerIntegration(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	job := validHTTPExecutorJob(target.URL, nil)
	resultReceived := make(chan protocol.JobResult, 1)
	var claims atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v1/agent/jobs/claim":
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			claim, decodeErr := protocol.DecodeClaimRequest(body)
			if decodeErr != nil {
				t.Errorf("decode claim: %v", decodeErr)
				return
			}
			if len(claim.SupportedProbeTypes) != 1 || claim.SupportedProbeTypes[0] != protocol.ProbeTypeHTTP {
				t.Errorf("capabilities = %v", claim.SupportedProbeTypes)
			}
			if claims.Add(1) == 1 {
				writeTestJSON(t, w, job)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/jobs/http-job/result":
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			result, decodeErr := protocol.DecodeJobResult(body, protocol.ProbeTypeHTTP)
			if decodeErr != nil {
				t.Errorf("decode result: %v", decodeErr)
				return
			}
			resultReceived <- result
			writeTestJSON(t, w, map[string]any{"accepted": true, "duplicate": false, "job_status": "finished"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer api.Close()

	runner := newJobTestRunner(t, api.URL, time.Second, time.Second, NewHTTPExecutor())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	select {
	case result := <-resultReceived:
		if !result.Success || result.Result.HTTP == nil || result.Result.HTTP.StatusCode != http.StatusNoContent || result.LeaseToken != job.LeaseToken || result.Attempt != job.Attempt {
			t.Fatalf("submitted result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not submit HTTP result")
	}
	cancel()
	waitSignal(t, done, "HTTP worker shutdown")
}

func validHTTPExecutorJob(target string, expectedStatus *int) protocol.Job {
	now := time.Now().UnixMilli()
	return protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion,
		JobID:           "http-job",
		ProbeType:       protocol.ProbeTypeHTTP,
		Config: protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{
			URL: target, Method: http.MethodGet, ExpectedStatus: expectedStatus,
		}},
		CreatedAt:      now,
		NotBefore:      now,
		ExpiresAt:      now + 60_000,
		TimeoutMS:      1_000,
		Attempt:        1,
		LeaseToken:     "lease-token",
		LeaseExpiresAt: now + 30_000,
	}
}

func redirectTestServer(t *testing.T, redirects int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		step, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/"))
		if err != nil {
			t.Errorf("redirect path = %q", request.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if step > 0 && request.Header.Get("Referer") != "" {
			t.Errorf("redirect request leaked Referer %q", request.Header.Get("Referer"))
		}
		if step < redirects {
			http.Redirect(w, request, fmt.Sprintf("/%d", step+1), http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}
