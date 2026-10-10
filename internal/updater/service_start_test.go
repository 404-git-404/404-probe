package updater

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAgentStartRequiresActualExecAndStablePID(t *testing.T) {
	observations := []agentServiceObservation{
		{ActiveState: "active", SubState: "running", PID: 12}, // forked, not exec'd
		{ActiveState: "active", SubState: "running", PID: 12, LiveExecutable: true},
		{ActiveState: "active", SubState: "running", PID: 12, LiveExecutable: true},
	}
	n := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForRunningAgent(ctx, func(context.Context) (agentServiceObservation, error) {
		if n >= len(observations) {
			t.Fatal("unexpected observation")
		}
		state := observations[n]
		n++
		return state, nil
	}, time.Millisecond)
	if err != nil || n != 3 {
		t.Fatal("accepted before actual stable execution", n, err)
	}
}

func TestAgentStartRejectsPIDReplacementEvenWithoutObservedFailureState(t *testing.T) {
	for _, replacement := range []int{0, 13} {
		n := 0
		err := waitForRunningAgent(context.Background(), func(context.Context) (agentServiceObservation, error) {
			n++
			pid := 12
			if n > 1 {
				pid = replacement
			}
			return agentServiceObservation{ActiveState: "active", SubState: "running", PID: pid, LiveExecutable: true}, nil
		}, time.Millisecond)
		if err == nil || n != 2 {
			t.Fatal("automatic process replacement was accepted", replacement, n, err)
		}
	}
}

func TestAgentStartRejectsExecFailureAndAutomaticRestart(t *testing.T) {
	for _, failed := range []agentServiceObservation{{ActiveState: "failed", SubState: "failed"}, {ActiveState: "activating", SubState: "auto-restart"}, {ActiveState: "inactive", SubState: "dead"}} {
		t.Run(failed.SubState, func(t *testing.T) {
			n := 0
			err := waitForRunningAgent(context.Background(), func(context.Context) (agentServiceObservation, error) {
				n++
				if n == 1 {
					return agentServiceObservation{ActiveState: "active", SubState: "running", PID: 12}, nil
				}
				return failed, nil
			}, time.Millisecond)
			if err == nil || n != 2 {
				t.Fatal("failed exec accepted or automatic retry hidden", n, err)
			}
		})
	}
}

func TestAgentStartConfirmationIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := waitForRunningAgent(ctx, func(context.Context) (agentServiceObservation, error) {
		return agentServiceObservation{ActiveState: "active", SubState: "running", PID: 12}, nil
	}, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestAgentServicePropertyParsingFailsClosed(t *testing.T) {
	for _, data := range []string{"ActiveState=active\nSubState=running\n", "ActiveState=active\nSubState=running\nMainPID=3\nMainPID=4", "ActiveState=active\nSubState=running\nMainPID=-1", "ActiveState=active\nSubState=running\nMainPID=4294967296", "ActiveState=active\nSubState=running\nMainPID=1\nOther=x"} {
		if _, err := parseAgentServiceObservation([]byte(data)); err == nil {
			t.Fatal("unsafe properties accepted", data)
		}
	}
	state, err := parseAgentServiceObservation([]byte("MainPID=123\nActiveState=active\nSubState=running\n"))
	if err != nil || state.PID != 123 {
		t.Fatal(state, err)
	}
}
