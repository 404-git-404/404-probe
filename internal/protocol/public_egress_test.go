package protocol

import (
	"encoding/json"
	"testing"
)

func TestM10PublicIPPolicy(t *testing.T) {
	for _, test := range []struct {
		ip, family string
		valid      bool
	}{
		{"8.8.8.8", "ipv4", true}, {"2606:4700:4700::1111", "ipv6", true},
		{"10.0.0.1", "ipv4", false}, {"100.64.0.1", "ipv4", false}, {"127.0.0.1", "ipv4", false}, {"169.254.1.1", "ipv4", false}, {"192.0.2.1", "ipv4", false}, {"198.18.0.1", "ipv4", false}, {"224.0.0.1", "ipv4", false}, {"255.255.255.255", "ipv4", false},
		{"::1", "ipv6", false}, {"fc00::1", "ipv6", false}, {"fe80::1", "ipv6", false}, {"2001:db8::1", "ipv6", false}, {"3fff::1", "ipv6", false}, {"2002:0808:0808::1", "ipv6", false}, {"2001::1", "ipv6", false}, {"64:ff9b::808:808", "ipv6", false}, {"64:ff9b:1::1", "ipv6", false}, {"::ffff:8.8.8.8", "ipv6", false}, {"8.8.8.8", "ipv6", false}, {"2606:4700::1%eth0", "ipv6", false}, {"nonsense", "ipv4", false},
	} {
		t.Run(test.ip+test.family, func(t *testing.T) {
			if got := PublicEgressIP(test.ip, test.family); got != test.valid {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestM10OptionalShapesAreIsolated(t *testing.T) {
	for _, raw := range []string{`"text"`, `[]`, `42`, `true`, `{}`, `{"version":2}`, `{"version":1,"ipv4":"bad","ipv6":[]}`, `{"version":1,"unexpected":true}`} {
		var p PublicEgress
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if p.IPv4.Status != "unknown" || p.IPv6.Status != "unknown" {
			t.Fatalf("bad shape %s: %+v", raw, p)
		}
	}
	var p PublicEgress
	if err := json.Unmarshal([]byte(`{"version":1,"ipv4":"bad","ipv6":{"family":"ipv6","status":"available","ip":"2606:4700::1111","checked_at":1000,"valid_until":2000,"reason":"verified"}}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.IPv4.Status != "unknown" || NormalizeEgress(p.IPv6, "ipv6", 2000, 2000).Status != "available" {
		t.Fatal(p)
	}
}
