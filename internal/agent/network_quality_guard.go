package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"

	"404-probe/internal/protocol"
)

// Network-quality probes only ever reach public addresses. The check is applied to the address that
// is actually about to be used (after DNS), with the same deny list as the public egress validation:
// private, loopback, link-local (including cloud metadata 169.254.169.254), CGNAT, documentation and
// other special-purpose ranges are never contacted and are recorded as unsafe_address.
var errUnsafeQualityAddress = errors.New("quality target address is not public")

// qualityAddressPolicy is the one decision point; only tests replace it (the fixtures dial loopback).
var qualityAddressPolicy = qualityAddressSafe

func qualityAddressSafe(ip netip.Addr) bool {
	ip = ip.Unmap()
	family := "ipv4"
	if ip.Is6() {
		family = "ipv6"
	}
	return protocol.PublicEgressIP(ip.String(), family)
}

func qualityGuardControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errUnsafeQualityAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !qualityAddressPolicy(ip) {
		return errUnsafeQualityAddress
	}
	return nil
}

// qualityDefaultDial is the production dialer: the guard runs on every resolved address before connect.
func qualityDefaultDial() func(context.Context, string, string) (net.Conn, error) {
	return (&net.Dialer{Control: qualityGuardControl}).DialContext
}
