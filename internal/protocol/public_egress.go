package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
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

// A job-based check is repeated every seven days; a result stays valid for one day more.
const EgressJobInterval = 7 * 24 * time.Hour
const EgressJobSuccessTTL = 8 * 24 * time.Hour

// PublicEgressConfig is the whole job: it has no fields, the destinations (api.ipify.org and
// api6.ipify.org) are compiled into the Agent and nothing in a job can change them.
type PublicEgressConfig struct{}

// PublicEgressResult is what the Agent returns for a public_egress job. Only the family, status, IP
// and reason matter: the Server stamps the check time and validity itself, so Agent clocks and
// requested lifetimes are never trusted.
type PublicEgressResult struct {
	IPv4 EgressObservation `json:"ipv4"`
	IPv6 EgressObservation `json:"ipv6"`
}

func (r PublicEgressResult) Validate() error {
	for _, item := range []struct {
		family string
		value  EgressObservation
	}{{"ipv4", r.IPv4}, {"ipv6", r.IPv6}} {
		if err := validateJobEgressObservation(item.value, item.family); err != nil {
			return err
		}
	}
	return nil
}

func validateJobEgressObservation(o EgressObservation, family string) error {
	if o.Family != family || o.CheckedAt < 0 || o.ValidUntil < 0 || o.LastSuccess != nil {
		return errors.New("public egress observation is invalid")
	}
	switch o.Status {
	case "available":
		if !PublicEgressIP(o.IP, family) || o.Reason != "verified" {
			return errors.New("available public egress requires a verified public address")
		}
	case "unavailable":
		if o.IP != "" || (o.Reason != "no_route" && o.Reason != "family_unavailable") {
			return errors.New("unavailable public egress is invalid")
		}
	case "unknown":
		if o.IP != "" {
			return errors.New("unknown public egress must not carry an address")
		}
		switch o.Reason {
		case "dns", "tls", "timeout", "http_status", "invalid_response", "network":
		default:
			return errors.New("unknown public egress reason is invalid")
		}
	default:
		return errors.New("public egress status is invalid")
	}
	return nil
}

// Succeeded reports whether at least one family has a verified address.
func (r PublicEgressResult) Succeeded() bool {
	return r.IPv4.Status == "available" || r.IPv6.Status == "available"
}

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

var nat64WellKnownPrefix = netip.MustParsePrefix("64:ff9b::/96")

// PublicDestinationIP decides whether the Agent may open a connection to an address it was given or
// resolved (a destination, as opposed to an observed egress address, which PublicEgressIP validates).
// It is PublicEgressIP plus one exception: an IPv6 address inside the well-known NAT64 prefix
// 64:ff9b::/96 is judged by the IPv4 address embedded in it, so an IPv6-only host can still reach public
// IPv4 destinations through its NAT64 gateway while an embedded private, loopback or link-local IPv4
// address stays refused. No other IPv6 range is relaxed.
func PublicDestinationIP(text, family string) bool {
	if PublicEgressIP(text, family) {
		return true
	}
	a, err := netip.ParseAddr(text)
	if family != "ipv6" || err != nil || a.Zone() != "" || !a.Is6() || a.Is4In6() || !nat64WellKnownPrefix.Contains(a) {
		return false
	}
	b := a.As16()
	return PublicEgressIP(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}).String(), "ipv4")
}

// NormalizeEgress isolates all optional errors and bounds times against the
// accepted report/server clocks. Stale successes remain explicitly timestamped.
func NormalizeEgress(o EgressObservation, family string, collected, received int64) EgressObservation {
	return normalizeEgress(o, family, collected, received, EgressSuccessTTL)
}

// NormalizeStoredEgress also accepts the longer validity the Server gives to job-based checks.
func NormalizeStoredEgress(o EgressObservation, family string, collected, received int64) EgressObservation {
	return normalizeEgress(o, family, collected, received, EgressJobSuccessTTL)
}

func normalizeEgress(o EgressObservation, family string, collected, received int64, successTTL time.Duration) EgressObservation {
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
		maxTTL = successTTL
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
