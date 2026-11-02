//go:build !windows

package updatecheck

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStateSaveWithStrictUmaskKeeps0600AndCurrentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	previous := unix.Umask(0777)
	defer unix.Umask(previous)
	if err := save(path, State{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal(info.Mode(), stat.Uid)
	}
	if s, err := load(path); err != nil || !s.Enabled {
		t.Fatal(s, err)
	}
	t.Logf("umask=0777 mode=%04o owner_uid=%d process_euid=%d", info.Mode().Perm(), stat.Uid, os.Geteuid())
}

type foreignOwnerInfo struct {
	os.FileInfo
	owner syscall.Stat_t
}

func (f foreignOwnerInfo) Sys() any { return &f.owner }

func TestStateOwnerValidationRejectsForeignUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := *info.Sys().(*syscall.Stat_t)
	foreign.Uid = uint32(os.Geteuid()) + 1
	other := foreignOwnerInfo{FileInfo: info, owner: foreign}
	if safeStateFile(other) || ownedTemporaryFile(nil, "", other) {
		t.Fatal("foreign UID accepted")
	}
	if !safeStateFile(info) || !ownedTemporaryFile(nil, "", info) {
		t.Fatal("current owner rejected")
	}
	t.Logf("current_uid=%d accepted; foreign_uid=%d rejected for state and temporary-file cleanup", os.Geteuid(), foreign.Uid)
}
