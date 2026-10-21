package agent

import (
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"404-probe/internal/protocol"
)

// qualityIPv6ConfirmAfter is how long every scan of a new process must have succeeded without
// finding a usable address before "none" is reported (design 7.2).
const qualityIPv6ConfirmAfter = 5 * time.Minute

// qualityIPv6Monitor reports the local IPv6 address state for the server's lane authorization.
//
//	present  a usable address exists now
//	none     for 5 minutes every scan succeeded and found no usable address, and this process never
//	         saw one
//	unknown  everything else: the first 5 minutes, a scan error (the observation period starts over),
//	         or an address that was seen and went away (a fault, not a "none")
//
// Only addresses count; routes and error codes do not (design 7.2).
type qualityIPv6Monitor struct {
	scan  func() (bool, error)
	mu    sync.Mutex
	since time.Time // start of the current run of successful scans without an address
	seen  bool
}

func newQualityIPv6Monitor() *qualityIPv6Monitor {
	return &qualityIPv6Monitor{scan: scanUsableIPv6}
}

// observe scans once and returns the state to report.
func (m *qualityIPv6Monitor) observe(now time.Time) string {
	present, err := m.scan()
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err != nil:
		m.since = time.Time{} // the observation period starts over
		return protocol.QualityIPv6Unknown
	case present:
		m.seen = true
		m.since = time.Time{}
		return protocol.QualityIPv6Present
	case m.seen:
		m.since = time.Time{}
		return protocol.QualityIPv6Unknown
	}
	if m.since.IsZero() {
		m.since = now
	}
	if now.Sub(m.since) >= qualityIPv6ConfirmAfter {
		return protocol.QualityIPv6None
	}
	return protocol.QualityIPv6Unknown
}

// usableIPv6 is the address filter: global unicast (2000::/3) or unique local (fc00::/7).
func usableIPv6(addr netip.Addr) bool {
	if !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
		return false
	}
	return netip.MustParsePrefix("2000::/3").Contains(addr) || netip.MustParsePrefix("fc00::/7").Contains(addr)
}

var (
	qualityIfInet6Path = "/proc/net/if_inet6"
	qualityReadFile    = os.ReadFile
	qualityInterfaces  = net.Interfaces
	qualityInterfaceAt = net.InterfaceByIndex
	qualityInterfaceIP = func(i *net.Interface) ([]net.Addr, error) { return i.Addrs() }
	qualityScanGOOS    = runtime.GOOS
)

// scanUsableIPv6 reports whether some interface that is up and not loopback holds a usable address.
// On Linux /proc/net/if_inet6 is read and tentative or failed-DAD addresses are excluded. A missing
// file means IPv6 is disabled: a successful scan with no address.
func scanUsableIPv6() (bool, error) {
	if qualityScanGOOS == "linux" {
		wire, err := qualityReadFile(qualityIfInet6Path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		for _, line := range strings.Split(string(wire), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			if len(fields[0]) != 32 {
				return false, strconv.ErrSyntax
			}
			var raw [16]byte
			for i := 0; i < 16; i++ {
				b, err := strconv.ParseUint(fields[0][2*i:2*i+2], 16, 8)
				if err != nil {
					return false, err
				}
				raw[i] = byte(b)
			}
			index, err := strconv.ParseInt(fields[1], 16, 32)
			if err != nil {
				return false, err
			}
			flags, err := strconv.ParseUint(fields[4], 16, 32)
			if err != nil {
				return false, err
			}
			const tentative, dadFailed = 0x40, 0x08
			if flags&(tentative|dadFailed) != 0 || !usableIPv6(netip.AddrFrom16(raw)) {
				continue
			}
			iface, err := qualityInterfaceAt(int(index))
			if err != nil {
				continue // the interface vanished between the two reads
			}
			if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
				return true, nil
			}
		}
		return false, nil
	}
	interfaces, err := qualityInterfaces()
	if err != nil {
		return false, err
	}
	for i := range interfaces {
		if interfaces[i].Flags&net.FlagUp == 0 || interfaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := qualityInterfaceIP(&interfaces[i])
		if err != nil {
			return false, err
		}
		for _, a := range addrs {
			if prefix, err := netip.ParsePrefix(a.String()); err == nil && usableIPv6(prefix.Addr()) {
				return true, nil
			}
		}
	}
	return false, nil
}
