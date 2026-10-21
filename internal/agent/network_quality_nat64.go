package agent

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// NAT64 prefix discovery (RFC 7050; design 5.1, decision N1-B).
//
// The Agent asks the system resolver for the AAAA records of ipv4only.arpa, finds the well-known
// addresses 192.0.0.170 and 192.0.0.171 inside them, and so learns the prefix its network uses to
// translate IPv4 into IPv6. An address inside a discovered prefix is judged by the IPv4 address
// embedded in it, in addition to the ordinary check, never instead of it: adding a prefix can only turn
// an allowed address into a refused one.
//
// Known limits (documented, not hidden): a NAT64 without DNS64, or one whose prefix is only advertised
// by RA PREF64 or configured by hand, is not discovered; before the first successful query nothing is
// known; the resolver interface returns no TTL, so refreshing is a fixed, bounded schedule.

const (
	nat64Name = "ipv4only.arpa"
	// nat64MaxPrefixes prefixes are judged by their embedded IPv4 address. Further candidates (up to
	// nat64MaxRefused in all) are not dropped, because dropping one would lift a refusal: addresses
	// inside them are refused outright. Beyond that the answer is unusable and no IPv6 destination
	// passes. More candidates therefore never allow anything that fewer refused.
	nat64MaxPrefixes   = 8
	nat64MaxRefused    = 64
	nat64MaxAnswers    = 128
	nat64QueryTimeout  = 5 * time.Second
	nat64RefreshEvery  = 10 * time.Minute
	nat64RetryFirst    = time.Minute
	nat64RetryMaxDelay = time.Hour
)

var nat64WellKnown = [2]netip.Addr{netip.AddrFrom4([4]byte{192, 0, 0, 170}), netip.AddrFrom4([4]byte{192, 0, 0, 171})}

// nat64Set is what discovery found. It is immutable and replaced as a whole.
type nat64Set struct {
	checked   []netip.Prefix // judged by the embedded IPv4 address
	refused   []netip.Prefix // overflow: every address inside is refused
	refuseAll bool           // far too many candidates: no IPv6 destination passes
}

// nat64Snapshot is the set read by the connection check.
var nat64Snapshot atomic.Pointer[nat64Set]

// nat64Embedded extracts the IPv4 address embedded in an IPv6 address by the RFC 6052 layout of the
// given prefix length. For lengths up to 64 the u octet (bits 64–71) must be zero.
func nat64Embedded(address netip.Addr, length int) (netip.Addr, bool) {
	if !address.Is6() || address.Is4In6() {
		return netip.Addr{}, false
	}
	b := address.As16()
	var v4 [4]byte
	switch length {
	case 32:
		copy(v4[:], b[4:8])
	case 40:
		v4 = [4]byte{b[5], b[6], b[7], b[9]}
	case 48:
		v4 = [4]byte{b[6], b[7], b[9], b[10]}
	case 56:
		v4 = [4]byte{b[7], b[9], b[10], b[11]}
	case 64:
		copy(v4[:], b[9:13])
	case 96:
		copy(v4[:], b[12:16])
		return netip.AddrFrom4(v4), true
	default:
		return netip.Addr{}, false
	}
	if b[8] != 0 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(v4), true
}

// nat64Allows is the extra condition of the connection check: every discovered prefix that contains
// the address must yield an embedded IPv4 address that is itself public.
func nat64Allows(address netip.Addr) bool {
	snapshot := nat64Snapshot.Load()
	if snapshot == nil || !address.Is6() {
		return true
	}
	if snapshot.refuseAll {
		return false
	}
	for _, prefix := range snapshot.refused {
		if prefix.Contains(address) {
			return false
		}
	}
	for _, prefix := range snapshot.checked {
		if !prefix.Contains(address) {
			continue
		}
		embedded, ok := nat64Embedded(address, prefix.Bits())
		if !ok || !qualityPublicIPv4(embedded) {
			return false
		}
	}
	return true
}

// nat64Discover derives the prefixes from the AAAA records of ipv4only.arpa.
func nat64Discover(answers []netip.Addr) *nat64Set {
	set := &nat64Set{}
	if len(answers) > nat64MaxAnswers {
		set.refuseAll = true
		return set
	}
	var found []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, answer := range answers {
		answer = answer.Unmap()
		if !answer.Is6() {
			continue
		}
		for _, length := range []int{32, 40, 48, 56, 64, 96} {
			embedded, ok := nat64Embedded(answer, length)
			if !ok || (embedded != nat64WellKnown[0] && embedded != nat64WellKnown[1]) {
				continue
			}
			prefix := netip.PrefixFrom(answer, length).Masked()
			if !seen[prefix] {
				seen[prefix] = true
				found = append(found, prefix)
			}
		}
	}
	// A fixed order, independent of the order of the answers: a candidate added later can only push
	// another one from the checked group into the refused group (stricter), never the other way round.
	sort.Slice(found, func(i, j int) bool {
		if c := found[i].Addr().Compare(found[j].Addr()); c != 0 {
			return c < 0
		}
		return found[i].Bits() < found[j].Bits()
	})
	switch {
	case len(found) <= nat64MaxPrefixes:
		set.checked = found
	case len(found) <= nat64MaxRefused:
		set.checked, set.refused = found[:nat64MaxPrefixes], found[nat64MaxPrefixes:]
	default:
		set.refuseAll = true
	}
	return set
}

type nat64Lookup interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// nat64Refresh asks once. A failed query keeps what is known; a successful one replaces it, an empty
// answer meaning there is no DNS64.
func nat64Refresh(ctx context.Context, resolver nat64Lookup) error {
	return nat64RefreshAs(ctx, resolver, 0)
}

func nat64RefreshAs(ctx context.Context, resolver nat64Lookup, generation uint64) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, nat64QueryTimeout)
	defer cancel()
	answers, err := resolver.LookupNetIP(ctx, "ip6", nat64Name)
	// An answer that arrives after the loop was stopped must not overwrite what a newer loop learned.
	if parent.Err() != nil {
		return parent.Err()
	}
	if err != nil {
		if isDNSNotFound(err) {
			nat64Publish(&nat64Set{}, generation) // an answer: no DNS64 here
			return nil
		}
		return err
	}
	nat64Publish(nat64Discover(answers), generation)
	return nil
}

// nat64PublishHook lets a test stop a loop right before it publishes; it is nil in production.
var nat64PublishHook func()

// Every loop that is started takes the next generation number. Publishing compares it with the newest
// one and stores in the same critical section, so a loop that was superseded after it passed its
// cancellation check cannot replace what a newer loop published. Generation 0 is a one-off refresh
// that belongs to no loop.
var (
	nat64Mu     sync.Mutex
	nat64Newest uint64
)

func nat64NextGeneration() uint64 {
	nat64Mu.Lock()
	defer nat64Mu.Unlock()
	nat64Newest++
	return nat64Newest
}

func nat64Publish(set *nat64Set, generation uint64) {
	if nat64PublishHook != nil {
		nat64PublishHook()
	}
	nat64Mu.Lock()
	defer nat64Mu.Unlock()
	if generation != 0 && generation != nat64Newest {
		return
	}
	nat64Snapshot.Store(set)
}

// nat64Delay is the wait before the next query: ten minutes after a success, then one minute doubling
// up to one hour while queries keep failing.
func nat64Delay(failures int) time.Duration {
	if failures <= 0 {
		return nat64RefreshEvery
	}
	delay := nat64RetryFirst
	for i := 1; i < failures && delay < nat64RetryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, nat64RetryMaxDelay)
}

// runNAT64Discovery queries at start and then on the fixed schedule until ctx ends. The connection
// check only reads the snapshot; no DNS query ever runs inside Dialer.Control.
func runNAT64Discovery(ctx context.Context, resolver nat64Lookup, wait func(time.Duration) <-chan time.Time, first func()) {
	runNAT64DiscoveryAs(ctx, resolver, wait, first, 0)
}

func runNAT64DiscoveryAs(ctx context.Context, resolver nat64Lookup, wait func(time.Duration) <-chan time.Time, first func(), generation uint64) {
	failures := 0
	for ctx.Err() == nil {
		if err := nat64RefreshAs(ctx, resolver, generation); err != nil {
			failures++
		} else {
			failures = 0
		}
		if first != nil {
			first()
			first = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-wait(nat64Delay(failures)):
		}
	}
}
