package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"404-probe/internal/releasemetadata"
)

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
		return &http.Response{StatusCode: http.StatusPartialContent, Body: io.NopCloser(bytes.NewBufferString(payload[7:])), Header: make(http.Header), Request: request}, nil
	})}
	engine := &Engine{config: EngineConfig{HTTPClient: client, DownloadAttempts: 3, RetryBaseDelay: time.Millisecond}}
	data, err := engine.fetch(context.Background(), "https://example.test/asset", 64)
	if err != nil || string(data) != payload || requests != 2 {
		t.Fatalf("data=%q requests=%d err=%v", data, requests, err)
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
