//go:build ignore

// Converts the user-provided, overlapping journalctl text exports into a
// repository-safe JSON-lines fixture. It removes shell prompts, hostnames,
// process IDs and duplicate full event lines. Source IPs become documentation
// addresses and cursors are fixture-only event numbers.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"
)

var timestampPattern = regexp.MustCompile(`(-[0-9]{4}) (2026-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2})`)

func main() {
	if len(os.Args) != 3 {
		panic("usage: go run convert-security-fixture.go INPUT OUTPUT")
	}
	input, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer input.Close()
	output, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	seen := make(map[string]bool)
	sources := make(map[netip.Addr]netip.Addr)
	var ipv4, ipv6 uint32
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	raw, unique := 0, 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "REALITY: processed invalid connection") {
			continue
		}
		raw++
		if seen[line] {
			continue
		}
		seen[line] = true
		match := timestampPattern.FindStringSubmatch(line)
		messageAt := strings.Index(line, "inbound/")
		if len(match) != 3 || messageAt < 0 {
			panic("unrecognized event line")
		}
		at, err := time.Parse("-0700 2006-01-02 15:04:05", match[1]+" "+match[2])
		if err != nil {
			panic(err)
		}
		message := line[messageAt:]
		before, after, ok := strings.Cut(message, "process connection from ")
		endpoint, suffix, foundSuffix := strings.Cut(after, ": ")
		source, err := netip.ParseAddrPort(endpoint)
		if !ok || !foundSuffix || err != nil {
			panic("unrecognized source endpoint")
		}
		original := source.Addr().Unmap()
		synthetic, exists := sources[original]
		if !exists {
			if original.Is4() {
				ranges := [...][3]byte{{192, 0, 2}, {198, 51, 100}, {203, 0, 113}}
				if ipv4 >= uint32(len(ranges))*254 {
					panic("fixture exceeds the IPv4 documentation address pool")
				}
				prefix := ranges[ipv4/254]
				synthetic = netip.AddrFrom4([4]byte{prefix[0], prefix[1], prefix[2], byte(ipv4%254 + 1)})
				ipv4++
			} else {
				ipv6++
				synthetic = netip.MustParseAddr(fmt.Sprintf("2001:db8::%x:%x", ipv6>>16, ipv6&0xffff))
			}
			sources[original] = synthetic
		}
		message = before + "process connection from " + netip.AddrPortFrom(synthetic, source.Port()).String() + ": " + suffix
		entry := map[string]string{"_SYSTEMD_UNIT": "sing-box.service", "__CURSOR": fmt.Sprintf("fixture-%08d", unique+1), "__REALTIME_TIMESTAMP": fmt.Sprintf("%d", at.UnixMicro()), "MESSAGE": message}
		if err := encoder.Encode(entry); err != nil {
			panic(err)
		}
		unique++
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	fmt.Printf("raw=%d unique=%d\n", raw, unique)
}
