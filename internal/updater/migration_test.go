package updater

import (
	"404-probe/internal/releasemetadata"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func versionedEngineFixture(t *testing.T, target string, schema int) (*Engine, Request, string, string, *commandLog) {
	t.Helper()
	engine, request, live, previous, commands := testEngine(t, false, false, time.Second)
	artifact := []byte("new-agent")
	hash := sha256.Sum256(artifact)
	document := releasemetadata.Document{SchemaVersion: schema, Version: target, Commit: "0123456789abcdef0123456789abcdef01234567", Assets: []releasemetadata.Asset{{Name: "404-probe-agent-linux-amd64", GOOS: "linux", GOARCH: "amd64", SHA256: fmt.Sprintf("%x", hash)}}}
	if schema == 2 {
		document.Compatibility = &releasemetadata.Compatibility{MinServerVersion: "v1.0.1", UpgradeProtocol: 2}
	}
	metadata, err := releasemetadata.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	sums := fmt.Sprintf("%x  404-probe-agent-linux-amd64\n", hash)
	if schema == 2 {
		metadataHash := sha256.Sum256(metadata)
		sums += fmt.Sprintf("%x  RELEASE-METADATA.json\n", metadataHash)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "RELEASE-METADATA.json":
			w.Write(metadata)
		case "SHA256SUMS":
			w.Write([]byte(sums))
		case "404-probe-agent-linux-amd64":
			w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(source.Close)
	engine.config.ReleaseBase = source.URL
	originalInspect := engine.config.Inspect
	engine.config.Inspect = func(path string) (CandidateBuildInfo, error) {
		if path == engine.config.LiveBinary {
			data, err := os.ReadFile(path)
			info := CandidateBuildInfo{Path: "404-probe/cmd/agent", GOOS: "linux", GOARCH: "amd64", Version: target, Commit: document.Commit}
			if string(data) == "old-agent" {
				info.Version = engine.config.CurrentVersion
				info.Commit = strings.Repeat("f", 40)
			}
			return info, err
		}
		info, err := originalInspect(path)
		info.Version = target
		return info, err
	}
	request.TargetVersion = target
	if schema == 2 {
		request.ProtocolVersion = 2
		request.Channel = "beta"
		request.TargetCommit = document.Commit
		request.ServerVersion = "v1.0.1"
	}
	return engine, request, live, previous, commands
}

func TestLocalMigrationPreservesExistingBinaryUntilRealHealth(t *testing.T) {
	for _, from := range []string{"v0.9.3", "v1.0.0", "v1.0.1-beta.1", "v1.0.1-beta.2"} {
		t.Run(from, func(t *testing.T) {
			engine, request, live, previous, _ := versionedEngineFixture(t, "v1.0.1", 1)
			engine.config.LocalMigration = true
			engine.config.CurrentVersion = from
			if _, err := engine.Start(request); err != nil {
				t.Fatal(err)
			}
			state := waitLocalStatus(t, engine, request, "health_check")
			if !state.LocalMigration {
				t.Fatal("root authorization lost")
			}
			persisted, err := readStateFile(filepath.Join(engine.config.StateDirectory, "operation.json"))
			if err != nil || !persisted.LocalMigration {
				t.Fatal("persistent authorization", err)
			}
			if old, err := os.ReadFile(previous); err != nil || string(old) != "old-agent" {
				t.Fatal("previous binary not preserved")
			}
			request.Action = ActionHealthy
			if _, err := engine.Healthy(request); err != nil {
				t.Fatal(err)
			}
			waitLocalStatus(t, engine, request, "succeeded")
			if installed, err := os.ReadFile(live); err != nil || string(installed) != "new-agent" {
				t.Fatal("installed binary", err)
			}
		})
	}
}

func TestV2InterruptedStatesRetainAuthorizationAndRecover(t *testing.T) {
	for _, status := range []string{"claimed", "downloading", "verifying", "staging", "installing", "restarting", "health_check"} {
		t.Run(status, func(t *testing.T) {
			engine, request, live, previous, _ := versionedEngineFixture(t, "v1.0.2-beta.1", 2)
			engine.config.CurrentVersion = "v1.0.1"
			saved := State{OperationID: request.OperationID, TargetVersion: request.TargetVersion, ReleaseCommit: request.TargetCommit, ProtocolVersion: 2, Channel: request.Channel, TargetCommit: request.TargetCommit, ServerVersion: request.ServerVersion, Status: status}
			if err := writeRootFile(filepath.Join(engine.config.StateDirectory, "operation.json"), marshalStateFile(t, saved), 0600); err != nil {
				t.Fatal(err)
			}
			postInstall := status == "installing" || status == "restarting" || status == "health_check"
			if postInstall {
				if err := os.Link(live, previous); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(live); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(live, []byte("new-agent"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := NewEngine(engine.config)
			if err != nil {
				t.Fatal(err)
			}
			recovered.Recover()
			if !postInstall {
				state := waitLocalStatus(t, recovered, request, "failed")
				if !stateMatches(state, request) || state.FailureCode != "updater_restarted" {
					t.Fatal("authorization changed", state)
				}
				return
			}
			waitLocalStatus(t, recovered, request, "health_check")
			request.Action = ActionHealthy
			wrong := request
			wrong.TargetCommit = strings.Repeat("f", 40)
			if _, err := recovered.Healthy(wrong); err == nil {
				t.Fatal("different target authorization confirmed health")
			}
			if _, err := recovered.Healthy(request); err != nil {
				t.Fatal(err)
			}
			waitLocalStatus(t, recovered, request, "succeeded")
		})
	}
}

func TestActualVersionMismatchFailsBeforeServiceOrBinaryMutation(t *testing.T) {
	engine, request, live, _, commands := versionedEngineFixture(t, "v1.0.1", 1)
	original := engine.config.Inspect
	engine.config.Inspect = func(path string) (CandidateBuildInfo, error) {
		info, err := original(path)
		info.Version = "v1.0.2-beta.1"
		return info, err
	}
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "candidate_version_mismatch" {
		t.Fatal(state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatal("live changed", err)
	}
	commands.mu.Lock()
	defer commands.mu.Unlock()
	if len(commands.values) != 0 {
		t.Fatal("service changed", commands.values)
	}
}

func TestInstallingInterruptedBeforeSwapRestartsOriginalAgent(t *testing.T) {
	engine, request, live, _, commands := versionedEngineFixture(t, "v1.0.1", 1)
	engine.config.CurrentVersion = "v0.9.3"
	saved := State{OperationID: request.OperationID, TargetVersion: request.TargetVersion, SourceVersion: "v0.9.3", ReleaseCommit: "0123456789abcdef0123456789abcdef01234567", Status: "installing"}
	if err := writeRootFile(filepath.Join(engine.config.StateDirectory, "operation.json"), marshalStateFile(t, saved), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewEngine(engine.config)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Recover()
	state := waitLocalStatus(t, recovered, request, "failed")
	if state.FailureCode != "updater_restarted" {
		t.Fatal(state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatal("original changed", err)
	}
	commands.mu.Lock()
	defer commands.mu.Unlock()
	if fmt.Sprint(commands.values) != "[restart]" {
		t.Fatal(commands.values)
	}
}

func TestRollbackSignalsDaemonRestartAndAllowsFreshActualVersionRetry(t *testing.T) {
	engine, request, live, _, _ := versionedEngineFixture(t, "v1.0.1", 1)
	engine.config.CurrentVersion = "v0.9.3"
	restarted := make(chan struct{}, 1)
	engine.config.OnRolledBack = func() { restarted <- struct{}{} }
	restarts := 0
	engine.config.ServiceCommand = func(_ context.Context, action string) error {
		if action == "restart" {
			restarts++
			if restarts == 1 {
				return fmt.Errorf("new Agent startup failed")
			}
		}
		return nil
	}
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, engine, request, "rolled_back")
	if repeated, err := engine.Start(request); err != nil || repeated.Status != "rolled_back" {
		t.Fatal("old Agent retry reran terminal operation", repeated, err)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("daemon did not restart after rollback")
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatal("rollback did not restore actual binary")
	}
	config := engine.config
	config.CurrentVersion = "v0.9.3"
	fresh, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = "abcdef0123456789abcdef0123456789"
	if _, err := fresh.Start(request); err != nil {
		t.Fatal("retry rejected against restored version", err)
	}
	waitLocalStatus(t, fresh, request, "health_check")
	request.Action = ActionHealthy
	if _, err := fresh.Healthy(request); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, fresh, request, "succeeded")
}

func TestRollbackStartFailureCannotPublishRolledBackOrHandoff(t *testing.T) {
	engine, request, live, _, _ := versionedEngineFixture(t, "v1.0.1", 1)
	engine.config.ServiceCommand = func(_ context.Context, action string) error {
		if action == "restart" {
			return fmt.Errorf("main process failed execution")
		}
		return nil
	}
	engine.config.OnRolledBack = func() { t.Error("handoff called for failed restored process") }
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "rollback_failed" {
		t.Fatal(state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatal("original not restored", err)
	}
}

func TestRollbackJournalFailureCannotHandoff(t *testing.T) {
	engine, request, live, previous, _ := versionedEngineFixture(t, "v1.0.1", 1)
	if err := os.Link(live, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("new-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	engine.state = State{OperationID: request.OperationID, TargetVersion: request.TargetVersion, Status: "health_check"}
	engine.running = true
	engine.config.OnRolledBack = func() { t.Error("handoff called before terminal journal persisted") }
	// A directory at the journal destination rejects the atomic rename.
	if err := os.MkdirAll(engine.config.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(engine.config.StateDirectory, "operation.json"), 0700); err != nil {
		t.Fatal(err)
	}
	engine.finishFailure(true, "health_timeout")
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "rollback_failed" {
		t.Fatal(state)
	}
	if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
		t.Fatal("original not restored", err)
	}
}
