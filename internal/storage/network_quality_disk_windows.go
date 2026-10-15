package storage

import (
	"golang.org/x/sys/windows"
	"os"
)

func qualityFilesystem(path string) (int64, int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var free, total, unused uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &unused)
	return int64(free), int64(total), err
}
func qualityRootFilesystem() string {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	return drive + `\`
}
