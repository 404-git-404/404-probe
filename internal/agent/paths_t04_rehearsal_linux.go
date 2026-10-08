//go:build linux && t04_rehearsal

package agent

const (
	DefaultSelectorOrderPath = "/etc/404-probe-t04-agent/selector-order.json"
	DefaultSecurityExportDir = "/var/lib/404-probe-t04-agent-security/export"
	DefaultSecurityAckPath   = "/var/lib/404-probe-t04-agent/agent.security-acks.json"
)
