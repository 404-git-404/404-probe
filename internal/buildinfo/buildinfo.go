package buildinfo

import (
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

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Dirty   bool   `json:"dirty"`
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
