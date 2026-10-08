//go:build linux

package updater

import (
	"bytes"
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
)

func platformServe(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent updater must run as root")
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
	if err := removalLauncher.Prepare(); err != nil {
		slog.Warn("remote Agent removal capability is unavailable", "error", err)
	}
	engine, err := NewEngine(EngineConfig{CurrentVersion: appbuildinfo.Current().Version, StateDirectory: updaterStateDirectory,
		LiveBinary: liveAgentBinary, StagedBinary: stagedAgentBinary, PreviousBinary: previousAgentBinary,
		Inspect: inspectCandidateBuild, ServiceCommand: controlAgentService, HealthTimeout: 2 * time.Minute,
		AgentRemoval: removalLauncher,
		OnCommitted: func() {
			commitOnce.Do(func() { close(committed) })
		}})
	if err != nil {
		return err
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
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-committed:
				return nil
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil || request.Validate() != nil {
		_ = json.NewEncoder(connection).Encode(Response{Error: "invalid updater request"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
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
		_ = json.NewEncoder(connection).Encode(Response{Accepted: true, Capabilities: UpdaterCapabilities{RemoteRemoval: engine.SupportsRemoteRemoval()}})
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
	if action != "restart" && action != "stop" {
		return errors.New("invalid fixed service action")
	}
	return exec.CommandContext(ctx, "systemctl", action, agentServiceName).Run()
}
