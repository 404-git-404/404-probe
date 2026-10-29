//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUpdaterHealthMetadataPolicyWithoutRoot(t *testing.T) {
	for _, kind := range []string{"safe", "one_link", "directory_missing", "socket_missing", "directory_owner", "directory_writable", "socket_symlink", "socket_owner", "socket_mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := unix.Stat_t{Mode: unix.S_IFDIR | 0755, Uid: 1234, Nlink: 2}
			sock := unix.Stat_t{Mode: unix.S_IFSOCK | 0660, Uid: 1234, Gid: 42}
			switch kind {
			case "one_link":
				dir.Nlink = 1
			case "directory_owner":
				dir.Uid++
			case "directory_writable":
				dir.Mode |= 0020
			case "socket_symlink":
				sock.Mode = unix.S_IFLNK | 0777
			case "socket_owner":
				sock.Uid++
			case "socket_mode":
				sock.Mode = unix.S_IFSOCK | 0600
			}
			lstat := func(path string) (socketIdentity, unix.Stat_t, error) {
				if path == "/fixture" {
					if kind == "directory_missing" {
						return socketIdentity{}, dir, os.ErrNotExist
					}
					return socketIdentity{}, dir, nil
				}
				if kind == "socket_missing" {
					return socketIdentity{}, sock, os.ErrNotExist
				}
				return socketIdentity{1, 2, 3}, sock, nil
			}
			id, _, h := inspectHealthMetadata("/fixture/s", 42, 1234, lstat)
			if kind == "safe" || kind == "one_link" {
				if h.Status != "" || id != (socketIdentity{1, 2, 3}) {
					t.Fatal(h, id)
				}
			} else if kind == "directory_missing" || kind == "socket_missing" {
				if h.Status != "missing" {
					t.Fatal(h)
				}
			} else if h.Status != "identity_mismatch" {
				t.Fatal(h)
			}
		})
	}
	a := socketIdentity{1, 2, 3}
	b := a
	b.CTime++
	if a == b || !a.sameNode(b) {
		t.Fatal("ctime fence lost")
	}
	b.Inode++
	if a.sameNode(b) {
		t.Fatal("node fence lost")
	}
}

// A held Engine lock must block repair even before any filesystem inspection.
func TestUpdaterHealthClosedRepairRespectsEngineMutexWithoutRoot(t *testing.T) {
	now := time.Now()
	budget := &listenerManager{nextAttempt: now.Add(10 * time.Second)}
	if budget.repairBudgetAvailable(now) || !budget.repairBudgetAvailable(budget.nextAttempt) {
		t.Fatal("backoff boundary")
	}
	budget.attempts = socketRepairLimit
	if budget.repairBudgetAvailable(now.Add(time.Hour)) {
		t.Fatal("exhausted budget retried")
	}
	e := &Engine{}
	m := &listenerManager{closed: true}
	e.mu.Lock()
	done := make(chan struct{})
	go func() { _ = m.repair(e, time.Now()); close(done) }()
	select {
	case <-done:
		e.mu.Unlock()
		t.Fatal("repair crossed engine lock")
	case <-time.After(20 * time.Millisecond):
	}
	e.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("repair deadlocked")
	}
}

type blockingRemovalLauncher struct{ started, release chan struct{} }

func (l *blockingRemovalLauncher) SupportsRemoteRemoval() bool { return true }
func (l *blockingRemovalLauncher) Start(ctx context.Context, _, _ string) error {
	close(l.started)
	select {
	case <-l.release:
		return errors.New("injected launch failure")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestUpdaterHealthRemovalLaunchDoesNotHoldEngineLockWithoutRoot(t *testing.T) {
	l := &blockingRemovalLauncher{make(chan struct{}), make(chan struct{})}
	e := &Engine{config: EngineConfig{AgentRemoval: l}}
	request := Request{ProtocolVersion: ProtocolVersion, Action: ActionRemove, OperationID: "0123456789abcdef0123456789abcdef", ReceiptToken: "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcde_-"}
	done := make(chan error, 1)
	go func() { done <- e.StartAgentRemoval(request) }()
	<-l.started
	locked := make(chan bool, 1)
	go func() { e.mu.Lock(); locked <- e.removing; e.mu.Unlock() }()
	select {
	case removing := <-locked:
		if !removing {
			t.Error("removal not fenced")
		}
	case <-time.After(time.Second):
		t.Error("systemctl held engine mutex")
	}
	close(l.release)
	if <-done == nil {
		t.Fatal("launch failure lost")
	}
	if e.removing {
		t.Fatal("failed launch latch not rolled back")
	}
}
