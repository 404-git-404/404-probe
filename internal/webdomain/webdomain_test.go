package webdomain

import "testing"

func TestParseSuffixAndBoundaryMatching(t *testing.T) {
	for _, value := range []string{"navolyn.com", "example.co.uk"} {
		if got, err := ParseSuffix(value); err != nil || got != value {
			t.Fatalf("ParseSuffix(%q)=%q,%v", value, got, err)
		}
	}
	for _, value := range []string{"com", "co.uk", ".navolyn.com", "a.navolyn.com", "navolyn.com:443", "https://navolyn.com", "navolyn.com/", "导航.com", "127.0.0.1"} {
		if _, err := ParseSuffix(value); err == nil {
			t.Fatalf("ParseSuffix(%q) unexpectedly succeeded", value)
		}
	}
	for host, want := range map[string]bool{
		"navolyn.com": true, "a.navolyn.com": true, "x.a.navolyn.com": true,
		"evilnavolyn.com": false, "navolyn.com.evil.example": false,
	} {
		if got := MatchesSuffix(host, "navolyn.com"); got != want {
			t.Fatalf("MatchesSuffix(%q)=%v want %v", host, got, want)
		}
	}
}

func TestCanonicalOriginsAndPorts(t *testing.T) {
	for _, test := range []struct{ value, scheme, host, hostname string }{
		{"A.Navolyn.com", "https", "a.navolyn.com", "a.navolyn.com"},
		{"a.navolyn.com:443", "https", "a.navolyn.com", "a.navolyn.com"},
		{"a.navolyn.com:8443", "https", "a.navolyn.com:8443", "a.navolyn.com"},
		{"[::1]:443", "https", "[::1]", "::1"},
	} {
		host, hostname, err := CanonicalHostPort(test.value, test.scheme)
		if err != nil || host != test.host || hostname != test.hostname {
			t.Fatalf("CanonicalHostPort(%q)=%q,%q,%v", test.value, host, hostname, err)
		}
	}
	for _, value := range []string{"", "user@navolyn.com", "https://navolyn.com", "navolyn.com/path", "navolyn.com.", "navolyn..com", "navolyn.com:0", "navolyn.com:0443", "navolyn.com:65536", "navolyn.com%2f.evil"} {
		if _, _, err := CanonicalHostPort(value, "https"); err == nil {
			t.Fatalf("CanonicalHostPort(%q) unexpectedly succeeded", value)
		}
	}
	origin, err := ParseRequestOrigin("https://A.Navolyn.com:443/")
	if err != nil || origin.String() != "https://a.navolyn.com" {
		t.Fatalf("origin=%v err=%v", origin, err)
	}
}
