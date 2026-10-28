//go:build linux

package updater

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

const updaterHealthInterval = 10 * time.Second
const socketRepairLimit = 5

// listenerManager serializes listener replacement. Engine.mu is acquired first
// for repairs, so Start/Recover/removal cannot cross the missing-path check.
type listenerManager struct {
	mu            sync.Mutex
	path          string
	gid           uint32
	directory     socketIdentity
	identity      socketIdentity
	listener      *net.UnixListener
	directoryFile *os.File
	closed        bool
	attempts      int
	nextAttempt   time.Time
	listen        func(string, *net.UnixAddr) (*net.UnixListener, error)
}

func newListenerManager(path string, gid uint32) (*listenerManager, error) {
	directory, info, err := pathIdentity(filepath.Dir(path))
	if err != nil || !safeAgentRuntimeDirectory(&info, gid) || info.Nlink < 2 {
		return nil, errors.New("unsafe updater runtime directory")
	}
	m := &listenerManager{path: path, gid: gid, directory: directory, listen: net.ListenUnix}
	fd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	m.directoryFile = os.NewFile(uintptr(fd), filepath.Dir(path))
	var opened unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil || uint64(opened.Dev) != directory.Device || opened.Ino != directory.Inode || !safeAgentRuntimeDirectory(&opened, gid) {
		_ = m.directoryFile.Close()
		return nil, errors.New("runtime directory changed while opening")
	}
	if err = m.bind(); err != nil {
		_ = m.directoryFile.Close()
		return nil, err
	}
	return m, nil
}

func (m *listenerManager) directorySafe() bool {
	id, s, err := pathIdentity(filepath.Dir(m.path))
	return err == nil && id.sameNode(m.directory) && safeAgentRuntimeDirectory(&s, m.gid) && s.Nlink >= 2
}

func (m *listenerManager) bind() error {
	if !m.directorySafe() {
		return errors.New("runtime directory changed; refusing repair")
	}
	if _, _, err := pathIdentity(m.path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("socket path is occupied; preserved")
	}
	l, err := m.listen("unix", &net.UnixAddr{Name: m.path, Net: "unix"})
	if err != nil {
		return err
	}
	l.SetUnlinkOnClose(false)
	id, s, err := pathIdentity(m.path)
	if err != nil || s.Mode&unix.S_IFMT != unix.S_IFSOCK || s.Uid != 0 {
		_ = l.Close()
		return errors.New("new socket identity is unsafe")
	}
	cleanup := func() { _ = l.Close(); m.removeOwned(id) }
	if !m.directorySafe() {
		cleanup()
		return errors.New("runtime directory changed during bind")
	}
	if err = os.Chown(m.path, 0, int(m.gid)); err != nil {
		cleanup()
		return err
	}
	if err = os.Chmod(m.path, 0660); err != nil {
		cleanup()
		return err
	}
	current, s, err := pathIdentity(m.path)
	if err != nil || !current.sameNode(id) || s.Uid != 0 || s.Gid != m.gid || s.Mode&07777 != 0660 {
		cleanup()
		return errors.New("new socket changed during bind")
	}
	old := m.listener
	m.listener = l
	m.identity = current
	if old != nil {
		_ = old.Close()
	} // automatic unlink disabled before publication
	return nil
}

func (m *listenerManager) removeOwned(id socketIdentity) {
	if current, info, err := pathIdentity(m.path); err == nil && current == id && info.Mode&unix.S_IFMT == unix.S_IFSOCK && info.Uid == 0 && info.Gid == m.gid && info.Mode&07777 == 0660 && m.directorySafe() {
		_ = os.Remove(m.path)
	}
}

func (m *listenerManager) current() *net.UnixListener {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listener
}
func (m *listenerManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	_ = m.listener.Close()
	m.removeOwned(m.identity)
	_ = m.directoryFile.Close()
}
func (m *listenerManager) proof(nonce string) *HealthProof {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, s, err := pathIdentity(m.path)
	if err != nil || !m.directorySafe() || id != m.identity || s.Mode&07777 != 0660 || s.Uid != 0 || s.Gid != m.gid {
		return nil
	}
	return &HealthProof{Nonce: nonce, socketIdentity: m.identity}
}

func (m *listenerManager) check(ctx context.Context) protocol.UpdaterHealth {
	m.mu.Lock()
	safe := m.directorySafe()
	expected := m.identity
	m.mu.Unlock()
	if !safe {
		return protocol.UpdaterHealth{Status: "identity_mismatch", Reason: "directory_unsafe"}
	}
	if id, _, err := pathIdentity(m.path); err == nil && id != expected {
		return protocol.UpdaterHealth{Status: "identity_mismatch", Reason: "socket_replaced"}
	}
	return inspectHealth(ctx, m.path, m.gid, int32(os.Getpid()))
}

func (m *listenerManager) repair(e *Engine, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if e.running || e.removing || isActiveLocalStatus(e.state.Status) {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(e.config.StateDirectory, "removal", "request.json")); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !m.directorySafe() {
		return errors.New("runtime directory unavailable; no recreation or permission change")
	}
	if _, _, err := pathIdentity(m.path); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if m.attempts >= socketRepairLimit || now.Before(m.nextAttempt) {
		return nil
	}
	m.attempts++
	m.nextAttempt = now.Add(updaterHealthInterval * time.Duration(1<<uint(m.attempts-1)))
	if err := m.bind(); err != nil {
		if m.attempts == socketRepairLimit {
			slog.Warn("Agent updater socket repair retry limit reached; local inspection required", "attempts", m.attempts)
		}
		return err
	}
	m.attempts = 0
	m.nextAttempt = time.Time{}
	return nil
}

func (m *listenerManager) monitor(ctx context.Context, e *Engine) {
	ticker := time.NewTicker(updaterHealthInterval)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			health := m.check(ctx)
			if health.Status == "healthy" {
				m.mu.Lock()
				m.attempts = 0
				m.nextAttempt = time.Time{}
				m.mu.Unlock()
			}
			key := health.Status + ":" + health.Reason
			if key != last && health.Status != "healthy" {
				slog.Warn("Agent updater health check failed; existing objects preserved", "status", health.Status, "reason", health.Reason)
			}
			last = key
			if health.Status == "missing" && health.Reason == "socket_missing" {
				if err := m.repair(e, now); err != nil {
					slog.Warn("Agent updater socket repair failed", "error", err)
				}
			}
		}
	}
}
