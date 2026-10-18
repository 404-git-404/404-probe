package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Only catalog targets can be configured. These tests use the real resolvers, not the loopback
// fixtures the rest of the package installs.
func useRealQualityResolvers(t *testing.T) {
	t.Helper()
	previous, previousV2 := qualityResolver, qualityResolverV2
	qualityResolver, qualityResolverV2 = resolveQualityTarget, resolveQualityTargetV2
	t.Cleanup(func() { qualityResolver, qualityResolverV2 = previous, previousV2 })
}

func TestQualityConfigRejectsManualTargets(t *testing.T) {
	useRealQualityResolvers(t)
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	path := webAgentPathPrefix + id + "/network-quality/config"
	regionJSON, _ := json.Marshal(qualityDirectory.Regions[0].ID) // region ids are JSON text themselves
	region := string(regionJSON)
	for name, body := range map[string]string{
		"manual host":       `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"manual","protocol":"tcp","host":"203.0.113.9","port":443}]}`,
		"manual icmp":       `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"manual","protocol":"icmp","host":"198.51.100.4"}]}`,
		"manual loopback":   `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"manual","protocol":"tcp","host":"127.0.0.1","port":22}]}`,
		"manual no host":    `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"manual","protocol":"tcp"}]}`,
		"unknown source":    `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"custom","protocol":"tcp","host":"203.0.113.9","port":443}]}`,
		"catalog with host": `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[{"slot":"telecom","source":"catalog","region":` + region + `,"protocol":"tcp","host":"203.0.113.9","port":443}]}`,
	} {
		if w := qualityWebRequest(t, a, http.MethodPut, path, body); w.Code != http.StatusBadRequest {
			t.Errorf("v1 %s: status=%d body=%s", name, w.Code, w.Body.String())
		}
	}
	// The v2 resolver (reached after the session check) refuses them as well.
	regionID := qualityDirectory.Regions[0].ID
	for name, choice := range map[string]protocol.QualityChoice{
		"v2 manual":            {Slot: "telecom", Source: "manual", Region: regionID, Protocol: "tcp", Host: "203.0.113.9", Port: 443},
		"v2 manual icmp":       {Slot: "telecom", Source: "manual", Region: regionID, Protocol: "icmp", Host: "198.51.100.4"},
		"v2 catalog with host": {Slot: "telecom", Source: "catalog", Region: regionID, Protocol: "tcp", Host: "203.0.113.9", Port: 443},
		"v2 unknown source":    {Slot: "telecom", Source: "custom", Region: regionID, Protocol: "tcp"},
	} {
		if _, err := qualityResolverV2(choice, "ipv4"); !errors.Is(err, storage.ErrQualityInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := qualityResolverV2(protocol.QualityChoice{Slot: "telecom", Source: "catalog", Region: regionID, Protocol: "tcp"}, "ipv4"); err != nil {
		t.Errorf("v2 catalog selection: %v", err)
	}
	// Nothing was saved by any of the rejected requests.
	w := qualityWebRequest(t, a, http.MethodGet, path, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "203.0.113.9") || strings.Contains(w.Body.String(), `"manual"`) {
		t.Fatalf("a rejected manual request left data behind: %d %s", w.Code, w.Body.String())
	}
	// A catalog selection still works.
	choice := func(slot string) string {
		return `{"slot":"` + slot + `","source":"catalog","region":` + region + `,"protocol":"tcp"}`
	}
	ok := `{"expected_revision":"0","enabled":true,"ipv6":false,"choices":[` + choice("telecom") + `,` + choice("unicom") + `,` + choice("mobile") + `]}`
	if w := qualityWebRequest(t, a, http.MethodPut, path, ok); w.Code != http.StatusOK {
		t.Fatalf("catalog selection: status=%d body=%s", w.Code, w.Body.String())
	}
}
