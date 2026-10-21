package agent

import (
	"time"

	"404-probe/internal/protocol"
)

func (r *Runner) qualityCapabilities() *protocol.QualityCapabilities {
	c := &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityV2Semantics}
	if r.qualityStagedSupported.Load() {
		c.Semantics = protocol.QualityStagedSemantics
	}
	if r.quality != nil {
		c.ExcludedReason = r.quality.excludedReason
	}
	// Step two of the negotiation: only after the Server answered the header (design 4.2).
	if r.qualityUserTargets.Load() {
		c.Targets = protocol.QualityTargetsUserV1
		c.IPv6Address = protocol.QualityIPv6Unknown
		if r.quality != nil && r.quality.ipv6 != nil {
			now := time.Now
			if r.quality.now != nil {
				now = r.quality.now
			}
			c.IPv6Address = r.quality.ipv6.observe(now())
		}
	}
	var icmp *ICMPExecutor
	if e, ok := r.executor.(*ProbeExecutor); ok {
		icmp, _ = e.icmp.(*ICMPExecutor)
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		for _, kind := range []string{"tcp", "icmp"} {
			l := protocol.QualityLaneCapability{Family: family, Protocol: kind, Supported: kind == "tcp"}
			if kind == "icmp" && icmp != nil && icmp.backend != nil {
				f := icmpFamilyIPv4
				if family == "ipv6" {
					f = icmpFamilyIPv6
				}
				l.Supported = icmp.backend.Supports(f)
			}
			if c.ExcludedReason != "" {
				l.Supported = false
				l.Reason = "platform_excluded"
			} else if !l.Supported {
				l.Reason = "unsupported"
			}
			c.Lanes = append(c.Lanes, l)
		}
	}
	return c
}
