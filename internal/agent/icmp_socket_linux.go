//go:build linux

package agent

func newPlatformICMPBackend() icmpBackend {
	return detectICMPSocketBackend(map[icmpFamily][]icmpSocketMode{
		icmpFamilyIPv4: {
			{network: "udp4", address: "0.0.0.0", datagram: true},
			{network: "ip4:icmp", address: "0.0.0.0"},
		},
		icmpFamilyIPv6: {
			{network: "udp6", address: "::", datagram: true},
			{network: "ip6:ipv6-icmp", address: "::"},
		},
	}, listenICMPPacket)
}
