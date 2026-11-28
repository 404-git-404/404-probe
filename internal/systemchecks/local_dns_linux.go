//go:build linux

package systemchecks

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/idna"
)

func (c *Collector) collectDNS(ctx context.Context, serverHost string) *protocol.LocalDNSCheck {
	file, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return &protocol.LocalDNSCheck{Status: protocol.CheckError}
	}
	defer file.Close()
	addresses, err := localDNSAddresses(file)
	if err != nil {
		return &protocol.LocalDNSCheck{Status: protocol.CheckError}
	}
	if len(addresses) == 0 {
		c.dnsFailures = nil
		return &protocol.LocalDNSCheck{Status: protocol.DNSNotApplicable}
	}
	query, err := localDNSName(serverHost)
	if err != nil {
		return &protocol.LocalDNSCheck{Status: protocol.CheckError}
	}
	servers := make([]netip.AddrPort, len(addresses))
	for i, address := range addresses {
		servers[i] = netip.AddrPortFrom(address, 53)
	}
	return c.checkDNS(ctx, query, servers, 2*time.Second)
}

func localDNSAddresses(reader io.Reader) ([]netip.Addr, error) {
	var addresses []netip.Addr
	seen := make(map[netip.Addr]bool)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err == nil && addr.Zone() == "" && addr.IsLoopback() && !addr.Is4In6() && !seen[addr] {
			if len(addresses) == protocol.MaxLocalDNSServers {
				return nil, errors.New("too many local nameservers")
			}
			addresses = append(addresses, addr)
			seen[addr] = true
		}
	}
	return addresses, scanner.Err()
}

func localDNSName(host string) (dnsmessage.Name, error) {
	if _, err := netip.ParseAddr(host); err == nil {
		host = "cloudflare.com"
	}
	name, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || name == "" || len(name) > 253 {
		return dnsmessage.Name{}, errors.New("invalid DNS query name")
	}
	return dnsmessage.NewName(name + ".")
}

func (c *Collector) checkDNS(ctx context.Context, query dnsmessage.Name, servers []netip.AddrPort, timeout time.Duration) *protocol.LocalDNSCheck {
	check := &protocol.LocalDNSCheck{Status: protocol.CheckOK, Query: strings.TrimSuffix(query.String(), "."), Servers: make([]protocol.LocalDNSObservation, len(servers))}
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Go(func() {
			result := queryLocalDNS(ctx, server, query, timeout)
			if protocol.IsLocalDNSFailure(result) && ctx.Err() == nil {
				result = queryLocalDNS(ctx, server, query, timeout)
			}
			check.Servers[i] = protocol.LocalDNSObservation{Address: server.Addr().String(), Result: result, CheckedAt: time.Now().UnixMilli()}
		})
	}
	wg.Wait()
	failures := make(map[string]int)
	for i := range check.Servers {
		server := &check.Servers[i]
		if protocol.IsLocalDNSFailure(server.Result) {
			server.ConsecutiveFailures = min(c.dnsFailures[server.Address]+1, 2)
			failures[server.Address] = server.ConsecutiveFailures
		}
	}
	c.dnsFailures = failures
	return check
}

func queryLocalDNS(ctx context.Context, server netip.AddrPort, name dnsmessage.Name, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: uint16(rand.Uint32()), RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	packet, err := query.Pack()
	if err != nil {
		return protocol.DNSServerError
	}
	response, err := exchangeLocalDNS(ctx, "udp", server, packet, query)
	if err == nil && response.Truncated {
		response, err = exchangeLocalDNS(ctx, "tcp", server, packet, query)
	}
	if err != nil {
		var timeoutError net.Error
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			return protocol.DNSRefused
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeoutError) && timeoutError.Timeout():
			return protocol.DNSTimeout
		default:
			return protocol.DNSServerError
		}
	}
	if response.Truncated || (response.RCode != dnsmessage.RCodeSuccess && response.RCode != dnsmessage.RCodeNameError) {
		return protocol.DNSServerError
	}
	if response.RCode == dnsmessage.RCodeSuccess {
		addresses, aliases := make(map[string]bool), make(map[string]string)
		for _, record := range response.Answers {
			if record.Header.Class != dnsmessage.ClassINET {
				continue
			}
			owner := strings.ToLower(record.Header.Name.String())
			switch body := record.Body.(type) {
			case *dnsmessage.AResource:
				addresses[owner] = true
			case *dnsmessage.CNAMEResource:
				aliases[owner] = strings.ToLower(body.CNAME.String())
			}
		}
		owner := strings.ToLower(name.String())
		for i := 0; owner != "" && i <= len(aliases); i++ {
			if addresses[owner] {
				return protocol.CheckOK
			}
			owner = aliases[owner]
		}
	}
	return protocol.DNSNoRecord
}

func exchangeLocalDNS(ctx context.Context, network string, server netip.AddrPort, packet []byte, query dnsmessage.Message) (dnsmessage.Message, error) {
	var response dnsmessage.Message
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, server.String())
	if err != nil {
		return response, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return response, err
	}
	if network == "tcp" {
		packet = append(binary.BigEndian.AppendUint16(nil, uint16(len(packet))), packet...)
	}
	if _, err := conn.Write(packet); err != nil {
		return response, err
	}
	if network == "tcp" {
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return response, err
		}
		packet = make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(conn, packet); err != nil {
			return response, err
		}
	} else {
		packet = make([]byte, 64<<10)
		n, err := conn.Read(packet)
		if err != nil {
			if ctx.Err() != nil {
				return response, ctx.Err()
			}
			return response, err
		}
		packet = packet[:n]
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	question, questionErr := parser.Question()
	want := query.Questions[0]
	if err != nil || questionErr != nil || !header.Response || header.ID != query.ID || header.OpCode != query.OpCode ||
		question.Class != want.Class || question.Type != want.Type || !strings.EqualFold(question.Name.String(), want.Name.String()) {
		return response, errors.New("invalid DNS response")
	}
	response.Header = header
	if !header.Truncated {
		if err := response.Unpack(packet); err != nil || len(response.Questions) != 1 {
			return response, errors.New("invalid DNS response data")
		}
	}
	return response, nil
}
