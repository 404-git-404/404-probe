// Package platformsupport gates v1.1-only host services, not basic telemetry.
package platformsupport

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const maxReleaseBytes = 4096

type Policy struct{ DeferredUnsupported bool }

func Host() Policy { return detect(runtime.GOOS, readRelease) }

type Identity struct {
	OS       string
	Deferred Policy
	Quality  QualityPolicy
}

func UnknownIdentity() Identity {
	return Identity{Deferred: Policy{DeferredUnsupported: true}, Quality: QualityPolicy{true, "platform_identity_unknown"}}
}

// ReadIdentity uses one set of file reads for both independently gated features
// and the reported OS name. Callers that need a deadline run it off their hot path.
func ReadIdentity() Identity { return identify(runtime.GOOS, readRelease) }

func identify(goos string, read func(string) ([]byte, error)) Identity {
	if goos != "linux" {
		return Identity{OS: goos}
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
		data, err := read(path)
		if path == "/etc/os-release" && os.IsNotExist(err) {
			data, err = read("/usr/lib/os-release")
		}
		files[path] = readResult{data, err}
		return data, err
	}
	deferred := detect(goos, readOnce)
	quality := detectQuality(goos, readOnce)
	raw, err := readOnce("/etc/os-release")
	id, valid := releaseID(string(raw))
	var name string
	if err == nil && valid && len(id) <= 128 {
		name = id
		for _, line := range strings.Split(string(raw), "\n") {
			if version, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION_ID="); ok {
				version = strings.Trim(version, `"'`)
				if version != "" && len(name)+len(version)+1 <= 128 && !strings.ContainsAny(version, "\x00\r\n") {
					name += " " + version
				}
				break
			}
		}
	}
	return Identity{OS: name, Deferred: deferred, Quality: quality}
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
		return Policy{DeferredUnsupported: true}
	}
	id, ok := releaseID(string(release))
	if !ok {
		return Policy{DeferredUnsupported: true}
	}
	_, alpineErr := read("/etc/alpine-release")
	// Contradictory markers or unreadable identity close only deferred services.
	return Policy{DeferredUnsupported: id == "alpine" || alpineErr == nil || !os.IsNotExist(alpineErr)}
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
