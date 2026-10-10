//go:build linux

package updater

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Only the compile-time socket directory is prepared. The migration CLI does
// not run inside the old service's RuntimeDirectory lifecycle.
func prepareAgentUpdaterRuntimeDirectory(agentGID uint32) error {
	if filepath.Dir(agentUpdaterSocketDirectory) != "/run" {
		return errors.New("invalid fixed Agent runtime directory")
	}
	return prepareAgentRuntimeDirectory("/run", filepath.Base(agentUpdaterSocketDirectory), agentGID)
}

func prepareAgentRuntimeDirectory(parentPath, name string, agentGID uint32) error {
	parent, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open fixed runtime parent: %w", err)
	}
	defer unix.Close(parent)
	return prepareAgentRuntimeDirectoryAt(parent, name, agentGID)
}

func safeAgentRuntimeDirectory(stat *unix.Stat_t, agentGID uint32) bool {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Mode&022 != 0 || stat.Mode&07000 != 0 {
		return false
	}
	// The unprivileged Agent needs search permission, not a writable directory.
	return stat.Mode&0001 != 0 || stat.Gid == agentGID && stat.Mode&0010 != 0
}

func prepareAgentRuntimeDirectoryAt(parent int, name string, agentGID uint32) error {
	if name == "." || name == ".." || name == "" || filepath.Base(name) != name {
		return errors.New("invalid fixed runtime directory name")
	}
	var parentStat unix.Stat_t
	if err := unix.Fstat(parent, &parentStat); err != nil || !safeAgentRuntimeDirectory(&parentStat, agentGID) {
		return errors.New("fixed runtime parent is unsafe or inaccessible to Agent")
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	// Open first: an existing stock directory also works when the daemon's
	// sandbox makes /run read-only except for this directory.
	directory, err := unix.Openat(parent, name, flags, 0)
	created := false
	if errors.Is(err, unix.ENOENT) {
		if mkdirErr := unix.Mkdirat(parent, name, 0755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return fmt.Errorf("create fixed Agent runtime directory: %w", mkdirErr)
		} else {
			created = mkdirErr == nil
		}
		directory, err = unix.Openat(parent, name, flags, 0)
	}
	if err != nil {
		return fmt.Errorf("open fixed Agent runtime directory without symlinks: %w", err)
	}
	defer unix.Close(directory)
	var stat unix.Stat_t
	if err := unix.Fstat(directory, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Mode&022 != 0 || stat.Mode&07000 != 0 {
		return errors.New("fixed Agent runtime directory is unsafe")
	}
	if created {
		if err := unix.Fchown(directory, 0, 0); err != nil {
			return err
		}
		// Only a directory created here is normalized after restrictive umask.
		if err := unix.Fchmod(directory, 0755); err != nil {
			return err
		}
		if err := unix.Fstat(directory, &stat); err != nil {
			return err
		}
	}
	if !safeAgentRuntimeDirectory(&stat, agentGID) {
		return errors.New("existing Agent runtime directory is inaccessible; preserved without changing permissions")
	}
	var visible unix.Stat_t
	if err := unix.Fstatat(parent, name, &visible, unix.AT_SYMLINK_NOFOLLOW); err != nil || visible.Mode&unix.S_IFMT != unix.S_IFDIR || visible.Dev != stat.Dev || visible.Ino != stat.Ino {
		return errors.New("fixed Agent runtime directory changed during preparation")
	}
	return nil
}
