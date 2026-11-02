//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	appbuildinfo "404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
)

func platformServe(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent updater must run as root")
	}
	currentVersion := appbuildinfo.Current().Version
	account, err := user.Lookup(serviceUserName)
	if err != nil {
		return fmt.Errorf("look up Agent service user: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return fmt.Errorf("parse Agent service UID: %w", err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return fmt.Errorf("parse Agent service GID: %w", err)
	}
	if err := os.MkdirAll(updaterStateDirectory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(updaterStateDirectory, 0700); err != nil {
		return err
	}
	committed := make(chan struct{})
	var commitOnce sync.Once
	removalLauncher := newFixedAgentRemovalLauncher()
	engine, err := NewEngine(EngineConfig{CurrentVersion: currentVersion, StateDirectory: updaterStateDirectory,
		LiveBinary: liveAgentBinary, StagedBinary: stagedAgentBinary, PreviousBinary: previousAgentBinary,
		Inspect: inspectCandidateBuild, ServiceCommand: controlAgentService, HealthTimeout: 2 * time.Minute,
		AgentRemoval: removalLauncher,
		OnCommitted: func() {
			commitOnce.Do(func() { close(committed) })
		}, OnRolledBack: func() {
			commitOnce.Do(func() { close(committed) })
		}})
	if err != nil {
		return err
	}
	if err := prepareAgentUpdaterRuntimeDirectory(uint32(gid)); err != nil {
		return err
	}
	if err := removalLauncher.Prepare(); err != nil {
		slog.Warn("remote Agent removal capability is unavailable", "error", err)
	}
	manager, err := newListenerManager(DefaultSocket, uint32(gid))
	if err != nil {
		return err
	}
	defer manager.close()
	engine.Recover()
	healthCtx, stopHealth := context.WithCancel(ctx)
	defer stopHealth()
	go manager.monitor(healthCtx, engine)
	go func() {
		select {
		case <-ctx.Done():
		case <-committed:
		}
		stopHealth()
		manager.close()
	}()
	for {
		listener := manager.current()
		connection, err := listener.Accept()
		if err != nil {
			if manager.current() != listener {
				continue
			}
			return finishPlatformServe(ctx, committed, engine, err, reexecConfirmedUpdater)
		}
		go handleConnection(connection, engine, uint32(uid), manager)
	}
}

// Preserve shutdown priority; only a committed/rolled-back transaction hands
// the daemon to the confirmed installed executable after its listener closes.
func finishPlatformServe(ctx context.Context, committed <-chan struct{}, engine *Engine, acceptErr error, reexec func(State) error) error {
	if ctx.Err() != nil {
		return nil
	}
	select {
	case <-committed:
		engine.mu.Lock()
		confirmed := engine.state
		engine.mu.Unlock()
		return reexec(confirmed)
	default:
		return acceptErr
	}
}

func handleConnection(connection net.Conn, engine *Engine, allowedUID uint32, managers ...*listenerManager) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	credential, peerErr := socketPeer(connection)
	self := peerErr == nil && credential.Uid == 0 && credential.Pid == int32(os.Getpid()) && len(managers) != 0
	if peerErr != nil || credential.Uid != allowedUID && !self {
		_ = json.NewEncoder(connection).Encode(Response{Error: "unauthorized updater client"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(connection, maxIPCBytes+1))
	if err != nil || len(body) > maxIPCBytes {
		_ = json.NewEncoder(connection).Encode(Response{Error: "invalid updater request"})
		return
	}
	var request Request
	if err := protocol.DecodeUpgradeJSON(body, &request); err != nil || request.Validate() != nil {
		_ = json.NewEncoder(connection).Encode(Response{Error: "invalid updater request"})
		return
	}
	if self && credential.Uid != allowedUID && request.Action != ActionHealth {
		_ = json.NewEncoder(connection).Encode(Response{Error: "self probe is health-only"})
		return
	}
	var state State
	switch request.Action {
	case ActionHealth:
		var proof *HealthProof
		if len(managers) != 0 {
			proof = managers[0].proof(request.Nonce)
		}
		_ = json.NewEncoder(connection).Encode(Response{Accepted: proof != nil, Health: proof})
		return
	case ActionStart:
		state, err = engine.Start(request)
	case ActionStatus:
		state, err = engine.Status(request)
	case ActionHealthy:
		state, err = engine.Healthy(request)
	case ActionCapabilities, ActionRCCapabilities:
		if request.ProtocolVersion == 1 {
			_ = json.NewEncoder(connection).Encode(Response{Accepted: true, Capabilities: UpdaterCapabilities{RemoteRemoval: engine.SupportsRemoteRemoval()}})
			return
		}
		_ = json.NewEncoder(connection).Encode(Response{Accepted: true, Capabilities: UpdaterCapabilities{RemoteRemoval: engine.SupportsRemoteRemoval(), UpgradeV2: true}})
		return
	case ActionRemove:
		err = engine.StartAgentRemoval(request)
	}
	response := Response{Accepted: err == nil, State: state}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(connection).Encode(response)
}

func peerUID(connection net.Conn) uint32 {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return ^uint32(0)
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return ^uint32(0)
	}
	uid := ^uint32(0)
	if err := raw.Control(func(fd uintptr) {
		credential, getErr := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if getErr == nil {
			uid = credential.Uid
		}
	}); err != nil {
		return ^uint32(0)
	}
	return uid
}

func controlAgentService(ctx context.Context, action string) error {
	return runAgentServiceCommand(ctx, action)
}

func runAgentServiceCommand(ctx context.Context, action string) error {
	if action != "restart" && action != "stop" {
		return errors.New("invalid fixed service action")
	}
	args := []string{action, agentServiceName}
	if err := exec.CommandContext(ctx, "systemctl", args...).Run(); err != nil {
		return err
	}
	if action == "stop" {
		return nil
	}
	live, err := protectedInstalledBinary(liveAgentBinary)
	if err != nil {
		return err
	}
	return waitForRunningAgent(ctx, func(ctx context.Context) (agentServiceObservation, error) {
		output, err := exec.CommandContext(ctx, "systemctl", "show", agentServiceName, "--property=ActiveState,SubState,MainPID", "--no-pager").Output()
		if err != nil {
			return agentServiceObservation{}, err
		}
		state, err := parseAgentServiceObservation(output)
		if err != nil {
			return state, err
		}
		if state.PID > 0 {
			state.LiveExecutable = runningAgentMatchesLive(state.PID, live)
		}
		return state, nil
	}, 100*time.Millisecond)
}
