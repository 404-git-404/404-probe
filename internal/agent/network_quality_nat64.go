package agent

import (
	"context"
	"net/netip"
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
	nat64Name          = "ipv4only.arpa"
	nat64MaxPrefixes   = 8
	nat64QueryTimeout  = 5 * time.Second
	nat64RefreshEvery  = 10 * time.Minute
	nat64RetryFirst    = time.Minute
	nat64RetryMaxDelay = time.Hour
)

var nat64WellKnown = [2]netip.Addr{netip.AddrFrom4([4]byte{192, 0, 0, 170}), netip.AddrFrom4([4]byte{192, 0, 0, 171})}

// nat64Snapshot is the immutable set read by the connection check. It is replaced as a whole.
var nat64Snapshot atomic.Pointer[[]netip.Prefix]

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
	for _, prefix := range *snapshot {
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
func nat64Discover(answers []netip.Addr) []netip.Prefix {
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
			if !seen[prefix] && len(found) < nat64MaxPrefixes {
				seen[prefix] = true
				found = append(found, prefix)
			}
		}
	}
	return found
}

type nat64Lookup interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// nat64Refresh asks once. A failed query keeps what is known; a successful one replaces it, an empty
// answer meaning there is no DNS64.
func nat64Refresh(ctx context.Context, resolver nat64Lookup) error {
	ctx, cancel := context.WithTimeout(ctx, nat64QueryTimeout)
	defer cancel()
	answers, err := resolver.LookupNetIP(ctx, "ip6", nat64Name)
	if err != nil {
		if isDNSNotFound(err) {
			nat64Snapshot.Store(&[]netip.Prefix{}) // an answer: no DNS64 here
			return nil
		}
		return err
	}
	prefixes := nat64Discover(answers)
	nat64Snapshot.Store(&prefixes)
	return nil
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
func runNAT64Discovery(ctx context.Context, resolver nat64Lookup, wait func(time.Duration) <-chan time.Time) {
	failures := 0
	for ctx.Err() == nil {
		if err := nat64Refresh(ctx, resolver); err != nil {
			failures++
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-wait(nat64Delay(failures)):
		}
	}
}
