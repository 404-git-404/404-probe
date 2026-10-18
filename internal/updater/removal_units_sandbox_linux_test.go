//go:build linux

package updater

import (
	"strings"
	"testing"
)

func without(unit string, drop ...string) string {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSuffix(unit, "\n"), "\n") {
		key := line
		if i := strings.IndexByte(line, '='); i >= 0 {
			key = line[:i]
		}
		if !skip[key] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n") + "\n"
}

func TestRemovalUnitsAcceptTheFullSetAndSandboxReducedVariants(t *testing.T) {
	for name, canonical := range map[string]string{"worker": removalWorkerUnitContents, "finalizer": removalFinalizerUnitContents} {
		if !removalUnitMatches([]byte(canonical), canonical) {
			t.Fatalf("%s: the canonical unit must match", name)
		}
		for _, drop := range [][]string{
			{"PrivateTmp"},
			{"ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs"},
			{"RestrictSUIDSGID", "LockPersonality", "MemoryDenyWriteExecute", "RestrictAddressFamilies"},
			{"PrivateTmp", "PrivateNetwork", "ProtectSystem", "ProtectHome", "ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs", "ProtectControlGroups", "RestrictSUIDSGID", "LockPersonality", "MemoryDenyWriteExecute", "RestrictAddressFamilies"},
		} {
			if !removalUnitMatches([]byte(without(canonical, drop...)), canonical) {
				t.Fatalf("%s without %v was refused", name, drop)
			}
		}
	}
}

func TestRemovalUnitsRefuseAnyOtherChange(t *testing.T) {
	canonical := removalWorkerUnitContents
	for name, actual := range map[string]string{
		"missing user":              without(canonical, "User"),
		"missing no new privileges": without(canonical, "NoNewPrivileges"),
		"missing exec":              without(canonical, "ExecStart"),
		"missing read-write":        without(canonical, "ReadWritePaths"),
		"extra line":                strings.Replace(canonical, "[Install]", "ExecStartPre=/bin/true\n\n[Install]", 1),
		"changed value":             strings.Replace(canonical, "ProtectSystem=strict", "ProtectSystem=full", 1),
		"no trailing newline":       strings.TrimSuffix(without(canonical, "PrivateTmp"), "\n"),
		"reordered":                 strings.Replace(canonical, "NoNewPrivileges=true\nPrivateTmp=true\n", "PrivateTmp=true\nNoNewPrivileges=true\n", 1),
		"empty":                     "",
	} {
		if removalUnitMatches([]byte(actual), canonical) {
			t.Fatalf("%s was accepted", name)
		}
	}
}
