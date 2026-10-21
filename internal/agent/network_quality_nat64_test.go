package agent

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fixedNAT64 answers the discovery query with no AAAA record: no DNS64 on this network.
type fixedNAT64 struct{}

func (fixedNAT64) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, &net.DNSError{IsNotFound: true}
}

type scriptedNAT64 struct {
	mu      sync.Mutex
	answers []netip.Addr
	err     error
	asked   int
}

func (s *scriptedNAT64) set(answers []netip.Addr, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers, s.err = answers, err
}

func (s *scriptedNAT64) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	if network != "ip6" || host != "ipv4only.arpa" {
		return nil, errors.New("unexpected question")
	}
	return s.answers, s.err
}

func setNAT64(t *testing.T, prefixes ...string) {
	t.Helper()
	previous := nat64Snapshot.Load()
	set := []netip.Prefix{}
	for _, p := range prefixes {
		set = append(set, netip.MustParsePrefix(p))
	}
	nat64Snapshot.Store(&nat64Set{checked: set})
	t.Cleanup(func() { nat64Snapshot.Store(previous) })
}

// nat64Compose is the inverse of nat64Embedded, written independently from RFC 6052 section 2.2.
func nat64Compose(prefix netip.Prefix, v4 netip.Addr) netip.Addr {
	b := prefix.Masked().Addr().As16()
	a := v4.As4()
	switch prefix.Bits() {
	case 32:
		copy(b[4:8], a[:])
	case 40:
		b[5], b[6], b[7], b[9] = a[0], a[1], a[2], a[3]
	case 48:
		b[6], b[7], b[9], b[10] = a[0], a[1], a[2], a[3]
	case 56:
		b[7], b[9], b[10], b[11] = a[0], a[1], a[2], a[3]
	case 64:
		copy(b[9:13], a[:])
	case 96:
		copy(b[12:16], a[:])
	}
	return netip.AddrFrom16(b)
}

func TestNAT64EmbeddedMatchesRFC6052Examples(t *testing.T) {
	// RFC 6052 section 2.4: 192.0.2.33 under 2001:db8::/N.
	for text, length := range map[string]int{
		"2001:db8:c000:221::":          32,
		"2001:db8:1c0:2:21::":          40,
		"2001:db8:122:c000:2:2100::":   48,
		"2001:db8:122:3c0:0:221::":     56,
		"2001:db8:122:344:c0:2:2100:0": 64,
		"2001:db8:122:344::c000:221":   96,
	} {
		got, ok := nat64Embedded(netip.MustParseAddr(text), length)
		if !ok || got != netip.MustParseAddr("192.0.2.33") {
			t.Errorf("/%d %s: %v %v", length, text, got, ok)
		}
	}
	// The u octet must be zero up to /64 and is part of the prefix at /96.
	if _, ok := nat64Embedded(netip.MustParseAddr("2001:db8:122:344:ff00:2:2100:0"), 64); ok {
		t.Error("a non-zero u octet was accepted at /64")
	}
	if _, ok := nat64Embedded(netip.MustParseAddr("2001:db8:1:2:ff00:3:4:5"), 48); ok {
		t.Error("a non-zero u octet was accepted at /48")
	}
	if _, ok := nat64Embedded(netip.MustParseAddr("2606:4700:1234:5678:ff00:abcd:c000:aa"), 96); !ok {
		t.Error("/96 must not look at the u octet")
	}
}

func TestNAT64DiscoveryDerivesEveryPrefixLength(t *testing.T) {
	for _, length := range []int{32, 40, 48, 56, 64, 96} {
		for _, well := range nat64WellKnown {
			prefix := netip.PrefixFrom(netip.MustParseAddr("2001:db8:122:344::"), length).Masked()
			answer := nat64Compose(prefix, well)
			found := nat64Discover([]netip.Addr{answer}).checked
			if len(found) != 1 || found[0] != prefix {
				t.Errorf("/%d %v: %v", length, answer, found)
			}
		}
	}
	// A /96 whose bits 64-71 are not zero is still discovered (design 5.1, review R2).
	found := nat64Discover([]netip.Addr{netip.MustParseAddr("2606:4700:1234:5678:9abc:def0:c000:00aa")}).checked
	if len(found) != 1 || found[0] != netip.MustParsePrefix("2606:4700:1234:5678:9abc:def0::/96") {
		t.Fatalf("%v", found)
	}
	// /32 to /64 candidates with a non-zero u octet are not candidates.
	if got := nat64Discover([]netip.Addr{netip.MustParseAddr("2001:db8:122:344:ff00:c0:0:aa")}).checked; len(got) != 0 {
		t.Fatalf("%v", got)
	}
	// Several prefixes are kept once each, at most eight.
	var answers []netip.Addr
	for i := 0; i < 12; i++ {
		prefix := netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(i + 1), 0, 0, 0}), 96)
		answers = append(answers, nat64Compose(prefix, nat64WellKnown[0]), nat64Compose(prefix, nat64WellKnown[0]))
	}
	if got := nat64Discover(answers); len(got.checked) != nat64MaxPrefixes || len(got.refused) != 4 {
		t.Fatalf("kept %d checked and %d refused", len(got.checked), len(got.refused))
	}
	// Non-IPv6 answers are ignored.
	if got := nat64Discover([]netip.Addr{netip.MustParseAddr("192.0.0.170")}).checked; len(got) != 0 {
		t.Fatal(got)
	}
}

func TestNAT64RefreshKeepsKnowledgeOnFailureAndClearsOnNoDNS64(t *testing.T) {
	previous := nat64Snapshot.Load()
	t.Cleanup(func() { nat64Snapshot.Store(previous) })
	nat64Snapshot.Store(nil)
	prefix := netip.MustParsePrefix("2001:db8:100::/96")
	resolver := &scriptedNAT64{answers: []netip.Addr{nat64Compose(prefix, nat64WellKnown[0])}}
	if err := nat64Refresh(context.Background(), resolver); err != nil || len(nat64Snapshot.Load().checked) != 1 {
		t.Fatal(err)
	}
	resolver.answers, resolver.err = nil, errors.New("SERVFAIL")
	if err := nat64Refresh(context.Background(), resolver); err == nil || len(nat64Snapshot.Load().checked) != 1 {
		t.Fatal("a failed query lost the previous result")
	}
	if err := nat64Refresh(context.Background(), fixedNAT64{}); err != nil || len(nat64Snapshot.Load().checked) != 0 {
		t.Fatal("a definite answer without AAAA must clear the prefixes")
	}
	resolver.err, resolver.answers = nil, nil
	if err := nat64Refresh(context.Background(), resolver); err != nil || len(nat64Snapshot.Load().checked) != 0 {
		t.Fatal("an empty answer means no DNS64")
	}
}

func TestNAT64ScheduleIsBounded(t *testing.T) {
	want := []time.Duration{10 * time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	for failures, delay := range want {
		if got := nat64Delay(failures); got != delay {
			t.Errorf("after %d failures: %v, want %v", failures, got, delay)
		}
	}
	if nat64Delay(1000) != time.Hour {
		t.Fatal("not capped")
	}
	// The loop queries at start, waits the scheduled time and stops with its context.
	previous := nat64Snapshot.Load()
	t.Cleanup(func() { nat64Snapshot.Store(previous) })
	resolver := &scriptedNAT64{}
	ctx, cancel := context.WithCancel(context.Background())
	waits := make(chan time.Duration, 4)
	done := make(chan struct{})
	go func() {
		runNAT64Discovery(ctx, resolver, func(d time.Duration) <-chan time.Time {
			select {
			case waits <- d:
			default:
			}
			c := make(chan time.Time, 1)
			c <- time.Now()
			return c
		}, nil)
		close(done)
	}()
	if d := <-waits; d != 10*time.Minute {
		t.Fatalf("first wait %v", d)
	}
	resolver.set(nil, errors.New("down")) // the loop is running in another goroutine
	if d := <-waits; d != 10*time.Minute && d != time.Minute {
		t.Fatalf("second wait %v", d)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop outlived its context")
	}
}

func TestNAT64PrefixesOnlyTightenTheCheck(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	public := netip.MustParsePrefix("2606:4700:100::/96")
	embeddedPrivate := nat64Compose(public, netip.MustParseAddr("10.1.2.3"))
	embeddedPublic := nat64Compose(public, netip.MustParseAddr("8.8.8.8"))
	// Without a discovered prefix an ordinary global address is allowed whatever it embeds.
	if !qualityAddressSafe(embeddedPrivate) || !qualityAddressSafe(embeddedPublic) {
		t.Fatal("the fixtures should be ordinary public IPv6 addresses")
	}
	nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{public}})
	if qualityAddressSafe(embeddedPrivate) {
		t.Fatal("an address embedding a private IPv4 inside a discovered prefix was allowed")
	}
	if !qualityAddressSafe(embeddedPublic) {
		t.Fatal("an address embedding a public IPv4 inside a discovered prefix should stay allowed")
	}
	for _, text := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.0.1", "100.64.0.1", "0.0.0.0"} {
		if qualityAddressSafe(nat64Compose(public, netip.MustParseAddr(text))) {
			t.Errorf("%s embedded in a discovered prefix was allowed", text)
		}
	}
	// A forged prefix cannot make a refused address acceptable: 2001:db8::/32 stays refused.
	nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")}})
	if qualityAddressSafe(nat64Compose(netip.MustParsePrefix("2001:db8::/32"), netip.MustParseAddr("8.8.8.8"))) {
		t.Fatal("a documentation address was allowed because a prefix covers it")
	}
	// Overlapping prefixes: all of them must pass. Under the /96 this address embeds 10.0.0.1.
	nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{netip.MustParsePrefix("2606:4700::/32"), netip.MustParsePrefix("2606:4700::/96")}})
	if qualityAddressSafe(netip.MustParseAddr("2606:4700::a00:1")) {
		t.Fatal("one overlapping prefix embedding a private IPv4 must refuse")
	}
	// A prefix whose layout cannot yield an IPv4 refuses the address (u octet not zero at /48).
	nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{netip.MustParsePrefix("2606:4700:1::/48")}})
	if qualityAddressSafe(netip.MustParseAddr("2606:4700:1:0:ff00:0:0:1")) {
		t.Fatal("an address with no embedded IPv4 was allowed inside a prefix")
	}
	// The well-known prefix is still decided by the ordinary check.
	nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{}})
	if !qualityAddressSafe(netip.MustParseAddr("64:ff9b::808:808")) || qualityAddressSafe(netip.MustParseAddr("64:ff9b::a00:1")) || qualityAddressSafe(netip.MustParseAddr("64:ff9b:1::1")) {
		t.Fatal("the well-known prefix rules changed")
	}
}

// Property: for any set of discovered prefixes, an address that the check refuses without them is
// refused with them. Adding discoveries never allows anything.
func TestNAT64DiscoveryNeverAllowsWhatWasRefused(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	rng := rand.New(rand.NewSource(7))
	randomAddr := func() netip.Addr {
		var b [16]byte
		rng.Read(b[:])
		switch rng.Intn(5) {
		case 0:
			copy(b[:], []byte{0x20, 0x01, 0x0d, 0xb8})
		case 1:
			copy(b[:], []byte{0x26, 0x06, 0x47, 0x00})
		case 2:
			copy(b[:], []byte{0x00, 0x64, 0xff, 0x9b})
		case 3:
			copy(b[:], []byte{0xfd, 0x00})
		}
		return netip.AddrFrom16(b)
	}
	for i := 0; i < 3000; i++ {
		var prefixes []netip.Prefix
		for n := rng.Intn(5); n > 0; n-- {
			prefixes = append(prefixes, netip.PrefixFrom(randomAddr(), []int{32, 40, 48, 56, 64, 96}[rng.Intn(6)]).Masked())
		}
		addr := randomAddr()
		if rng.Intn(2) == 0 && len(prefixes) > 0 {
			addr = nat64Compose(prefixes[rng.Intn(len(prefixes))], netip.AddrFrom4([4]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}))
		}
		nat64Snapshot.Store(&nat64Set{checked: []netip.Prefix{}})
		without := qualityAddressSafe(addr)
		nat64Snapshot.Store(&nat64Set{checked: prefixes})
		with := qualityAddressSafe(addr)
		if with && !without {
			t.Fatalf("%v was refused without the prefixes %v and allowed with them", addr, prefixes)
		}
	}
}
