package updater

import (
	"errors"
	"fmt"
	"runtime"

	"404-probe/internal/buildinfo"
)

func finishUpdaterHandoff(migration bool, state State, reexec func(State) error) error {
	if migration {
		if state.Status == "succeeded" {
			return nil
		}
		return fmt.Errorf("local migration did not commit: %s (%s); rerun upgrade-agent to recover", state.Status, state.FailureCode)
	}
	return reexec(state)
}

type updaterHandoffOps struct {
	readState func(string) (State, error)
	protected func(string) error
	inspect   func(string, string) (CandidateBuildInfo, error)
	exec      func(string, []string, []string) error
}

// Only a durable terminal transaction can replace the daemon. The executable
// path and arguments come from the service wiring, never from an IPC request.
func handoffConfirmedUpdater(state State, live, journal string, env []string, ops updaterHandoffOps) error {
	if state.Status != "succeeded" && state.Status != "rolled_back" {
		return errors.New("Updater handoff requires a confirmed transaction")
	}
	saved, err := ops.readState(journal)
	if err != nil || saved != state {
		return errors.New("Updater handoff transaction is not durably confirmed")
	}
	if err := ops.protected(live); err != nil {
		return err
	}
	version := state.TargetVersion
	if state.Status == "rolled_back" {
		version = state.SourceVersion
	}
	info, err := ops.inspect(live, version)
	if err != nil || info.Path != "404-probe/cmd/agent" || info.Dirty || !validHexCommit(info.Commit) || info.GOOS != "linux" || info.GOARCH != runtime.GOARCH {
		return errors.New("installed Updater identity is unsafe")
	}
	if requiresLinkedVersion(version) && info.Version != version {
		return errors.New("installed Updater actual version mismatch")
	}
	if state.Status == "succeeded" && (!buildinfo.IsReleaseVersion(version) || info.Commit != state.ReleaseCommit) {
		return errors.New("committed Updater identity mismatch")
	}
	return ops.exec(live, []string{live, "updater"}, env)
}
