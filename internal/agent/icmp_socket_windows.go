//go:build windows

package agent

func newPlatformICMPBackend() icmpBackend {
	return detectICMPSocketBackend(map[icmpFamily][]icmpSocketMode{
		icmpFamilyIPv4: {{network: "ip4:icmp", address: "0.0.0.0"}},
		icmpFamilyIPv6: {{network: "ip6:ipv6-icmp", address: "::"}},
	}, listenICMPPacket)
}
