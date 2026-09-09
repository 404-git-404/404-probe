package updater

import "404-probe/internal/buildinfo"

func inspectCandidateBuild(path string) (CandidateBuildInfo, error) {
	metadata, err := buildinfo.InspectExecutable(path)
	if err != nil {
		return CandidateBuildInfo{}, err
	}
	return CandidateBuildInfo{Path: metadata.Path, Commit: metadata.Commit, Dirty: metadata.Dirty, GOOS: metadata.GOOS, GOARCH: metadata.GOARCH}, nil
}
