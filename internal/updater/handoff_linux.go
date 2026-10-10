//go:build linux

package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appbuildinfo "404-probe/internal/buildinfo"
	"golang.org/x/sys/unix"
)

func runningAgentMatchesLive(pid int, live os.FileInfo) bool {
	if pid <= 0 {
		return false
	}
	running, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	return err == nil && os.SameFile(live, running)
}

func protectedInstalledBinary(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("installed Agent is not a regular file")
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil || stat.Uid != 0 || stat.Mode&022 != 0 || stat.Mode&0111 == 0 || stat.Mode&07000 != 0 {
		return nil, errors.New("installed Agent is not root-owned and protected executable")
	}
	return info, nil
}

func reexecConfirmedUpdater(state State) error {
	return handoffConfirmedUpdater(state, liveAgentBinary, filepath.Join(updaterStateDirectory, "operation.json"), os.Environ(), updaterHandoffOps{
		readState: readStateFile,
		protected: func(path string) error { _, err := protectedInstalledBinary(path); return err },
		inspect: func(path, version string) (CandidateBuildInfo, error) {
			var info appbuildinfo.ExecutableInfo
			var err error
			if requiresLinkedVersion(version) {
				info, err = appbuildinfo.InspectLinkedRelease(path)
			} else {
				info, err = appbuildinfo.InspectExecutable(path)
			}
			return CandidateBuildInfo{Path: info.Path, Version: info.Version, Commit: info.Commit, Dirty: info.Dirty, GOOS: info.GOOS, GOARCH: info.GOARCH}, err
		},
		exec: unix.Exec,
	})
}
