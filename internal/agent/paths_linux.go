//go:build linux && !role_rehearsal && !t04_rehearsal

package agent

const (
	DefaultSelectorOrderPath = "/etc/404-probe/selector-order.json"
	DefaultSecurityExportDir = "/var/lib/404-probe-security/export"
	DefaultSecurityAckPath   = "/var/lib/404-probe/agent.security-acks.json"
)
