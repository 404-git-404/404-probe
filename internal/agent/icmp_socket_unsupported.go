//go:build !linux && !windows

package agent

type unsupportedICMPBackend struct{}

func (unsupportedICMPBackend) Supports(icmpFamily) bool { return false }

func (unsupportedICMPBackend) Open(icmpFamily) (icmpPacketConn, bool, error) {
	return nil, false, errICMPUnsupportedFamily
}

func newPlatformICMPBackend() icmpBackend { return unsupportedICMPBackend{} }
