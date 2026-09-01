//go:build linux

package updater

import (
	"bytes"
	"context"
	stdbuildinfo "debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	appbuildinfo "404-probe/internal/buildinfo"
)

const (
	updaterStateDirectory = "/var/lib/404-probe-updater"
	liveAgentBinary       = "/usr/local/bin/404-probe-agent"
	stagedAgentBinary     = "/usr/local/bin/.404-probe-agent.candidate"
	previousAgentBinary   = "/usr/local/bin/.404-probe-agent.previous"
	agentServiceName      = "404-probe-agent.service"
	serviceUserName       = "404-probe"
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
	engine, err := NewEngine(EngineConfig{CurrentVersion: appbuildinfo.Current().Version, StateDirectory: updaterStateDirectory,
		LiveBinary: liveAgentBinary, StagedBinary: stagedAgentBinary, PreviousBinary: previousAgentBinary,
		Inspect: inspectCandidate(uid, gid), ServiceCommand: controlAgentService, HealthTimeout: 2 * time.Minute})
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
		<-ctx.Done()
		listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
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

func inspectCandidate(uid, gid int) Inspector {
	return func(ctx context.Context, path string) (appbuildinfo.Info, error) {
		metadata, err := stdbuildinfo.ReadFile(path)
		if err != nil {
			return appbuildinfo.Info{}, err
		}
		var revision string
		var dirty bool
		for _, setting := range metadata.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
		command := exec.CommandContext(ctx, path, "version", "--json")
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, Noctty: true}
		output, err := command.Output()
		if err != nil || len(output) > 4096 {
			return appbuildinfo.Info{}, errors.New("candidate version metadata is unavailable")
		}
		var info appbuildinfo.Info
		if json.Unmarshal(output, &info) != nil || info.Commit != revision || info.Dirty != dirty {
			return appbuildinfo.Info{}, errors.New("candidate build metadata is inconsistent")
		}
		return info, nil
	}
}
