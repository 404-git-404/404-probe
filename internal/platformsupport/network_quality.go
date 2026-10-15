package platformsupport

import (
	"os"
	"runtime"
)

// This policy is independent from the deferred updater/security service policy.
type QualityPolicy struct {
	Excluded bool
	Reason   string
}

func QualityHost() QualityPolicy { return detectQuality(runtime.GOOS, readRelease) }
func detectQuality(goos string, read func(string) ([]byte, error)) QualityPolicy {
	if goos != "linux" {
		return QualityPolicy{}
	}
	raw, err := read("/etc/os-release")
	if err != nil {
		return QualityPolicy{true, "platform_identity_unknown"}
	}
	id, ok := releaseID(string(raw))
	if !ok {
		return QualityPolicy{true, "platform_identity_unknown"}
	}
	_, alpineErr := read("/etc/alpine-release")
	if id == "alpine" || alpineErr == nil {
		return QualityPolicy{true, "platform_excluded:alpine"}
	}
	if !os.IsNotExist(alpineErr) {
		return QualityPolicy{true, "platform_identity_unknown"}
	}
	return QualityPolicy{}
}
