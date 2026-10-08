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

func (p Policy) CheckCommand(command string) error {
	if p.DeferredUnsupported && (command == "updater" || command == "security-collect") {
		return fmt.Errorf("%s is unsupported on this platform in v1.0 (deferred to v1.1)", command)
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
