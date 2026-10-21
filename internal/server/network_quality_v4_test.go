package server

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func v4Path(id, suffix string) string {
	return webAgentPathPrefix + id + "/network-quality/v4/" + suffix
}

// v4Body builds a save request. hosts are in slot order; an empty host leaves the slot unset.
func v4Body(expected string, interval int, hosts ...string) string {
	type slot struct {
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
	request := struct {
		ExpectedRevision string `json:"expected_revision"`
		IntervalMS       int    `json:"interval_ms"`
		Slots            []slot `json:"slots"`
	}{ExpectedRevision: expected, IntervalMS: interval}
	for i, name := range protocol.QualitySlotOrder() {
		s := slot{Slot: name, Kind: "custom", Region: "R", ISP: "I"}
		if i < len(hosts) && hosts[i] != "" {
			s.Host, s.Port, s.SlotEnabled, s.IPv4TCP = hosts[i], 443, true, true
		}
		request.Slots = append(request.Slots, s)
	}
	wire, _ := json.Marshal(request)
	return string(wire)
}

type v4ErrorBody struct {
	Error struct {
		Code    string
		Message string
		Details []protocol.QualityFieldError
	}
}

func v4Decode(t *testing.T, w *httptest.ResponseRecorder) v4ErrorBody {
	t.Helper()
	var out v4ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return out
}

// fakeResolver answers per name and network and records every question.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr // key: network+"/"+name
	failed  map[string]bool
	asked   []string
	block   chan struct{}
}

func (f *fakeResolver) lookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	f.asked = append(f.asked, network+"/"+host)
	key := network + "/" + host
	failed, answers, block := f.failed[key], f.answers[key], f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failed || answers == nil {
		return nil, errors.New("no such host")
	}
	return answers, nil
}

func (f *fakeResolver) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func v4App(t *testing.T) (*App, string, *fakeResolver, func()) {
	t.Helper()
	a, s, id, _ := testApp(t)
	resolver := &fakeResolver{answers: map[string][]netip.Addr{}, failed: map[string]bool{}}
	a.qualityLookup = resolver.lookup
	return a, id, resolver, func() { s.Close(); a.Shutdown() }
}

func addr(s string) []netip.Addr { return []netip.Addr{netip.MustParseAddr(s)} }

func TestR07V4ConfigSaveReadAndSwitch(t *testing.T) {
	a, id, resolver, done := v4App(t)
	defer done()
	resolver.answers["ip4/example.com"] = addr("93.184.216.34")
	resolver.answers["ip6/example.com"] = addr("2606:2800:220:1:248:1893:25c8:1946")
	w := qualityWebRequest(t, a, http.MethodGet, v4Path(id, "config"), "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var cfg protocol.QualityV4Config
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || cfg.SettingsRevision != "0" || cfg.Enabled || len(cfg.Slots) != 3 || len(cfg.Lanes) != 12 {
		t.Fatal(err, w.Body.String())
	}
	w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "config"), v4Body("0", 60000, "Example.COM."))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || cfg.SettingsRevision != "1" || cfg.Enabled || cfg.IntervalMS != 60000 || cfg.Slots[0].Host != "example.com" {
		t.Fatal(err, w.Body.String())
	}
	// Saving never turns detection on.
	w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "enabled"), `{"expected_revision":"1","enabled":true}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cfg)
	if !cfg.Enabled || cfg.SettingsRevision != "2" {
		t.Fatalf("%+v", cfg)
	}
	// A stale revision cannot switch on, and switching off ignores the version.
	if w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "enabled"), `{"expected_revision":"1","enabled":true}`); w.Code != 409 || v4Decode(t, w).Error.Code != "revision_conflict" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "enabled"), `{"expected_revision":"1","enabled":false}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "config"), v4Body("1", 60000, "example.com")); w.Code != 409 {
		t.Fatal("stale save", w.Code)
	}
}

func TestR07V4RequestShapeAndAuthentication(t *testing.T) {
	a, id, _, done := v4App(t)
	defer done()
	path := v4Path(id, "config")
	for name, body := range map[string]string{
		"unknown field":      `{"expected_revision":"0","interval_ms":30000,"slots":[],"enabled":true}`,
		"trailing value":     v4Body("0", 30000) + ` {}`,
		"not json":           `nope`,
		"two slots":          `{"expected_revision":"0","interval_ms":30000,"slots":[{"slot":"telecom","kind":"builtin","isp":"telecom"},{"slot":"unicom","kind":"builtin","isp":"unicom"}]}`,
		"bad revision":       v4Body("01", 30000),
		"unknown slot field": `{"expected_revision":"0","interval_ms":30000,"slots":[{"slot":"telecom","kind":"builtin","isp":"telecom","extra":1},{"slot":"unicom"},{"slot":"mobile"}]}`,
	} {
		if w := qualityWebRequest(t, a, http.MethodPut, path, body); w.Code != 400 || v4Decode(t, w).Error.Code != "invalid_request" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// Interval: every step from 30 to 300 seconds, nothing else.
	for _, interval := range []int{0, 29999, 30001, 45000, 330000, -30000} {
		w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("0", interval))
		details := v4Decode(t, w).Error.Details
		if w.Code != 400 || len(details) != 1 || details[0].Field != "interval_ms" || details[0].Code != "invalid_interval" {
			t.Fatalf("interval %d: %d %s", interval, w.Code, w.Body.String())
		}
	}
	// 413 at 16384 bytes + 1, with no decoding.
	if w := qualityWebRequest(t, a, http.MethodPut, path, strings.Repeat(" ", 16385)); w.Code != 413 {
		t.Fatal("body budget", w.Code)
	}
	if w := qualityWebRequest(t, a, http.MethodPut, v4Path(id, "enabled"), strings.Repeat(" ", 1025)); w.Code != 413 {
		t.Fatal("switch body budget", w.Code)
	}
	for _, body := range []string{`{}`, `{"expected_revision":"0"}`, `{"expected_revision":"0","enabled":"yes"}`, `{"expected_revision":"0","enabled":true,"x":1}`} {
		if w := qualityWebRequest(t, a, http.MethodPut, v4Path(id, "enabled"), body); w.Code != 400 {
			t.Fatalf("switch %s: %d", body, w.Code)
		}
	}
	// A query string, a wrong content type, no session and no CSRF header are refused.
	if w := qualityWebRequest(t, a, http.MethodGet, path+"?x=1", ""); w.Code != 400 {
		t.Fatal("query", w.Code)
	}
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(v4Body("0", 30000)))
	session := addTestWebSession(t, a, r)
	r.Header.Set("Origin", "https://probe.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-CSRF-Token", session.csrfToken)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("content type", w.Code)
	}
	for _, header := range []string{"X-CSRF-Token", "Origin"} {
		r = httptest.NewRequest(http.MethodPut, path, strings.NewReader(v4Body("0", 30000)))
		session = addTestWebSession(t, a, r)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-CSRF-Token", session.csrfToken)
		r.Header.Del(header)
		w = httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatalf("missing %s accepted", header)
		}
	}
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code == 200 {
		t.Fatal("unauthenticated read")
	}
}

func TestR07V4FieldErrorsAreReportedPerInput(t *testing.T) {
	a, id, _, done := v4App(t)
	defer done()
	body := `{"expected_revision":"0","interval_ms":30000,"slots":[
		{"slot":"telecom","kind":"custom","region":"R","isp":"I","host":"127.0.0.1","port":80,"slot_enabled":true,"ipv4_tcp":true},
		{"slot":"unicom","kind":"custom","region":"","isp":"I","host":"example.com","port":0,"slot_enabled":true,"ipv4_tcp":true},
		{"slot":"mobile","kind":"custom","region":"R","isp":"I","host":"1.1.1.1","port":53,"slot_enabled":true,"ipv6_tcp":true}]}`
	w := qualityWebRequest(t, a, http.MethodPut, v4Path(id, "config"), body)
	got := map[string]string{}
	for _, d := range v4Decode(t, w).Error.Details {
		got[d.Slot+"/"+d.Field] = d.Code
	}
	want := map[string]string{"telecom/host": "unsafe_address", "unicom/port": "invalid_port", "unicom/region": "label_required", "mobile/ipv6_tcp": "family_mismatch"}
	if w.Code != 400 || len(got) != len(want) {
		t.Fatalf("%d %v", w.Code, got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q (%v)", k, got[k], v, got)
		}
	}
	// Invalid UTF-8 and lone surrogates are rejected before decoding.
	bad := strings.Replace(v4Body("0", 30000, "example.com"), `"R"`, "\"R\xff\"", 1)
	if w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "config"), bad); w.Code != 400 || v4Decode(t, w).Error.Details[0].Code != "invalid_encoding" {
		t.Fatal(w.Code, w.Body.String())
	}
	lone := strings.Replace(v4Body("0", 30000, "example.com"), `"R"`, `"\ud800"`, 1)
	if w = qualityWebRequest(t, a, http.MethodPut, v4Path(id, "config"), lone); w.Code != 400 || v4Decode(t, w).Error.Details[0].Code != "invalid_encoding" {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The Server-side check refuses a name that resolves to a non-public address on either side, allows
// one that does not resolve at all, asks only about changed names, and never echoes an answer.
func TestR07V4ServerSideNameCheck(t *testing.T) {
	a, id, resolver, done := v4App(t)
	defer done()
	path := v4Path(id, "config")
	resolver.answers["ip4/private.example.com"] = addr("10.0.0.5")
	resolver.answers["ip4/mixed.example.com"] = append(addr("93.184.216.34"), netip.MustParseAddr("192.168.1.1"))
	resolver.answers["ip4/v6only.example.com"] = nil
	resolver.answers["ip6/v6only.example.com"] = addr("fd00::1")
	resolver.failed["ip4/v6only.example.com"] = true
	resolver.answers["ip4/good.example.com"] = addr("93.184.216.34")
	resolver.answers["ip6/mapped.example.com"] = addr("::ffff:10.0.0.1")
	for _, name := range []string{"private.example.com", "mixed.example.com", "v6only.example.com", "mapped.example.com"} {
		w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("0", 30000, name))
		details := v4Decode(t, w).Error.Details
		if w.Code != 400 || len(details) != 1 || details[0].Slot != "telecom" || details[0].Field != "host" || details[0].Code != "unsafe_address" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if containsAny(w.Body.String(), "10.0.0", "192.168", "fd00", "::ffff") {
			t.Fatalf("an answer leaked: %s", w.Body.String())
		}
	}
	// Both sides fail: saved (the Agent checks again before every connection).
	if w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("0", 30000, "nxdomain.example.com")); w.Code != 200 {
		t.Fatal("unresolvable name refused", w.Code, w.Body.String())
	}
	// A resolvable public name on one side while the other side fails is fine.
	if w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("1", 30000, "good.example.com")); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Unchanged names and literals are not resolved again.
	before := len(resolver.questions())
	if w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("2", 60000, "good.example.com")); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("3", 60000, "good.example.com", "8.8.8.8")); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if after := len(resolver.questions()); after != before {
		t.Fatalf("resolved %d more times for unchanged names and literals: %v", after-before, resolver.questions()[before:])
	}
	// The same changed name in two slots is asked once per family, and refused in both.
	w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("4", 60000, "good.example.com", "private.example.com", "private.example.com"))
	if details := v4Decode(t, w).Error.Details; w.Code != 400 || len(details) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	count := 0
	for _, q := range resolver.questions()[before:] {
		if strings.HasSuffix(q, "/private.example.com") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("private name asked %d times, want 2 (one per family)", count)
	}
}

func TestR07V4NameCheckBudgetAndConcurrency(t *testing.T) {
	a, id, resolver, done := v4App(t)
	defer done()
	path := v4Path(id, "config")
	// One shared 2-second budget for the whole request; a slow resolver does not hold the save.
	resolver.block = make(chan struct{})
	defer close(resolver.block)
	started := time.Now()
	w := qualityWebRequest(t, a, http.MethodPut, path, v4Body("0", 30000, "a.example.com", "b.example.com", "c.example.com"))
	elapsed := time.Since(started)
	if w.Code != 200 || elapsed < 1800*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("slow resolver: %d after %v", w.Code, elapsed)
	}
	// Four checks at a time on the whole Server; the fifth is told to retry.
	for i := 0; i < qualityDNSCheckSlots; i++ {
		a.qualityDNSSlots <- struct{}{}
	}
	w = qualityWebRequest(t, a, http.MethodPut, path, v4Body("1", 30000, "d.example.com"))
	if w.Code != 503 || v4Decode(t, w).Error.Code != "retryable" {
		t.Fatal("busy", w.Code, w.Body.String())
	}
	// A save that changes no name does not need a slot.
	if w = qualityWebRequest(t, a, http.MethodPut, path, v4Body("1", 60000, "a.example.com", "b.example.com", "c.example.com")); w.Code != 200 {
		t.Fatal("no names to check", w.Code, w.Body.String())
	}
	for i := 0; i < qualityDNSCheckSlots; i++ {
		<-a.qualityDNSSlots
	}
}

// A save that stalls on the request body is cut at the five-second request budget.
func TestR07V4RealHTTPFiveSecondBodyBudget(t *testing.T) {
	a, id, _, done := v4App(t)
	defer done()
	completed := make(chan time.Duration, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		a.Handler().ServeHTTP(w, r)
		completed <- time.Since(started)
	}))
	server.Config.ReadTimeout = 15 * time.Second
	server.Start()
	defer server.Close()
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(8 * time.Second))
	path := v4Path(id, "config")
	request := httptest.NewRequest("PUT", path, nil)
	session := addTestWebSession(t, a, request)
	headers := "Cookie: " + request.Header.Get("Cookie") + "\r\nOrigin: https://probe.test\r\nSec-Fetch-Site: same-origin\r\nX-CSRF-Token: " + session.csrfToken + "\r\n"
	fmt.Fprintf(connection, "PUT %s HTTP/1.1\r\nHost: probe.test\r\nContent-Type: application/json\r\nContent-Length: 1000\r\nConnection: close\r\n%s\r\n{", path, headers)
	select {
	case elapsed := <-completed:
		if elapsed < 4500*time.Millisecond || elapsed > 6500*time.Millisecond {
			t.Fatalf("body not budgeted: %v", elapsed)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("handler remained blocked beyond budget")
	}
}
