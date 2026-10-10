//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Harmless native test executable: no product service or candidate is started.
func init() {
	if os.Getenv("PROBE_EXEC_TEST_PHASE") == "after" && len(os.Args) == 2 && os.Args[1] == "updater" {
		if _, err := os.Stat(filepath.Join(os.Getenv("PROBE_EXEC_TEST_RUNTIME"), "sentinel")); err != nil {
			os.Exit(42)
		}
		fmt.Printf("after:%d\n", os.Getpid())
		os.Exit(0)
	}
}

func TestHandoffLinuxExecChild(t *testing.T) {
	if os.Getenv("PROBE_EXEC_TEST_PHASE") != "before" {
		t.Skip("native exec subprocess helper")
	}
	live, journal := os.Getenv("PROBE_EXEC_TEST_LIVE"), os.Getenv("PROBE_EXEC_TEST_JOURNAL")
	state, err := readStateFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("before:%d\n", os.Getpid())
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PROBE_EXEC_TEST_PHASE=") {
			env = append(env, entry)
		}
	}
	env = append(env, "PROBE_EXEC_TEST_PHASE=after")
	// Only identity inspection is stubbed for this harmless test executable;
	// durable state, root protection, syscall exec, PID and directory are real.
	if err := handoffConfirmedUpdater(state, live, journal, env, updaterHandoffOps{
		readState: readStateFile,
		protected: func(path string) error { _, err := protectedInstalledBinary(path); return err },
		inspect: func(string, string) (CandidateBuildInfo, error) {
			return CandidateBuildInfo{Path: "404-probe/cmd/agent", Version: state.SourceVersion, Commit: strings.Repeat("a", 40), GOOS: "linux", GOARCH: runtime.GOARCH}, nil
		},
		exec: unix.Exec,
	}); err != nil {
		t.Fatal(err)
	}
	t.Fatal("successful exec returned")
}

func TestHandoffLinuxRealExecKeepsPIDAndRuntimeDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership verifier requires isolated root test")
	}
	directory := t.TempDir()
	live, journal := filepath.Join(directory, "agent"), filepath.Join(directory, "operation.json")
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, data, 0700); err != nil {
		t.Fatal(err)
	}
	state := State{OperationID: strings.Repeat("a", 32), SourceVersion: "v1.0.1", TargetVersion: "v1.0.2-beta.1", ProtocolVersion: 2, Channel: "beta", TargetCommit: strings.Repeat("a", 40), ServerVersion: "v1.0.1", Status: "rolled_back"}
	if err := writeRootFile(journal, marshalStateFile(t, state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "sentinel"), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestHandoffLinuxExecChild$")
	command.Env = append(os.Environ(), "PROBE_EXEC_TEST_PHASE=before", "PROBE_EXEC_TEST_LIVE="+live, "PROBE_EXEC_TEST_JOURNAL="+journal, "PROBE_EXEC_TEST_RUNTIME="+directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(string(output), err)
	}
	lines := strings.Fields(string(output))
	if len(lines) != 2 {
		t.Fatal(string(output))
	}
	first := strings.TrimPrefix(lines[0], "before:")
	second := strings.TrimPrefix(lines[1], "after:")
	pid, err := strconv.Atoi(first)
	if err != nil || pid <= 0 || first != second {
		t.Fatal("exec changed PID", string(output))
	}
	after, err := os.Stat(directory)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("runtime directory changed", err)
	}
}

func TestRunningAgentLinuxProcExecutableMustMatchLive(t *testing.T) {
	live, err := os.Stat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if !runningAgentMatchesLive(os.Getpid(), live) || runningAgentMatchesLive(0, live) {
		t.Fatal("actual proc inode matching failed")
	}
	command := exec.Command("/bin/sleep", "5")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = waitForRunningAgent(ctx, func(context.Context) (agentServiceObservation, error) {
		return agentServiceObservation{ActiveState: "active", SubState: "running", PID: command.Process.Pid, LiveExecutable: runningAgentMatchesLive(command.Process.Pid, live)}, nil
	}, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("foreign executable counted as actual Agent", err)
	}
}
