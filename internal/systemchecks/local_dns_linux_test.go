//go:build linux

package systemchecks

import (
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/net/dns/dnsmessage"
)

func TestLocalDNSResolvConfAndQueryName(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []netip.Addr
	}{
		{"# nameserver 127.0.0.2\nnameserver 1.1.1.1\nnameserver 2001:db8::1", nil},
		{"nameserver\t127.0.0.53 # comment\n nameserver 0:0:0:0:0:0:0:1; comment\nnameserver ::1\nnameserver 127.8.4.2\nnameserver ::ffff:127.0.0.1",
			[]netip.Addr{netip.MustParseAddr("127.0.0.53"), netip.IPv6Loopback(), netip.MustParseAddr("127.8.4.2")}},
	} {
		got, err := localDNSAddresses(strings.NewReader(tc.input))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("resolv.conf %q: %v, %v", tc.input, got, err)
		}
	}
	for host, want := range map[string]string{
		"probe.example.com": "probe.example.com.", "127.0.0.1": "cloudflare.com.", "2001:db8::1": "cloudflare.com.", "例子.test": "xn--fsqu00a.test.",
	} {
		name, err := localDNSName(host)
		if err != nil || name.String() != want {
			t.Fatalf("query %s: %s, %v", host, name.String(), err)
		}
	}
}

func openFakeLocalDNS(t *testing.T) *net.UDPConn {
	t.Helper()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	return socket
}

func TestLocalDNSResultsAndConsecutiveChecks(t *testing.T) {
	const (
		respondOK int32 = iota
		respondSERVFAIL
		respondNoRecord
		dropQuery
	)
	var mode, requests atomic.Int32
	socket := openFakeLocalDNS(t)
	server := socket.LocalAddr().(*net.UDPAddr).AddrPort()
	go func() {
		buffer := make([]byte, 512)
		for {
			n, peer, err := socket.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			requests.Add(1)
			var message dnsmessage.Message
			if err := message.Unpack(buffer[:n]); err != nil || len(message.Questions) != 1 {
				t.Error("invalid DNS request")
				continue
			}
			question := message.Questions[0]
			if question.Name.String() != "probe.example.test." || question.Type != dnsmessage.TypeA {
				t.Errorf("unexpected question: %+v", question)
			}
			message.Response = true
			switch mode.Load() {
			case dropQuery:
				continue
			case respondSERVFAIL:
				message.RCode = dnsmessage.RCodeServerFailure
			case respondNoRecord:
				message.RCode = dnsmessage.RCodeNameError
			case respondOK:
				message.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET},
					Body:   &dnsmessage.AResource{A: [4]byte{192, 0, 2, 20}},
				}}
			}
			wire, err := message.Pack()
			if err == nil {
				_, err = socket.WriteToUDP(wire, peer)
			}
			if err != nil {
				t.Error(err)
			}
		}
	}()
	collector := &Collector{}
	name, _ := localDNSName("probe.example.test")
	for _, tc := range []struct {
		mode   int32
		status string
		streak int
		tries  int32
	}{
		{respondOK, protocol.CheckOK, 0, 1},
		{respondSERVFAIL, protocol.DNSServerError, 1, 2},
		{dropQuery, protocol.DNSTimeout, 2, 2},
		{respondNoRecord, protocol.DNSNoRecord, 2, 2},
		{respondOK, protocol.CheckOK, 0, 1},
		{respondSERVFAIL, protocol.DNSServerError, 1, 2},
	} {
		mode.Store(tc.mode)
		before := requests.Load()
		check := collector.checkDNS(t.Context(), name, []netip.AddrPort{server}, 80*time.Millisecond)
		if got := check.Servers[0]; got.Result != tc.status || got.ConsecutiveFailures != tc.streak || got.Address != "127.0.0.1" || got.CheckedAt <= 0 {
			t.Errorf("mode %d: %+v", tc.mode, got)
		}
		if got := requests.Load() - before; got != tc.tries {
			t.Errorf("mode %d: sent %d attempts, want %d", tc.mode, got, tc.tries)
		}
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	refused := collector.checkDNS(t.Context(), name, []netip.AddrPort{server}, time.Second)
	if got := refused.Servers[0]; got.Result != protocol.DNSRefused || got.ConsecutiveFailures != 2 {
		t.Fatalf("closed listener did not refuse: %+v", got)
	}
}
