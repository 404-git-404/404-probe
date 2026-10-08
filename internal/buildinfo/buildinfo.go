package buildinfo

import (
	stdbuildinfo "debug/buildinfo"
	"errors"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

var (
	Version = ""
	Commit  = ""
)

var canonicalVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.([1-9][0-9]*))?$`)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Dirty   bool   `json:"dirty"`
}

type ExecutableInfo struct {
	Path   string `json:"path"`
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

func InspectExecutable(path string) (ExecutableInfo, error) {
	metadata, err := stdbuildinfo.ReadFile(path)
	if err != nil {
		return ExecutableInfo{}, err
	}
	result := ExecutableInfo{Path: metadata.Path}
	var revisionSeen, modifiedSeen, goosSeen, goarchSeen bool
	for _, setting := range metadata.Settings {
		switch setting.Key {
		case "vcs.revision":
			if revisionSeen {
				return ExecutableInfo{}, errors.New("duplicate vcs.revision")
			}
			revisionSeen, result.Commit = true, setting.Value
		case "vcs.modified":
			if modifiedSeen || (setting.Value != "true" && setting.Value != "false") {
				return ExecutableInfo{}, errors.New("invalid vcs.modified")
			}
			modifiedSeen, result.Dirty = true, setting.Value == "true"
		case "GOOS":
			if goosSeen {
				return ExecutableInfo{}, errors.New("duplicate GOOS")
			}
			goosSeen, result.GOOS = true, setting.Value
		case "GOARCH":
			if goarchSeen {
				return ExecutableInfo{}, errors.New("duplicate GOARCH")
			}
			goarchSeen, result.GOARCH = true, setting.Value
		}
	}
	if !revisionSeen || !modifiedSeen || !goosSeen || !goarchSeen {
		return ExecutableInfo{}, errors.New("executable build settings are incomplete")
	}
	return result, nil
}

func Current() Info {
	info := Info{Version: strings.TrimSpace(Version), Commit: strings.TrimSpace(Commit)}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.modified":
				info.Dirty = setting.Value == "true"
			}
		}
	}
	if info.Dirty {
		info.Version = "dirty"
	} else if info.Version == "" {
		if info.Commit != "" {
			info.Version = "dev"
		} else {
			info.Version = "unknown"
		}
	}
	return info
}

func (i Info) UpgradeEligible() bool {
	return canonicalVersion.MatchString(i.Version) && i.Commit != "" && !i.Dirty
}

func IsCanonicalVersion(value string) bool { return canonicalVersion.MatchString(value) }

func CompareVersions(left, right string) (int, bool) {
	l := canonicalVersion.FindStringSubmatch(left)
	r := canonicalVersion.FindStringSubmatch(right)
	if l == nil || r == nil {
		return 0, false
	}
	for index := 1; index <= 3; index++ {
		lv, _ := strconv.ParseUint(l[index], 10, 64)
		rv, _ := strconv.ParseUint(r[index], 10, 64)
		if lv < rv {
			return -1, true
		}
		if lv > rv {
			return 1, true
		}
	}
	return 0, true
}

// IsReleaseVersion permits an explicitly selected release. Stable auto-upgrade
// boundaries must continue using IsCanonicalVersion and UpgradeEligible.
func IsReleaseVersion(value string) bool { return releaseVersion.MatchString(value) }

// CompareReleaseVersions is for explicit installer/Server transitions only.
// Agent remote upgrades keep the stable-only CompareVersions boundary.
func CompareReleaseVersions(left, right string) (int, bool) {
	if !IsReleaseVersion(left) || !IsReleaseVersion(right) {
		return 0, false
	}
	l, lb, lp := strings.Cut(left, "-beta.")
	r, rb, rp := strings.Cut(right, "-beta.")
	if comparison, ok := CompareVersions(l, r); !ok || comparison != 0 {
		return comparison, ok
	}
	if !lp && !rp {
		return 0, true
	}
	if !lp {
		return 1, true
	}
	if !rp {
		return -1, true
	}
	lv, le := strconv.ParseUint(lb, 10, 64)
	rv, re := strconv.ParseUint(rb, 10, 64)
	if le != nil || re != nil {
		return 0, false
	}
	if lv < rv {
		return -1, true
	}
	if lv > rv {
		return 1, true
	}
	return 0, true
}
