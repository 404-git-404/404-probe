package protocol

import (
	"encoding/json"
	"errors"
)

// A1 negotiates lanes, not A2 measurement/statistics semantics.
const QualityV2Semantics = "single_probe_v1"
const QualityDetailsLimit = 2048

// Discovery uses an HTTP header that legacy Servers safely ignore. Report JSON
// remains v1 until a Server explicitly advertises support in its response.
const QualityVersionHeader = "X-404-Probe-Quality-Version"

type QualityLaneCapability struct {
	Family    string `json:"family"`
	Protocol  string `json:"protocol"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}
type QualityCapabilities struct {
	Version        int                     `json:"version"`
	Semantics      string                  `json:"semantics"`
	ExcludedReason string                  `json:"excluded_reason,omitempty"`
	Lanes          []QualityLaneCapability `json:"lanes"`
	// Targets and IPv6Address are only sent after the Server answered the quality-targets header
	// (two-step negotiation); an older Server never sees them.
	Targets     string `json:"targets,omitempty"`
	IPv6Address string `json:"ipv6_address,omitempty"`
}

func (c QualityCapabilities) Validate() error {
	if c.Version != 2 || (c.Semantics != QualityV2Semantics && c.Semantics != QualityStagedSemantics) || len(c.Lanes) != 4 {
		return errors.New("invalid quality v2 capability")
	}
	if c.ExcludedReason != "" && c.ExcludedReason != "platform_excluded:alpine" && c.ExcludedReason != "platform_identity_unknown" {
		return errors.New("invalid quality exclusion")
	}
	if c.Targets != "" && c.Targets != QualityTargetsUserV1 {
		return errors.New("invalid quality targets capability")
	}
	switch c.IPv6Address {
	case "":
	case QualityIPv6Present, QualityIPv6None, QualityIPv6Unknown:
		if c.Targets != QualityTargetsUserV1 {
			return errors.New("ipv6 address state requires user targets")
		}
	default:
		return errors.New("invalid ipv6 address state")
	}
	seen := map[string]bool{}
	for _, l := range c.Lanes {
		key := l.Family + "/" + l.Protocol
		if !QualityFamily(l.Family) || !QualityProtocol(l.Protocol) || seen[key] || (l.Reason != "" && l.Reason != "unsupported" && l.Reason != "platform_excluded") || (l.Supported && (l.Reason != "" || c.ExcludedReason != "")) {
			return errors.New("invalid quality lane capability")
		}
		seen[key] = true
	}
	wire, _ := json.Marshal(c)
	if len(wire) > QualityDetailsLimit {
		return errors.New("quality capability exceeds budget")
	}
	return nil
}
func QualityFamily(v string) bool   { return v == "ipv4" || v == "ipv6" }
func QualityProtocol(v string) bool { return v == "tcp" || v == "icmp" }
func QualitySlot(v string) bool     { return v == "telecom" || v == "unicom" || v == "mobile" }

type QualityLaneChoice struct {
	QualityChoice
	Family  string `json:"family"`
	Enabled bool   `json:"enabled"`
}
type QualityV2ConfigUpdate struct {
	ExpectedRevision string              `json:"expected_revision"`
	Enabled          bool                `json:"enabled"`
	IPv6             bool                `json:"ipv6"`
	RegionID         string              `json:"region_id"`
	Lanes            []QualityLaneChoice `json:"lanes"`
}

func (u QualityV2ConfigUpdate) Validate() error {
	if u.ExpectedRevision != "0" {
		if _, err := QualityInteger(u.ExpectedRevision); err != nil {
			return err
		}
	}
	if !validText(u.RegionID, 120) || len(u.Lanes) > 12 {
		return errors.New("invalid quality v2 config")
	}
	seen := map[string]bool{}
	for _, l := range u.Lanes {
		key := l.Slot + "/" + l.Family + "/" + l.Protocol
		if !QualitySlot(l.Slot) || !QualityFamily(l.Family) || !QualityProtocol(l.Protocol) || seen[key] || (l.Region != "" && l.Region != u.RegionID) || len(l.Host) > 253 || (l.Source != "catalog" && l.Source != "manual") {
			return errors.New("invalid quality v2 lane")
		}
		seen[key] = true
	}
	return nil
}
