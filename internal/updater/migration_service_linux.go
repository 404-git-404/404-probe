//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func controlLocalMigrationAgentService(ctx context.Context, action string) error {
	if action != "restart" && action != "stop" {
		return errors.New("invalid fixed service action")
	}
	if action == "restart" {
		if err := checkLocalMigrationServices(ctx); err != nil {
			return err
		}
	}
	return runAgentServiceCommand(ctx, action, true)
}

func checkLocalMigrationServices(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return checkLocalMigrationServicesWith(ctx, func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", args...).Output()
	})
}

func checkLocalMigrationServicesWith(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) error {
	show := func(unit, properties string) (map[string]string, error) {
		data, err := run(ctx, "show", "--property="+properties, "--no-pager", "--", unit)
		parsed, parseErr := migrationServiceProperties(data)
		if parseErr != nil {
			return nil, parseErr
		}
		if err != nil && parsed["LoadState"] != "not-found" {
			return nil, fmt.Errorf("inspect migration service %s: %w", unit, err)
		}
		return parsed, nil
	}
	// The supported Debian 12/13 managers have this mode. Refuse older or
	// unidentifiable managers before touching the transaction; never fall back.
	version, err := run(ctx, "--version")
	fields := strings.Fields(string(version))
	if err != nil || len(fields) < 2 || fields[0] != "systemd" {
		return errors.New("cannot identify migration systemd manager")
	}
	major, err := strconv.Atoi(fields[1])
	if err != nil || major < 252 {
		return errors.New("migration requires the supported systemd ignore-requirements mode")
	}
	updater := filepath.Base(agentUpdaterUnitPath)
	stopped, err := show(updater, "LoadState,ActiveState,MainPID,Job")
	if err != nil {
		return err
	}
	if err := migrationUpdaterStopped(stopped); err != nil {
		return err
	}
	agent, err := show(agentServiceName, "LoadState,FragmentPath,Type,User,Group,ExecStart,Wants,Requires,Requisite,BindsTo,Conflicts")
	if err != nil {
		return err
	}
	if agent["LoadState"] != "loaded" || agent["FragmentPath"] != agentUnitPath || agent["Type"] != "simple" || agent["User"] != serviceUserName || (agent["Group"] != "" && agent["Group"] != serviceUserName) || !strings.Contains(agent["ExecStart"], "path="+liveAgentBinary+" ;") || !strings.Contains(agent["ExecStart"], "argv[]="+liveAgentBinary+" ") {
		return errors.New("effective Agent service is not the supported installed unit")
	}
	ready, inactive, err := migrationDependencies(agent, updater)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, unit := range ready {
		if seen[unit] {
			continue
		}
		seen[unit] = true
		state, err := show(unit, "LoadState,ActiveState,Job")
		if err != nil || state["LoadState"] != "loaded" || state["ActiveState"] != "active" || (state["Job"] != "" && state["Job"] != "0") {
			return fmt.Errorf("migration dependency is not ready: %s", unit)
		}
	}
	for _, unit := range inactive {
		state, err := show(unit, "ActiveState,Job")
		if err != nil || state["ActiveState"] != "inactive" || (state["Job"] != "" && state["Job"] != "0") {
			return fmt.Errorf("migration conflicts with an active or queued unit: %s", unit)
		}
	}
	return nil
}
