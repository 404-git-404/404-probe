//go:build !linux

package systemchecks

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
)

func (*Collector) collect(context.Context, protocol.SystemCheckSettings, string, string) protocol.SystemChecks {
	return protocol.SystemChecks{
		SecurityBaseline: &protocol.SecurityBaselineReport{Status: protocol.InventoryUnavailable, Reason: "unsupported"},
		SingboxStructure: &protocol.SingboxStructureReport{Status: protocol.InventoryUnavailable, Reason: "unsupported"},
	}
}

func RunRoot(context.Context, bool) error { return errors.New("root collection requires Debian Linux") }
