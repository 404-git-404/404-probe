//go:build linux

package updater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

type socketIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	CTime  int64  `json:"ctime_ns"`
}

func (s socketIdentity) sameNode(other socketIdentity) bool {
	return s.Device == other.Device && s.Inode == other.Inode
}

type HealthProof struct {
	Nonce string `json:"nonce"`
	socketIdentity
}

var errUnexpectedUpdaterPeer = errors.New("unexpected updater peer")

func pathIdentity(path string) (socketIdentity, unix.Stat_t, error) {
	var s unix.Stat_t
	err := unix.Lstat(path, &s)
	return socketIdentity{uint64(s.Dev), s.Ino, s.Ctim.Sec*1e9 + s.Ctim.Nsec}, s, err
}

// InspectHealth authenticates the root peer and its bound path proof. A legacy
// daemon can still prove its root peer and capabilities, without new IPC fields.
func InspectHealth(ctx context.Context, path string, gid uint32) protocol.UpdaterHealth {
	return inspectHealth(ctx, path, gid, 0)
}

func inspectHealth(ctx context.Context, path string, gid uint32, selfPID int32) protocol.UpdaterHealth {
	bad := func(status, reason string) protocol.UpdaterHealth {
		return protocol.UpdaterHealth{Status: status, Reason: reason}
	}
	identity, info, health := inspectHealthMetadata(path, gid, 0, pathIdentity)
	if health.Status != "" {
		return health
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return bad("unreachable", "probe_failed")
	}
	request := Request{ProtocolVersion: 1, Action: ActionHealth, Nonce: hex.EncodeToString(nonce[:])}
	response, err := authenticatedHealthCall(ctx, path, request, selfPID)
	if err != nil {
		if errors.Is(err, errUnexpectedUpdaterPeer) {
			return bad("identity_mismatch", "authentication_failed")
		}
		return bad("unreachable", "connect_failed")
	}
	if response.Error != "" && selfPID == 0 {
		// Old Updaters reject the new action. Only the existing capability action
		// is retried, over another authenticated root connection.
		response, err = authenticatedHealthCall(ctx, path, Request{ProtocolVersion: 1, Action: ActionCapabilities}, 0)
		if err != nil || !response.Accepted {
			return bad("unreachable", "probe_failed")
		}
	} else if !response.Accepted || response.Health == nil || response.Health.Nonce != request.Nonce || response.Health.socketIdentity != identity {
		return bad("identity_mismatch", "socket_replaced")
	}
	after, current, err := pathIdentity(path)
	if err != nil || after != identity || current.Mode != info.Mode || current.Uid != info.Uid || current.Gid != info.Gid {
		return bad("identity_mismatch", "socket_replaced")
	}
	return protocol.UpdaterHealth{Status: "healthy"}
}

// Metadata policy is independent of root and IPC; tests inject lstat and uid.
func inspectHealthMetadata(path string, gid, uid uint32, lstat func(string) (socketIdentity, unix.Stat_t, error)) (socketIdentity, unix.Stat_t, protocol.UpdaterHealth) {
	bad := func(status, reason string) (socketIdentity, unix.Stat_t, protocol.UpdaterHealth) {
		return socketIdentity{}, unix.Stat_t{}, protocol.UpdaterHealth{Status: status, Reason: reason}
	}
	_, directory, err := lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return bad("missing", "runtime_directory_missing")
	}
	if err != nil || !safeAgentRuntimeDirectoryForUID(&directory, gid, uid) || directory.Nlink == 0 {
		return bad("identity_mismatch", "directory_unsafe")
	}
	id, info, err := lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return bad("missing", "socket_missing")
	}
	if err != nil || info.Mode&unix.S_IFMT != unix.S_IFSOCK || info.Uid != uid || info.Gid != gid || info.Mode&07777 != 0660 {
		return bad("identity_mismatch", "socket_unsafe")
	}
	return id, info, protocol.UpdaterHealth{}
}

func authenticatedHealthCall(ctx context.Context, path string, request Request, pid int32) (Response, error) {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	credential, err := socketPeer(conn)
	if err != nil || credential.Uid != 0 || pid != 0 && credential.Pid != pid {
		return Response{}, errUnexpectedUpdaterPeer
	}
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return Response{}, err
	}
	if err = conn.(*net.UnixConn).CloseWrite(); err != nil {
		return Response{}, err
	}
	body, err := io.ReadAll(io.LimitReader(conn, maxIPCBytes+1))
	if err != nil || len(body) > maxIPCBytes {
		return Response{}, errors.New("invalid health response")
	}
	var response Response
	if err = json.Unmarshal(body, &response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func socketPeer(conn net.Conn) (*unix.Ucred, error) {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, errors.New("not a Unix socket")
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var callErr error
	err = raw.Control(func(fd uintptr) { cred, callErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return nil, err
	}
	return cred, callErr
}
