package updater

import (
	stdbuildinfo "debug/buildinfo"
	"errors"
)

func inspectCandidateBuild(path string) (CandidateBuildInfo, error) {
	metadata, err := stdbuildinfo.ReadFile(path)
	if err != nil {
		return CandidateBuildInfo{}, err
	}
	info := CandidateBuildInfo{Path: metadata.Path}
	var revisionSeen, modifiedSeen, goosSeen, goarchSeen bool
	for _, setting := range metadata.Settings {
		switch setting.Key {
		case "vcs.revision":
			if revisionSeen {
				return CandidateBuildInfo{}, errors.New("duplicate vcs.revision")
			}
			revisionSeen, info.Commit = true, setting.Value
		case "vcs.modified":
			if modifiedSeen || (setting.Value != "true" && setting.Value != "false") {
				return CandidateBuildInfo{}, errors.New("invalid vcs.modified")
			}
			modifiedSeen, info.Dirty = true, setting.Value == "true"
		case "GOOS":
			if goosSeen {
				return CandidateBuildInfo{}, errors.New("duplicate GOOS")
			}
			goosSeen, info.GOOS = true, setting.Value
		case "GOARCH":
			if goarchSeen {
				return CandidateBuildInfo{}, errors.New("duplicate GOARCH")
			}
			goarchSeen, info.GOARCH = true, setting.Value
		}
	}
	if !revisionSeen || !modifiedSeen || !goosSeen || !goarchSeen {
		return CandidateBuildInfo{}, errors.New("candidate build settings are incomplete")
	}
	return info, nil
}
