package updater

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func agentServiceCommandArgs(action, unit string, migration bool) ([]string, error) {
	if action != "restart" && action != "stop" {
		return nil, errors.New("invalid fixed service action")
	}
	if migration && action == "restart" {
		return []string{"--job-mode=ignore-requirements", action, unit}, nil
	}
	return []string{action, unit}, nil
}

func migrationServiceProperties(data []byte) (map[string]string, error) {
	if len(data) > 16384 {
		return nil, errors.New("migration service properties exceed limit")
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid migration service property")
		}
		if _, exists := result[key]; exists {
			return nil, errors.New("duplicate migration service property")
		}
		result[key] = value
	}
	return result, nil
}

func migrationUpdaterStopped(properties map[string]string) error {
	pid, err := strconv.Atoi(properties["MainPID"])
	load := properties["LoadState"]
	state := properties["ActiveState"]
	job := properties["Job"]
	if err != nil || pid != 0 || (load != "loaded" && load != "not-found") || (state != "inactive" && state != "failed") || (job != "" && job != "0") {
		return errors.New("migration requires a stopped Updater with no pending job")
	}
	return nil
}

// All existing required resources must already be ready because migration
// restarts deliberately do not pull in dependencies. Keep implicit stock
// sysinit/slice/mount dependencies; do not replace them with a name allowlist.
func migrationDependencies(properties map[string]string, updater string) (ready, inactive []string, err error) {
	for _, key := range []string{"Wants", "Requires", "Requisite", "BindsTo"} {
		value, exists := properties[key]
		if !exists {
			return nil, nil, fmt.Errorf("missing migration dependency property %s", key)
		}
		for _, unit := range strings.Fields(value) {
			if unit == updater {
				if key != "Wants" {
					return nil, nil, errors.New("migration does not support a mandatory Updater dependency")
				}
				continue
			}
			if strings.HasPrefix(unit, "-") || strings.ContainsAny(unit, "/\x00") {
				// -.mount is the normal implicit root mount dependency.
				if unit != "-.mount" {
					return nil, nil, errors.New("invalid migration dependency unit")
				}
			}
			ready = append(ready, unit)
		}
	}
	value, exists := properties["Conflicts"]
	if !exists {
		return nil, nil, errors.New("missing migration conflict property")
	}
	inactive = strings.Fields(value)
	for _, unit := range inactive {
		if strings.HasPrefix(unit, "-") || strings.ContainsAny(unit, "/\x00") {
			return nil, nil, errors.New("invalid migration conflict unit")
		}
	}
	return ready, inactive, nil
}
