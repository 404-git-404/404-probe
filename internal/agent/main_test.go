package agent

import (
	"net/netip"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The older fixtures dial loopback and private test addresses. The guard itself is exercised
	// with the real policy in network_quality_guard_test.go.
	qualityAddressPolicy = func(netip.Addr) bool { return true }
	os.Exit(m.Run())
}
