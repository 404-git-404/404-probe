//go:build linux && role_rehearsal

package agent

// This mapping is available only in explicitly tagged role-rehearsal builds.
const (
	DefaultSelectorOrderPath = "/etc/404-probe-role-rehearsal/selector-order.json"
	DefaultSecurityExportDir = "/var/lib/404-probe-role-rehearsal-security/export"
	DefaultSecurityAckPath   = "/var/lib/404-probe-role-rehearsal/agent.security-acks.json"
)
