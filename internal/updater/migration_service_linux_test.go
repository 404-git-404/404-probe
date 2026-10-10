//go:build linux

package updater

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalMigrationEffectiveServicePreconditions(t *testing.T) {
	for _, scenario := range []string{"stock", "missing-updater", "busy-updater", "queued-updater", "missing-network", "active-conflict", "foreign-unit", "unsupported-mode"} {
		t.Run(scenario, func(t *testing.T) {
			run := func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					if scenario == "unsupported-mode" {
						return []byte("systemd 249"), nil
					}
					return []byte("systemd 252 (252.39-1~deb12u1)"), nil
				}
				if len(args) != 5 || args[0] != "show" || args[3] != "--" {
					t.Fatal("unsafe show arguments", args)
				}
				unit := args[4]
				switch unit {
				case filepath.Base(agentUpdaterUnitPath):
					if scenario == "busy-updater" {
						return []byte("LoadState=loaded\nActiveState=active\nMainPID=12\nJob=0"), nil
					}
					if scenario == "queued-updater" {
						return []byte("LoadState=loaded\nActiveState=inactive\nMainPID=0\nJob=1"), nil
					}
					if scenario == "missing-updater" {
						return []byte("LoadState=not-found\nActiveState=inactive\nMainPID=0\nJob=0"), errors.New("unit not found")
					}
					return []byte("LoadState=loaded\nActiveState=inactive\nMainPID=0\nJob=0"), nil
				case agentServiceName:
					fragment := agentUnitPath
					if scenario == "foreign-unit" {
						fragment = "/tmp/foreign.service"
					}
					return []byte("LoadState=loaded\nFragmentPath=" + fragment + "\nType=simple\nUser=" + serviceUserName + "\nGroup=" + serviceUserName + "\nExecStart={ path=" + liveAgentBinary + " ; argv[]=" + liveAgentBinary + " --interval 10s ; }\nWants=network-online.target " + filepath.Base(agentUpdaterUnitPath) + "\nRequires=sysinit.target system.slice -.mount\nRequisite=\nBindsTo=\nConflicts=shutdown.target"), nil
				case "shutdown.target":
					if scenario == "active-conflict" {
						return []byte("ActiveState=active\nJob=0"), nil
					}
					return []byte("ActiveState=inactive\nJob=0"), nil
				default:
					if scenario == "missing-network" && unit == "network-online.target" {
						return []byte("LoadState=loaded\nActiveState=inactive\nJob=0"), nil
					}
					if !strings.HasSuffix(unit, ".target") && unit != "system.slice" && unit != "-.mount" {
						t.Fatal("unexpected dependency", unit)
					}
					return []byte("LoadState=loaded\nActiveState=active\nJob=0"), nil
				}
			}
			err := checkLocalMigrationServicesWith(context.Background(), run)
			if (err == nil) != (scenario == "stock" || scenario == "missing-updater") {
				t.Fatal(scenario, err)
			}
		})
	}
}
