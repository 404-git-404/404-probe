package agent

import (
	"errors"

	"404-probe/internal/protocol"
)

// setQualityUserTargets records whether the Server answered the targets header of this session. It
// is the Agent's whole negotiation state: it makes the Agent send the second-step capability, send
// the header with configuration requests, and accept source "user" targets. It is cleared with the
// v2 negotiation on every fallback.
func (r *Runner) setQualityUserTargets(on bool) {
	r.qualityUserTargets.Store(on)
	if r.quality == nil {
		return
	}
	r.quality.mu.Lock()
	r.quality.userTargets = on
	r.quality.mu.Unlock()
}

// qualityValidateUserTarget applies to an active user-entered target the host rules the Server
// applied when it was saved (internal/protocol), so a configuration that does not satisfy them is
// refused as a whole. The address check before every connection remains the safety boundary.
func qualityValidateUserTarget(c protocol.QualityConfig, t protocol.QualityTarget) error {
	if c.Version != 2 || c.Semantics != protocol.QualityStagedSemantics {
		return errors.New("user targets need staged semantics")
	}
	canonical, literal, code := protocol.NormalizeQualityHost(t.Host)
	if t.Host == "" || code != "" || canonical != t.Host {
		return errors.New("invalid user target host")
	}
	if (literal == "ipv4" && t.Family == "ipv6") || (literal == "ipv6" && t.Family == "ipv4") {
		return errors.New("user target host does not match its family")
	}
	if t.Protocol == "tcp" && (t.Port < 1 || t.Port > 65535) {
		return errors.New("invalid user target port")
	}
	if t.Region != "" || t.Kind != "" || t.ISP != "" {
		return errors.New("user target carries labels")
	}
	return nil
}
