package updater

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
)

func TestEngineVerifiedUpgradeAndHealthCommit(t *testing.T) {
	engine, request, live, previous, _ := testEngine(t, false, 2*time.Second)
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
}

func TestEngineHealthTimeoutRollsBack(t *testing.T) {
	engine, request, live, _, commands := testEngine(t, false, 25*time.Millisecond)
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "rolled_back")
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatalf("live=%q err=%v", data, err)
	}
	commands.mu.Lock()
	defer commands.mu.Unlock()
	if fmt.Sprint(commands.values) != "[restart stop restart]" {
		t.Fatalf("service commands=%v", commands.values)
	}
}

func TestEngineChecksumMismatchLeavesOldBinary(t *testing.T) {
	engine, request, live, _, _ := testEngine(t, true, time.Second)
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

type commandLog struct {
	mu     sync.Mutex
	values []string
}

func testEngine(t *testing.T, badChecksum bool, healthTimeout time.Duration) (*Engine, Request, string, string, *commandLog) {
	t.Helper()
	artifact := []byte("new-agent")
	digest := sha256.Sum256(artifact)
	checksum := fmt.Sprintf("%x  404-probe-agent-linux-amd64\n", digest)
	if badChecksum {
		checksum = fmt.Sprintf("%064x  404-probe-agent-linux-amd64\n", 1)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
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
		Inspect: func(context.Context, string) (buildinfo.Info, error) {
			return buildinfo.Info{Version: "v0.8.1", Commit: "0123456789abcdef", Dirty: false}, nil
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
