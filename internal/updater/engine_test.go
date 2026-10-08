package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/releasemetadata"
)

func TestEngineStartPrecommitPersistenceFailuresPreservePriorStateAndRetry(t *testing.T) {
	stages := []string{"mkdir", "open", "write", "file_sync", "close", "rename"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			engine, request, _, _, _ := testEngine(t, false, false, time.Second)
			previous := State{
				OperationID: "fedcba9876543210fedcba9876543210", TargetVersion: "v0.8.1",
				Status: "failed", FailureCode: "previous_operation_failed", UpdatedAt: 12345,
			}
			previousBytes := marshalStateFile(t, previous)
			if err := os.MkdirAll(engine.config.StateDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(engine.config.StateDirectory, "operation.json")
			if err := os.WriteFile(statePath, previousBytes, 0600); err != nil {
				t.Fatal(err)
			}
			unchangedHealth := make(chan struct{})
			engine.mu.Lock()
			engine.state = previous
			engine.health = unchangedHealth
			engine.mu.Unlock()

			injected := errors.New("injected " + stage + " failure")
			ops := defaultRootFileOps()
			ops.mkdirAll = func(path string, mode os.FileMode) error {
				if stage == "mkdir" {
					return injected
				}
				return os.MkdirAll(path, mode)
			}
			ops.openFile = func(path string, flag int, mode os.FileMode) (rootStateFile, error) {
				if stage == "open" {
					return nil, injected
				}
				file, err := os.OpenFile(path, flag, mode)
				if err != nil {
					return nil, err
				}
				return &injectedRootStateFile{rootStateFile: file, stage: stage, err: injected}, nil
			}
			ops.rename = func(oldPath, newPath string) error {
				if stage == "rename" {
					return injected
				}
				return os.Rename(oldPath, newPath)
			}
			engine.startStateOps = ops

			if _, err := engine.Start(request); !errors.Is(err, injected) {
				t.Fatalf("Start error=%v, want injected %s failure", err, stage)
			}
			engine.mu.Lock()
			gotState, gotHealth, running := engine.state, engine.health, engine.running
			engine.mu.Unlock()
			if gotState != previous || gotHealth != unchangedHealth || running {
				t.Fatalf("failed start changed in-memory state: state=%+v healthChanged=%t running=%t", gotState, gotHealth != unchangedHealth, running)
			}
			statusRequest := Request{ProtocolVersion: ProtocolVersion, Action: ActionStatus, OperationID: previous.OperationID, TargetVersion: previous.TargetVersion}
			if got, err := engine.Status(statusRequest); err != nil || got != previous {
				t.Fatalf("previous terminal state status=%+v err=%v", got, err)
			}
			if got, err := os.ReadFile(statePath); err != nil || !bytes.Equal(got, previousBytes) {
				t.Fatalf("failed start changed persisted state: bytes=%q err=%v", got, err)
			}
			if _, err := os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
				t.Fatalf("precommit temporary file remains: %v", err)
			}

			// Repair the injected filesystem operation and submit the same request
			// again. It must perform a genuine run, not merely return stale state.
			engine.startStateOps = defaultRootFileOps()
			if _, err := engine.Start(request); err != nil {
				t.Fatalf("retry Start after repair: %v", err)
			}
			waitLocalStatus(t, engine, request, "health_check")
			healthyRequest := request
			healthyRequest.Action = ActionHealthy
			if _, err := engine.Healthy(healthyRequest); err != nil {
				t.Fatal(err)
			}
			waitLocalStatus(t, engine, request, "succeeded")
		})
	}
}

func TestEngineStartPostRenameSyncFailureConfirmsCandidateAndRunsOnce(t *testing.T) {
	engine, request, _, _, _ := testEngine(t, false, false, time.Second)
	var requests int
	var requestsMu sync.Mutex
	client := *engine.config.HTTPClient
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		return baseTransport.RoundTrip(request)
	})
	engine.config.HTTPClient = &client

	syncFailure := errors.New("injected parent directory sync failure")
	syncCalls := 0
	ops := defaultRootFileOps()
	ops.syncDirectory = func(string) error {
		syncCalls++
		return syncFailure
	}
	engine.startStateOps = ops

	state, err := engine.Start(request)
	if !errors.Is(err, syncFailure) || state.OperationID != request.OperationID || state.Status != "claimed" {
		t.Fatalf("Start state=%+v err=%v", state, err)
	}
	// A retry for the same operation is idempotent even though Start returned
	// the durability diagnostic after confirming the exact renamed bytes.
	retried, err := engine.Start(request)
	if err != nil || retried.OperationID != request.OperationID {
		t.Fatalf("same-operation retry state=%+v err=%v", retried, err)
	}
	waitLocalStatus(t, engine, request, "health_check")
	healthyRequest := request
	healthyRequest.Action = ActionHealthy
	if _, err := engine.Healthy(healthyRequest); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "succeeded")
	requestsMu.Lock()
	requestCount := requests
	requestsMu.Unlock()
	if requestCount != 3 || syncCalls != 1 {
		t.Fatalf("run count evidence: HTTP requests=%d parent sync calls=%d, want 3 and 1", requestCount, syncCalls)
	}
}

func TestEngineStartPostRenameSyncFailureDoesNotPublishUnconfirmedCandidate(t *testing.T) {
	engine, request, _, _, _ := testEngine(t, false, false, time.Second)
	var requests int
	var requestsMu sync.Mutex
	client := *engine.config.HTTPClient
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		return baseTransport.RoundTrip(request)
	})
	engine.config.HTTPClient = &client
	previous := State{
		OperationID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", TargetVersion: "v0.8.1",
		Status: "succeeded", UpdatedAt: 54321,
	}
	previousBytes := marshalStateFile(t, previous)
	if err := os.MkdirAll(engine.config.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(engine.config.StateDirectory, "operation.json")
	if err := os.WriteFile(statePath, previousBytes, 0600); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.state = previous
	engine.mu.Unlock()

	syncFailure := errors.New("injected parent directory sync failure")
	ops := defaultRootFileOps()
	ops.syncDirectory = func(directory string) error {
		if err := os.WriteFile(filepath.Join(directory, "operation.json"), previousBytes, 0600); err != nil {
			return errors.Join(syncFailure, err)
		}
		return syncFailure
	}
	engine.startStateOps = ops

	if _, err := engine.Start(request); !errors.Is(err, syncFailure) {
		t.Fatalf("Start error=%v, want injected sync failure", err)
	}
	engine.mu.Lock()
	gotState, running, health := engine.state, engine.running, engine.health
	engine.mu.Unlock()
	if gotState != previous || running || health != nil {
		t.Fatalf("unconfirmed commit changed memory: state=%+v running=%t health=%v", gotState, running, health)
	}
	if got, err := os.ReadFile(statePath); err != nil || !bytes.Equal(got, previousBytes) {
		t.Fatalf("unconfirmed commit did not preserve prior disk state: bytes=%q err=%v", got, err)
	}
	if _, err := os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("unexpected temporary file: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	requestsMu.Lock()
	requestCount := requests
	requestsMu.Unlock()
	if requestCount != 0 {
		t.Fatalf("unconfirmed candidate started an updater run: HTTP requests=%d", requestCount)
	}
}

func TestEngineNewEngineLoadsPersistedStateAndRecoverMarksInterruptedStart(t *testing.T) {
	engine, _, _, _, _ := testEngine(t, false, false, time.Second)
	persisted := State{
		OperationID: "dddddddddddddddddddddddddddddddd", TargetVersion: "v0.8.1",
		Status: "claimed", UpdatedAt: 98765,
	}
	if err := os.MkdirAll(engine.config.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine.config.StateDirectory, "operation.json"), marshalStateFile(t, persisted), 0600); err != nil {
		t.Fatal(err)
	}

	recovered, err := NewEngine(engine.config)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.state != persisted {
		t.Fatalf("NewEngine state=%+v, want persisted=%+v", recovered.state, persisted)
	}
	request := Request{ProtocolVersion: ProtocolVersion, Action: ActionStatus, OperationID: persisted.OperationID, TargetVersion: persisted.TargetVersion}
	recovered.Recover()
	failed := waitLocalStatus(t, recovered, request, "failed")
	if failed.FailureCode != "updater_restarted" {
		t.Fatalf("recovered interrupted operation=%+v", failed)
	}
}

type injectedRootStateFile struct {
	rootStateFile
	stage string
	err   error
}

func (f *injectedRootStateFile) Write(data []byte) (int, error) {
	if f.stage == "write" {
		return 0, f.err
	}
	return f.rootStateFile.Write(data)
}

func (f *injectedRootStateFile) Sync() error {
	if f.stage == "file_sync" {
		return f.err
	}
	return f.rootStateFile.Sync()
}

func (f *injectedRootStateFile) Close() error {
	closeErr := f.rootStateFile.Close()
	if f.stage == "close" {
		return errors.Join(closeErr, f.err)
	}
	return closeErr
}

func marshalStateFile(t *testing.T, state State) []byte {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestEngineVerifiedUpgradeAndHealthCommit(t *testing.T) {
	engine, request, live, previous, _ := testEngine(t, false, false, 2*time.Second)
	committed := make(chan struct{}, 1)
	engine.config.OnCommitted = func() { committed <- struct{}{} }
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "health_check")
	request.Action = ActionHealthy
	if _, err := engine.Healthy(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "succeeded")
	if data, err := os.ReadFile(live); err != nil || string(data) != "new-agent" {
		t.Fatalf("live=%q err=%v", data, err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("previous binary was not cleaned: %v", err)
	}
	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("successful target health did not request updater refresh")
	}
}

func TestProductionReleaseBaseIsOfficialRepository(t *testing.T) {
	if officialReleaseBase != "https://github.com/404-git-404/404-probe/releases/download/" {
		t.Fatalf("unexpected production release base %q", officialReleaseBase)
	}
}

func TestFetchRetriesTransientStatusAndDoesNotRetryPermanentStatus(t *testing.T) {
	var transientRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		transientRequests++
		if transientRequests == 1 {
			http.Error(w, "temporary", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte("verified"))
	}))
	defer server.Close()
	engine := &Engine{config: EngineConfig{HTTPClient: server.Client(), DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), server.URL+"/asset", 64)
	if err != nil || string(data) != "verified" || transientRequests != 2 {
		t.Fatalf("data=%q requests=%d err=%v", data, transientRequests, err)
	}

	var permanentRequests int
	permanent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		permanentRequests++
		http.NotFound(w, nil)
	}))
	defer permanent.Close()
	engine.config.HTTPClient = permanent.Client()
	if _, err := engine.fetch(context.Background(), permanent.URL+"/missing", 64); err == nil || permanentRequests != 1 {
		t.Fatalf("permanent status requests=%d err=%v", permanentRequests, err)
	}
}

func TestFetchResumesPartialResponse(t *testing.T) {
	const payload = "partial-download"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			if request.Header.Get("Range") != "" {
				t.Fatalf("first request unexpectedly had Range")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(payload[:7])}), Header: make(http.Header), Request: request}, nil
		}
		if got := request.Header.Get("Range"); got != "bytes=7-" {
			t.Fatalf("resume Range=%q", got)
		}
		header := make(http.Header)
		header.Set("Content-Range", fmt.Sprintf("bytes 7-%d/%d", len(payload)-1, len(payload)))
		return &http.Response{StatusCode: http.StatusPartialContent, Body: io.NopCloser(bytes.NewBufferString(payload[7:])), Header: header, Request: request}, nil
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != payload || requests != 2 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
	}
}

func TestFetchRejectsWrongResumeRangeAndRestarts(t *testing.T) {
	const payload = "partial-download"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(payload[:7])}), Header: make(http.Header), Request: request}, nil
		case 2:
			header := make(http.Header)
			header.Set("Content-Range", fmt.Sprintf("bytes 6-%d/%d", len(payload)-1, len(payload)))
			return &http.Response{StatusCode: http.StatusPartialContent, Body: io.NopCloser(bytes.NewBufferString(payload[7:])), Header: header, Request: request}, nil
		default:
			if request.Header.Get("Range") != "" {
				t.Fatalf("restart unexpectedly used Range %q", request.Header.Get("Range"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(payload)), Header: make(http.Header), Request: request}, nil
		}
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != payload || requests != 3 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
	}
}

func TestFetchRestartsAfterRangeNotSatisfiable(t *testing.T) {
	const payload = "partial-download"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(payload[:7])}), Header: make(http.Header), Request: request}, nil
		case 2:
			return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
		default:
			if request.Header.Get("Range") != "" {
				t.Fatalf("restart unexpectedly used Range %q", request.Header.Get("Range"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(payload)), Header: make(http.Header), Request: request}, nil
		}
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != payload || requests != 3 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
	}
}

func TestFetchRestartsWhenServerIgnoresRange(t *testing.T) {
	const payload = "partial-download"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(payload[:7])}), Header: make(http.Header), Request: request}, nil
		}
		if request.Header.Get("Range") != "bytes=7-" {
			t.Fatalf("Range=%q", request.Header.Get("Range"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(payload)), Header: make(http.Header), Request: request}, nil
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 2, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != payload || requests != 2 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
	}
}

func TestFetchUsesIfRangeAndRestartsChangedObject(t *testing.T) {
	const oldPayload = "old-object-data"
	const newPayload = "replacement-object"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			header := make(http.Header)
			header.Set("ETag", `"old"`)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(oldPayload[:5])}), Header: header, Request: request}, nil
		}
		if request.Header.Get("Range") != "bytes=5-" || request.Header.Get("If-Range") != `"old"` {
			t.Fatalf("Range=%q If-Range=%q", request.Header.Get("Range"), request.Header.Get("If-Range"))
		}
		header := make(http.Header)
		header.Set("ETag", `"new"`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(newPayload)), Header: header, Request: request}, nil
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 2, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != newPayload || requests != 2 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
	}
}

func TestFetchRejectsTruncatedResumeChunk(t *testing.T) {
	const payload = "partial-download"
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&partialErrorReader{data: []byte(payload[:7])}), Header: make(http.Header), Request: request}, nil
		}
		header := make(http.Header)
		header.Set("Content-Range", fmt.Sprintf("bytes 7-%d/%d", len(payload)-1, len(payload)))
		return &http.Response{StatusCode: http.StatusPartialContent, Body: io.NopCloser(strings.NewReader(payload[7:10])), Header: header, Request: request}, nil
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 2, RetryBaseDelay: time.Millisecond}}
	if data, err := engine.fetch(context.Background(), "https://example.test/asset", 64); err == nil || string(data) == payload {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestFetchUsesVersionBoundOfficialAPIAssetFallback(t *testing.T) {
	oldReleaseBase, oldAPIBase := officialReleaseBase, officialReleaseAPIBase
	officialReleaseBase = "https://download.example/releases/download/"
	officialReleaseAPIBase = "https://api.example/repos/project/releases/"
	t.Cleanup(func() { officialReleaseBase, officialReleaseAPIBase = oldReleaseBase, oldAPIBase })
	requests := make([]string, 0, 3)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		switch request.URL.String() {
		case "https://download.example/releases/download/v1.2.3/asset":
			return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
		case "https://api.example/repos/project/releases/tags/v1.2.3":
			body := `{"tag_name":"v1.2.3","assets":[{"name":"asset","url":"https://api.example/repos/project/releases/assets/42"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
		case "https://api.example/repos/project/releases/assets/42":
			if request.Header.Get("Accept") != "application/octet-stream" {
				t.Fatalf("Accept=%q", request.Header.Get("Accept"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("verified")), Header: make(http.Header), Request: request}, nil
		default:
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), officialReleaseBase+"v1.2.3/asset", 64)
	if err != nil || string(data) != "verified" || len(requests) != 3 {
		t.Fatalf("data=%q requests=%v err=%v", data, requests, err)
	}
}

func TestFetchHonorsCancellationDuringRetry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 5, RetryBaseDelay: time.Hour}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.fetch(ctx, "https://example.test/asset", 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestFetchHonorsTotalDeadlineDuringRetry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 5, RetryBaseDelay: time.Hour}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := engine.fetch(ctx, "https://example.test/asset", 64); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline took %s", elapsed)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type partialErrorReader struct {
	data []byte
	done bool
}

func (r *partialErrorReader) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(buffer, r.data), errors.New("connection reset")
}

func TestEngineHealthTimeoutRollsBack(t *testing.T) {
	engine, request, live, _, commands := testEngine(t, false, false, 25*time.Millisecond)
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "rolled_back")
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatalf("live=%q err=%v", data, err)
	}
	candidate := filepath.Join(filepath.Dir(filepath.Dir(live)), "state", "candidate")
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate was not cleaned after rollback: %v", err)
	}
	commands.mu.Lock()
	defer commands.mu.Unlock()
	if fmt.Sprint(commands.values) != "[restart stop restart]" {
		t.Fatalf("service commands=%v", commands.values)
	}
}

func TestEngineChecksumMismatchLeavesOldBinary(t *testing.T) {
	engine, request, live, _, _ := testEngine(t, true, false, time.Second)
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "checksum_mismatch" {
		t.Fatalf("failure=%+v", state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatalf("live=%q err=%v", data, err)
	}
}

func TestEngineRejectsReleaseAndChecksumManifestMismatch(t *testing.T) {
	engine, request, live, _, _ := testEngine(t, false, true, time.Second)
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "checksum_manifest_mismatch" {
		t.Fatalf("failure=%+v", state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatalf("live=%q err=%v", data, err)
	}
}

func TestEngineRejectsInvalidStaticCandidateBuildInfo(t *testing.T) {
	tests := map[string]func(*CandidateBuildInfo){
		"candidate_revision_mismatch": func(info *CandidateBuildInfo) { info.Commit = "ffffffffffffffffffffffffffffffffffffffff" },
		"candidate_dirty":             func(info *CandidateBuildInfo) { info.Dirty = true },
		"candidate_platform_mismatch": func(info *CandidateBuildInfo) { info.GOARCH = "arm64" },
	}
	for want, mutate := range tests {
		t.Run(want, func(t *testing.T) {
			engine, request, live, _, _ := testEngine(t, false, false, time.Second)
			inspect := engine.config.Inspect
			engine.config.Inspect = func(path string) (CandidateBuildInfo, error) {
				info, err := inspect(path)
				mutate(&info)
				return info, err
			}
			if _, err := engine.Start(request); err != nil {
				t.Fatal(err)
			}
			state := waitLocalStatus(t, engine, request, "failed")
			if state.FailureCode != want {
				t.Fatalf("failure=%+v", state)
			}
			if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
				t.Fatalf("live=%q err=%v", data, err)
			}
		})
	}
}

type commandLog struct {
	mu     sync.Mutex
	values []string
}

func testEngine(t *testing.T, badChecksum, manifestMismatch bool, healthTimeout time.Duration) (*Engine, Request, string, string, *commandLog) {
	t.Helper()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	artifact := []byte("new-agent")
	digest := sha256.Sum256(artifact)
	declaredDigest := digest
	if badChecksum {
		declaredDigest[0] ^= 0xff
	}
	checksum := fmt.Sprintf("%x  404-probe-agent-linux-amd64\n", declaredDigest)
	metadataDigest := declaredDigest
	if manifestMismatch {
		metadataDigest[1] ^= 0xff
	}
	metadata, err := releasemetadata.Encode(releasemetadata.Document{SchemaVersion: 1, Version: "v0.8.1", Commit: commit, Assets: []releasemetadata.Asset{
		{Name: "404-probe-agent-linux-amd64", GOOS: "linux", GOARCH: "amd64", SHA256: fmt.Sprintf("%x", metadataDigest)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "RELEASE-METADATA.json":
			_, _ = w.Write(metadata)
		case "SHA256SUMS":
			_, _ = w.Write([]byte(checksum))
		case "404-probe-agent-linux-amd64":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	binDirectory := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(binDirectory, "agent")
	staged := filepath.Join(binDirectory, ".candidate")
	previous := filepath.Join(binDirectory, ".previous")
	if err := os.WriteFile(live, []byte("old-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	commands := &commandLog{}
	engine, err := NewEngine(EngineConfig{CurrentVersion: "v0.8.0", GOOS: "linux", GOARCH: "amd64", StateDirectory: stateDirectory,
		LiveBinary: live, StagedBinary: staged, PreviousBinary: previous, ReleaseBase: server.URL,
		Inspect: func(path string) (CandidateBuildInfo, error) {
			metadata, err := os.Stat(path)
			if err != nil {
				return CandidateBuildInfo{}, err
			}
			if got := metadata.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
				return CandidateBuildInfo{}, fmt.Errorf("candidate mode=%#o want=0600", got)
			}
			return CandidateBuildInfo{Path: "404-probe/cmd/agent", Commit: commit, GOOS: "linux", GOARCH: "amd64"}, nil
		}, ServiceCommand: func(_ context.Context, action string) error {
			commands.mu.Lock()
			defer commands.mu.Unlock()
			commands.values = append(commands.values, action)
			return nil
		}, HealthTimeout: healthTimeout})
	if err != nil {
		t.Fatal(err)
	}
	return engine, Request{ProtocolVersion: ProtocolVersion, Action: ActionStart, OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v0.8.1"}, live, previous, commands
}

func waitLocalStatus(t *testing.T, engine *Engine, request Request, want string) State {
	t.Helper()
	request.Action = ActionStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err := engine.Status(request)
		if err == nil && state.Status == want {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, err := engine.Status(request)
	t.Fatalf("status=%+v err=%v want=%s", state, err, want)
	return State{}
}
