// Package platformsupport gates v1.1-only host services, not basic telemetry.
package platformsupport

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/host"
)

const maxReleaseBytes = 4096

type Policy struct {
	DeferredUnsupported bool
	IdentityUnknown     bool
}

func Host() Policy { return detect(runtime.GOOS, readRelease) }

type Identity struct {
	OS       string
	Deferred Policy
	Quality  QualityPolicy
}

func UnknownIdentity() Identity {
	return Identity{Deferred: Policy{DeferredUnsupported: true, IdentityUnknown: true}, Quality: QualityPolicy{true, "platform_identity_unknown"}}
}

// ReadIdentity reads the platform policies and the legacy system name. Callers
// run it in the background because filesystem reads may outlive the deadline.
func ReadIdentity(ctx context.Context) Identity {
	if runtime.GOOS != "linux" {
		return Identity{OS: runtime.GOOS}
	}
	type readResult struct {
		data []byte
		err  error
	}
	files := make(map[string]readResult, 2)
	readOnce := func(path string) ([]byte, error) {
		if result, ok := files[path]; ok {
			return result.data, result.err
		}
		data, err := readRelease(path)
		if path == "/etc/os-release" && os.IsNotExist(err) {
			data, err = readRelease("/usr/lib/os-release")
		}
		files[path] = readResult{data, err}
		return data, err
	}
	deferred := detect(runtime.GOOS, readOnce)
	quality := detectQuality(runtime.GOOS, readOnce)
	_, releaseErr := readOnce("/etc/os-release")
	return Identity{OS: systemName(ctx, releaseErr), Deferred: deferred, Quality: quality}
}

func systemName(ctx context.Context, releaseErr error) string {
	if ctx.Err() != nil {
		return ""
	}
	// v1.1.2 used host.InfoWithContext, which gets these fields from
	// PlatformInformationWithContext. Debian's version comes from debian_version.
	platform, _, version, err := host.PlatformInformationWithContext(ctx)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	name := strings.TrimSpace(strings.Join([]string{platform, version}, " "))
	if len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return ""
	}
	if name == "" && (releaseErr == nil || os.IsNotExist(releaseErr)) {
		return runtime.GOOS
	}
	return name
}

func (p Policy) CheckCommand(command string) error {
	if p.DeferredUnsupported && (command == "updater" || command == "security-collect") {
		return fmt.Errorf("%s is not supported yet on this platform (暂不支持)", command)
	}
	return nil
}

func readRelease(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReleaseBytes+1))
	if len(data) > maxReleaseBytes {
		return nil, fmt.Errorf("release file exceeds limit")
	}
	return data, err
}

func detect(goos string, read func(string) ([]byte, error)) Policy {
	if goos != "linux" {
		return Policy{}
	}
	release, err := read("/etc/os-release")
	if err != nil {
		return Policy{DeferredUnsupported: true, IdentityUnknown: true}
	}
	id, ok := releaseID(string(release))
	if !ok {
		return Policy{DeferredUnsupported: true, IdentityUnknown: true}
	}
	_, alpineErr := read("/etc/alpine-release")
	// Contradictory markers or unreadable identity close only deferred services.
	if id == "alpine" || alpineErr == nil {
		return Policy{DeferredUnsupported: true}
	}
	if !os.IsNotExist(alpineErr) {
		return Policy{DeferredUnsupported: true, IdentityUnknown: true}
	}
	return Policy{}
}

func releaseID(data string) (string, bool) {
	var id string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ID=") {
			continue
		}
		if id != "" {
			return "", false
		}
		value := strings.TrimPrefix(line, "ID=")
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if value == "" {
			return "", false
		}
		for _, ch := range value {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
				return "", false
			}
		}
		id = value
	}
	return id, id != ""
}
