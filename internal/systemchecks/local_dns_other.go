//go:build !linux

package systemchecks

import (
	"context"

	"404-probe/internal/protocol"
)

func (*Collector) collectDNS(context.Context, string) *protocol.LocalDNSCheck { return nil }
