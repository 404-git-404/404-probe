package systemchecks

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

type systemdUnit struct {
	id, names, state, subState string
	restarts                   uint64
	counterOK                  bool
}

func systemdUnits(output string) []systemdUnit {
	var units []systemdUnit
	for _, block := range strings.Split(output, "\n\n") {
		var unit systemdUnit
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, "=")
			switch key {
			case "Id":
				unit.id = value
			case "Names":
				unit.names = value
			case "ActiveState":
				unit.state = value
			case "SubState":
				unit.subState = value
			case "NRestarts":
				count, err := strconv.ParseUint(value, 10, 32)
				unit.restarts, unit.counterOK = count, err == nil
			}
		}
		if unit.id != "" && len(unit.id) <= 255 && unit.state != "" && len(unit.state) <= 32 && len(unit.subState) <= 32 {
			units = append(units, unit)
		}
	}
	return units
}

func (tracker *serviceRestartTracker) collect(ctx context.Context, now time.Time, watched []string, systemd bool, run commandRunner) (string, []protocol.WatchedService, []string, []protocol.ServiceRestart) {
	if !systemd {
		return protocol.CheckUnsupported, nil, nil, nil
	}
	args := []string{"show", "--all", "--no-pager", "--property=Id,Names,ActiveState,SubState,NRestarts", "--", "*"}
	for _, name := range watched {
		args = append(args, name+".service")
	}
	output, err := run(ctx, "/usr/bin/systemctl", args...)
	if err != nil {
		return protocol.CheckError, nil, nil, nil
	}
	units := systemdUnits(output)
	byName := make(map[string]systemdUnit, len(units))
	counters := make(map[string]uint64, len(units))
	var failed []string
	for _, unit := range units {
		if _, seen := byName[unit.id]; seen {
			continue
		}
		byName[unit.id] = unit
		for _, alias := range strings.Fields(unit.names) {
			byName[alias] = unit
		}
		if unit.state == "failed" {
			failed = append(failed, unit.id)
		}
		if unit.counterOK && strings.HasSuffix(unit.id, ".service") {
			counters[unit.id] = unit.restarts
		}
	}
	sort.Strings(failed)
	failed = failed[:min(len(failed), protocol.MaxFailedUnits)]
	services := make([]protocol.WatchedService, 0, len(watched))
	for _, name := range watched {
		unit := byName[name+".service"]
		state := unit.state
		if state == "" {
			state = "unknown"
		}
		services = append(services, protocol.WatchedService{Name: name, State: state, SubState: unit.subState})
	}
	return protocol.CheckOK, services, failed, tracker.observe(now, counters)
}
