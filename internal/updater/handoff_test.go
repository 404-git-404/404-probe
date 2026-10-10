package updater

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestConfirmedHandoffUsesOnlyInstalledBinaryAndFixedUpdaterArguments(t *testing.T) {
	for _, status := range []string{"succeeded", "rolled_back"} {
		t.Run(status, func(t *testing.T) {
			state := State{SourceVersion: "v1.0.1", TargetVersion: "v1.0.2-beta.1", Status: status, ReleaseCommit: strings.Repeat("a", 40)}
			executed := false
			ops := updaterHandoffOps{
				readState: func(path string) (State, error) {
					if path != "journal" {
						t.Fatal(path)
					}
					return state, nil
				},
				protected: func(path string) error {
					if path != "live" {
						t.Fatal(path)
					}
					return nil
				},
				inspect: func(path, version string) (CandidateBuildInfo, error) {
					want := state.TargetVersion
					if status == "rolled_back" {
						want = state.SourceVersion
					}
					if path != "live" || version != want {
						t.Fatal(path, version)
					}
					return CandidateBuildInfo{Path: "404-probe/cmd/agent", Version: version, Commit: state.ReleaseCommit, GOOS: "linux", GOARCH: runtime.GOARCH}, nil
				},
				exec: func(path string, args, env []string) error {
					if path != "live" || !reflect.DeepEqual(args, []string{"live", "updater"}) || !reflect.DeepEqual(env, []string{"FIXTURE=1"}) {
						t.Fatal(path, args, env)
					}
					executed = true
					return nil
				},
			}
			if err := handoffConfirmedUpdater(state, "live", "journal", []string{"FIXTURE=1"}, ops); err != nil || !executed {
				t.Fatal(err)
			}
		})
	}
}

func TestHandoffNeverExecutesPendingUnpersistedOrUnsafeBinary(t *testing.T) {
	for _, kind := range []string{"pending", "journal-error", "journal-mismatch", "unsafe-file", "dirty", "wrong-version", "wrong-commit", "wrong-path", "wrong-platform", "exec-error"} {
		t.Run(kind, func(t *testing.T) {
			state := State{TargetVersion: "v1.0.1", Status: "succeeded", ReleaseCommit: strings.Repeat("a", 40)}
			if kind == "pending" {
				state.Status = "health_check"
			}
			executed := false
			ops := updaterHandoffOps{
				readState: func(string) (State, error) {
					if kind == "journal-error" {
						return State{}, errors.New("not durable")
					}
					saved := state
					if kind == "journal-mismatch" {
						saved.Status = "health_check"
					}
					return saved, nil
				},
				protected: func(string) error {
					if kind == "unsafe-file" {
						return errors.New("unprotected")
					}
					return nil
				},
				inspect: func(string, string) (CandidateBuildInfo, error) {
					info := CandidateBuildInfo{Path: "404-probe/cmd/agent", Version: "v1.0.1", Commit: state.ReleaseCommit, GOOS: "linux", GOARCH: runtime.GOARCH}
					switch kind {
					case "dirty":
						info.Dirty = true
					case "wrong-version":
						info.Version = "v1.0.2"
					case "wrong-commit":
						info.Commit = strings.Repeat("b", 40)
					case "wrong-path":
						info.Path = "other"
					case "wrong-platform":
						info.GOOS = "windows"
					}
					return info, nil
				},
				exec: func(string, []string, []string) error { executed = true; return errors.New("exec failed") },
			}
			if err := handoffConfirmedUpdater(state, "live", "journal", nil, ops); err == nil || executed != (kind == "exec-error") {
				t.Fatal(kind, executed, err)
			}
		})
	}
}

func TestLocalMigrationReturnsWithoutDaemonExec(t *testing.T) {
	for _, status := range []string{"succeeded", "rolled_back", "failed"} {
		err := finishUpdaterHandoff(true, State{Status: status}, func(State) error { t.Fatal("migration exec'd instead of returning"); return nil })
		if (err == nil) != (status == "succeeded") {
			t.Fatal(status, err)
		}
	}
	called := false
	if err := finishUpdaterHandoff(false, State{Status: "rolled_back"}, func(State) error { called = true; return nil }); err != nil || !called {
		t.Fatal("normal daemon not wired to exec", err)
	}
}
