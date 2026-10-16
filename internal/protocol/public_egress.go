package protocol

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"time"
)

// Egress is optional telemetry. An invalid optional shape must never reject CPU,
// RAM or traffic. The outer report decoder remains strict about unknown fields.
type PublicEgress struct {
	Version int               `json:"version"`
	IPv4    EgressObservation `json:"ipv4"`
	IPv6    EgressObservation `json:"ipv6"`
}

type EgressSuccess struct {
	IP        string `json:"ip"`
	CheckedAt int64  `json:"checked_at"`
}

type EgressObservation struct {
	Family      string         `json:"family"`
	Status      string         `json:"status"`
	IP          string         `json:"ip,omitempty"`
	CheckedAt   int64          `json:"checked_at"`
	ValidUntil  int64          `json:"valid_until"`
	Reason      string         `json:"reason"`
	LastSuccess *EgressSuccess `json:"last_success,omitempty"`
}

const EgressSuccessTTL = 45 * time.Minute
const EgressFailureTTL = 5 * time.Minute

func UnknownEgress(family, reason string) EgressObservation {
	return EgressObservation{Family: family, Status: "unknown", Reason: reason}
}

func strictEgressJSON(data []byte, v any) bool {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v) == nil
}

func (p *PublicEgress) UnmarshalJSON(data []byte) error {
	*p = PublicEgress{Version: 1, IPv4: UnknownEgress("ipv4", "invalid_report"), IPv6: UnknownEgress("ipv6", "invalid_report")}
	var raw struct {
		Version int             `json:"version"`
		IPv4    json.RawMessage `json:"ipv4"`
		IPv6    json.RawMessage `json:"ipv6"`
	}
	if len(data) > 4096 || !strictEgressJSON(data, &raw) || raw.Version != 1 {
		return nil
	}
	var v4, v6 EgressObservation
	if strictEgressJSON(raw.IPv4, &v4) {
		p.IPv4 = v4
	}
	if strictEgressJSON(raw.IPv6, &v6) {
		p.IPv6 = v6
	}
	return nil
}

// Conservative snapshot of IANA special-purpose registries (2026-10-15).
// IsGlobalUnicast alone accepts documentation, CGNAT and transition addresses.
var egressDenied = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16", "192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "2620:4f:8000::/48", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func PublicEgressIP(text, family string) bool {
	a, err := netip.ParseAddr(text)
	if err != nil || a.Zone() != "" || a.Is4In6() || !a.IsGlobalUnicast() {
		return false
	}
	if family == "ipv4" {
		if !a.Is4() {
			return false
		}
	} else if family == "ipv6" {
		if !a.Is6() || !netip.MustParsePrefix("2000::/3").Contains(a) {
			return false
		}
	} else {
		return false
	}
	for _, p := range egressDenied {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// NormalizeEgress isolates all optional errors and bounds times against the
// accepted report/server clocks. Stale successes remain explicitly timestamped.
func NormalizeEgress(o EgressObservation, family string, collected, received int64) EgressObservation {
	bad := func() EgressObservation { return UnknownEgress(family, "invalid_report") }
	if o.Family != family || o.CheckedAt < 0 || o.CheckedAt > collected+60000 || o.CheckedAt > received+60000 {
		return bad()
	}
	if o.LastSuccess != nil && (!PublicEgressIP(o.LastSuccess.IP, family) || o.LastSuccess.CheckedAt <= 0 || o.LastSuccess.CheckedAt > collected+60000 || o.LastSuccess.CheckedAt > received+60000) {
		return bad()
	}
	maxTTL := EgressFailureTTL
	switch o.Status {
	case "available":
		if !PublicEgressIP(o.IP, family) || o.CheckedAt == 0 || o.Reason != "verified" {
			return bad()
		}
		maxTTL = EgressSuccessTTL
		o.LastSuccess = &EgressSuccess{IP: o.IP, CheckedAt: o.CheckedAt}
	case "unavailable":
		if o.IP != "" || o.CheckedAt == 0 || (o.Reason != "no_route" && o.Reason != "family_unavailable") {
			return bad()
		}
	case "unknown":
		if o.IP != "" {
			return bad()
		}
		switch o.Reason {
		case "not_checked", "invalid_report", "not_reported", "dns", "tls", "timeout", "http_status", "invalid_response", "network":
		default:
			return bad()
		}
	default:
		return bad()
	}
	if o.CheckedAt == 0 {
		if o.Status != "unknown" || o.ValidUntil != 0 {
			return bad()
		}
	} else if o.ValidUntil <= o.CheckedAt || o.ValidUntil-o.CheckedAt > maxTTL.Milliseconds() {
		return bad()
	}
	return o
}
