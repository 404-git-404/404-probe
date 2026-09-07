//go:build !windows

package securitycollector

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error      { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func unlockFile(file *os.File)          { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }
func replaceFile(from, to string) error { return os.Rename(from, to) }
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
