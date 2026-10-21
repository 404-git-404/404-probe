package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQualityHostRules(t *testing.T) {
	for _, tc := range []struct {
		host, canonical, literal, code string
	}{
		{"", "", "", ""},
		{"example.com", "example.com", "", ""},
		{"Example.COM.", "example.com", "", ""},
		{"xn--fiqs8s.example.org", "xn--fiqs8s.example.org", "", ""},
		{"a-b.c1.example.net", "a-b.c1.example.net", "", ""},
		{"8.8.8.8", "8.8.8.8", "ipv4", ""},
		{"2606:4700:4700::1111", "2606:4700:4700::1111", "ipv6", ""},
		{"2606:4700:4700:0:0:0:0:1111", "2606:4700:4700::1111", "ipv6", ""},
		{"::ffff:1.2.3.4", "1.2.3.4", "ipv4", ""},
		{"64:ff9b::808:808", "64:ff9b::808:808", "ipv6", ""},
		// Numbers that a resolver may read as an IPv4 address are not host names.
		{"0x7f.1", "", "", QualityErrInvalidHost},
		{"2130706433", "", "", QualityErrInvalidHost},
		{"127.1", "", "", QualityErrInvalidHost},
		{"1.2.3.04", "", "", QualityErrInvalidHost},
		{"1.2.3.4.", "", "", QualityErrInvalidHost},
		{"a.0x", "", "", QualityErrInvalidHost},
		{"a.0xff", "", "", QualityErrInvalidHost},
		{"a..b", "", "", QualityErrInvalidHost},
		{"-a.com", "", "", QualityErrInvalidHost},
		{"a-.com", "", "", QualityErrInvalidHost},
		{"singlelabel", "", "", QualityErrInvalidHost},
		{"exa_mple.com", "", "", QualityErrInvalidHost},
		{"例え.jp", "", "", QualityErrInvalidHost},
		{"a b.com", "", "", QualityErrInvalidHost},
		{"a.com/path", "", "", QualityErrInvalidHost},
		{"user@a.com", "", "", QualityErrInvalidHost},
		{"a.com:443", "", "", QualityErrInvalidHost},
		{"http://a.com", "", "", QualityErrInvalidHost},
		{"[::1]", "", "", QualityErrInvalidHost},
		{"[2606:4700:4700::1111]", "", "", QualityErrInvalidHost},
		{"fe80::1%eth0", "", "", QualityErrInvalidHost},
		{strings.Repeat("a", 64) + ".com", "", "", QualityErrInvalidHost},
		{strings.Repeat("a.", 130) + "com", "", "", QualityErrInvalidHost},
		{"localhost", "", "", QualityErrReservedName},
		{"LOCALHOST.", "", "", QualityErrReservedName},
		{"foo.localhost", "", "", QualityErrReservedName},
		{"printer.local", "", "", QualityErrReservedName},
		{"a.localdomain", "", "", QualityErrReservedName},
		{"db.internal", "", "", QualityErrReservedName},
		{"router.home.arpa", "", "", QualityErrReservedName},
		{"1.0.0.127.in-addr.arpa", "", "", QualityErrReservedName},
		{"x.invalid", "", "", QualityErrReservedName},
		{"x.test", "", "", QualityErrReservedName},
		{"x.example", "", "", QualityErrReservedName},
		{"abc.onion", "", "", QualityErrReservedName},
		{"x.alt", "", "", QualityErrReservedName},
		{"nas.intranet", "", "", QualityErrReservedName},
		{"x.private", "", "", QualityErrReservedName},
		{"x.corp", "", "", QualityErrReservedName},
		{"x.home", "", "", QualityErrReservedName},
		{"x.lan", "", "", QualityErrReservedName},
		{"127.0.0.1", "", "", QualityErrUnsafeAddress},
		{"10.0.0.1", "", "", QualityErrUnsafeAddress},
		{"192.168.1.1", "", "", QualityErrUnsafeAddress},
		{"169.254.169.254", "", "", QualityErrUnsafeAddress},
		{"100.64.0.1", "", "", QualityErrUnsafeAddress},
		{"224.0.0.1", "", "", QualityErrUnsafeAddress},
		{"::1", "", "", QualityErrUnsafeAddress},
		{"fc00::1", "", "", QualityErrUnsafeAddress},
		{"fe80::1", "", "", QualityErrUnsafeAddress},
		{"2001:db8::1", "", "", QualityErrUnsafeAddress},
		{"::ffff:127.0.0.1", "", "", QualityErrUnsafeAddress},
		{"64:ff9b::a00:1", "", "", QualityErrUnsafeAddress},
		{"64:ff9b:1::1", "", "", QualityErrUnsafeAddress},
	} {
		canonical, literal, code := NormalizeQualityHost(tc.host)
		if canonical != tc.canonical || literal != tc.literal || code != tc.code {
			t.Errorf("%q => (%q,%q,%q), want (%q,%q,%q)", tc.host, canonical, literal, code, tc.canonical, tc.literal, tc.code)
		}
	}
}

// qr builds a string from code points and qe an escaped JSON unit, so the test source holds no
// invisible characters and no \u sequences.
func qr(code ...int) string {
	var out string
	for _, c := range code {
		out += string(rune(c))
	}
	return out
}

var qb = string(rune(92))

func qe(hex string) string { return qb + "u" + hex }

func TestQualityLabelRules(t *testing.T) {
	for label, want := range map[string]bool{
		"广东": true, "上海 联通": true, "My ISP": true, "abc": true, strings.Repeat("字", 32): true,
		"<img src=x onerror=1>": true,
		"":                      false, " lead": false, "trail ": false, "two  spaces": false, strings.Repeat("字", 33): false,
		"tab" + qr(9) + "here": false, "line" + qr(10) + "break": false, "nul" + qr(0): false, "bell" + qr(7): false,
		"ideographic" + qr(0x3000) + "space": false, "nb" + qr(0xa0) + "sp": false, "zero" + qr(0x200b) + "width": false,
		"bom" + qr(0xfeff): false, "rtl" + qr(0x202e) + "override": false, "iso" + qr(0x2066) + "late": false,
		"word" + qr(0x2060) + "joiner": false, "sep" + qr(0x2028) + "line": false, "para" + qr(0x2029) + "sep": false,
		"nonchar" + qr(0xfdd0): false, "nonchar" + qr(0x10ffff): false, "nonchar" + qr(0xfffe): false,
		"replacement" + qr(0xfffd): false, "bad" + string([]byte{0xff}) + "utf8": false,
		strings.Repeat("a", 97): false,
	} {
		if got := ValidQualityLabel(label); got != want {
			t.Errorf("%q => %t, want %t", label, got, want)
		}
	}
	// 25 four-byte characters exceed the 96 byte limit although the rune count is allowed.
	if ValidQualityLabel(strings.Repeat(qr(0x1F600), 25)) {
		t.Error("label over 96 bytes accepted")
	}
	if !ValidQualityLabel(strings.Repeat(qr(0x1F600), 24)) {
		t.Error("96 byte label rejected")
	}
}

func TestQualitySlotNormalization(t *testing.T) {
	good := QualityV4Slot{Slot: "telecom", Kind: "builtin", Region: "广东", ISP: "telecom", Host: "Example.COM", Port: 443, SlotEnabled: true, IPv4TCP: true}
	out, errs := NormalizeQualitySlot(good)
	if len(errs) != 0 || out.Host != "example.com" || out.Port != 443 {
		t.Fatal(out, errs)
	}
	cases := []struct {
		name   string
		mutate func(*QualityV4Slot)
		want   []QualityFieldError
	}{
		{"unknown slot", func(s *QualityV4Slot) { s.Slot = "other" }, []QualityFieldError{{Slot: "other", Field: "slot", Code: QualityErrInvalidRequest}}},
		{"unknown kind", func(s *QualityV4Slot) { s.Kind = "catalog" }, []QualityFieldError{{Slot: "telecom", Field: "kind", Code: QualityErrInvalidRequest}}},
		{"private address", func(s *QualityV4Slot) { s.Host = "10.1.2.3" }, []QualityFieldError{{Slot: "telecom", Field: "host", Code: QualityErrUnsafeAddress}}},
		{"reserved name", func(s *QualityV4Slot) { s.Host = "host.local" }, []QualityFieldError{{Slot: "telecom", Field: "host", Code: QualityErrReservedName}}},
		{"port missing with host", func(s *QualityV4Slot) { s.Port = 0 }, []QualityFieldError{{Slot: "telecom", Field: "port", Code: QualityErrInvalidPort}}},
		{"port too large", func(s *QualityV4Slot) { s.Port = 65536 }, []QualityFieldError{{Slot: "telecom", Field: "port", Code: QualityErrInvalidPort}}},
		{"negative port", func(s *QualityV4Slot) { s.Port = -1 }, []QualityFieldError{{Slot: "telecom", Field: "port", Code: QualityErrInvalidPort}}},
		{"ipv4 literal with ipv6 sub-items", func(s *QualityV4Slot) { s.Host = "8.8.8.8"; s.IPv6TCP = true; s.IPv6ICMP = true }, []QualityFieldError{{Slot: "telecom", Field: "ipv6_tcp", Code: QualityErrFamilyMismatch}, {Slot: "telecom", Field: "ipv6_icmp", Code: QualityErrFamilyMismatch}}},
		{"ipv6 literal with ipv4 sub-items", func(s *QualityV4Slot) { s.Host = "2606:4700:4700::1111" }, []QualityFieldError{{Slot: "telecom", Field: "ipv4_tcp", Code: QualityErrFamilyMismatch}}},
		{"mapped ipv4 counts as ipv4", func(s *QualityV4Slot) { s.Host = "::ffff:8.8.8.8"; s.IPv4TCP = false; s.IPv6ICMP = true }, []QualityFieldError{{Slot: "telecom", Field: "ipv6_icmp", Code: QualityErrFamilyMismatch}}},
		{"builtin region must be a province", func(s *QualityV4Slot) { s.Region = "深圳" }, []QualityFieldError{{Slot: "telecom", Field: "region", Code: QualityErrInvalidLabel}}},
		{"builtin isp must be an enum", func(s *QualityV4Slot) { s.ISP = "电信" }, []QualityFieldError{{Slot: "telecom", Field: "isp", Code: QualityErrInvalidLabel}}},
		{"labels required with host", func(s *QualityV4Slot) { s.Region = ""; s.ISP = "" }, []QualityFieldError{{Slot: "telecom", Field: "region", Code: QualityErrLabelRequired}, {Slot: "telecom", Field: "isp", Code: QualityErrLabelRequired}}},
		{"custom label with control character", func(s *QualityV4Slot) { s.Kind = "custom"; s.Region = "a\tb"; s.ISP = "isp" }, []QualityFieldError{{Slot: "telecom", Field: "region", Code: QualityErrInvalidLabel}}},
		{"custom label with replacement character", func(s *QualityV4Slot) { s.Kind = "custom"; s.Region = "ok"; s.ISP = "x" + qr(0xfffd) }, []QualityFieldError{{Slot: "telecom", Field: "isp", Code: QualityErrInvalidLabel}}},
	}
	for _, tc := range cases {
		slot := good
		tc.mutate(&slot)
		_, errs := NormalizeQualitySlot(slot)
		if len(errs) != len(tc.want) {
			t.Errorf("%s: %v want %v", tc.name, errs, tc.want)
			continue
		}
		for i := range errs {
			if errs[i] != tc.want[i] {
				t.Errorf("%s: %v want %v", tc.name, errs, tc.want)
			}
		}
	}
	// An empty slot may keep empty labels and a stale port, which is dropped.
	empty, errs := NormalizeQualitySlot(QualityV4Slot{Slot: "mobile", Kind: "custom", Port: 443, IPv4TCP: true})
	if len(errs) != 0 || empty.Host != "" || empty.Port != 0 {
		t.Fatal(empty, errs)
	}
	// Custom labels are free text; builtin enum values are not.
	custom, errs := NormalizeQualitySlot(QualityV4Slot{Slot: "unicom", Kind: "custom", Region: "我的机房", ISP: "电信", Host: "8.8.4.4", Port: 53, IPv4ICMP: true})
	if len(errs) != 0 || custom.ISP != "电信" {
		t.Fatal(custom, errs)
	}
}

func TestQualityProvincesAndInterval(t *testing.T) {
	if len(QualityProvinces) != 31 {
		t.Fatal(len(QualityProvinces))
	}
	seen := map[string]bool{}
	for _, name := range QualityProvinces {
		if seen[name] || !QualityProvince(name) || !ValidQualityLabel(name) {
			t.Fatal(name)
		}
		seen[name] = true
	}
	for _, name := range []string{"深圳", "广州", "Guangdong", "", "香港", "澳门", "台湾"} {
		if QualityProvince(name) {
			t.Fatal(name)
		}
	}
	for ms, want := range map[int]bool{30000: true, 60000: true, 120000: true, 300000: true, 0: false, 29999: false, 45000: false, 30001: false, 330000: false, -30000: false} {
		if QualityIntervalValid(ms) != want {
			t.Errorf("interval %d", ms)
		}
	}
	for _, slot := range QualitySlotOrder() {
		if QualityISPForSlot(slot) != slot {
			t.Fatal(slot)
		}
	}
	for _, revision := range []string{"0", "1", "9223372036854775807"} {
		if _, ok := ParseQualityExpectedRevision(revision); !ok {
			t.Fatal(revision)
		}
	}
	for _, revision := range []string{"", "00", "01", "-1", "1.5", "abc", "9223372036854775808", "+1"} {
		if _, ok := ParseQualityExpectedRevision(revision); ok {
			t.Fatal(revision)
		}
	}
}

func TestQualityRequestEncodingCheck(t *testing.T) {
	str := func(inner string) []byte { return []byte(`{"region":"` + inner + `"}`) }
	for name, body := range map[string][]byte{
		"plain":                    str("广东"),
		"escaped quote":            str(qb + `"b`),
		"escaped backslash u":      str(qb + qb + "uD800"),
		"surrogate pair":           str(qe("d83d") + qe("de00")),
		"surrogate pair uppercase": str(qe("D83D") + qe("DE00")),
		"bmp escape":               str(qe("5e7f") + qe("4e1c")),
		"escaped slash":            str(qb + "/"),
		"outside strings":          []byte(`{"a":1,"b":[true,null]}`),
	} {
		if err := CheckQualityRequestEncoding(body); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, body := range map[string][]byte{
		"invalid utf-8 byte":        str("a" + string([]byte{0xff}) + "b"),
		"truncated sequence":        str(string([]byte{0xe5, 0xb9})),
		"overlong sequence":         str(string([]byte{0xc0, 0x80})),
		"encoded surrogate":         str(string([]byte{0xed, 0xa0, 0x80})),
		"lone high surrogate":       str(qe("D800")),
		"lone low surrogate":        str(qe("DC00")),
		"high without low":          str(qe("D800") + "x"),
		"high then high":            str(qe("D800") + qe("D800")),
		"low then high":             str(qe("DC00") + qe("D800")),
		"high then other escape":    str(qe("D800") + qe("0041")),
		"truncated escape":          []byte(`{"region":"` + qb + "uD8"),
		"bad hex":                   str(qe("ZZZZ")),
		"high surrogate at end":     []byte(`"` + qe("D800")),
		"lone low after backslash":  str(qb + qb + qe("DC00")),
		"surrogate in a key":        []byte(`{"` + qe("D800") + `":1}`),
		"low after a valid pair":    str(qe("d83d") + qe("de00") + qe("DE00")),
		"high before escaped slash": str(qe("D83D") + qb + "/"),
	} {
		if err := CheckQualityRequestEncoding(body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestQualityCapabilitiesTargetsNegotiation(t *testing.T) {
	c := QualityCapabilities{Version: 2, Semantics: QualityStagedSemantics}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Lanes = append(c.Lanes, QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{QualityIPv6Present, QualityIPv6None, QualityIPv6Unknown} {
		ok := c
		ok.Targets, ok.IPv6Address = QualityTargetsUserV1, state
		if err := ok.Validate(); err != nil {
			t.Fatal(state, err)
		}
	}
	ok := c
	ok.Targets = QualityTargetsUserV1
	if err := ok.Validate(); err != nil {
		t.Fatal("targets without an ipv6 state", err)
	}
	for name, change := range map[string]func(*QualityCapabilities){
		"unknown targets":            func(c *QualityCapabilities) { c.Targets = "user_v2" },
		"ipv6 state without targets": func(c *QualityCapabilities) { c.IPv6Address = QualityIPv6None },
		"unknown ipv6 state":         func(c *QualityCapabilities) { c.Targets = QualityTargetsUserV1; c.IPv6Address = "maybe" },
	} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// An older Server decodes the first-step capability without the new fields.
	wire, _ := json.Marshal(c)
	if strings.Contains(string(wire), "targets") || strings.Contains(string(wire), "ipv6_address") {
		t.Fatal(string(wire))
	}
	capabilities, _ := json.Marshal(ReportCapabilities{NetworkQualityUserTargets: true})
	if string(capabilities) != `{"network_quality_user_targets":true}` {
		t.Fatal(string(capabilities))
	}
}

func notFoundSample(now time.Time, proto string) (QualitySample, QualityTarget) {
	target := QualityTarget{ID: "r07-target", Protocol: proto, Family: "ipv4", Source: "user"}
	s := QualitySample{Sequence: "1", ConfigRevision: "1", SlotRevision: "1", TargetID: target.ID,
		ScheduledAt: now.UnixMilli() - 40, StartedAt: now.UnixMilli() - 40, FinishedAt: now.UnixMilli() - 5, DurationMS: 35, Outcome: "dns_error"}
	d := QualityDetails{Semantics: QualityStagedSemantics, DNSMS: 30, DNSOutcome: "not_found", ProbeComplete: true, ErrorStage: "dns", ErrorCode: "not_found"}
	if proto == "icmp" {
		d.ProbeComplete = false
		d.Requested = 3
		d.WindowMS = 2000
		for i := 0; i < 3; i++ {
			d.Echoes = append(d.Echoes, QualityEcho{Seq: i + 1, Status: "not_sent"})
		}
	}
	s.Details = d.Encode()
	return s, target
}

func TestQualityDetailsAcceptNotFound(t *testing.T) {
	now := time.Now()
	for _, proto := range []string{"tcp", "icmp"} {
		s, target := notFoundSample(now, proto)
		if reason := s.Validate(now, target); reason != "" {
			t.Fatalf("%s not_found sample rejected: %q", proto, reason)
		}
		// The three places that name the DNS result must agree.
		for name, change := range map[string]func(*QualityDetails){
			"unknown dns outcome": func(d *QualityDetails) { d.DNSOutcome = "gone" },
			"unknown error code":  func(d *QualityDetails) { d.ErrorCode = "gone" },
		} {
			d, _ := DecodeQualityDetails(s.Details)
			change(d)
			bad := s
			bad.Details = d.Encode()
			if reason := bad.Validate(now, target); reason != "invalid" {
				t.Errorf("%s %s accepted: %q", proto, name, reason)
			}
		}
	}
	// The TCP DNS stage requires the error code to repeat the DNS outcome.
	s, target := notFoundSample(now, "tcp")
	d, _ := DecodeQualityDetails(s.Details)
	d.ErrorCode = "dns_error"
	s.Details = d.Encode()
	if reason := s.Validate(now, target); reason != "invalid" {
		t.Fatalf("mismatched TCP error code accepted: %q", reason)
	}
}
