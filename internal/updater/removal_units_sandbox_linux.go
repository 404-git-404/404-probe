//go:build linux

package updater

import "strings"

// The installer may leave sandbox directives out of the fixed removal units when the machine
// (typically a container) cannot run them; see sandbox_probe in install.sh. Only these directives
// may be missing, nothing else may differ.
var sandboxRemovableUnitKeys = map[string]bool{
	"PrivateTmp": true, "PrivateNetwork": true, "ProtectSystem": true, "ProtectHome": true,
	"ProtectKernelTunables": true, "ProtectKernelModules": true, "ProtectKernelLogs": true,
	"ProtectControlGroups": true, "RestrictSUIDSGID": true, "LockPersonality": true,
	"MemoryDenyWriteExecute": true, "RestrictAddressFamilies": true,
}

// removalUnitMatches reports whether actual is the canonical fixed unit, or the canonical unit minus
// some removable sandbox directives (same order, no other line changed, added or reordered).
func removalUnitMatches(actual []byte, canonical string) bool {
	text := string(actual)
	if text == canonical {
		return true
	}
	if !strings.HasSuffix(text, "\n") || !strings.HasSuffix(canonical, "\n") {
		return false
	}
	got := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	want := strings.Split(strings.TrimSuffix(canonical, "\n"), "\n")
	next := 0
	for _, line := range want {
		if next < len(got) && got[next] == line {
			next++
			continue
		}
		key := line
		if index := strings.IndexByte(line, '='); index >= 0 {
			key = line[:index]
		}
		if !sandboxRemovableUnitKeys[key] {
			return false
		}
	}
	return next == len(got)
}
