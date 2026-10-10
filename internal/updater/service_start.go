package updater

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseAgentServiceObservation(data []byte) (agentServiceObservation, error) {
	var state agentServiceObservation
	if len(data) > 4096 {
		return state, errors.New("Agent service properties exceed limit")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ActiveState" && key != "SubState" && key != "MainPID") {
			return state, errors.New("invalid Agent service properties")
		}
		if _, exists := values[key]; exists {
			return state, errors.New("duplicate Agent service property")
		}
		values[key] = value
	}
	pid, err := strconv.ParseInt(values["MainPID"], 10, 32)
	if len(values) != 3 || err != nil || pid < 0 || values["ActiveState"] == "" || values["SubState"] == "" {
		return state, errors.New("incomplete Agent service properties")
	}
	state.ActiveState, state.SubState, state.PID = values["ActiveState"], values["SubState"], int(pid)
	return state, nil
}

type agentServiceObservation struct {
	ActiveState    string
	SubState       string
	PID            int
	LiveExecutable bool
}

// Type=simple may report active before exec. Require the actual live inode,
// then a second observation of the same running process. Never wait through
// an automatic restart to hide a failed start.
func waitForRunningAgent(ctx context.Context, observe func(context.Context) (agentServiceObservation, error), interval time.Duration) error {
	var confirmedPID int
	var observedPID int
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := observe(ctx)
		if err != nil {
			return fmt.Errorf("inspect restarted Agent: %w", err)
		}
		if state.ActiveState == "failed" || state.ActiveState == "inactive" || state.SubState == "auto-restart" {
			return errors.New("restarted Agent did not remain running")
		}
		if observedPID > 0 && state.PID != observedPID {
			return errors.New("restarted Agent main process was replaced or exited")
		}
		if state.PID > 0 {
			observedPID = state.PID
		}
		if state.ActiveState == "active" && state.SubState == "running" && state.PID > 0 && state.LiveExecutable {
			if confirmedPID == state.PID {
				return nil
			}
			confirmedPID = state.PID
		} else {
			confirmedPID = 0
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
