//go:build linux

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUpdaterHealthUnpublishedFailuresCleanAndRepair(t *testing.T) {
	for _, failure := range []string{"chown", "chmod", "review"} {
		t.Run(failure, func(t *testing.T) {
			m, e, _ := healthFixture(t)
			if err := os.Remove(m.path); err != nil {
				t.Fatal(err)
			}
			chown, chmod, statAt := m.chown, m.chmod, m.statAt
			injected := errors.New("injected metadata failure")
			switch failure {
			case "chown":
				m.chown = func(int, string, int, int, int) error { return injected }
			case "chmod":
				m.chmod = func(int, string, uint32, int) error { return injected }
			case "review":
				calls := 0
				m.statAt = func(fd int, name string, s *unix.Stat_t, flags int) error {
					calls++
					if calls == 1 {
						return injected
					}
					return statAt(fd, name, s, flags)
				}
			}
			if err := m.repair(e, time.Now()); err == nil {
				t.Fatal("failure not injected")
			}
			if _, err := os.Lstat(m.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unpublished socket left behind", err)
			}
			m.chown, m.chmod, m.statAt = chown, chmod, statAt
			if err := m.repair(e, m.nextAttempt); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(m.path); err != nil {
				t.Fatal("could not self-heal", err)
			}
		})
	}
}

func TestUpdaterHealthStartupPreservesExistingSocket(t *testing.T) {
	m, _, _ := healthFixture(t)
	before, info, err := pathIdentity(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newListenerManager(m.path, m.gid); err == nil {
		t.Fatal("occupied startup succeeded")
	}
	after, current, err := pathIdentity(m.path)
	if err != nil || before != after || info.Mode != current.Mode {
		t.Fatal("startup changed existing socket", err)
	}
}

func TestUpdaterHealthNoFollowModePreservesSymlinkTarget(t *testing.T) {
	m, _, _ := healthFixture(t)
	before, info, err := pathIdentity(m.path)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(m.path) + "-link"
	if err = os.Symlink(m.path, filepath.Join(filepath.Dir(m.path), name)); err != nil {
		t.Fatal(err)
	}
	if err = chmodSocketAtNoFollow(int(m.directoryFile.Fd()), name, 0777, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		t.Fatal("symlink chmod accepted")
	}
	after, current, err := pathIdentity(m.path)
	if err != nil || before != after || info.Mode != current.Mode {
		t.Fatal("symlink target changed", err)
	}
}

func TestUpdaterHealthUnpublishedDirectoryChangePreservesSocket(t *testing.T) {
	m, e, root := healthFixture(t)
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	chown := m.chown
	m.chown = func(fd int, name string, uid, gid, flags int) error {
		if err := chown(fd, name, uid, gid, flags); err != nil {
			return err
		}
		return os.Rename(filepath.Dir(m.path), filepath.Join(root, "moved"))
	}
	if err := m.repair(e, time.Now()); err == nil {
		t.Fatal("directory change accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, "moved", "s")); err != nil {
		t.Fatal("changed directory socket deleted", err)
	}
}
