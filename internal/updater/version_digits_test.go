package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTwoDigitStartCommitRollbackAndDuplicate(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(map[bool]string{true: "commit", false: "rollback"}[healthy], func(t *testing.T) {
			e, r, live, previous, commands := versionedEngineFixture(t, "v1.0.10", 2)
			e.config.CurrentVersion = "v1.0.9"
			r.Channel = "stable"
			r.ServerVersion = "v1.0.10"
			if !healthy {
				e.config.HealthTimeout = 40 * time.Millisecond
			}
			if _, err := e.Start(r); err != nil {
				t.Fatal(err)
			}
			waitLocalStatus(t, e, r, "health_check")
			if _, err := e.Start(r); err != nil {
				t.Fatal("active replay", err)
			}
			wantStatus, wantBinary := "rolled_back", "old-agent"
			if healthy {
				health := r
				health.Action = ActionHealthy
				if _, err := e.Healthy(health); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantBinary = "succeeded", "new-agent"
			}
			state := waitLocalStatus(t, e, r, wantStatus)
			commands.mu.Lock()
			before := len(commands.values)
			commands.mu.Unlock()
			if replay, err := e.Start(r); err != nil || replay != state {
				t.Fatal("terminal replay", replay, err)
			}
			commands.mu.Lock()
			after := len(commands.values)
			commands.mu.Unlock()
			if before != after {
				t.Fatal("replay changed service commands")
			}
			if data, err := os.ReadFile(live); err != nil || string(data) != wantBinary {
				t.Fatal(string(data), err)
			}
			if _, err := os.Stat(previous); !os.IsNotExist(err) {
				t.Fatal("previous not cleaned", err)
			}
		})
	}
}

func TestTwoDigitSameAndDowngradeStartPreserveFilesAndServices(t *testing.T) {
	for _, target := range []string{"v1.0.9", "v1.0.10"} {
		t.Run(target, func(t *testing.T) {
			e, r, live, previous, commands := versionedEngineFixture(t, target, 2)
			e.config.CurrentVersion = "v1.0.10"
			r.Channel = "stable"
			r.ServerVersion = "v1.0.10"
			if _, err := e.Start(r); err == nil {
				t.Fatal("non-upgrade accepted")
			}
			if data, err := os.ReadFile(live); err != nil || string(data) != "old-agent" {
				t.Fatal(string(data), err)
			}
			for _, path := range []string{previous, e.config.StagedBinary, filepath.Join(e.config.StateDirectory, "operation.json")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("unexpected mutation", path, err)
				}
			}
			commands.mu.Lock()
			defer commands.mu.Unlock()
			if len(commands.values) != 0 {
				t.Fatal(commands.values)
			}
		})
	}
}

func TestTwoDigitStoredJournalRejectsSameAndDescendingSource(t *testing.T) {
	for _, target := range []string{"v1.0.9", "v1.0.10"} {
		e, r, _, _, _ := versionedEngineFixture(t, target, 2)
		saved := State{OperationID: r.OperationID, SourceVersion: "v1.0.10", TargetVersion: target, ProtocolVersion: 2, Channel: "stable", TargetCommit: r.TargetCommit, ServerVersion: "v1.0.10", Status: "installing"}
		path := filepath.Join(e.config.StateDirectory, "operation.json")
		if err := writeRootFile(path, marshalStateFile(t, saved), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStateFile(path); err == nil {
			t.Fatal("nonascending persisted journal accepted", target)
		}
	}
}
