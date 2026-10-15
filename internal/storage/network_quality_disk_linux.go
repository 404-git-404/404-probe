package storage

import "golang.org/x/sys/unix"

func qualityFilesystem(path string) (int64, int64, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize), err
}
func qualityRootFilesystem() string { return "/" }
