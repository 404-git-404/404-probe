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

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(beta|rc)\.([1-9][0-9]*))?$`)

const FirstRCSupportVersion = "v1.0.3-rc.1"

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Dirty   bool   `json:"dirty"`
}

type ExecutableInfo struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit"`
	Dirty   bool   `json:"dirty"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
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
	return IsCanonicalVersion(i.Version) && i.Commit != "" && !i.Dirty
}

func IsCanonicalVersion(value string) bool { _, ok := parseVersion(value, false); return ok }

func parseVersion(value string, release bool) ([5]uint64, bool) {
	var result [5]uint64
	if len(value) > 64 {
		return result, false
	}
	pattern := canonicalVersion
	if release {
		pattern = releaseVersion
	}
	parts := pattern.FindStringSubmatch(value)
	if parts == nil {
		return result, false
	}
	for i := 1; i <= 3; i++ {
		n, err := strconv.ParseUint(parts[i], 10, 64)
		if err != nil {
			return result, false
		}
		result[i-1] = n
	}
	result[3] = 2 // beta < rc < stable, followed by the numeric prerelease index.
	if release && parts[5] != "" {
		result[3] = 0
		if parts[5] == "rc" {
			result[3] = 1
		}
		n, err := strconv.ParseUint(parts[6], 10, 64)
		if err != nil {
			return result, false
		}
		result[4] = n
	}
	return result, true
}

func CompareVersions(left, right string) (int, bool) {
	l, lok := parseVersion(left, false)
	r, rok := parseVersion(right, false)
	if !lok || !rok {
		return 0, false
	}
	for index := 0; index < 3; index++ {
		lv, rv := l[index], r[index]
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
func IsReleaseVersion(value string) bool { _, ok := parseVersion(value, true); return ok }

func IsRCVersion(value string) bool { v, ok := parseVersion(value, true); return ok && v[3] == 1 }

// Explicit RC operations do not grant the Stable default/automatic permission.
func (i Info) ExplicitUpgradeEligible() bool {
	return !i.Dirty && i.Commit != "" && IsReleaseVersion(i.Version)
}

// CompareReleaseVersions is for explicit installer/Server transitions only.
// Agent remote upgrades keep the stable-only CompareVersions boundary.
func CompareReleaseVersions(left, right string) (int, bool) {
	l, lok := parseVersion(left, true)
	r, rok := parseVersion(right, true)
	if !lok || !rok {
		return 0, false
	}
	for i := range l {
		if l[i] < r[i] {
			return -1, true
		}
		if l[i] > r[i] {
			return 1, true
		}
	}
	return 0, true
}
