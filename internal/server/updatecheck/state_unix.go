//go:build !windows

package updatecheck

import (
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func openStateFile(root *os.Root, name string) (*os.File, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// O_NONBLOCK also prevents a racing FIFO replacement from blocking startup.
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func safeStateFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && ok && stat.Uid == uint32(os.Geteuid())
}

func ownedTemporaryFile(_ *os.Root, _ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func syncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
