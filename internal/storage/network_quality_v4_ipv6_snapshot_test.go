package storage

import (
	"testing"

	"404-probe/internal/protocol"
)

// unknown never replaces the last determinate IPv6 state (design 11.1 G2); a determinate value does.
func TestV4IPv6UnknownKeepsLastDeterminateState(t *testing.T) {
	setup := func(t *testing.T, state string) *v4Kit {
		k := newV4Kit(t)
		k.negotiate(v4Caps(state))
		k.save(30000, v4Slot("telecom", "example.com", 443, "v6tcp"))
		k.enable(true)
		return k
	}
	t.Run("none then unknown, same session", func(t *testing.T) {
		k := setup(t, protocol.QualityIPv6None)
		before := k.get()
		if len(k.open()) != 0 {
			t.Fatal("fixture")
		}
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Unknown)))
		if len(k.open()) != 0 || k.get().ConfigRevision != before.ConfigRevision {
			t.Fatalf("unknown replaced none: open=%d revision %s -> %s", len(k.open()), before.ConfigRevision, k.get().ConfigRevision)
		}
	})
	t.Run("none then unknown after a restart", func(t *testing.T) {
		k := setup(t, protocol.QualityIPv6None)
		before := k.get()
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6Unknown))
		if len(k.open()) != 0 || k.get().ConfigRevision != before.ConfigRevision {
			t.Fatal("restart with unknown reopened the IPv6 lane")
		}
		k.invariants()
	})
	t.Run("none then present reopens", func(t *testing.T) {
		k := setup(t, protocol.QualityIPv6None)
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Present)))
		if len(k.open()) != 1 {
			t.Fatal("present did not open the IPv6 lane")
		}
		k.invariants()
	})
	t.Run("present then unknown keeps running", func(t *testing.T) {
		k := setup(t, protocol.QualityIPv6Present)
		before := k.get()
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Unknown)))
		if len(k.open()) != 1 || k.get().ConfigRevision != before.ConfigRevision {
			t.Fatal("unknown closed or moved a running IPv6 lane")
		}
	})
	t.Run("first report is unknown: the lane may be tried", func(t *testing.T) {
		k := setup(t, protocol.QualityIPv6Unknown)
		if len(k.open()) != 1 {
			t.Fatal("an unknown IPv6 state must still be tried")
		}
	})
	t.Run("a real ICMP withdrawal is still applied with unknown IPv6", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		k.save(30000, v4Slot("telecom", "example.com", 443, "v6tcp", "v6icmp"))
		k.enable(true)
		if len(k.open()) != 2 {
			t.Fatal("fixture")
		}
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Unknown, "ipv6/icmp")))
		if len(k.open()) != 1 {
			t.Fatalf("ICMP withdrawal ignored: %s", k.summary())
		}
	})
}
