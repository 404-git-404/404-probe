package protocol

import (
	"errors"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// R07: user-entered network quality targets. These rules are shared by the Server (on save) and
// the Agent (before it accepts a configuration), so both sides agree on what a valid target is.

// QualityTargetsHeader is sent by an Agent that understands user targets. It only distinguishes a
// current Agent from an old one; the safety boundary is the Agent's per-connection address check.
const QualityTargetsHeader = "X-404-Probe-Quality-Targets"
const QualityTargetsUserV1 = "user_v1"

const (
	QualityIPv6Present = "present"
	QualityIPv6None    = "none"
	QualityIPv6Unknown = "unknown"
)

// Agent support states reported with the configuration.
const (
	QualitySupportUserTargets     = "user_targets"
	QualitySupportNegotiating     = "negotiating"
	QualitySupportUpgradeRequired = "upgrade_required"
	QualitySupportUnsupported     = "unsupported"
	QualitySupportExcluded        = "excluded"
)

// Field error codes for the save request.
const (
	QualityErrInvalidHost     = "invalid_host"
	QualityErrReservedName    = "reserved_name"
	QualityErrUnsafeAddress   = "unsafe_address"
	QualityErrFamilyMismatch  = "family_mismatch"
	QualityErrInvalidPort     = "invalid_port"
	QualityErrInvalidLabel    = "invalid_label"
	QualityErrLabelRequired   = "label_required"
	QualityErrInvalidInterval = "invalid_interval"
	QualityErrInvalidEncoding = "invalid_encoding"
	QualityErrInvalidRequest  = "invalid_request"
)

const (
	QualityMinIntervalMS  = 30000
	QualityMaxIntervalMS  = 300000
	QualityStepIntervalMS = 30000
	qualityLabelBytes     = 96
	qualityLabelRunes     = 32
)

// QualityProvinces are the 31 provincial-level regions a built-in slot may name.
var QualityProvinces = []string{
	"河北", "山西", "辽宁", "吉林", "黑龙江", "江苏", "浙江", "安徽", "福建", "江西", "山东", "河南", "湖北", "湖南", "广东", "海南",
	"四川", "贵州", "云南", "陕西", "甘肃", "青海", "内蒙古", "广西", "西藏", "宁夏", "新疆", "北京", "天津", "上海", "重庆",
}

func QualityProvince(name string) bool {
	for _, province := range QualityProvinces {
		if province == name {
			return true
		}
	}
	return false
}

// QualityISPForSlot is the built-in ISP implied by a slot position in v1.0.4.
func QualityISPForSlot(slot string) string {
	if QualitySlot(slot) {
		return slot
	}
	return ""
}

// QualityV4Slot is one of the three slots of a card. Region and ISP are the user's own labels;
// 404-probe does not verify, calibrate or warn about them.
type QualityV4Slot struct {
	Slot        string `json:"slot"`
	Kind        string `json:"kind"`
	Region      string `json:"region"`
	ISP         string `json:"isp"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	SlotEnabled bool   `json:"slot_enabled"`
	IPv4TCP     bool   `json:"ipv4_tcp"`
	IPv4ICMP    bool   `json:"ipv4_icmp"`
	IPv6TCP     bool   `json:"ipv6_tcp"`
	IPv6ICMP    bool   `json:"ipv6_icmp"`
}

// Sub reports the sub-item tick for a lane.
func (s QualityV4Slot) Sub(family, proto string) bool {
	switch family + "/" + proto {
	case "ipv4/tcp":
		return s.IPv4TCP
	case "ipv4/icmp":
		return s.IPv4ICMP
	case "ipv6/tcp":
		return s.IPv6TCP
	case "ipv6/icmp":
		return s.IPv6ICMP
	}
	return false
}

// QualityV4SaveRequest saves the settings. It never changes the master switch.
type QualityV4SaveRequest struct {
	ExpectedRevision string          `json:"expected_revision"`
	IntervalMS       int             `json:"interval_ms"`
	Slots            []QualityV4Slot `json:"slots"`
}

// QualityV4EnabledRequest changes only the master switch.
type QualityV4EnabledRequest struct {
	ExpectedRevision string `json:"expected_revision"`
	Enabled          *bool  `json:"enabled"`
}

type QualityV4Lane struct {
	Slot         string  `json:"slot"`
	Family       string  `json:"family"`
	Protocol     string  `json:"protocol"`
	TargetID     *string `json:"target_id"`
	SlotRevision string  `json:"slot_revision"`
	Status       string  `json:"status"`
	Condition    string  `json:"condition"`
}

// QualityV4Config is the read model of a card's network quality configuration.
type QualityV4Config struct {
	SettingsRevision    string          `json:"settings_revision"`
	ConfigRevision      string          `json:"config_revision"`
	Enabled             bool            `json:"enabled"`
	IntervalMS          int             `json:"interval_ms"`
	EffectiveIntervalMS int             `json:"effective_interval_ms"`
	State               string          `json:"state"`
	AgentSupport        string          `json:"agent_support"`
	Slots               []QualityV4Slot `json:"slots"`
	Lanes               []QualityV4Lane `json:"lanes"`
}

// QualityFieldError points a validation failure at one input of one slot.
type QualityFieldError struct {
	Slot  string `json:"slot,omitempty"`
	Field string `json:"field"`
	Code  string `json:"code"`
}

func (e QualityFieldError) Error() string { return e.Slot + "/" + e.Field + ": " + e.Code }

var qualitySlotOrder = []string{"telecom", "unicom", "mobile"}

// QualitySlotOrder returns the canonical slot order.
func QualitySlotOrder() []string { return append([]string(nil), qualitySlotOrder...) }

func QualityIntervalValid(ms int) bool {
	return ms >= QualityMinIntervalMS && ms <= QualityMaxIntervalMS && ms%QualityStepIntervalMS == 0
}

func qualityReservedName(host string) bool {
	switch host {
	case "localhost", "intranet", "private", "corp", "home", "lan", "local", "localdomain", "internal", "invalid", "test", "example", "onion", "alt", "arpa":
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".localdomain", ".internal", ".home.arpa", ".invalid", ".test", ".example", ".onion", ".alt", ".arpa", ".intranet", ".private", ".corp", ".home", ".lan"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// NormalizeQualityHost applies the syntax rules (design 5.2, step 1). It returns the canonical
// host, the literal IP family ("ipv4", "ipv6") or "" for a name, and an error code. An IPv4-mapped
// IPv6 literal becomes an IPv4 literal. It never resolves anything.
func NormalizeQualityHost(host string) (canonical, literal, code string) {
	if host == "" {
		return "", "", ""
	}
	if len(host) > 253 {
		return "", "", QualityErrInvalidHost
	}
	// Brackets, zone identifiers, userinfo, paths and white space are never part of a host. A colon is
	// left to the address parser (IPv6 literals); any other colon fails the name rules below.
	if strings.ContainsAny(host, "[]%@/"+" \t\r\n") {
		return "", "", QualityErrInvalidHost
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return normalizeQualityAddr(addr)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || len(host) > 253 {
		return "", "", QualityErrInvalidHost
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		if qualityReservedName(host) {
			return "", "", QualityErrReservedName
		}
		return "", "", QualityErrInvalidHost
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", "", QualityErrInvalidHost
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", "", QualityErrInvalidHost
			}
		}
	}
	last := labels[len(labels)-1]
	hasLetter := false
	for i := 0; i < len(last); i++ {
		if last[i] >= 'a' && last[i] <= 'z' {
			hasLetter = true
		}
	}
	if !hasLetter || isHexNumber(last) {
		return "", "", QualityErrInvalidHost
	}
	if qualityReservedName(host) {
		return "", "", QualityErrReservedName
	}
	return host, "", ""
}

// "0x7f" and "0x" style last labels are read as numbers by inet_aton.
func isHexNumber(label string) bool {
	if len(label) < 2 || label[0] != '0' || label[1] != 'x' {
		return false
	}
	for i := 2; i < len(label); i++ {
		c := label[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func normalizeQualityAddr(addr netip.Addr) (string, string, string) {
	if addr.Zone() != "" {
		return "", "", QualityErrInvalidHost
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	family := "ipv6"
	if addr.Is4() {
		family = "ipv4"
	}
	if !PublicDestinationIP(addr.String(), family) {
		return "", "", QualityErrUnsafeAddress
	}
	return addr.String(), family, ""
}

// ValidQualityLabel checks a custom region or ISP label (design 6). It rejects instead of editing.
func ValidQualityLabel(label string) bool {
	if label == "" || !utf8.ValidString(label) || len(label) > qualityLabelBytes || utf8.RuneCountInString(label) > qualityLabelRunes {
		return false
	}
	if label[0] == ' ' || label[len(label)-1] == ' ' || strings.Contains(label, "  ") {
		return false
	}
	for _, r := range label {
		if r == 0xFFFD {
			return false
		}
		if r == ' ' {
			continue
		}
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Zs, r) || unicode.IsSpace(r) {
			return false
		}
		if (r >= 0xFDD0 && r <= 0xFDEF) || r&0xFFFE == 0xFFFE {
			return false
		}
	}
	return true
}

// NormalizeQualitySlot validates one slot and returns its canonical form. Rules that need the
// network (DNS) are not applied here. Port 0 means "not set" and is only allowed without a host.
func NormalizeQualitySlot(s QualityV4Slot) (QualityV4Slot, []QualityFieldError) {
	var errs []QualityFieldError
	add := func(field, code string) {
		errs = append(errs, QualityFieldError{Slot: s.Slot, Field: field, Code: code})
	}
	if !QualitySlot(s.Slot) {
		add("slot", QualityErrInvalidRequest)
		return s, errs
	}
	out := s
	switch s.Kind {
	case "builtin", "custom":
	default:
		add("kind", QualityErrInvalidRequest)
	}
	host, literal, code := NormalizeQualityHost(s.Host)
	if code != "" {
		add("host", code)
	}
	out.Host = host
	if s.Port < 0 || s.Port > 65535 {
		add("port", QualityErrInvalidPort)
	} else if host != "" && s.Port == 0 {
		add("port", QualityErrInvalidPort)
	} else if host == "" {
		out.Port = 0
	}
	if code == "" && literal == "ipv4" && (s.IPv6TCP || s.IPv6ICMP) {
		if s.IPv6TCP {
			add("ipv6_tcp", QualityErrFamilyMismatch)
		}
		if s.IPv6ICMP {
			add("ipv6_icmp", QualityErrFamilyMismatch)
		}
	}
	if code == "" && literal == "ipv6" && (s.IPv4TCP || s.IPv4ICMP) {
		if s.IPv4TCP {
			add("ipv4_tcp", QualityErrFamilyMismatch)
		}
		if s.IPv4ICMP {
			add("ipv4_icmp", QualityErrFamilyMismatch)
		}
	}
	checkLabel := func(field, value string, builtin func(string) bool) {
		if value == "" {
			if s.Host != "" {
				add(field, QualityErrLabelRequired)
			}
			return
		}
		if s.Kind == "builtin" {
			if !builtin(value) {
				add(field, QualityErrInvalidLabel)
			}
			return
		}
		if !ValidQualityLabel(value) {
			add(field, QualityErrInvalidLabel)
		}
	}
	checkLabel("region", s.Region, QualityProvince)
	checkLabel("isp", s.ISP, QualitySlot)
	return out, errs
}

var errQualityBody = errors.New("invalid quality request encoding")

// CheckQualityRequestEncoding inspects the raw request body before it is decoded. encoding/json
// silently replaces invalid UTF-8 and lone surrogate escapes with U+FFFD, so a decoded string can
// no longer tell whether the sender was malformed; the whole request is rejected instead.
func CheckQualityRequestEncoding(body []byte) error {
	if !utf8.Valid(body) {
		return errQualityBody
	}
	inString := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(body) {
				return errQualityBody
			}
			i++
			if body[i] != 'u' {
				continue
			}
			unit, ok := qualityHex4(body, i+1)
			if !ok {
				return errQualityBody
			}
			i += 4
			switch {
			case unit >= 0xD800 && unit <= 0xDBFF:
				// A high surrogate must be followed directly by an escaped low surrogate.
				if i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
					low, ok := qualityHex4(body, i+3)
					if ok && low >= 0xDC00 && low <= 0xDFFF {
						i += 6
						continue
					}
				}
				return errQualityBody
			case unit >= 0xDC00 && unit <= 0xDFFF:
				return errQualityBody
			}
		}
	}
	return nil
}

func qualityHex4(body []byte, at int) (rune, bool) {
	if at+4 > len(body) {
		return 0, false
	}
	var value rune
	for _, c := range body[at : at+4] {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			value |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

// ParseQualityExpectedRevision reads a canonical decimal revision ("0" is allowed).
func ParseQualityExpectedRevision(s string) (int64, bool) {
	if s == "0" {
		return 0, true
	}
	v, err := QualityInteger(s)
	return v, err == nil
}
