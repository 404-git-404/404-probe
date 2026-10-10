package updater

import "404-probe/internal/buildinfo"

func inspectCandidateBuild(path string) (CandidateBuildInfo, error) {
	metadata, err := buildinfo.InspectExecutable(path)
	if err != nil {
		return CandidateBuildInfo{}, err
	}
	if metadata.GOOS == "linux" {
		metadata, err = buildinfo.InspectLinkedRelease(path)
		if err != nil {
			return CandidateBuildInfo{}, err
		}
	}
	return CandidateBuildInfo{Path: metadata.Path, Version: metadata.Version, Commit: metadata.Commit, Dirty: metadata.Dirty, GOOS: metadata.GOOS, GOARCH: metadata.GOARCH}, nil
}
