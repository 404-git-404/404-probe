//go:build linux

package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	return platformServeMode(ctx, false)
}

// ServeLocalMigration is a root operator action, never an IPC action.
func ServeLocalMigration(ctx context.Context) error { return platformServeMode(ctx, true) }

func platformServeMode(ctx context.Context, migration bool) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent updater must run as root")
	}
	currentVersion := appbuildinfo.Current().Version
	if migration {
		info := appbuildinfo.Current()
		if info.Version != "v1.0.1" || info.Dirty || !validHexCommit(info.Commit) {
			return errors.New("migration requires the clean v1.0.1 Stable installer binary")
		}
		if err := requireRegular(liveAgentBinary); err != nil {
			return err
		}
		var installedStat unix.Stat_t
		if err := unix.Lstat(liveAgentBinary, &installedStat); err != nil || installedStat.Uid != 0 || installedStat.Mode&022 != 0 {
			return errors.New("installed Agent is not root-owned and protected")
		}
		if stateInfo, err := os.Lstat(updaterStateDirectory); err != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 || stateInfo.Mode().Perm() != 0700 {
			return errors.New("migration state directory is unsafe")
		}
		var stateStat unix.Stat_t
		if err := unix.Lstat(updaterStateDirectory, &stateStat); err != nil || stateStat.Uid != 0 {
			return errors.New("migration state directory is not root-owned")
		}
		inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		output, err := exec.CommandContext(inspectCtx, liveAgentBinary, "version", "--json").Output()
		cancel()
		var installed appbuildinfo.Info
		if err != nil || len(output) > 4096 || json.Unmarshal(output, &installed) != nil || installed.Dirty || !validHexCommit(installed.Commit) || (!supportedLocalMigrationSource(installed.Version) && installed.Version != "v1.0.1") {
			return errors.New("installed Agent is not a supported clean old Beta or recoverable v1.0.1 migration")
		}
		currentVersion = installed.Version
	}
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
	serviceCommand := controlAgentService
	if migration {
		serviceCommand = controlLocalMigrationAgentService
	}
	engine, err := NewEngine(EngineConfig{CurrentVersion: currentVersion, LocalMigration: migration, StateDirectory: updaterStateDirectory,
		LiveBinary: liveAgentBinary, StagedBinary: stagedAgentBinary, PreviousBinary: previousAgentBinary,
		Inspect: inspectCandidateBuild, ServiceCommand: serviceCommand, HealthTimeout: 2 * time.Minute,
		AgentRemoval: removalLauncher,
		OnCommitted: func() {
			commitOnce.Do(func() { close(committed) })
		}, OnRolledBack: func() {
			commitOnce.Do(func() { close(committed) })
		}})
	if err != nil {
		return err
	}
	if migration {
		engine.mu.Lock()
		saved := engine.state
		engine.mu.Unlock()
		if isActiveLocalStatus(saved.Status) && !saved.LocalMigration {
			return errors.New("another updater operation is pending; recover it before local migration")
		}
		if currentVersion == "v1.0.1" && (!saved.LocalMigration || saved.Status == "failed" || saved.Status == "rolled_back") {
			return errors.New("installed Stable is not a pending local migration")
		}
		if err := checkLocalMigrationServices(ctx); err != nil {
			return err
		}
	}
	if err := prepareAgentUpdaterRuntimeDirectory(uint32(gid)); err != nil {
		return err
	}
	if migration && currentVersion == "v1.0.1" {
		engine.mu.Lock()
		completed := engine.state.Status == "succeeded"
		engine.mu.Unlock()
		if completed {
			return nil
		}
	}
	if err := removalLauncher.Prepare(); err != nil {
		slog.Warn("remote Agent removal capability is unavailable", "error", err)
	}
	engine.Recover()
	if info, err := os.Lstat(DefaultSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("updater socket path exists and is not a socket")
		}
		if err := os.Remove(DefaultSocket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", DefaultSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(DefaultSocket)
	if err := os.Chown(DefaultSocket, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(DefaultSocket, 0660); err != nil {
		return err
	}
	if migration {
		go func() {
			engine.mu.Lock()
			saved := engine.state
			engine.mu.Unlock()
			if !isActiveLocalStatus(saved.Status) && currentVersion != "v1.0.1" {
				var id [16]byte
				if _, err := rand.Read(id[:]); err != nil {
					_ = listener.Close()
					return
				}
				if _, err := engine.Start(Request{ProtocolVersion: 1, Action: ActionStart, OperationID: hex.EncodeToString(id[:]), TargetVersion: "v1.0.1"}); err != nil {
					_ = listener.Close()
					return
				}
			}
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					engine.mu.Lock()
					status := engine.state.Status
					engine.mu.Unlock()
					if status == "failed" || status == "rolled_back" || status == "succeeded" {
						_ = listener.Close()
						return
					}
				}
			}
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-committed:
		}
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if migration {
				engine.mu.Lock()
				state := engine.state
				engine.mu.Unlock()
				return finishUpdaterHandoff(true, state, reexecConfirmedUpdater)
			}
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-committed:
				engine.mu.Lock()
				confirmed := engine.state
				engine.mu.Unlock()
				return finishUpdaterHandoff(false, confirmed, reexecConfirmedUpdater)
			default:
			}
			return err
		}
		go handleConnection(connection, engine, uint32(uid))
	}
}

func handleConnection(connection net.Conn, engine *Engine, allowedUID uint32) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	if peerUID(connection) != allowedUID {
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
	var state State
	switch request.Action {
	case ActionStart:
		state, err = engine.Start(request)
	case ActionStatus:
		state, err = engine.Status(request)
	case ActionHealthy:
		state, err = engine.Healthy(request)
	case ActionCapabilities:
		if request.ProtocolVersion == 1 {
			_ = json.NewEncoder(connection).Encode(Response{Accepted: true, Capabilities: UpdaterCapabilities{RemoteRemoval: engine.SupportsRemoteRemoval()}})
			return
		}
		engine.mu.Lock()
		migrationState := engine.state
		engine.mu.Unlock()
		if !migrationState.LocalMigration {
			migrationState = State{}
		}
		_ = json.NewEncoder(connection).Encode(Response{Accepted: true, State: migrationState, Capabilities: UpdaterCapabilities{RemoteRemoval: engine.SupportsRemoteRemoval(), UpgradeV2: true}})
		return
	case ActionRemove:
		err = engine.StartAgentRemoval(request)
	}
	if request.ProtocolVersion == 1 {
		state.LocalMigration = false
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
	return runAgentServiceCommand(ctx, action, false)
}

func runAgentServiceCommand(ctx context.Context, action string, migration bool) error {
	if action != "restart" && action != "stop" {
		return errors.New("invalid fixed service action")
	}
	args, _ := agentServiceCommandArgs(action, agentServiceName, migration)
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
