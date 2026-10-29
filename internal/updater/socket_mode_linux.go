//go:build linux

package updater

import (
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// chmodSocketAtNoFollow retains no-follow semantics on Linux before fchmodat2.
// Never retry a permission error or remove AT_SYMLINK_NOFOLLOW on a user path.
func chmodSocketAtNoFollow(dirfd int, name string, mode uint32, flags int) error {
	if flags != unix.AT_SYMLINK_NOFOLLOW {
		return unix.EINVAL
	}
	err := unix.Fchmodat(dirfd, name, mode, flags)
	if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	return chmodSocketProcFallback(dirfd, name, mode)
}

// This compatibility path depends on /proc; unavailable or non-proc mounts
// fail closed. Kept separate so newer kernels exercise it in acceptance tests.
func chmodSocketProcFallback(dirfd int, name string, mode uint32) error {
	// O_PATH pins the inode without connecting to the socket. NOFOLLOW opens a
	// planted symlink itself, which the type check below rejects. Linux cannot
	// fchmod an O_PATH fd, so use its kernel-owned procfs fd link, never name.
	fd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var info unix.Stat_t
	if err = unix.Fstat(fd, &info); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFSOCK || info.Uid != 0 || info.Nlink == 0 {
		return unix.EINVAL
	}
	proc, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(proc)
	var fs unix.Statfs_t
	if err = unix.Fstatfs(proc, &fs); err != nil {
		return err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return unix.EINVAL
	}
	return unix.Fchmodat(proc, strconv.Itoa(fd), mode, 0)
}
